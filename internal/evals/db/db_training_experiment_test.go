package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

func TestTrainingExperiments(t *testing.T) { dbtest.Run(t, trainingExperimentsScenario) }

func trainingExperimentsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	loss := 0.5
	mid, _, err := trainingdb.RecordTrainedModel(ctx, d, trainingdb.TrainedModel{
		JobID: "jobexp00001", ModelSize: "4b", BaseModel: "google/gemma", Dataset: "mine.jsonl", NumTrainSteps: 7, FinalLoss: &loss,
		Path: "trained/jobexp00001"})
	if err != nil {
		t.Fatal(err)
	}
	newEval := func() int64 {
		id, err := evalsdb.RecordTrainedModelEvalRun(ctx, d, evalsdb.TrainedModelRun{TrainedModelID: mid, Eval: "mine", NumExamples: 4})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	// An experiment is made once per name, and keeps its first description and settings.
	x, err := evalsdb.EnsureTrainingExperiment(ctx, d, "lr-sweep", "learning rates", map[string]any{"lr": 0.0002})
	if err != nil || x.ID == 0 || x.Name != "lr-sweep" || x.Description != "learning rates" || x.Config["lr"] != 0.0002 || x.NumRuns != 0 || x.CreatedAt.IsZero() {
		t.Fatalf("EnsureTrainingExperiment: %+v %v", x, err)
	}
	again, err := evalsdb.EnsureTrainingExperiment(ctx, d, "lr-sweep", "other", nil)
	if err != nil || again.ID != x.ID || again.Description != "learning rates" {
		t.Fatalf("second ensure: %+v %v", again, err)
	}

	// Two eval runs become runs of it; the run follows its eval run.
	e1, e2 := newEval(), newEval()
	run, err := evalsdb.StartTrainingExperimentRun(ctx, d, x.ID, e1)
	if err != nil || run.ID == 0 || run.ExperimentID != x.ID || run.ID != e1 || run.Status != evalsdb.TrainedModelRunRunning ||
		run.MeanScore != nil || run.CorrectRate != nil || run.Model.ID != mid || run.Model.NumTrainSteps != 7 || run.Model.BaseModel != "google/gemma" ||
		run.Model.FinalLoss == nil || *run.Model.FinalLoss != 0.5 || run.EvalRun.NumExamples != 4 {
		t.Fatalf("StartTrainingExperimentRun: %+v %v", run, err)
	}
	if _, err := evalsdb.StartTrainingExperimentRun(ctx, d, x.ID, e2); err != nil {
		t.Fatal(err)
	}
	e := evalsdb.TrainedModelRun{ID: e1, JudgeModel: "j", NumAnswered: 4, NumJudged: 4, NumCorrect: 3, NumIncorrect: 1, ScoreSum: 16, NumBatches: 1, JudgeCostUSD: 0.25}
	if err := evalsdb.SaveTrainedModelEvalRunBatch(ctx, d, e, []evalsdb.TrainedModelCase{{Idx: 0, Name: "c0", Batch: 1, Instruction: "q", Status: evalsdb.TrainedModelCaseErrored}}); err != nil {
		t.Fatal(err)
	}
	if err := evalsdb.FinishTrainedModelEvalRun(ctx, d, e1, evalsdb.TrainedModelRunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	runs, err := evalsdb.ListTrainingExperimentRuns(ctx, d, x.ID)
	if err != nil || len(runs) != 2 || runs[0].ID != e1 || runs[1].ID != e2 {
		t.Fatalf("ListTrainingExperimentRuns: %+v %v", runs, err)
	}
	if r := runs[0]; r.Status != evalsdb.TrainedModelRunCompleted || r.EvalRun.FinishedAt == nil || r.MeanScore == nil || *r.MeanScore != 4 ||
		r.CorrectRate == nil || *r.CorrectRate != 0.75 || r.EvalRun.JudgeCostUSD != 0.25 || r.EvalRun.JudgeModel != "j" {
		t.Fatalf("finished run: %+v", r)
	}
	if r := runs[1]; r.Status != evalsdb.TrainedModelRunRunning || r.MeanScore != nil {
		t.Fatalf("running run: %+v", r)
	}
	if got, err := evalsdb.GetTrainingExperimentRun(ctx, d, run.ID); err != nil || got.Status != evalsdb.TrainedModelRunCompleted {
		t.Fatalf("GetTrainingExperimentRun: %+v %v", got, err)
	}

	// An eval run is in one experiment only; unknown experiments and eval runs are refused.
	other, _ := evalsdb.EnsureTrainingExperiment(ctx, d, "other", "", nil)
	if _, err := evalsdb.StartTrainingExperimentRun(ctx, d, other.ID, e1); err == nil {
		t.Fatal("an eval run became a run of two experiments")
	}
	if _, err := evalsdb.StartTrainingExperimentRun(ctx, d, x.ID+100, e1); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("unknown experiment: %v", err)
	}
	if _, err := evalsdb.StartTrainingExperimentRun(ctx, d, x.ID, e2+100); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("unknown eval run: %v", err)
	}
	if _, err := evalsdb.GetTrainingExperimentRun(ctx, d, run.ID+100); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("unknown run: %v", err)
	}

	got, err := evalsdb.GetTrainingExperiment(ctx, d, x.ID)
	if err != nil || got.NumRuns != 2 {
		t.Fatalf("GetTrainingExperiment: %+v %v", got, err)
	}
	if _, err := evalsdb.GetTrainingExperiment(ctx, d, x.ID+100); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	list, err := evalsdb.ListTrainingExperiments(ctx, d)
	if err != nil || len(list) != 2 || list[0].Name != "other" || list[1].NumRuns != 2 || list[0].NumRuns != 0 {
		t.Fatalf("ListTrainingExperiments: %+v %v", list, err)
	}

	// Deleting the model deletes its eval runs and so its runs, not the experiment.
	if _, err := d.ExecContext(ctx, `DELETE FROM gb_trained_models WHERE id = `+fmt.Sprint(mid)); err != nil {
		t.Fatal(err)
	}
	if got, err := evalsdb.GetTrainingExperiment(ctx, d, x.ID); err != nil || got.NumRuns != 0 {
		t.Fatalf("after deleting the model: %+v %v", got, err)
	}
	var left int
	if err := d.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM gb_experiment_case_runs) + (SELECT COUNT(*) FROM gb_training_case_meta)`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("case results left after deleting the model: %d %v", left, err)
	}
}
