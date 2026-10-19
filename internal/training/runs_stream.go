package training

// The live feed of a run ("watch it run" in the admin UI): GET /v1/gb/training/evals|audits/{id}/events is a Server-Sent Events
// stream of what the run does as it does it, the same for evals and audits. The model's answers are sent as soon as it gives
// them, ahead of the judge's verdicts (which come per batch, since a batch is judged with one LLM call), so one sees how the model
// behaves from the first question on. The feed is in memory and only for the run's duration; a stream that connects later (or to a
// finished run) starts with a snapshot built from the database, so a reconnect loses nothing.

import (
	"sync"

	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
)

const (
	// runStreamBuffer is how many events a watcher may be behind before it is dropped (it reconnects and is caught up by
	// a snapshot). A whole run is a few thousand events at most, and a watcher normally keeps up.
	runStreamBuffer = 1024
)

// Events of the feed (the SSE event name is Type).
const (
	// RunEventSnapshot: the run and all its cases so far (stored ones, and answered ones awaiting the judge). First event.
	RunEventSnapshot = "snapshot"
	// RunEventAnswer: Cases has one case the model just answered (status "answered", or "error" if the model failed).
	RunEventAnswer = "answer"
	// RunEventBatch: a batch was judged; Cases are its final cases and Run the progress after it.
	RunEventBatch = "batch"
	// RunEventEnd: the run is over (Run has its final status); the stream closes after it.
	RunEventEnd = "end"
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

// RunStream is the feed of one running run: the watchers, and the answers not judged yet (for a watcher that joins midway).
type RunStream struct {
	mu      sync.Mutex
	pending map[int]evalsdb.TrainedModelCase // by idx
	subs    map[chan RunEvent]struct{}
	closed  bool
}

func NewRunStream() *RunStream {
	return &RunStream{pending: map[int]evalsdb.TrainedModelCase{}, subs: map[chan RunEvent]struct{}{}}
}

// Subscribe adds a watcher. It returns the answers awaiting the judge right now and the channel of the events after that
// moment (nil if the feed has ended). The channel is closed when the feed ends or when the watcher fell too far behind.
func (s *RunStream) Subscribe() ([]evalsdb.TrainedModelCase, chan RunEvent) {
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

func (s *RunStream) Unsubscribe(ch chan RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
}

// send hands ev to every watcher; one whose buffer is full is dropped. s.mu must be held.
func (s *RunStream) send(ev RunEvent) {
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
			delete(s.subs, ch)
			close(ch)
		}
	}
}

// Answer announces a case the model answered.
func (s *RunStream) Answer(it evalsdb.TrainedModelCase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if it.Status == "" {
		it.Status = evalsdb.TrainedModelCaseAnswered
	}
	s.pending[it.Idx] = it
	s.send(RunEvent{Type: RunEventAnswer, Cases: []evalsdb.TrainedModelCase{it}})
}

// Batch announces the judged cases of a batch and the progress after it.
func (s *RunStream) Batch(r evalsdb.TrainedModelRun, cases []evalsdb.TrainedModelCase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for _, it := range cases {
		delete(s.pending, it.Idx)
	}
	s.send(RunEvent{Type: RunEventBatch, Run: &r, Cases: cases})
}

// End announces the end of the run and closes the feed.
func (s *RunStream) End(r evalsdb.TrainedModelRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.pending = nil
	s.send(RunEvent{Type: RunEventEnd, Run: &r})
	for ch := range s.subs {
		delete(s.subs, ch)
		close(ch)
	}
}
