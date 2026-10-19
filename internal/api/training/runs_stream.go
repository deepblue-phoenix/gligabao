package training

// The live feed of a run ("watch it run" in the admin UI): GET /v1/gb/training/evals|audits/{id}/events is a Server-Sent Events
// stream of what the run does as it does it, the same for evals and audits. The model's answers are sent as soon as it gives
// them, ahead of the judge's verdicts (which come per batch, since a batch is judged with one LLM call), so one sees how the model
// behaves from the first question on. The feed is in memory and only for the run's duration; a stream that connects later (or to a
// finished run) starts with a snapshot built from the database, so a reconnect loses nothing.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	log "github.com/sirupsen/logrus"
)

const (
	runStreamKeepalive = 15 * time.Second
	// runStreamBuffer is how many events a watcher may be behind before it is dropped (it reconnects and is caught up by
	// a snapshot). A whole run is a few thousand events at most, and a watcher normally keeps up.
	runStreamBuffer = 1024
)

// Events of the feed (the SSE event name is Type).
const (
	// runEventSnapshot: the run and all its cases so far (stored ones, and answered ones awaiting the judge). First event.
	runEventSnapshot = "snapshot"
	// runEventAnswer: Cases has one case the model just answered (status "answered", or "error" if the model failed).
	runEventAnswer = "answer"
	// runEventBatch: a batch was judged; Cases are its final cases and Run the progress after it.
	runEventBatch = "batch"
	// runEventEnd: the run is over (Run has its final status); the stream closes after it.
	runEventEnd = "end"
)

// RunEvent is one event of the feed, for an eval and for an audit alike.
type RunEvent struct {
	Type  string                     `json:"type"`
	Run   *evalsdb.TrainedModelRun   `json:"run,omitempty"`
	Cases []evalsdb.TrainedModelCase `json:"cases,omitempty"`
}

type streamKey struct {
	db  string
	run int64
}

// runStream is the feed of one running run: the watchers, and the answers not judged yet (for a watcher that joins midway).
type runStream struct {
	mu      sync.Mutex
	pending map[int]evalsdb.TrainedModelCase // by idx
	subs    map[chan RunEvent]struct{}
	closed  bool
}

func newRunStream() *runStream {
	return &runStream{pending: map[int]evalsdb.TrainedModelCase{}, subs: map[chan RunEvent]struct{}{}}
}

// subscribe adds a watcher. It returns the answers awaiting the judge right now and the channel of the events after that
// moment (nil if the feed has ended). The channel is closed when the feed ends or when the watcher fell too far behind.
func (s *runStream) subscribe() ([]evalsdb.TrainedModelCase, chan RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil
	}
	pending := make([]evalsdb.TrainedModelCase, 0, len(s.pending))
	for _, it := range s.pending {
		pending = append(pending, it)
	}
	ch := make(chan RunEvent, runStreamBuffer)
	s.subs[ch] = struct{}{}
	return pending, ch
}

func (s *runStream) unsubscribe(ch chan RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
}

// send hands ev to every watcher; one whose buffer is full is dropped. s.mu must be held.
func (s *runStream) send(ev RunEvent) {
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
func (s *runStream) answer(it evalsdb.TrainedModelCase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if it.Status == "" {
		it.Status = evalsdb.TrainedModelCaseAnswered
	}
	s.pending[it.Idx] = it
	s.send(RunEvent{Type: runEventAnswer, Cases: []evalsdb.TrainedModelCase{it}})
}

// batch announces the judged cases of a batch and the progress after it.
func (s *runStream) batch(r evalsdb.TrainedModelRun, cases []evalsdb.TrainedModelCase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for _, it := range cases {
		delete(s.pending, it.Idx)
	}
	s.send(RunEvent{Type: runEventBatch, Run: &r, Cases: cases})
}

// end announces the end of the run and closes the feed.
func (s *runStream) end(r evalsdb.TrainedModelRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.pending = nil
	s.send(RunEvent{Type: runEventEnd, Run: &r})
	for ch := range s.subs {
		delete(s.subs, ch)
		close(ch)
	}
}

// EventsOf serves GET /v1/gb/training/evals|audits/{id}/events[?db=<name>]: the live feed of a run as Server-Sent Events. Every
// event's data is a RunEvent JSON. The first is a "snapshot" (the run and all of its cases so far: the judged and errored
// ones from the database, and the ones the model answered that the judge has not scored yet, with status "answered"); then, while
// the run goes on, an "answer" for every question the model answers, a "batch" when a batch is judged, and finally an "end" after
// which the stream closes. For a run that is over, the stream is a snapshot and the "end". A client that reconnects gets a new
// snapshot, so it applies cases by idx. 404 for an unknown run.
func (m *runManager) EventsOf() http.HandlerFunc {
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
		stream := m.streams[streamKey{n.Name, id}]
		m.mu.Unlock()
		var pending []evalsdb.TrainedModelCase
		var ch chan RunEvent
		if stream != nil {
			pending, ch = stream.subscribe()
			if ch != nil {
				defer stream.unsubscribe(ch)
			}
		}
		run, ok := m.readRun(w, r, n, id)
		if !ok {
			return
		}
		cases, err := m.ops.cases(r.Context(), n.DB, id)
		if err != nil {
			log.WithError(err).Errorf("training: list %s cases failed", m.ops.noun())
			httputil.Error(w, http.StatusInternalServerError, "failed to read the "+m.ops.noun()+"'s cases")
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
		send := func(e RunEvent) bool {
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
		if !send(RunEvent{Type: runEventSnapshot, Run: run, Cases: cases}) {
			return
		}
		if ch == nil { // nothing is running this run (any more): it is as stored
			send(RunEvent{Type: runEventEnd, Run: run})
			return
		}
		keepalive := time.NewTicker(runStreamKeepalive)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-m.ctx.Done():
				return
			case e, ok := <-ch:
				if !ok { // the run ended (the "end" event was sent before) or this watcher fell behind: it reconnects
					return
				}
				if !send(e) || e.Type == runEventEnd {
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

// publishEnd announces the end of run runID on its stream, with the run as stored.
func (m *runManager) publishEnd(ctx context.Context, n Database, s *runStream, runID int64) {
	if run, err := m.ops.get(ctx, n.DB, runID); err == nil {
		s.end(*run)
	} else {
		log.WithError(err).WithField("run_id", runID).Warnf("training: read the ended %s for its stream failed", m.ops.noun())
		s.end(evalsdb.TrainedModelRun{ID: runID, Kind: m.ops.kind})
	}
}
