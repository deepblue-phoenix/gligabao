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
	rec, err := relational.RecordExperimentCaseResult(ctx, d, relational.ExperimentCaseResult{RunID: run.ID, Eval: "basic", Name: "job_change",
		Status: relational.ExperimentFailed, Answer: "Globex", Problems: []string{"p"}, PassedChecks: []string{"q"}, Steps: 3, CostUSD: 0.5,
		StartedAt: now, FinishedAt: now})
	if err != nil || rec.Type != relational.EvalTypeKnowledge {
		t.Fatalf("RecordExperimentCaseResult: %+v %v", rec, err)
	}
	if n := count(`SELECT COUNT(*) FROM gb_experiment_case_results WHERE type = 'knowledge' AND eval = 'basic' AND case_name = 'job_change'`); n != 1 {
		t.Fatalf("universal row: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM gb_knowledge_case_meta WHERE answer = 'Globex' AND steps = 3`); n != 1 {
		t.Fatalf("knowledge metadata: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM gb_experiment_cases WHERE type = 'knowledge' AND eval = 'basic'`); n != 1 {
		t.Fatalf("experiment cases: %d", n)
	}
	got, err := relational.GetExperimentRun(ctx, d, run.ID)
	if err != nil || len(got.CaseResults) != 1 || got.CaseResults[0].Answer != "Globex" || got.CaseResults[0].Steps != 3 || got.CaseResults[0].Type != relational.EvalTypeKnowledge {
		t.Fatalf("GetExperimentRun: %+v %v", got, err)
	}
	if _, err := relational.GetExperimentRun(ctx, d, run.ID+1000); err != relational.ErrNotFound {
		t.Fatalf("unknown run: %v", err)
	}

	// A training eval run: an ad-hoc one is a run in no experiment; joining an experiment sets it and lists the eval there.
	loss := 0.5
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobunify001", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 7, FinalLoss: &loss, Path: "trained/jobunify001"})
	if err != nil {
		t.Fatal(err)
	}
	eid, err := relational.RecordTrainedModelEvalRun(ctx, d, relational.TrainedModelEvalRun{TrainedModelID: mid, Dataset: "mine", NumExamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	// An ad-hoc eval run is a run in no experiment, with its metadata and no case results before its first batch.
	if n := count(fmt.Sprintf(`SELECT COUNT(*) FROM gb_experiment_runs WHERE id = %d AND type = 'training' AND experiment_id IS NULL`, eid)); n != 1 {
		t.Fatalf("ad-hoc training eval run rows: %d", n)
	}
	if n := count(fmt.Sprintf(`SELECT COUNT(*) FROM gb_training_eval_run_meta WHERE run_id = %d AND trained_model_id = %d`, eid, mid)); n != 1 {
		t.Fatalf("training metadata: %d", n)
	}
	if n := count(fmt.Sprintf(`SELECT COUNT(*) FROM gb_experiment_case_results WHERE run_id = %d`, eid)); n != 0 {
		t.Fatalf("case results before the first batch: %d", n)
	}
	// A batch stores one case result (with its training metadata) per example; one that joined the experiment already is listed there.
	score := 5
	batch1 := []relational.TrainedModelCaseResult{{Idx: 0, Batch: 1, Instruction: "q0", Response: "a0", Status: relational.TrainedModelCaseJudged, Score: &score, Reason: "ok"}}
	if err := relational.SaveTrainedModelEvalRunBatch(ctx, d, relational.TrainedModelEvalRun{ID: eid, NumAnswered: 1, NumJudged: 1, NumCorrect: 1, ScoreSum: 5, NumBatches: 1}, batch1); err != nil {
		t.Fatal(err)
	}
	if n := count(fmt.Sprintf(`SELECT COUNT(*) FROM gb_experiment_case_results e JOIN gb_training_case_meta c ON c.case_result_id = e.id
		WHERE e.run_id = %d AND e.type = 'training' AND e.eval = 'mine' AND e.case_name = '0' AND e.status = 'judged' AND c.score = 5`, eid)); n != 1 {
		t.Fatalf("training case result rows: %d", n)
	}
	if _, err := relational.StartTrainingExperimentRun(ctx, d, tx.ID, eid); err != nil {
		t.Fatal(err)
	}
	if n := count(fmt.Sprintf(`SELECT COUNT(*) FROM gb_experiment_cases WHERE experiment_id = %d AND type = 'training' AND eval = 'mine' AND case_name = '0'`, tx.ID)); n != 1 {
		t.Fatalf("training experiment cases after joining: %d", n)
	}
	// A case saved after the run joined the experiment is added to its cases, and an errored one is an errored case result.
	batch2 := []relational.TrainedModelCaseResult{{Idx: 1, Batch: 2, Instruction: "q1", Status: relational.TrainedModelCaseErrored, Error: "boom"}}
	if err := relational.SaveTrainedModelEvalRunBatch(ctx, d, relational.TrainedModelEvalRun{ID: eid, NumAnswered: 2, NumJudged: 1, NumErrored: 1, NumCorrect: 1, ScoreSum: 5, NumBatches: 2}, batch2); err != nil {
		t.Fatal(err)
	}
	if n := count(fmt.Sprintf(`SELECT COUNT(*) FROM gb_experiment_cases WHERE experiment_id = %d AND type = 'training' AND eval = 'mine'`, tx.ID)); n != 2 {
		t.Fatalf("training experiment cases: %d", n)
	}
	if n := count(fmt.Sprintf(`SELECT COUNT(*) FROM gb_experiment_case_results WHERE run_id = %d AND case_name = '1' AND status = 'error' AND error = 'boom'`, eid)); n != 1 {
		t.Fatalf("errored training case result: %d", n)
	}
	// The database refuses a second result for the same example.
	if _, err := d.ExecContext(ctx, fmt.Sprintf(`INSERT INTO gb_experiment_case_results (type, run_id, eval, case_name, status, started_at) VALUES ('training', %d, 'mine', '0', 'judged', '2026-01-01')`, eid)); err == nil {
		t.Error("accepted a second result for one training case")
	}
	// Judge cost and the end of the eval run reach the universal run: it is totalled and takes the eval run's status.
	if err := relational.SaveTrainedModelEvalRunBatch(ctx, d, relational.TrainedModelEvalRun{ID: eid, NumJudged: 1, NumErrored: 1, NumCorrect: 1, ScoreSum: 5, JudgeCostUSD: 0.25}, nil); err != nil {
		t.Fatal(err)
	}
	if err := relational.FinishTrainedModelEvalRun(ctx, d, eid, relational.TrainedModelRunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	var status string
	var total, passed, failed, errored, judged int
	var cost float64
	if err := d.QueryRowContext(ctx, fmt.Sprintf(`SELECT r.status, r.total_cases, r.passed_cases, r.failed_cases, r.error_cases, r.judged_cases, r.cost_usd
		FROM gb_experiment_runs r WHERE r.id = %d`, eid)).Scan(&status, &total, &passed, &failed, &errored, &judged, &cost); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || total != 2 || passed != 0 || failed != 0 || errored != 1 || judged != 1 || cost != 0.25 {
		t.Fatalf("training run: %s %d %d %d %d %d %v", status, total, passed, failed, errored, judged, cost)
	}
	// The knowledge run was not touched by it.
	if r, _ := relational.GetExperimentRun(ctx, d, run.ID); r.Status != relational.ExperimentRunning {
		t.Fatalf("knowledge run: %+v", r)
	}

	var trainCaseID int64
	if err := d.QueryRowContext(ctx, fmt.Sprintf(`SELECT MIN(id) FROM gb_experiment_case_results WHERE run_id = %d`, eid)).Scan(&trainCaseID); err != nil {
		t.Fatal(err)
	}

	// The database refuses what mixes the types.
	bad := []string{
		// a training run in a knowledge experiment
		fmt.Sprintf(`INSERT INTO gb_experiment_runs (type, experiment_id, status, started_at) VALUES ('training', %d, 'running', '2026-01-01')`, kx.ID),
		// a result of one type in a run of the other
		fmt.Sprintf(`INSERT INTO gb_experiment_case_results (type, run_id, case_name, status, started_at) VALUES ('training', %d, 'x', 'running', '2026-01-01')`, run.ID),
		// a knowledge run in no experiment
		`INSERT INTO gb_experiment_runs (type, status, started_at) VALUES ('knowledge', 'running', '2026-01-01')`,
		// knowledge metadata on a training result, and training metadata on a knowledge result or run
		fmt.Sprintf(`INSERT INTO gb_knowledge_case_meta (case_result_id) VALUES (%d)`, trainCaseID),
		fmt.Sprintf(`INSERT INTO gb_training_case_meta (case_result_id, idx, batch, instruction) VALUES (%d, 0, 1, 'q')`, rec.ID),
		fmt.Sprintf(`INSERT INTO gb_training_eval_run_meta (run_id, trained_model_id) VALUES (%d, %d)`, run.ID, mid),
		// an unknown type
		`INSERT INTO gb_experiments (type, name, created_at) VALUES ('other', 'z', '2026-01-01')`,
	}
	for _, s := range bad {
		if _, err := d.ExecContext(ctx, s); err == nil {
			t.Errorf("accepted: %s", s)
		}
	}
}
