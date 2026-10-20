package training_audits_test

import (
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/training_audits"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
)

func TestFeed(t *testing.T) {
	f := training_audits.NewFeed()
	f.Answer(core.AuditCaseRun{Idx: 0, Response: "a0"})
	f.Answer(core.AuditCaseRun{Idx: 1, Status: core.StatusErrored, Error: "model: x"})

	// A watcher that joins midway gets the answers that wait for the judge, and the events from then on.
	pending, ch := f.Subscribe()
	if len(pending) != 2 || ch == nil {
		t.Fatalf("pending %+v", pending)
	}
	f.Answer(core.AuditCaseRun{Idx: 2})
	if e := <-ch; e.Type != training_audits.EventAnswer || e.CaseRuns[0].Idx != 2 || e.CaseRuns[0].Status != core.StatusRunning {
		t.Errorf("answer event: %+v", e)
	}

	// A batch settles its cases: they are not pending any more, and the run in the event has no case runs.
	f.Batch(core.AuditRun{ID: 1, CaseRuns: []core.AuditCaseRun{{}}}, []core.AuditCaseRun{{Idx: 0}, {Idx: 1}})
	if e := <-ch; e.Type != training_audits.EventBatch || len(e.CaseRuns) != 2 || e.Run.ID != 1 || e.Run.CaseRuns != nil {
		t.Errorf("batch event: %+v", e)
	}
	if pending, _ := f.Subscribe(); len(pending) != 1 || pending[0].Idx != 2 {
		t.Errorf("pending after the batch: %+v", pending)
	}

	// The end closes the channels and the feed.
	f.End(core.AuditRun{ID: 1, Status: core.StatusCompleted})
	if e := <-ch; e.Type != training_audits.EventEnd || e.Run.Status != core.StatusCompleted {
		t.Errorf("end event: %+v", e)
	}
	if _, ok := <-ch; ok {
		t.Error("channel open after the end")
	}
	if p, c := f.Subscribe(); p != nil || c != nil {
		t.Error("subscribed to a feed that ended")
	}
	f.Answer(core.AuditCaseRun{Idx: 9}) // ignored
	f.Unsubscribe(ch)                   // harmless
}

func TestFeedDropsASlowWatcher(t *testing.T) {
	f := training_audits.NewFeed()
	_, ch := f.Subscribe()
	for i := 0; i < 5000; i++ {
		f.Answer(core.AuditCaseRun{Idx: i})
	}
	n := 0
	for range ch { // closed once the buffer overflowed
		n++
	}
	if n == 0 || n >= 5000 {
		t.Errorf("slow watcher got %d events", n)
	}
}
