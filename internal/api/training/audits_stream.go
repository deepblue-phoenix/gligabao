package training

// The live feed of an audit ("watch it run" in the admin UI): GET /v1/gb/training/audits/{id}/events is a Server-Sent Events
// stream of what the audit does as it does it. The model's answers are sent as soon as it gives them, ahead of the judge's
// verdicts (which come per batch, since a batch is judged with one LLM call), so one sees how the model behaves from the first
// question on. The feed is in memory and only for the audit's duration; a stream that connects later (or to a finished audit)
// starts with a snapshot built from the database, so a reconnect loses nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	trainingeval "github.com/deepblue-phoenix/gligabao/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	log "github.com/sirupsen/logrus"
)

const (
	auditStreamKeepalive = 15 * time.Second
	// auditStreamBuffer is how many events a watcher may be behind before it is dropped (it reconnects and is caught up by
	// a snapshot). A whole audit is a few thousand events at most, and a watcher normally keeps up.
	auditStreamBuffer = 1024
)

// Events of the feed (the SSE event name is Type).
const (
	// auditEventSnapshot: the audit and all its cases so far (stored ones, and answered ones awaiting the judge). First event.
	auditEventSnapshot = "snapshot"
	// auditEventAnswer: Cases has one case the model just answered (status "answered", or "failed" if the model failed).
	auditEventAnswer = "answer"
	// auditEventBatch: a batch was judged; Cases are its final cases and Audit the progress after it.
	auditEventBatch = "batch"
	// auditEventEnd: the audit is over (Audit has its final status); the stream closes after it.
	auditEventEnd = "end"
)

// AuditEvent is one event of the feed.
type AuditEvent struct {
	Type  string                             `json:"type"`
	Audit *relational.TrainedModelAuditRun   `json:"audit,omitempty"`
	Cases []relational.TrainedModelAuditCase `json:"cases,omitempty"`
}

type auditStreamKey struct {
	db    string
	audit int64
}

// auditStream is the feed of one running audit: the watchers, and the answers not judged yet (for a watcher that joins midway).
type auditStream struct {
	mu      sync.Mutex
	pending map[int]relational.TrainedModelAuditCase // by idx
	subs    map[chan AuditEvent]struct{}
	closed  bool
}

func newAuditStream() *auditStream {
	return &auditStream{pending: map[int]relational.TrainedModelAuditCase{}, subs: map[chan AuditEvent]struct{}{}}
}

// subscribe adds a watcher. It returns the answers awaiting the judge right now and the channel of the events after that
// moment (nil if the feed has ended). The channel is closed when the feed ends or when the watcher fell too far behind.
func (s *auditStream) subscribe() ([]relational.TrainedModelAuditCase, chan AuditEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil
	}
	pending := make([]relational.TrainedModelAuditCase, 0, len(s.pending))
	for _, it := range s.pending {
		pending = append(pending, it)
	}
	ch := make(chan AuditEvent, auditStreamBuffer)
	s.subs[ch] = struct{}{}
	return pending, ch
}

func (s *auditStream) unsubscribe(ch chan AuditEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
}

// send hands ev to every watcher; one whose buffer is full is dropped. s.mu must be held.
func (s *auditStream) send(ev AuditEvent) {
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
			delete(s.subs, ch)
			close(ch)
		}
	}
}

// answer announces a case the model answered.
func (s *auditStream) answer(it relational.TrainedModelAuditCase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if it.Status == "" {
		it.Status = relational.TrainedModelCaseAnswered
	}
	s.pending[it.Idx] = it
	s.send(AuditEvent{Type: auditEventAnswer, Cases: []relational.TrainedModelAuditCase{it}})
}

// batch announces the judged cases of a batch and the progress after it.
func (s *auditStream) batch(e relational.TrainedModelAuditRun, cases []relational.TrainedModelAuditCase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for _, it := range cases {
		delete(s.pending, it.Idx)
	}
	s.send(AuditEvent{Type: auditEventBatch, Audit: &e, Cases: cases})
}

// end announces the end of the audit and closes the feed.
func (s *auditStream) end(e relational.TrainedModelAuditRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.pending = nil
	s.send(AuditEvent{Type: auditEventEnd, Audit: &e})
	for ch := range s.subs {
		delete(s.subs, ch)
		close(ch)
	}
}

// auditCaseRow is the stored form of a case of the run.
func auditCaseRow(auditID int64, it trainingeval.CaseResult) relational.TrainedModelAuditCase {
	return relational.TrainedModelAuditCase{AuditRunID: auditID, Idx: it.Idx, Batch: it.Batch, Instruction: it.Instruction, Input: it.Input,
		Reference: it.Reference, Response: it.Response, Status: it.Status, Error: it.Error, Score: it.Score, Reason: it.Reason,
		InputTokens: it.InputTokens, OutputTokens: it.OutputTokens, DurationMS: it.DurationMS}
}

// EventsOf serves GET /v1/gb/training/audits/{id}/events[?db=<name>]: the live feed of an audit as Server-Sent Events. Every
// event's data is an AuditEvent JSON. The first is a "snapshot" (the audit and all of its cases so far: the judged and failed
// ones from the database, and the ones the model answered that the judge has not scored yet, with status "answered"); then, while
// the audit runs, an "answer" for every question the model answers, a "batch" when a batch is judged, and finally an "end" after
// which the stream closes. For an audit that is over, the stream is a snapshot and the "end". A client that reconnects gets a new
// snapshot, so it applies cases by idx. 404 for an unknown audit.
func (m *AuditManager) EventsOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(m.dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		// Subscribe before reading the database, so no event falls between the two; the overlap is harmless: the cases are
		// applied by idx, and the judged version wins.
		m.mu.Lock()
		stream := m.streams[auditStreamKey{n.Name, id}]
		m.mu.Unlock()
		var pending []relational.TrainedModelAuditCase
		var ch chan AuditEvent
		if stream != nil {
			pending, ch = stream.subscribe()
			if ch != nil {
				defer stream.unsubscribe(ch)
			}
		}
		ev, err := relational.GetTrainedModelAuditRun(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown audit")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read audit failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the audit")
			return
		}
		cases, err := relational.ListTrainedModelAuditCases(r.Context(), n.DB, id)
		if err != nil {
			log.WithError(err).Error("training: list audit cases failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the audit's cases")
			return
		}
		stored := make(map[int]bool, len(cases))
		for _, it := range cases {
			stored[it.Idx] = true
		}
		for _, it := range pending {
			if !stored[it.Idx] {
				cases = append(cases, it)
			}
		}
		sort.Slice(cases, func(i, j int) bool { return cases[i].Idx < cases[j].Idx })

		rc := http.NewResponseController(w)
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		send := func(e AuditEvent) bool {
			b, err := json.Marshal(e)
			if err != nil {
				return true
			}
			_, err = w.Write([]byte("event: " + e.Type + "\ndata: " + string(b) + "\n\n"))
			return err == nil && rc.Flush() == nil
		}
		if _, err := w.Write([]byte(": connected\n\n")); err != nil || rc.Flush() != nil {
			return
		}
		if !send(AuditEvent{Type: auditEventSnapshot, Audit: ev, Cases: cases}) {
			return
		}
		if ch == nil { // nothing is running this audit (any more): it is as stored
			send(AuditEvent{Type: auditEventEnd, Audit: ev})
			return
		}
		keepalive := time.NewTicker(auditStreamKeepalive)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-m.ctx.Done():
				return
			case e, ok := <-ch:
				if !ok { // the audit ended (the "end" event was sent before) or this watcher fell behind: it reconnects
					return
				}
				if !send(e) || e.Type == auditEventEnd {
					return
				}
			case <-keepalive.C:
				if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
					return
				}
			}
		}
	}
}

// publishEnd announces the end of audit auditID on its stream, with the audit as stored.
func (m *AuditManager) publishEnd(ctx context.Context, n Database, s *auditStream, auditID int64) {
	if ev, err := relational.GetTrainedModelAuditRun(ctx, n.DB, auditID); err == nil {
		s.end(*ev)
	} else {
		log.WithError(err).WithField("audit_run_id", auditID).Warn("training: read the ended audit for its stream failed")
		s.end(relational.TrainedModelAuditRun{ID: auditID})
	}
}
