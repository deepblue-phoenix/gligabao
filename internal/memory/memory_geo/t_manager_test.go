package memory_geo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recorder is a resolver that logs the order of the queries, and can be held back.
type recorder struct {
	mu    sync.Mutex
	order []string
	gate  chan struct{} // if set, the first query waits for it
	once  sync.Once
}

func (r *recorder) Name() string { return "rec" }
func (r *recorder) Resolve(_ context.Context,
	q Query) (*Place, error) {
	r.once.Do(func() {
		if r.gate != nil {
			<-r.gate
		}
	})
	r.mu.Lock()
	r.order = append(r.order, q.Text)
	r.mu.Unlock()
	return &Place{Latitude: 1, Longitude: 2}, nil
}

func TestManagerQueryBeforeRemember(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &recorder{gate: make(chan struct{})}
	m := NewManager(ctx, rec)

	var wg sync.WaitGroup
	wg.Add(1)
	// The first job holds the goroutine; while it does, two remember jobs and then a query are queued.
	done := func(context.Context, Outcome) {}
	if err := m.Enqueue(Job{Key: 1, Query: Query{Text: "r1"}, Done: done}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // r1 is being resolved
	for i, name := range []string{"r2", "r3"} {
		if err := m.Enqueue(Job{Key: int64(i + 2), Query: Query{Text: name}, Done: done}); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		defer wg.Done()
		if _, err := m.Resolve(ctx, Query{Text: "q1"}); err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	close(rec.gate)
	wg.Wait()
	if err := m.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"r1", "q1", "r2", "r3"}
	if len(rec.order) != len(want) {
		t.Fatalf("order %v, want %v", rec.order, want)
	}
	for i := range want {
		if rec.order[i] != want[i] {
			t.Fatalf("order %v, want %v: the query must be served before the queued remember jobs", rec.order, want)
		}
	}
}

func TestManagerDedupAndDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &recorder{gate: make(chan struct{})}
	m := NewManager(ctx, rec)
	var mu sync.Mutex
	var got []Outcome
	done := func(_ context.Context, o Outcome) {
		mu.Lock()
		got = append(got, o)
		mu.Unlock()
	}
	if err := m.Enqueue(Job{Key: 7, Query: Query{Text: "a"}, Done: done}); err != nil {
		t.Fatal(err)
	}
	if err := m.Enqueue(Job{Key: 7, Query: Query{Text: "a"}, Done: done}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second enqueue of the same key: %v", err)
	}
	if err := m.Enqueue(Job{Key: 0, Done: done}); err == nil {
		t.Fatal("a job without a key was accepted")
	}
	close(rec.gate)
	if err := m.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Place == nil || got[0].Err != nil || m.Pending() != 0 {
		t.Fatalf("outcomes %+v, pending %d", got, m.Pending())
	}
	// once resolved, the key can be queued again
	if err := m.Enqueue(Job{Key: 7, Query: Query{Text: "a"}, Done: done}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerQueueFullAndStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &recorder{gate: make(chan struct{})}
	m := NewManager(ctx, rec, WithQueueDepths(1, 2))
	done := func(context.Context, Outcome) {}
	_ = m.Enqueue(Job{Key: 1, Query: Query{Text: "a"}, Done: done})
	time.Sleep(50 * time.Millisecond) // taken by the goroutine, which waits on the gate
	_ = m.Enqueue(Job{Key: 2, Query: Query{Text: "b"}, Done: done})
	_ = m.Enqueue(Job{Key: 3, Query: Query{Text: "c"}, Done: done})
	if err := m.Enqueue(Job{Key: 4, Query: Query{Text: "d"}, Done: done}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("a full queue: %v", err)
	}
	cancel()
	close(rec.gate)
	m.Wait()
	if err := m.Enqueue(Job{Key: 5, Query: Query{Text: "e"}, Done: done}); !errors.Is(err, ErrStopped) {
		t.Fatalf("after stop: %v", err)
	}
	if _, err := m.Resolve(context.Background(), Query{Text: "q"}); !errors.Is(err, ErrStopped) {
		t.Fatalf("resolve after stop: %v", err)
	}
}
