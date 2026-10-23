package training_audits_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/training_audits"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/audittest"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
)

func TestAuditATrainedModel(t *testing.T) { audittest.Run(t, testAuditATrainedModel) }

func testAuditATrainedModel(t *testing.T, d *sql.DB, ops training_audits.Ops) {
	ctx := context.Background()
	mid := audittest.RecordModel(t, d, "jobaudit1", "mine.jsonl")
	sc, judge := &audittest.Sidecar{Wrong: map[string]bool{"q3": true}}, &audittest.Judge{}
	m := training_audits.NewManager(ctx, d, ops, []string{audittest.DatasetDir(t, 5)}, sc, judge)

	temp := 0.2
	started, err := m.Start(ctx, mid, training_audits.Options{BatchSize: 2, JudgeModel: "my-judge", MaxTokens: 77, Temperature: &temp})
	if err != nil {
		t.Fatal(err)
	}
	if started.ID == 0 || started.Status != core.StatusRunning || started.TrainedModelID != mid || started.Dataset != "mine" || len(started.DatasetSHA256) != 64 ||
		started.Counts != (core.Counts{Total: 5, Pending: 5}) || started.StartedAt == nil || started.FinishedAt != nil ||
		started.Params["batch_size"] != 2.0 || started.Params["max_tokens"] != 77.0 || started.Params["judge_model"] != "my-judge" {
		t.Fatalf("started: %+v", started)
	}
	m.Wait()

	run, err := m.GetRunWithCaseRuns(ctx, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != core.StatusCompleted || run.FinishedAt == nil || run.Error != "" || run.Counts != (core.Counts{Total: 5, Completed: 5}) {
		t.Fatalf("finished: %+v", run)
	}
	stats := core.AuditStats{JudgeModel: "judge-model", NumAnswered: 5, NumJudged: 5, NumCorrect: 4, NumIncorrect: 1, ScoreSum: 21, NumBatches: 3,
		JudgeCalls: 3, JudgeInputTokens: 150, JudgeOutputTokens: 60}
	if run.AuditStats != stats || run.CostUSD != 1.5 {
		t.Fatalf("stats: %+v cost %v", run.AuditStats, run.CostUSD)
	}
	if fmt.Sprint(judge.Sizes) != "[2 2 1]" || judge.Model != "my-judge" {
		t.Fatalf("judge: sizes %v, model %q", judge.Sizes, judge.Model)
	}
	cs := run.CaseRuns
	if len(cs) != 5 || cs[3].Response != "wrong" || cs[3].JudgeScore == nil || *cs[3].JudgeScore != 1 || cs[0].Reference != "a0" || cs[0].JudgeReason != "because" ||
		cs[0].Status != core.StatusCompleted || cs[0].RunID != started.ID || cs[4].Batch != 3 || cs[0].Batch != 1 {
		t.Fatalf("case runs: %+v", cs)
	}
	for i, c := range cs {
		if c.Idx != i {
			t.Errorf("case run %d has idx %d", i, c.Idx)
		}
	}
	// The model got the dataset's questions on its own weights, with the settings of the audit.
	reqs := sc.Requests()
	if len(reqs) != 5 || reqs[0].Prompt != "q0" || reqs[0].ModelPath != "trained/jobaudit1" || reqs[0].ModelSize != "12b" ||
		reqs[0].MaxTokens != 77 || reqs[0].Temperature == nil || *reqs[0].Temperature != 0.2 {
		t.Fatalf("sidecar requests: %+v", reqs)
	}
	// The judge's calls are recorded like the other LLM calls.
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM gb_inferences WHERE type = 'training_audit_judge' AND stage = 'judge'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("recorded judge calls: %d %v", n, err)
	}

	// The model's audits are listed, newest first, and run again.
	second, err := m.Start(ctx, mid, training_audits.Options{Limit: 3})
	if err != nil || second.Counts.Total != 3 {
		t.Fatalf("second audit: %+v %v", second, err)
	}
	m.Wait()
	list, err := m.ListRuns(ctx, mid, 10)
	if err != nil || len(list) != 2 || list[0].ID != second.ID || list[0].Status != core.StatusCompleted || list[0].CaseRuns != nil || list[1].ID != started.ID {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list, err = m.ListRuns(ctx, mid, 1); err != nil || len(list) != 1 {
		t.Fatalf("limited list: %+v %v", list, err)
	}
}

func TestAuditRefusals(t *testing.T) { audittest.Run(t, testAuditRefusals) }

func testAuditRefusals(t *testing.T, d *sql.DB, ops training_audits.Ops) {
	ctx := context.Background()
	dir := audittest.DatasetDir(t, 2)
	ok := audittest.RecordModel(t, d, "j-ok", "mine.jsonl")
	noDataset := audittest.RecordModel(t, d, "j-none", "")
	gone := audittest.RecordModel(t, d, "j-gone", "deleted.jsonl")
	m := training_audits.NewManager(ctx, d, ops, []string{dir}, &audittest.Sidecar{}, &audittest.Judge{})

	for _, tc := range []struct {
		name  string
		model int64
		opts  training_audits.Options
		kind  training_audits.Kind
		msg   string
	}{
		{"unknown model", 999, training_audits.Options{}, training_audits.KindNotFound, "unknown trained model"},
		{"default dataset", noDataset, training_audits.Options{}, training_audits.KindInvalid, "default dataset"},
		{"dataset gone", gone, training_audits.Options{}, training_audits.KindInvalid, "not in the training datasets directory"},
		{"bad batch size", ok, training_audits.Options{BatchSize: 99}, training_audits.KindInvalid, "batch size"},
		{"negative limit", ok, training_audits.Options{Limit: -1}, training_audits.KindInvalid, "limit"},
		{"too many tokens", ok, training_audits.Options{MaxTokens: 1 << 20}, training_audits.KindInvalid, "max_tokens"},
	} {
		_, err := m.Start(ctx, tc.model, tc.opts)
		var e *training_audits.Error
		if !errors.As(err, &e) || e.Kind != tc.kind || !strings.Contains(e.Msg, tc.msg) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if _, err := m.ListRuns(ctx, 999, 10); !isKind(err, training_audits.KindNotFound) {
		t.Errorf("list of an unknown model: %v", err)
	}
	if _, err := m.GetRun(ctx, 999); !isKind(err, training_audits.KindNotFound) {
		t.Errorf("get of an unknown audit: %v", err)
	}
	if _, err := m.CancelRun(ctx, 999); !isKind(err, training_audits.KindNotFound) {
		t.Errorf("cancel of an unknown audit: %v", err)
	}
	if _, err := m.Watch(ctx, 999); !isKind(err, training_audits.KindNotFound) {
		t.Errorf("watch of an unknown audit: %v", err)
	}
	if rs, _ := ops.Store.ListAuditRuns(ctx, d, 0, 0); len(rs) != 0 {
		t.Errorf("a refused audit was recorded: %+v", rs)
	}

	// Without a sidecar or an LLM nothing starts.
	if _, err := training_audits.NewManager(ctx, d, ops, []string{dir}, nil, &audittest.Judge{}).Start(ctx, ok, training_audits.Options{}); !isKind(err, training_audits.KindUnavailable) {
		t.Errorf("no sidecar: %v", err)
	}
	if _, err := training_audits.NewManager(ctx, d, ops, []string{dir}, &audittest.Sidecar{}, nil).Start(ctx, ok, training_audits.Options{}); !isKind(err, training_audits.KindUnavailable) {
		t.Errorf("no LLM: %v", err)
	}
}

func isKind(err error, k training_audits.Kind) bool {
	var e *training_audits.Error
	return errors.As(err, &e) && e.Kind == k
}

func TestAuditOneAtATimeAndCancel(t *testing.T) { audittest.Run(t, testAuditOneAtATimeAndCancel) }

func testAuditOneAtATimeAndCancel(t *testing.T, d *sql.DB, ops training_audits.Ops) {
	ctx := context.Background()
	mid := audittest.RecordModel(t, d, "j-cancel", "mine.jsonl")
	sc := &audittest.Sidecar{Block: make(chan struct{}), Started: make(chan struct{}, 1)}
	m := training_audits.NewManager(ctx, d, ops, []string{audittest.DatasetDir(t, 6)}, sc, &audittest.Judge{})

	run, err := m.Start(ctx, mid, training_audits.Options{BatchSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	<-sc.Started
	if _, err := m.Start(ctx, mid, training_audits.Options{}); !isKind(err, training_audits.KindConflict) {
		t.Fatalf("second start while running: %v", err)
	}
	if _, err := m.CancelRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	got, _ := m.GetRunWithCaseRuns(ctx, run.ID)
	if got.Status != core.StatusCancelled || got.FinishedAt == nil || got.Error != "" {
		t.Fatalf("cancelled: %+v", got)
	}
	// The question in flight was reached and is kept as cancelled; the rest were never reached.
	if want := (core.Counts{Total: 6, Pending: 5, Cancelled: 1}); got.Counts != want || len(got.CaseRuns) != 1 || got.CaseRuns[0].Status != core.StatusCancelled {
		t.Fatalf("cancelled: counts %+v, case runs %+v", got.Counts, got.CaseRuns)
	}
	if _, err := m.CancelRun(ctx, run.ID); !isKind(err, training_audits.KindConflict) {
		t.Errorf("cancel of a finished audit: %v", err)
	}
	// The model is free again.
	close(sc.Block)
	again, err := m.Start(ctx, mid, training_audits.Options{BatchSize: 3})
	if err != nil {
		t.Fatalf("start after the cancel: %v", err)
	}
	m.Wait()
	if got, _ := m.GetRun(ctx, again.ID); got.Status != core.StatusCompleted {
		t.Errorf("second audit: %+v", got)
	}
}

func TestAuditFailsWhenTheModelDoes(t *testing.T) { audittest.Run(t, testAuditFailsWhenTheModelDoes) }

func testAuditFailsWhenTheModelDoes(t *testing.T, d *sql.DB, ops training_audits.Ops) {
	ctx := context.Background()
	mid := audittest.RecordModel(t, d, "j-fail", "mine.jsonl")
	sc := &audittest.Sidecar{Fail: map[string]bool{"q0": true, "q1": true, "q2": true, "q3": true, "q4": true, "q5": true}}
	m := training_audits.NewManager(ctx, d, ops, []string{audittest.DatasetDir(t, 8)}, sc, &audittest.Judge{})
	run, err := m.Start(ctx, mid, training_audits.Options{BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	got, _ := m.GetRunWithCaseRuns(ctx, run.ID)
	if got.Status != core.StatusErrored || !strings.Contains(got.Error, "model failed to answer 2 batches in a row") || !strings.Contains(got.Error, "out of memory") {
		t.Fatalf("errored: %+v", got)
	}
	if want := (core.Counts{Total: 8, Pending: 4, Errored: 4}); got.Counts != want || len(got.CaseRuns) != 4 || got.CaseRuns[0].Status != core.StatusErrored ||
		got.CaseRuns[0].Error != "model: out of memory" {
		t.Fatalf("counts %+v, case runs %+v", got.Counts, got.CaseRuns)
	}
}

func TestAuditsInterruptedByARestart(t *testing.T) { audittest.Run(t, testAuditsInterruptedByARestart) }

func testAuditsInterruptedByARestart(t *testing.T, d *sql.DB, ops training_audits.Ops) {
	ctx := context.Background()
	mid := audittest.RecordModel(t, d, "j-restart", "mine.jsonl")
	r := &core.AuditRun{TrainedModelID: mid, Dataset: "mine", Status: core.StatusRunning}
	if err := ops.Store.CreateAuditRun(ctx, d, r); err != nil {
		t.Fatal(err)
	}
	m := training_audits.NewManager(ctx, d, ops, []string{audittest.DatasetDir(t, 1)}, &audittest.Sidecar{}, &audittest.Judge{})
	got, err := m.GetRun(ctx, r.ID)
	if err != nil || got.Status != core.StatusErrored || !strings.Contains(got.Error, "restarted") || got.FinishedAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
	// A run that nothing runs, but that is marked running, is cancelled by the cancel.
	r2 := &core.AuditRun{TrainedModelID: mid, Dataset: "mine", Status: core.StatusRunning}
	if err := ops.Store.CreateAuditRun(ctx, d, r2); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelRun(ctx, r2.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetRun(ctx, r2.ID); got.Status != core.StatusCancelled {
		t.Errorf("%+v", got)
	}
}

func TestWatchAnAudit(t *testing.T) { audittest.Run(t, testWatchAnAudit) }

func testWatchAnAudit(t *testing.T, d *sql.DB, ops training_audits.Ops) {
	ctx := context.Background()
	mid := audittest.RecordModel(t, d, "j-watch", "mine.jsonl")
	sc := &audittest.Sidecar{Block: make(chan struct{}), Started: make(chan struct{}, 1)}
	m := training_audits.NewManager(ctx, d, ops, []string{audittest.DatasetDir(t, 4)}, sc, &audittest.Judge{})
	run, err := m.Start(ctx, mid, training_audits.Options{BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	<-sc.Started
	w, err := m.Watch(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.Events == nil || w.Run.ID != run.ID || len(w.CaseRuns) != 0 {
		t.Fatalf("snapshot of an audit that just started: %+v %d", w.Run, len(w.CaseRuns))
	}
	close(sc.Block)

	var types []string
	var answers, batched int
	timeout := time.After(10 * time.Second)
loop:
	for {
		select {
		case e, ok := <-w.Events:
			if !ok {
				t.Fatal("feed closed without an end")
			}
			types = append(types, e.Type)
			switch e.Type {
			case training_audits.EventAnswer:
				answers++
				if len(e.CaseRuns) != 1 || e.CaseRuns[0].Status != core.StatusRunning || e.CaseRuns[0].JudgeScore != nil {
					t.Errorf("answer: %+v", e.CaseRuns)
				}
			case training_audits.EventBatch:
				batched += len(e.CaseRuns)
				if e.Run == nil || e.Run.CaseRuns != nil || e.Run.NumJudged != batched {
					t.Errorf("batch: %+v", e.Run)
				}
			case training_audits.EventEnd:
				if e.Run.Status != core.StatusCompleted || e.Run.Counts.Completed != 4 {
					t.Errorf("end: %+v", e.Run)
				}
				break loop
			}
		case <-timeout:
			t.Fatalf("timeout; events so far %v", types)
		}
	}
	if answers != 4 || batched != 4 || types[len(types)-1] != training_audits.EventEnd {
		t.Errorf("events %v", types)
	}
	m.Wait()

	// A watch of a finished audit is the snapshot as stored, and no feed.
	w2, err := m.Watch(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if w2.Events != nil || w2.Run.Status != core.StatusCompleted || len(w2.CaseRuns) != 4 || w2.CaseRuns[3].Idx != 3 {
		t.Errorf("snapshot of a finished audit: %+v %+v", w2.Run, w2.CaseRuns)
	}
}
