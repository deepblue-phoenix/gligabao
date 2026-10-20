package sqlite

import (
	"context"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
)

// reads are the functions that read the runs, the same SQL for both backends.
var reads = db.NewReads(db.SQLiteDialect)

// Store holds the functions of this package, for code that works with either backend (db.Store).
var Store = db.Store{
	CreateEvalDef:               CreateEvalDef,
	UpdateEvalDef:               UpdateEvalDef,
	GetEvalDefByName:            GetEvalDefByName,
	CreateCaseDef:               CreateCaseDef,
	UpdateCaseDef:               UpdateCaseDef,
	CreateCaseDefMeta:           CreateCaseDefMeta,
	UpdateCaseDefMeta:           UpdateCaseDefMeta,
	CreateExperimentDef:         CreateExperimentDef,
	CreateExperimentDefEval:     CreateExperimentDefEval,
	GetExperimentDefByName:      GetExperimentDefByName,
	CreateExperimentRun:         CreateExperimentRun,
	UpdateExperimentRun:         UpdateExperimentRun,
	CreateEvalRun:               CreateEvalRun,
	UpdateEvalRun:               UpdateEvalRun,
	CreateEvalRunMeta:           CreateEvalRunMeta,
	UpdateEvalRunMeta:           UpdateEvalRunMeta,
	CreateCaseRun:               CreateCaseRun,
	UpdateCaseRun:               UpdateCaseRun,
	CreateCaseRunMeta:           CreateCaseRunMeta,
	ListExperimentDefs:          reads.ListExperimentDefs,
	GetExperimentDef:            reads.GetExperimentDef,
	GetEvalDef:                  reads.GetEvalDef,
	ListCaseDefs:                reads.ListCaseDefs,
	ListExperimentCases:         reads.ListExperimentCases,
	ListExperimentRuns:          reads.ListExperimentRuns,
	GetExperimentRun:            reads.GetExperimentRun,
	GetEvalRun:                  reads.GetEvalRun,
	ListEvalRunsOfExperimentRun: reads.ListEvalRunsOfExperimentRun,
	ListEvalRunsOfExperiment:    reads.ListEvalRunsOfExperiment,
	ListTrainingEvalRuns:        reads.ListTrainingEvalRuns,
	ListCaseRuns:                reads.ListCaseRuns,
	GetCaseRun:                  reads.GetCaseRun,
	FindKnowledgeTrajectory:     reads.FindKnowledgeTrajectory,
	ListCaseHistory:             reads.ListCaseHistory,
	FailRunning:                 reads.FailRunning,
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
	res, err := q.ExecContext(ctx, `UPDATE gb_eval_defs SET description = ?, tags = ?, params = ?, updated_at = ? WHERE id = ?`,
		d.Description, tags, params, text(d.UpdatedAt), int64(d.ID))
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
	res, err := q.ExecContext(ctx, `UPDATE gb_case_defs SET position = ?, description = ?, tags = ?, updated_at = ? WHERE id = ?`,
		d.Position, d.Description, tags, text(d.UpdatedAt), int64(d.ID))
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

// UpdateKnowledgeCaseDefMeta rewrites the row of m in gb_case_def_meta_knowledge.
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
		UPDATE gb_case_def_meta_knowledge SET remember = ?, query = ?, expect_contains = ?, expect_edges = ?,
			expect_min_chunks = ?, expect_web_searches = ?, expect_series = ?, chunk_size = ?, models = ?, reasoning = ?
		WHERE case_def_id = ?`,
		remember, m.Query, contains, edges, m.ExpectMinChunks, m.ExpectWebSearches, series, m.ChunkSize, models, reasoning, int64(m.CaseDefID))
	if err != nil {
		return fmt.Errorf("update knowledge case def meta of case def %d: %w", m.CaseDefID, err)
	}
	return db.Affected(res, fmt.Sprintf("update knowledge case def meta of case def %d", m.CaseDefID))
}

// UpdateTrainingCaseDefMeta rewrites the row of m in gb_case_def_meta_training.
func UpdateTrainingCaseDefMeta(ctx context.Context, q db.Execer, m *core.TrainingCaseDefMeta) error {
	res, err := q.ExecContext(ctx, `UPDATE gb_case_def_meta_training SET instruction = ?, input = ?, output = ? WHERE case_def_id = ?`,
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
		UPDATE gb_experiment_runs SET status = ?, error = ?, params = ?,
			counts_total = ?, counts_pending = ?, counts_running = ?, counts_completed = ?, counts_errored = ?, counts_cancelled = ?,
			counts_passed = ?, counts_failed = ?, cost_usd = ?, started_at = ?, finished_at = ?
		WHERE id = ?`,
		string(r.Status), r.Error, params,
		r.Counts.Total, r.Counts.Pending, r.Counts.Running, r.Counts.Completed, r.Counts.Errored, r.Counts.Cancelled, r.Counts.Passed, r.Counts.Failed,
		r.CostUSD, textPtr(r.StartedAt), textPtr(r.FinishedAt), int64(r.ID))
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
		UPDATE gb_eval_runs SET status = ?, error = ?, params = ?,
			counts_total = ?, counts_pending = ?, counts_running = ?, counts_completed = ?, counts_errored = ?, counts_cancelled = ?,
			counts_passed = ?, counts_failed = ?, cost_usd = ?, started_at = ?, finished_at = ?
		WHERE id = ?`,
		string(r.Status), r.Error, params,
		r.Counts.Total, r.Counts.Pending, r.Counts.Running, r.Counts.Completed, r.Counts.Errored, r.Counts.Cancelled, r.Counts.Passed, r.Counts.Failed,
		r.CostUSD, textPtr(r.StartedAt), textPtr(r.FinishedAt), int64(r.ID))
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

// UpdateKnowledgeEvalRunMeta rewrites the row of m in gb_eval_runs_meta_knowledge.
func UpdateKnowledgeEvalRunMeta(ctx context.Context, q db.Execer, m *core.KnowledgeEvalRunMeta) error {
	res, err := q.ExecContext(ctx, `
		UPDATE gb_eval_runs_meta_knowledge SET chunks = ?, steps = ?, remember_calls = ?, recall_calls = ?, remember_cost_usd = ?,
			recall_cost_usd = ?, checks_passed = ?, checks_failed = ?
		WHERE eval_run_id = ?`,
		m.Chunks, m.Steps, m.RememberCalls, m.RecallCalls, m.RememberCostUSD, m.RecallCostUSD, m.ChecksPassed, m.ChecksFailed, int64(m.EvalRunID))
	if err != nil {
		return fmt.Errorf("update knowledge eval run meta of eval run %d: %w", m.EvalRunID, err)
	}
	return db.Affected(res, fmt.Sprintf("update knowledge eval run meta of eval run %d", m.EvalRunID))
}

// UpdateTrainingEvalRunMeta rewrites the row of m in gb_eval_runs_meta_training. The trained model does not change.
func UpdateTrainingEvalRunMeta(ctx context.Context, q db.Execer, m *core.TrainingEvalRunMeta) error {
	res, err := q.ExecContext(ctx, `
		UPDATE gb_eval_runs_meta_training SET judge_model = ?, num_answered = ?, num_judged = ?, num_correct = ?, num_partial = ?,
			num_incorrect = ?, score_sum = ?, num_batches = ?, judge_calls = ?, judge_input_tokens = ?, judge_output_tokens = ?, judge_cost_usd = ?
		WHERE eval_run_id = ?`,
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
		UPDATE gb_case_runs SET status = ?, error = ?, verdict = ?, score = ?, cost_usd = ?, started_at = ?, finished_at = ?
		WHERE id = ?`,
		string(r.Status), r.Error, string(r.Verdict), r.Score, r.CostUSD, textPtr(r.StartedAt), textPtr(r.FinishedAt), int64(r.ID))
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
