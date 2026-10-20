package training

// The live feed of an eval ("watch it run" in the admin UI): what the eval does, as it does it. The model's answers are sent as soon
// as it gives them, ahead of the judge's verdicts (which come per batch, since a batch is judged with one LLM call), so one sees how
// the model behaves from the first question on. The feed is in memory and only for the eval's duration; a watcher that joins later
// (or watches a finished eval) starts with a snapshot built from the database, so a reconnect loses nothing.

import "sync"

// feedBuffer is how many events a watcher may be behind before it is dropped (it reconnects and is caught up by a snapshot). A
// whole eval is a few thousand events at most, and a watcher normally keeps up.
const feedBuffer = 1024

// The types of the events of the feed.
const (
	// EventSnapshot: the run and all its cases so far (stored ones, and answered ones awaiting the judge). First event.
	EventSnapshot = "snapshot"
	// EventAnswer: Cases has one case the model just answered (running: awaiting the judge; or errored if the model failed).
	EventAnswer = "answer"
	// EventBatch: a batch was judged; Cases are its final cases and Run the progress after it.
	EventBatch = "batch"
	// EventEnd: the eval is over (Run has its final status); the feed closes after it.
	EventEnd = "end"
)

// Event is one event of the feed (the name of the SSE event is Type).
type Event struct {
	Type  string     `json:"type"`
	Run   *RunView   `json:"run,omitempty"`
	Cases []CaseView `json:"cases,omitempty"`
}

// Feed is the feed of one running eval: the watchers, and the answers not judged yet (for a watcher that joins midway).
type Feed struct {
	mu      sync.Mutex
	pending map[int]CaseView // by idx
	subs    map[chan Event]struct{}
	closed  bool
}

// NewFeed makes the feed of an eval that starts.
func NewFeed() *Feed {
	return &Feed{pending: map[int]CaseView{}, subs: map[chan Event]struct{}{}}
}

// Subscribe adds a watcher. It returns the answers awaiting the judge right now and the channel of the events after that moment
// (nil if the feed has ended). The channel is closed when the feed ends or when the watcher fell too far behind.
func (f *Feed) Subscribe() ([]CaseView, chan Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, nil
	}
	pending := make([]CaseView, 0, len(f.pending))
	for _, c := range f.pending {
		pending = append(pending, c)
	}
	ch := make(chan Event, feedBuffer)
	f.subs[ch] = struct{}{}
	return pending, ch
}

// Unsubscribe removes a watcher and closes its channel.
func (f *Feed) Unsubscribe(ch chan Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
}

// send hands ev to every watcher; one whose buffer is full is dropped. f.mu must be held.
func (f *Feed) send(ev Event) {
	for ch := range f.subs {
		select {
		case ch <- ev:
		default:
			delete(f.subs, ch)
			close(ch)
		}
	}
}

// Answer announces a case the model answered.
func (f *Feed) Answer(c CaseView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.pending[c.Idx] = c
	f.send(Event{Type: EventAnswer, Cases: []CaseView{c}})
}

// Batch announces the cases of a batch that was judged and the progress after it.
func (f *Feed) Batch(r RunView, cases []CaseView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	for _, c := range cases {
		delete(f.pending, c.Idx)
	}
	f.send(Event{Type: EventBatch, Run: &r, Cases: cases})
}

// End announces the end of the eval and closes the feed.
func (f *Feed) End(r RunView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	f.pending = nil
	f.send(Event{Type: EventEnd, Run: &r})
	for ch := range f.subs {
		delete(f.subs, ch)
		close(ch)
	}
}
