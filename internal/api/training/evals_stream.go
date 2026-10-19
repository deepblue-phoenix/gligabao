package training

// The live feed of an eval ("watch it run" in the admin UI): GET /v1/gb/training/evals/{id}/events is a Server-Sent Events
// stream of what the eval does as it does it. The model's answers are sent as soon as it gives them, ahead of the judge's
// verdicts (which come per batch, since a batch is judged with one LLM call), so one sees how the model behaves from the first
// question on. The feed is in memory and only for the eval's duration; a stream that connects later (or to a finished eval)
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
	evalStreamKeepalive = 15 * time.Second
	// evalStreamBuffer is how many events a watcher may be behind before it is dropped (it reconnects and is caught up by
	// a snapshot). A whole eval is a few thousand events at most, and a watcher normally keeps up.
	evalStreamBuffer = 1024
)

// Events of the feed (the SSE event name is Type).
const (
	// evalEventSnapshot: the eval and all its cases so far (stored ones, and answered ones awaiting the judge). First event.
	evalEventSnapshot = "snapshot"
	// evalEventAnswer: Cases has one case the model just answered (status "answered", or "failed" if the model failed).
	evalEventAnswer = "answer"
	// evalEventBatch: a batch was judged; Cases are its final cases and Eval the progress after it.
	evalEventBatch = "batch"
	// evalEventEnd: the eval is over (Eval has its final status); the stream closes after it.
	evalEventEnd = "end"
)

// EvalEvent is one event of the feed.
type EvalEvent struct {
	Type  string                              `json:"type"`
	Eval  *relational.TrainedModelEvalRun     `json:"eval,omitempty"`
	Cases []relational.TrainedModelCaseResult `json:"cases,omitempty"`
}

type evalStreamKey struct {
	db   string
	eval int64
}

// evalStream is the feed of one running eval: the watchers, and the answers not judged yet (for a watcher that joins midway).
type evalStream struct {
	mu      sync.Mutex
	pending map[int]relational.TrainedModelCaseResult // by idx
	subs    map[chan EvalEvent]struct{}
	closed  bool
}

func newEvalStream() *evalStream {
	return &evalStream{pending: map[int]relational.TrainedModelCaseResult{}, subs: map[chan EvalEvent]struct{}{}}
}

// subscribe adds a watcher. It returns the answers awaiting the judge right now and the channel of the events after that
// moment (nil if the feed has ended). The channel is closed when the feed ends or when the watcher fell too far behind.
func (s *evalStream) subscribe() ([]relational.TrainedModelCaseResult, chan EvalEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil
	}
	pending := make([]relational.TrainedModelCaseResult, 0, len(s.pending))
	for _, it := range s.pending {
		pending = append(pending, it)
	}
	ch := make(chan EvalEvent, evalStreamBuffer)
	s.subs[ch] = struct{}{}
	return pending, ch
}

func (s *evalStream) unsubscribe(ch chan EvalEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
}

// send hands ev to every watcher; one whose buffer is full is dropped. s.mu must be held.
func (s *evalStream) send(ev EvalEvent) {
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
func (s *evalStream) answer(it relational.TrainedModelCaseResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if it.Status == "" {
		it.Status = relational.TrainedModelCaseAnswered
	}
	s.pending[it.Idx] = it
	s.send(EvalEvent{Type: evalEventAnswer, Cases: []relational.TrainedModelCaseResult{it}})
}

// batch announces the judged cases of a batch and the progress after it.
func (s *evalStream) batch(e relational.TrainedModelEvalRun, cases []relational.TrainedModelCaseResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for _, it := range cases {
		delete(s.pending, it.Idx)
	}
	s.send(EvalEvent{Type: evalEventBatch, Eval: &e, Cases: cases})
}

// end announces the end of the eval and closes the feed.
func (s *evalStream) end(e relational.TrainedModelEvalRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.pending = nil
	s.send(EvalEvent{Type: evalEventEnd, Eval: &e})
	for ch := range s.subs {
		delete(s.subs, ch)
		close(ch)
	}
}

// caseResultRow is the stored form of a case of the run. The engine's statuses (trainingeval.CaseJudged, CaseFailed, "" for an
// answer the judge has not scored yet) are those of TrainedModelCaseResult, so the status is copied as it is.
func caseResultRow(evalID int64, it trainingeval.CaseResult) relational.TrainedModelCaseResult {
	return relational.TrainedModelCaseResult{EvalRunID: evalID, Idx: it.Idx, Batch: it.Batch, Instruction: it.Instruction, Input: it.Input,
		Reference: it.Reference, Response: it.Response, Status: it.Status, Error: it.Error, Score: it.Score, Reason: it.Reason,
		InputTokens: it.InputTokens, OutputTokens: it.OutputTokens, DurationMS: it.DurationMS}
}

// EventsOf serves GET /v1/gb/training/evals/{id}/events[?db=<name>]: the live feed of an eval as Server-Sent Events. Every
// event's data is an EvalEvent JSON. The first is a "snapshot" (the eval and all of its cases so far: the judged and failed
// ones from the database, and the ones the model answered that the judge has not scored yet, with status "answered"); then, while
// the eval runs, an "answer" for every question the model answers, a "batch" when a batch is judged, and finally an "end" after
// which the stream closes. For an eval that is over, the stream is a snapshot and the "end". A client that reconnects gets a new
// snapshot, so it applies cases by idx. 404 for an unknown eval.
func (m *EvalManager) EventsOf() http.HandlerFunc {
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
		stream := m.streams[evalStreamKey{n.Name, id}]
		m.mu.Unlock()
		var pending []relational.TrainedModelCaseResult
		var ch chan EvalEvent
		if stream != nil {
			pending, ch = stream.subscribe()
			if ch != nil {
				defer stream.unsubscribe(ch)
			}
		}
		ev, err := relational.GetTrainedModelEvalRun(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown eval")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read eval failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the eval")
			return
		}
		cases, err := relational.ListTrainedModelCaseResults(r.Context(), n.DB, id)
		if err != nil {
			log.WithError(err).Error("training: list eval cases failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the eval's cases")
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
		send := func(e EvalEvent) bool {
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
		if !send(EvalEvent{Type: evalEventSnapshot, Eval: ev, Cases: cases}) {
			return
		}
		if ch == nil { // nothing is running this eval (any more): it is as stored
			send(EvalEvent{Type: evalEventEnd, Eval: ev})
			return
		}
		keepalive := time.NewTicker(evalStreamKeepalive)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-m.ctx.Done():
				return
			case e, ok := <-ch:
				if !ok { // the eval ended (the "end" event was sent before) or this watcher fell behind: it reconnects
					return
				}
				if !send(e) || e.Type == evalEventEnd {
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

// publishEnd announces the end of eval evalID on its stream, with the eval as stored.
func (m *EvalManager) publishEnd(ctx context.Context, n Database, s *evalStream, evalID int64) {
	if ev, err := relational.GetTrainedModelEvalRun(ctx, n.DB, evalID); err == nil {
		s.end(*ev)
	} else {
		log.WithError(err).WithField("eval_run_id", evalID).Warn("training: read the ended eval for its stream failed")
		s.end(relational.TrainedModelEvalRun{ID: evalID})
	}
}
