package relational_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestUnifiedEvalTables(t *testing.T) { dbtest.Run(t, unifiedEvalTablesScenario) }

// unifiedEvalTablesScenario runs both kinds of eval in one database: they share the universal tables, tell apart by type, and
// each keeps its own columns in its metadata table.
func unifiedEvalTablesScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	count := func(q string) (n int) {
		t.Helper()
		if err := d.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}

	// The same name is two experiments, one of each type, and each kind sees only its own.
	kx, err := relational.EnsureExperiment(ctx, d, "same", "", nil)
	if err != nil || kx.Type != relational.EvalTypeKnowledge {
		t.Fatalf("EnsureExperiment: %+v %v", kx, err)
	}
	tx, err := relational.EnsureTrainingExperiment(ctx, d, "same", "", nil)
	if err != nil || tx.ID == kx.ID {
		t.Fatalf("EnsureTrainingExperiment: %+v %v", tx, err)
	}
	if got, _ := relational.ListExperiments(ctx, d); len(got) != 1 || got[0].ID != kx.ID {
		t.Fatalf("ListExperiments: %+v", got)
	}
	if got, _ := relational.ListTrainingExperiments(ctx, d); len(got) != 1 || got[0].ID != tx.ID {
		t.Fatalf("ListTrainingExperiments: %+v", got)
	}
	if got := count(`SELECT COUNT(*) FROM gb_experiments WHERE type = 'knowledge'`) + count(`SELECT COUNT(*) FROM gb_experiments WHERE type = 'training'`); got != 2 {
		t.Fatalf("experiments by type: %d", got)
	}
	// Each kind refuses the other's IDs.
	if _, err := relational.StartExperimentRun(ctx, d, tx.ID, nil); err != relational.ErrNotFound {
		t.Fatalf("a knowledge run under a training experiment: %v", err)
	}
	if _, err := relational.GetTrainingExperiment(ctx, d, kx.ID); err != relational.ErrNotFound {
		t.Fatalf("a knowledge experiment as a training one: %v", err)
	}

	// A knowledge case: universal row plus knowledge metadata.
	run, err := relational.StartExperimentRun(ctx, d, kx.ID, map[string]any{"cases": []string{"a"}})
	if err != nil || run.Type != relational.EvalTypeKnowledge {
		t.Fatalf("StartExperimentRun: %+v %v", run, err)
	}
	now := time.Now()
	rec, err := relational.RecordExperimentCaseResult(ctx, d, relational.ExperimentCaseResult{RunID: run.ID, Suite: "basic", Name: "job_change",
		Status: relational.ExperimentFailed, Answer: "Globex", Problems: []string{"p"}, PassedChecks: []string{"q"}, Steps: 3, CostUSD: 0.5,
		StartedAt: now, FinishedAt: now})
	if err != nil || rec.Type != relational.EvalTypeKnowledge {
		t.Fatalf("RecordExperimentCaseResult: %+v %v", rec, err)
	}
	if n := count(`SELECT COUNT(*) FROM gb_experiment_case_results WHERE type = 'knowledge' AND suite = 'basic' AND case_name = 'job_change'`); n != 1 {
		t.Fatalf("universal row: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM gb_knowledge_case_meta WHERE answer = 'Globex' AND steps = 3`); n != 1 {
		t.Fatalf("knowledge metadata: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM gb_experiment_cases WHERE type = 'knowledge' AND suite = 'basic'`); n != 1 {
		t.Fatalf("experiment cases: %d", n)
	}
	got, err := relational.GetExperimentRun(ctx, d, run.ID)
	if err != nil || len(got.CaseResults) != 1 || got.CaseResults[0].Answer != "Globex" || got.CaseResults[0].Steps != 3 || got.CaseResults[0].Type != relational.EvalTypeKnowledge {
		t.Fatalf("GetExperimentRun: %+v %v", got, err)
	}
	if _, err := relational.GetExperimentRun(ctx, d, run.ID+1000); err != relational.ErrNotFound {
		t.Fatalf("unknown run: %v", err)
	}

	// A training suite run: an ad-hoc one is a run in no experiment; joining an experiment sets it and lists the suite there.
	loss := 0.5
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobunify001", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 7, FinalLoss: &loss, Path: "trained/jobunify001"})
	if err != nil {
		t.Fatal(err)
	}
	eid, err := relational.RecordTrainedModelSuiteRun(ctx, d, relational.TrainedModelSuiteRun{TrainedModelID: mid, Dataset: "mine", NumExamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	q := fmt.Sprintf(`SELECT COUNT(*) FROM gb_experiment_runs r JOIN gb_experiment_case_results e ON e.run_id = r.id
		WHERE e.id = %d AND r.type = 'training' AND e.type = 'training' AND r.experiment_id IS NULL AND e.suite = 'mine' AND e.case_name = ''`, eid)
	if n := count(q); n != 1 {
		t.Fatalf("ad-hoc training suite run rows: %d", n)
	}
	if n := count(fmt.Sprintf(`SELECT COUNT(*) FROM gb_training_suite_run_meta WHERE case_result_id = %d AND trained_model_id = %d`, eid, mid)); n != 1 {
		t.Fatalf("training metadata: %d", n)
	}
	if _, err := relational.StartTrainingExperimentRun(ctx, d, tx.ID, eid); err != nil {
		t.Fatal(err)
	}
	if n := count(fmt.Sprintf(`SELECT COUNT(*) FROM gb_experiment_cases WHERE experiment_id = %d AND type = 'training' AND suite = 'mine' AND case_name = ''`, tx.ID)); n != 1 {
		t.Fatalf("training experiment cases: %d", n)
	}
	// Judge cost and the end of the suite run reach the universal rows: the run is totalled and takes the suite run's status.
	if err := relational.SaveTrainedModelSuiteRunBatch(ctx, d, relational.TrainedModelSuiteRun{ID: eid, NumJudged: 2, NumCorrect: 2, ScoreSum: 9, JudgeCostUSD: 0.25}, nil); err != nil {
		t.Fatal(err)
	}
	if err := relational.FinishTrainedModelSuiteRun(ctx, d, eid, relational.TrainedModelSuiteRunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	var status string
	var total, passed, failed int
	var cost float64
	if err := d.QueryRowContext(ctx, fmt.Sprintf(`SELECT r.status, r.total_cases, r.passed_cases, r.failed_cases, r.cost_usd
		FROM gb_experiment_runs r JOIN gb_experiment_case_results e ON e.run_id = r.id WHERE e.id = %d`, eid)).Scan(&status, &total, &passed, &failed, &cost); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || total != 2 || passed != 2 || failed != 0 || cost != 0.25 {
		t.Fatalf("training run: %s %d %d %d %v", status, total, passed, failed, cost)
	}
	// The knowledge run was not touched by it.
	if r, _ := relational.GetExperimentRun(ctx, d, run.ID); r.Status != relational.ExperimentRunning {
		t.Fatalf("knowledge run: %+v", r)
	}

	// The database refuses what mixes the types.
	bad := []string{
		// a training run in a knowledge experiment
		fmt.Sprintf(`INSERT INTO gb_experiment_runs (type, experiment_id, status, started_at) VALUES ('training', %d, 'running', '2026-01-01')`, kx.ID),
		// a result of one type in a run of the other
		fmt.Sprintf(`INSERT INTO gb_experiment_case_results (type, run_id, case_name, status, started_at) VALUES ('training', %d, 'x', 'running', '2026-01-01')`, run.ID),
		// a knowledge run in no experiment
		`INSERT INTO gb_experiment_runs (type, status, started_at) VALUES ('knowledge', 'running', '2026-01-01')`,
		// knowledge metadata on a training result, and training metadata on a knowledge result
		fmt.Sprintf(`INSERT INTO gb_knowledge_case_meta (case_result_id) VALUES (%d)`, eid),
		fmt.Sprintf(`INSERT INTO gb_training_suite_run_meta (case_result_id, trained_model_id) VALUES (%d, %d)`, rec.ID, mid),
		// an unknown type
		`INSERT INTO gb_experiments (type, name, created_at) VALUES ('other', 'z', '2026-01-01')`,
	}
	for _, s := range bad {
		if _, err := d.ExecContext(ctx, s); err == nil {
			t.Errorf("accepted: %s", s)
		}
	}
}
