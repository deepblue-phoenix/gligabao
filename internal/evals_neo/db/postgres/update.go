package postgres

import (
	"context"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/db"
)

// Store holds the functions of this package, for code that works with either backend (db.Store).
var Store = db.Store{
	CreateEvalDef:           CreateEvalDef,
	UpdateEvalDef:           UpdateEvalDef,
	GetEvalDefByName:        GetEvalDefByName,
	CreateCaseDef:           CreateCaseDef,
	UpdateCaseDef:           UpdateCaseDef,
	CreateCaseDefMeta:       CreateCaseDefMeta,
	UpdateCaseDefMeta:       UpdateCaseDefMeta,
	CreateExperimentDef:     CreateExperimentDef,
	CreateExperimentDefEval: CreateExperimentDefEval,
	GetExperimentDefByName:  GetExperimentDefByName,
	CreateExperimentRun:     CreateExperimentRun,
	UpdateExperimentRun:     UpdateExperimentRun,
	CreateEvalRun:           CreateEvalRun,
	UpdateEvalRun:           UpdateEvalRun,
	CreateEvalRunMeta:       CreateEvalRunMeta,
	UpdateEvalRunMeta:       UpdateEvalRunMeta,
	CreateCaseRun:           CreateCaseRun,
	UpdateCaseRun:           UpdateCaseRun,
	CreateCaseRunMeta:       CreateCaseRunMeta,
}

// The update functions rewrite one record, found by its ID, with the fields that can change after it is created (what they are
// is said for each). The ones that are fixed (the name and type of a def, the links of a run, ...) are left alone. They return
// db.ErrNotFound if the record does not exist.

// UpdateEvalDef rewrites the description, tags and params of d and sets its UpdatedAt to now. Its name and type do not change.
func UpdateEvalDef(ctx context.Context, q db.Execer, d *core.EvalDef) error {
	tags, err := db.JSON(d.Tags, "[]")
	if err != nil {
		return err
	}
	params, err := db.JSON(d.Params, "{}")
	if err != nil {
		return err
	}
	d.UpdatedAt = now()
	res, err := q.ExecContext(ctx, `UPDATE gb_neo_eval_defs SET description = $1, tags = $2::jsonb, params = $3::jsonb, updated_at = $4 WHERE id = $5`,
		d.Description, tags, params, d.UpdatedAt, int64(d.ID))
	if err != nil {
		return fmt.Errorf("update eval def %d: %w", d.ID, err)
	}
	return db.Affected(res, fmt.Sprintf("update eval def %d", d.ID))
}

// UpdateCaseDef rewrites the position, description and tags of d and sets its UpdatedAt to now. Its name, eval and type do not
// change. The position is unique within the eval, so a caller that swaps positions must go through one that is free.
func UpdateCaseDef(ctx context.Context, q db.Execer, d *core.CaseDef) error {
	tags, err := db.JSON(d.Tags, "[]")
	if err != nil {
		return err
	}
	d.UpdatedAt = now()
	res, err := q.ExecContext(ctx, `UPDATE gb_neo_case_defs SET position = $1, description = $2, tags = $3::jsonb, updated_at = $4 WHERE id = $5`,
		d.Position, d.Description, tags, d.UpdatedAt, int64(d.ID))
	if err != nil {
		return fmt.Errorf("update case def %d: %w", d.ID, err)
	}
	return db.Affected(res, fmt.Sprintf("update case def %d", d.ID))
}

// CreateCaseDefMeta inserts m into the table of its type, for the case def it names.
func CreateCaseDefMeta(ctx context.Context, q db.Execer, m core.CaseDefMeta) error {
	switch m := m.(type) {
	case *core.KnowledgeCaseDefMeta:
		return CreateKnowledgeCaseDefMeta(ctx, q, m)
	case core.KnowledgeCaseDefMeta:
		return CreateKnowledgeCaseDefMeta(ctx, q, &m)
	case *core.TrainingCaseDefMeta:
		return CreateTrainingCaseDefMeta(ctx, q, m)
	case core.TrainingCaseDefMeta:
		return CreateTrainingCaseDefMeta(ctx, q, &m)
	}
	return fmt.Errorf("create case def meta: unknown type %T", m)
}

// UpdateCaseDefMeta rewrites the row of m in the table of its type.
func UpdateCaseDefMeta(ctx context.Context, q db.Execer, m core.CaseDefMeta) error {
	switch m := m.(type) {
	case *core.KnowledgeCaseDefMeta:
		return UpdateKnowledgeCaseDefMeta(ctx, q, m)
	case core.KnowledgeCaseDefMeta:
		return UpdateKnowledgeCaseDefMeta(ctx, q, &m)
	case *core.TrainingCaseDefMeta:
		return UpdateTrainingCaseDefMeta(ctx, q, m)
	case core.TrainingCaseDefMeta:
		return UpdateTrainingCaseDefMeta(ctx, q, &m)
	}
	return fmt.Errorf("update case def meta: unknown type %T", m)
}

// UpdateKnowledgeCaseDefMeta rewrites the row of m in gb_neo_case_def_meta_knowledge.
func UpdateKnowledgeCaseDefMeta(ctx context.Context, q db.Execer, m *core.KnowledgeCaseDefMeta) error {
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
	res, err := q.ExecContext(ctx, `
		UPDATE gb_neo_case_def_meta_knowledge SET remember = $1::jsonb, query = $2, expect_contains = $3::jsonb, expect_edges = $4::jsonb,
			expect_min_chunks = $5, expect_web_searches = $6, expect_series = $7::jsonb, chunk_size = $8, models = $9::jsonb, reasoning = $10::jsonb
		WHERE case_def_id = $11`,
		remember, m.Query, contains, edges, m.ExpectMinChunks, m.ExpectWebSearches, series, m.ChunkSize, models, reasoning, int64(m.CaseDefID))
	if err != nil {
		return fmt.Errorf("update knowledge case def meta of case def %d: %w", m.CaseDefID, err)
	}
	return db.Affected(res, fmt.Sprintf("update knowledge case def meta of case def %d", m.CaseDefID))
}

// UpdateTrainingCaseDefMeta rewrites the row of m in gb_neo_case_def_meta_training.
func UpdateTrainingCaseDefMeta(ctx context.Context, q db.Execer, m *core.TrainingCaseDefMeta) error {
	res, err := q.ExecContext(ctx, `UPDATE gb_neo_case_def_meta_training SET instruction = $1, input = $2, output = $3 WHERE case_def_id = $4`,
		m.Instruction, m.Input, m.Output, int64(m.CaseDefID))
	if err != nil {
		return fmt.Errorf("update training case def meta of case def %d: %w", m.CaseDefID, err)
	}
	return db.Affected(res, fmt.Sprintf("update training case def meta of case def %d", m.CaseDefID))
}

// UpdateExperimentRun rewrites the status, error, params, counts, cost and times of r.
func UpdateExperimentRun(ctx context.Context, q db.Execer, r *core.ExperimentRun) error {
	params, err := db.JSON(r.Params, "{}")
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `
		UPDATE gb_neo_experiment_runs SET status = $1, error = $2, params = $3::jsonb,
			counts_total = $4, counts_pending = $5, counts_running = $6, counts_completed = $7, counts_errored = $8, counts_cancelled = $9,
			counts_passed = $10, counts_failed = $11, cost_usd = $12, started_at = $13, finished_at = $14
		WHERE id = $15`,
		string(r.Status), r.Error, params,
		r.Counts.Total, r.Counts.Pending, r.Counts.Running, r.Counts.Completed, r.Counts.Errored, r.Counts.Cancelled, r.Counts.Passed, r.Counts.Failed,
		r.CostUSD, ptr(r.StartedAt), ptr(r.FinishedAt), int64(r.ID))
	if err != nil {
		return fmt.Errorf("update experiment run %d: %w", r.ID, err)
	}
	return db.Affected(res, fmt.Sprintf("update experiment run %d", r.ID))
}

// UpdateEvalRun rewrites the status, error, params, counts, cost and times of r. Its Meta is not stored: see UpdateEvalRunMeta.
func UpdateEvalRun(ctx context.Context, q db.Execer, r *core.EvalRun) error {
	params, err := db.JSON(r.Params, "{}")
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `
		UPDATE gb_neo_eval_runs SET status = $1, error = $2, params = $3::jsonb,
			counts_total = $4, counts_pending = $5, counts_running = $6, counts_completed = $7, counts_errored = $8, counts_cancelled = $9,
			counts_passed = $10, counts_failed = $11, cost_usd = $12, started_at = $13, finished_at = $14
		WHERE id = $15`,
		string(r.Status), r.Error, params,
		r.Counts.Total, r.Counts.Pending, r.Counts.Running, r.Counts.Completed, r.Counts.Errored, r.Counts.Cancelled, r.Counts.Passed, r.Counts.Failed,
		r.CostUSD, ptr(r.StartedAt), ptr(r.FinishedAt), int64(r.ID))
	if err != nil {
		return fmt.Errorf("update eval run %d: %w", r.ID, err)
	}
	return db.Affected(res, fmt.Sprintf("update eval run %d", r.ID))
}

// CreateEvalRunMeta inserts m into the table of its type, for the eval run it names.
func CreateEvalRunMeta(ctx context.Context, q db.Execer, m core.EvalRunMeta) error {
	switch m := m.(type) {
	case *core.KnowledgeEvalRunMeta:
		return CreateKnowledgeEvalRunMeta(ctx, q, m)
	case core.KnowledgeEvalRunMeta:
		return CreateKnowledgeEvalRunMeta(ctx, q, &m)
	case *core.TrainingEvalRunMeta:
		return CreateTrainingEvalRunMeta(ctx, q, m)
	case core.TrainingEvalRunMeta:
		return CreateTrainingEvalRunMeta(ctx, q, &m)
	}
	return fmt.Errorf("create eval run meta: unknown type %T", m)
}

// UpdateEvalRunMeta rewrites the row of m in the table of its type.
func UpdateEvalRunMeta(ctx context.Context, q db.Execer, m core.EvalRunMeta) error {
	switch m := m.(type) {
	case *core.KnowledgeEvalRunMeta:
		return UpdateKnowledgeEvalRunMeta(ctx, q, m)
	case core.KnowledgeEvalRunMeta:
		return UpdateKnowledgeEvalRunMeta(ctx, q, &m)
	case *core.TrainingEvalRunMeta:
		return UpdateTrainingEvalRunMeta(ctx, q, m)
	case core.TrainingEvalRunMeta:
		return UpdateTrainingEvalRunMeta(ctx, q, &m)
	}
	return fmt.Errorf("update eval run meta: unknown type %T", m)
}

// UpdateKnowledgeEvalRunMeta rewrites the row of m in gb_neo_eval_runs_meta_knowledge.
func UpdateKnowledgeEvalRunMeta(ctx context.Context, q db.Execer, m *core.KnowledgeEvalRunMeta) error {
	res, err := q.ExecContext(ctx, `
		UPDATE gb_neo_eval_runs_meta_knowledge SET chunks = $1, steps = $2, remember_calls = $3, recall_calls = $4, remember_cost_usd = $5,
			recall_cost_usd = $6, checks_passed = $7, checks_failed = $8
		WHERE eval_run_id = $9`,
		m.Chunks, m.Steps, m.RememberCalls, m.RecallCalls, m.RememberCostUSD, m.RecallCostUSD, m.ChecksPassed, m.ChecksFailed, int64(m.EvalRunID))
	if err != nil {
		return fmt.Errorf("update knowledge eval run meta of eval run %d: %w", m.EvalRunID, err)
	}
	return db.Affected(res, fmt.Sprintf("update knowledge eval run meta of eval run %d", m.EvalRunID))
}

// UpdateTrainingEvalRunMeta rewrites the row of m in gb_neo_eval_runs_meta_training. The trained model does not change.
func UpdateTrainingEvalRunMeta(ctx context.Context, q db.Execer, m *core.TrainingEvalRunMeta) error {
	res, err := q.ExecContext(ctx, `
		UPDATE gb_neo_eval_runs_meta_training SET judge_model = $1, num_answered = $2, num_judged = $3, num_correct = $4, num_partial = $5,
			num_incorrect = $6, score_sum = $7, num_batches = $8, judge_calls = $9, judge_input_tokens = $10, judge_output_tokens = $11, judge_cost_usd = $12
		WHERE eval_run_id = $13`,
		m.JudgeModel, m.NumAnswered, m.NumJudged, m.NumCorrect, m.NumPartial, m.NumIncorrect, m.ScoreSum, m.NumBatches, m.JudgeCalls,
		m.JudgeInputTokens, m.JudgeOutputTokens, m.JudgeCostUSD, int64(m.EvalRunID))
	if err != nil {
		return fmt.Errorf("update training eval run meta of eval run %d: %w", m.EvalRunID, err)
	}
	return db.Affected(res, fmt.Sprintf("update training eval run meta of eval run %d", m.EvalRunID))
}

// UpdateCaseRun rewrites the status, error, verdict, score, cost and times of r. Its Meta is not stored.
func UpdateCaseRun(ctx context.Context, q db.Execer, r *core.CaseRun) error {
	res, err := q.ExecContext(ctx, `
		UPDATE gb_neo_case_runs SET status = $1, error = $2, verdict = $3, score = $4, cost_usd = $5, started_at = $6, finished_at = $7
		WHERE id = $8`,
		string(r.Status), r.Error, string(r.Verdict), r.Score, r.CostUSD, ptr(r.StartedAt), ptr(r.FinishedAt), int64(r.ID))
	if err != nil {
		return fmt.Errorf("update case run %d: %w", r.ID, err)
	}
	return db.Affected(res, fmt.Sprintf("update case run %d", r.ID))
}

// CreateCaseRunMeta inserts m into the table of its type, for the case run it names. A knowledge one is stored with its
// Trajectories.
func CreateCaseRunMeta(ctx context.Context, q db.Execer, m core.CaseRunMeta) error {
	switch m := m.(type) {
	case *core.KnowledgeCaseRunMeta:
		if err := CreateKnowledgeCaseRunMeta(ctx, q, m); err != nil {
			return err
		}
		for i := range m.Trajectories {
			if err := CreateKnowledgeTrajectory(ctx, q, m.CaseRunID, &m.Trajectories[i]); err != nil {
				return err
			}
		}
		return nil
	case core.KnowledgeCaseRunMeta:
		return CreateCaseRunMeta(ctx, q, &m)
	case *core.TrainingCaseRunMeta:
		return CreateTrainingCaseRunMeta(ctx, q, m)
	case core.TrainingCaseRunMeta:
		return CreateTrainingCaseRunMeta(ctx, q, &m)
	}
	return fmt.Errorf("create case run meta: unknown type %T", m)
}
