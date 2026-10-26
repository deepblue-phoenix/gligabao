package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
)

// The create functions insert one record into its table. Where the database generates the ID, it is set on the record; where
// a time is zero it is set to now (UTC, second precision, the precision that survives the storage round trip) and set on the
// record too; an empty Status is StatusPending. The rest is stored as it is: what the schema refuses (an unknown status, a
// type that is not that of the parent, a duplicate name, ...) comes back as the error of the database.

// now is the current time in the precision of the storage.
func now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// fill returns t normalized for storage, or now if t is zero.
func fill(t time.Time) time.Time {
	if t.IsZero() {
		return now()
	}
	return t.UTC().Truncate(time.Second)
}

// text formats t for storage (UTC RFC 3339).
func text(t time.Time) string { return t.Format(time.RFC3339) }

// textPtr formats t for storage; nil is NULL.
func textPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return text(t.UTC().Truncate(time.Second))
}

func status(s core.Status) core.Status {
	if s == "" {
		return core.StatusPending
	}
	return s
}

// CreateEvalDef inserts d into gb_eval_defs and sets its ID, CreatedAt and UpdatedAt. Its Cases are not stored.
func CreateEvalDef(ctx context.Context, q db.Execer, d *core.EvalDef) error {
	tags, err := db.JSON(d.Tags, "[]")
	if err != nil {
		return err
	}
	params, err := db.JSON(d.Params, "{}")
	if err != nil {
		return err
	}
	d.CreatedAt = fill(d.CreatedAt)
	d.UpdatedAt = fill(d.UpdatedAt)
	var id int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_eval_defs (name, description, type, tags, params, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		d.Name, d.Description, string(d.Type), tags, params, text(d.CreatedAt), text(d.UpdatedAt)).Scan(&id); err != nil {
		return fmt.Errorf("create eval def %q: %w", d.Name, err)
	}
	d.ID = core.EvalDefID(id)
	return nil
}

// CreateCaseDef inserts d into gb_case_defs and sets its ID, CreatedAt and UpdatedAt. Its Type must be that of its eval.
// Its Meta is not stored: it has its own function per type (CreateKnowledgeCaseDefMeta, ...).
func CreateCaseDef(ctx context.Context, q db.Execer, d *core.CaseDef) error {
	tags, err := db.JSON(d.Tags, "[]")
	if err != nil {
		return err
	}
	d.CreatedAt = fill(d.CreatedAt)
	d.UpdatedAt = fill(d.UpdatedAt)
	var id int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_case_defs (eval_def_id, type, name, position, description, tags, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		int64(d.EvalDefID), string(d.Type), d.Name, d.Position, d.Description, tags, text(d.CreatedAt), text(d.UpdatedAt)).Scan(&id); err != nil {
		return fmt.Errorf("create case def %q: %w", d.Name, err)
	}
	d.ID = core.CaseDefID(id)
	return nil
}

// CreateKnowledgeCaseDefMeta inserts m into gb_case_def_meta_knowledge, for the knowledge case def m.CaseDefID.
func CreateKnowledgeCaseDefMeta(ctx context.Context, q db.Execer, m *core.KnowledgeCaseDefMeta) error {
	remember, err := db.JSON(m.Remember, "[]")
	if err != nil {
		return err
	}
	contains, err := db.JSON(m.ExpectContains, "[]")
	if err != nil {
		return err
	}
	edges, err := db.JSON(m.ExpectEdges, "[]")
	if err != nil {
		return err
	}
	series, err := db.JSON(m.ExpectSeries, "[]")
	if err != nil {
		return err
	}
	models, err := db.JSON(m.Models, "{}")
	if err != nil {
		return err
	}
	reasoning, err := db.JSON(m.Reasoning, "{}")
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO gb_case_def_meta_knowledge (case_def_id, type, remember, query, expect_contains, expect_edges,
			expect_min_chunks, expect_web_searches, expect_series, chunk_size, models, reasoning)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(m.CaseDefID), string(core.TypeKnowledge), remember, m.Query, contains, edges,
		m.ExpectMinChunks, m.ExpectWebSearches, series, m.ChunkSize, models, reasoning); err != nil {
		return fmt.Errorf("create knowledge case def meta of case def %d: %w", m.CaseDefID, err)
	}
	return nil
}

// CreateTrainingCaseDefMeta inserts m into gb_case_def_meta_training, for the training case def m.CaseDefID.
func CreateTrainingCaseDefMeta(ctx context.Context, q db.Execer, m *core.TrainingCaseDefMeta) error {
	if _, err := q.ExecContext(ctx, `
		INSERT INTO gb_case_def_meta_training (case_def_id, type, instruction, input, output)
		VALUES (?, ?, ?, ?, ?)`,
		int64(m.CaseDefID), string(core.TypeTraining), m.Instruction, m.Input, m.Output); err != nil {
		return fmt.Errorf("create training case def meta of case def %d: %w", m.CaseDefID, err)
	}
	return nil
}

// CreateExperimentDef inserts d into gb_experiment_defs and sets its ID, CreatedAt and UpdatedAt. Its EvalDefIDs are not
// stored: they are linked with CreateExperimentDefEval.
func CreateExperimentDef(ctx context.Context, q db.Execer, d *core.ExperimentDef) error {
	params, err := db.JSON(d.Params, "{}")
	if err != nil {
		return err
	}
	d.CreatedAt = fill(d.CreatedAt)
	d.UpdatedAt = fill(d.UpdatedAt)
	var id int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_experiment_defs (name, description, params, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		RETURNING id`,
		d.Name, d.Description, params, text(d.CreatedAt), text(d.UpdatedAt)).Scan(&id); err != nil {
		return fmt.Errorf("create experiment def %q: %w", d.Name, err)
	}
	d.ID = core.ExperimentDefID(id)
	return nil
}

// CreateExperimentDefEval inserts into gb_experiment_def_evals the link that puts the eval in the experiment, at position
// (from 0) in the order the evals run. It is one element of ExperimentDef.EvalDefIDs.
func CreateExperimentDefEval(ctx context.Context, q db.Execer, experimentDefID core.ExperimentDefID, evalDefID core.EvalDefID, position int) error {
	if _, err := q.ExecContext(ctx, `
		INSERT INTO gb_experiment_def_evals (experiment_def_id, eval_def_id, position)
		VALUES (?, ?, ?)`,
		int64(experimentDefID), int64(evalDefID), position); err != nil {
		return fmt.Errorf("add eval def %d to experiment def %d: %w", evalDefID, experimentDefID, err)
	}
	return nil
}

// CreateExperimentRun inserts r into gb_experiment_runs and sets its ID, Status and CreatedAt. Its EvalRuns are not stored.
func CreateExperimentRun(ctx context.Context, q db.Execer, r *core.ExperimentRun) error {
	params, err := db.JSON(r.Params, "{}")
	if err != nil {
		return err
	}
	r.Status = status(r.Status)
	r.CreatedAt = fill(r.CreatedAt)
	var id int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_experiment_runs (def_id, status, error, params,
			counts_total, counts_pending, counts_running, counts_completed, counts_errored, counts_cancelled, counts_passed, counts_failed,
			cost_usd, created_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		int64(r.DefID), string(r.Status), r.Error, params,
		r.Counts.Total, r.Counts.Pending, r.Counts.Running, r.Counts.Completed, r.Counts.Errored, r.Counts.Cancelled, r.Counts.Passed, r.Counts.Failed,
		r.CostUSD, text(r.CreatedAt), textPtr(r.StartedAt), textPtr(r.FinishedAt)).Scan(&id); err != nil {
		return fmt.Errorf("create experiment run of experiment def %d: %w", r.DefID, err)
	}
	r.ID = core.ExperimentRunID(id)
	return nil
}

// CreateEvalRun inserts r into gb_eval_runs and sets its ID, Status and CreatedAt. Its Type must be that of its eval def.
// Its CaseRuns are not stored, and neither is its Meta: it has its own function per type (CreateKnowledgeEvalRunMeta, ...).
func CreateEvalRun(ctx context.Context, q db.Execer, r *core.EvalRun) error {
	params, err := db.JSON(r.Params, "{}")
	if err != nil {
		return err
	}
	var experimentRunID any
	if r.ExperimentRunID != nil {
		experimentRunID = int64(*r.ExperimentRunID)
	}
	r.Status = status(r.Status)
	r.CreatedAt = fill(r.CreatedAt)
	var id int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_eval_runs (def_id, experiment_run_id, type, status, error, params,
			counts_total, counts_pending, counts_running, counts_completed, counts_errored, counts_cancelled, counts_passed, counts_failed,
			cost_usd, created_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		int64(r.DefID), experimentRunID, string(r.Type), string(r.Status), r.Error, params,
		r.Counts.Total, r.Counts.Pending, r.Counts.Running, r.Counts.Completed, r.Counts.Errored, r.Counts.Cancelled, r.Counts.Passed, r.Counts.Failed,
		r.CostUSD, text(r.CreatedAt), textPtr(r.StartedAt), textPtr(r.FinishedAt)).Scan(&id); err != nil {
		return fmt.Errorf("create eval run of eval def %d: %w", r.DefID, err)
	}
	r.ID = core.EvalRunID(id)
	return nil
}

// CreateKnowledgeEvalRunMeta inserts m into gb_eval_runs_meta_knowledge, for the knowledge eval run m.EvalRunID.
func CreateKnowledgeEvalRunMeta(ctx context.Context, q db.Execer, m *core.KnowledgeEvalRunMeta) error {
	if _, err := q.ExecContext(ctx, `
		INSERT INTO gb_eval_runs_meta_knowledge (eval_run_id, type, chunks, steps, remember_calls, query_calls, remember_cost_usd,
			query_cost_usd, checks_passed, checks_failed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(m.EvalRunID), string(core.TypeKnowledge), m.Chunks, m.Steps, m.RememberCalls, m.QueryCalls, m.RememberCostUSD,
		m.QueryCostUSD, m.ChecksPassed, m.ChecksFailed); err != nil {
		return fmt.Errorf("create knowledge eval run meta of eval run %d: %w", m.EvalRunID, err)
	}
	return nil
}

// CreateTrainingEvalRunMeta inserts m into gb_eval_runs_meta_training, for the training eval run m.EvalRunID.
func CreateTrainingEvalRunMeta(ctx context.Context, q db.Execer, m *core.TrainingEvalRunMeta) error {
	if _, err := q.ExecContext(ctx, `
		INSERT INTO gb_eval_runs_meta_training (eval_run_id, type, trained_model_id, judge_model, num_answered, num_judged, num_correct,
			num_partial, num_incorrect, score_sum, num_batches, judge_calls, judge_input_tokens, judge_output_tokens, judge_cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(m.EvalRunID), string(core.TypeTraining), m.TrainedModelID, m.JudgeModel, m.NumAnswered, m.NumJudged, m.NumCorrect,
		m.NumPartial, m.NumIncorrect, m.ScoreSum, m.NumBatches, m.JudgeCalls, m.JudgeInputTokens, m.JudgeOutputTokens, m.JudgeCostUSD); err != nil {
		return fmt.Errorf("create training eval run meta of eval run %d: %w", m.EvalRunID, err)
	}
	return nil
}

// CreateCaseRun inserts r into gb_case_runs and sets its ID, Status and CreatedAt. Its Type must be that of its case def.
// Its Meta is not stored: it has its own function per type (CreateKnowledgeCaseRunMeta, ...).
func CreateCaseRun(ctx context.Context, q db.Execer, r *core.CaseRun) error {
	r.Status = status(r.Status)
	r.CreatedAt = fill(r.CreatedAt)
	var id int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_case_runs (def_id, eval_run_id, type, status, error, verdict, score, cost_usd, created_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		int64(r.DefID), int64(r.EvalRunID), string(r.Type), string(r.Status), r.Error, string(r.Verdict), r.Score,
		r.CostUSD, text(r.CreatedAt), textPtr(r.StartedAt), textPtr(r.FinishedAt)).Scan(&id); err != nil {
		return fmt.Errorf("create case run of case def %d: %w", r.DefID, err)
	}
	r.ID = core.CaseRunID(id)
	return nil
}

// CreateKnowledgeCaseRunMeta inserts m into gb_case_runs_meta_knowledge, for the knowledge case run m.CaseRunID. Its
// Trajectories are not stored: they are added with CreateKnowledgeTrajectory.
func CreateKnowledgeCaseRunMeta(ctx context.Context, q db.Execer, m *core.KnowledgeCaseRunMeta) error {
	input, err := db.JSON(m.Input, "{}")
	if err != nil {
		return err
	}
	problems, err := db.JSON(m.Problems, "[]")
	if err != nil {
		return err
	}
	passed, err := db.JSON(m.PassedChecks, "[]")
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO gb_case_runs_meta_knowledge (case_run_id, type, input, answer, problems, passed_checks, dataset_id, steps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(m.CaseRunID), string(core.TypeKnowledge), input, m.Answer, problems, passed, m.DatasetID, m.Steps); err != nil {
		return fmt.Errorf("create knowledge case run meta of case run %d: %w", m.CaseRunID, err)
	}
	return nil
}

// CreateKnowledgeTrajectory inserts t into gb_case_runs_meta_knowledge_trajectories, as a trajectory of the knowledge case run
// caseRunID (whose CreateKnowledgeCaseRunMeta must have been called), and sets its CreatedAt.
func CreateKnowledgeTrajectory(ctx context.Context, q db.Execer, caseRunID core.CaseRunID, t *core.KnowledgeTrajectory) error {
	artifacts, err := db.JSON(t.ArtifactIDs, "[]")
	if err != nil {
		return err
	}
	t.CreatedAt = fill(t.CreatedAt)
	if _, err := q.ExecContext(ctx, `
		INSERT INTO gb_case_runs_meta_knowledge_trajectories (case_run_id, seq, kind, trajectory_id, agent_name, steps, cost_usd, path,
			artifact_ids, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(caseRunID), t.Seq, t.Kind, t.TrajectoryID, t.AgentName, t.Steps, t.CostUSD, t.Path, artifacts, text(t.CreatedAt)); err != nil {
		return fmt.Errorf("create knowledge trajectory %d of case run %d: %w", t.Seq, caseRunID, err)
	}
	return nil
}

// CreateTrainingCaseRunMeta inserts m into gb_case_runs_meta_training, for the training case run m.CaseRunID.
func CreateTrainingCaseRunMeta(ctx context.Context, q db.Execer, m *core.TrainingCaseRunMeta) error {
	if _, err := q.ExecContext(ctx, `
		INSERT INTO gb_case_runs_meta_training (case_run_id, type, instruction, input, reference, response, batch, judge_score,
			judge_reason, input_tokens, output_tokens, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(m.CaseRunID), string(core.TypeTraining), m.Instruction, m.Input, m.Reference, m.Response, m.Batch, m.JudgeScore,
		m.JudgeReason, m.InputTokens, m.OutputTokens, m.DurationMS); err != nil {
		return fmt.Errorf("create training case run meta of case run %d: %w", m.CaseRunID, err)
	}
	return nil
}
