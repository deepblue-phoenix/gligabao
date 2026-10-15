package memory_test

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// gateLLM answers like an empty extraction, but each call waits for a release, and it records how many calls ran at once.
type gateLLM struct {
	release chan struct{}
	entered chan struct{}
	cur     atomic.Int32
	max     atomic.Int32
}

func (g *gateLLM) Complete(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	n := g.cur.Add(1)
	defer g.cur.Add(-1)
	for {
		m := g.max.Load()
		if n <= m || g.max.CompareAndSwap(m, n) {
			break
		}
	}
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &ai.Response{Text: `{"entities":[],"edges":[]}`, StopReason: "end_turn", Model: "fake-model"}, nil
}

func TestRememberManager(t *testing.T) { dbtest.Run(t, testRememberManager) }

// The manager returns the run ID as soon as the request is queued, runs the requests one at a time in order, and sends the
// outcome of each when its run is over.
func testRememberManager(t *testing.T, d *sql.DB) {
	llm := &gateLLM{release: make(chan struct{}), entered: make(chan struct{}, 10)}
	mgr := memory_remember.NewManager(t.Context(), d, llm)

	var ids [2]int64
	var outs [2]chan memory_remember.Outcome
	for i := range ids {
		outs[i] = make(chan memory_remember.Outcome, 1)
		id, err := mgr.Submit(memory_remember.Request{UserID: "u1", Dataset: "work", Text: "text", Outcome: outs[i]})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	if ids[0] == 0 || ids[1] <= ids[0] {
		t.Fatalf("run IDs = %v", ids)
	}
	<-llm.entered // the first run is in progress; the second is queued behind it
	run, err := mgr.Status(t.Context(), ids[1])
	if err != nil || run.Status != relational.RememberStarted || len(stageNames(run, relational.RememberStarted)) != 0 {
		t.Errorf("queued run = %+v, %v", run, err)
	}
	select {
	case <-outs[0]:
		t.Fatal("run 1 completed before it was released")
	case <-llm.entered:
		t.Fatal("run 2 started while run 1 was running")
	case <-time.After(50 * time.Millisecond):
	}

	llm.release <- struct{}{}
	if o := <-outs[0]; !o.OK() || o.Result == nil || o.Result.RememberRunID != ids[0] {
		t.Fatalf("outcome 1 = %+v", o)
	}
	<-llm.entered
	llm.release <- struct{}{}
	if o := <-outs[1]; !o.OK() || o.Result == nil || o.Result.RememberRunID != ids[1] {
		t.Fatalf("outcome 2 = %+v", o)
	}
	if llm.max.Load() != 1 {
		t.Errorf("%d LLM calls ran at once", llm.max.Load())
	}
	for _, id := range ids {
		if run, err := mgr.Status(t.Context(), id); err != nil || run.Status != relational.RememberSucceeded {
			t.Errorf("run %d = %+v, %v", id, run, err)
		}
	}
}

func TestRememberManagerCancelledAndInvalid(t *testing.T) {
	dbtest.Run(t, testRememberManagerCancelledAndInvalid)
}

// A request cancelled while queued fails without running; an invalid one is refused; a manager that is closed refuses
// and fails what is queued.
func testRememberManagerCancelledAndInvalid(t *testing.T, d *sql.DB) {
	llm := &gateLLM{release: make(chan struct{}), entered: make(chan struct{}, 10)}
	ctx, stop := context.WithCancel(context.Background())
	mgr := memory_remember.NewManager(ctx, d, llm)

	if _, err := mgr.Submit(memory_remember.Request{UserID: "", Text: "x"}); err == nil {
		t.Error("a request without a user was queued")
	}
	if _, err := mgr.Submit(memory_remember.Request{UserID: "u1", Text: "x", Outcome: make(chan memory_remember.Outcome)}); err == nil {
		t.Error("an unbuffered outcome channel was accepted")
	}

	first := make(chan memory_remember.Outcome, 1)
	if _, err := mgr.Submit(memory_remember.Request{UserID: "u1", Text: "first", Outcome: first}); err != nil {
		t.Fatal(err)
	}
	<-llm.entered
	reqCtx, cancel := context.WithCancel(context.Background())
	cancelled, third := make(chan memory_remember.Outcome, 1), make(chan memory_remember.Outcome, 1)
	cid, err := mgr.Submit(memory_remember.Request{UserID: "u1", Text: "second", Ctx: reqCtx, Outcome: cancelled})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	closedID, err := mgr.Submit(memory_remember.Request{UserID: "u1", Text: "third", Outcome: third})
	if err != nil {
		t.Fatal(err)
	}

	stop() // cancels the run in progress and closes the manager
	mgr.Wait()
	for _, ch := range []chan memory_remember.Outcome{first, cancelled, third} {
		if o := <-ch; o.OK() {
			t.Error("a run succeeded after the manager was closed")
		}
	}
	for _, id := range []int64{cid, closedID} {
		run, err := relational.GetRememberRun(context.Background(), d, id)
		if err != nil || run.Status != relational.RememberFailed || run.Error == nil || run.FinishedAt == nil {
			t.Errorf("run %d = %+v, %v", id, run, err)
		}
	}
	if _, err := mgr.Submit(memory_remember.Request{UserID: "u1", Text: "x"}); !errors.Is(err, memory_remember.ErrManagerClosed) {
		t.Errorf("submit after close: %v", err)
	}
}

func TestRememberManagerQueueHooks(t *testing.T) { dbtest.Run(t, testRememberManagerQueueHooks) }

// With WithQueue the manager puts the run IDs in the embedder's queue and takes them out of it; an ID it does not know is skipped.
func testRememberManagerQueueHooks(t *testing.T, d *sql.DB) {
	ext := make(chan int64, 10)
	var enqueued, dequeued atomic.Int32
	mgr := memory_remember.NewManager(t.Context(), d, &gateLLM{release: closedChan(), entered: make(chan struct{}, 10)},
		memory_remember.WithQueue(memory_remember.QueueHooks{
			Enqueue: func(ctx context.Context, id int64) error { enqueued.Add(1); ext <- id; return nil },
			Dequeue: func(ctx context.Context) (int64, error) {
				select {
				case id := <-ext:
					dequeued.Add(1)
					return id, nil
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			},
		}))
	ext <- 424242 // not a request of this process
	out := make(chan memory_remember.Outcome, 1)
	id, err := mgr.Submit(memory_remember.Request{UserID: "u1", Text: "x", Outcome: out})
	if err != nil {
		t.Fatal(err)
	}
	if o := <-out; !o.OK() || o.Result.RememberRunID != id {
		t.Fatalf("outcome = %+v", o)
	}
	if enqueued.Load() != 1 || dequeued.Load() != 2 {
		t.Errorf("enqueued %d, dequeued %d; want 1 and 2", enqueued.Load(), dequeued.Load())
	}

	// A queue that refuses the run: Submit fails, and the run is failed.
	refuse := memory_remember.NewManager(t.Context(), d, &gateLLM{release: closedChan(), entered: make(chan struct{}, 10)},
		memory_remember.WithQueue(memory_remember.QueueHooks{
			Enqueue: func(context.Context, int64) error { return errors.New("queue down") },
			Dequeue: func(ctx context.Context) (int64, error) { <-ctx.Done(); return 0, ctx.Err() },
		}))
	if _, err := refuse.Submit(memory_remember.Request{UserID: "u2", Text: "x"}); err == nil || err.Error() != "queue down" {
		t.Errorf("submit to a refusing queue: %v", err)
	}
	runs, _, err := relational.ListRememberRuns(context.Background(), d, relational.RememberRunFilter{UserID: "u2"}, 10, 0)
	if err != nil || len(runs) != 1 || runs[0].Status != relational.RememberFailed {
		t.Errorf("runs = %+v, %v", runs, err)
	}
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}
