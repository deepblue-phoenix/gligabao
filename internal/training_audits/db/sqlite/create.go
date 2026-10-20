package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db"
)

// The create functions insert one record into its table. Where the database generates the ID, it is set on the record; where
// a time is zero it is set to now (UTC, second precision, the precision that survives the storage round trip) and set on the
// record too; an empty Status is StatusPending. The rest is stored as it is: what the schema refuses (an unknown status, a
// score out of range, a duplicate idx, ...) comes back as the error of the database.

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

// CreateAuditRun inserts r into gb_training_audit_runs, with its AuditStats, and sets its ID, Status and CreatedAt. Its
// CaseRuns are not stored: they are added with CreateAuditCaseRun.
func CreateAuditRun(ctx context.Context, q db.Execer, r *core.AuditRun) error {
	params, err := db.JSON(r.Params, "{}")
	if err != nil {
		return err
	}
	r.Status = status(r.Status)
	r.CreatedAt = fill(r.CreatedAt)
	var id int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_training_audit_runs (trained_model_id, dataset, dataset_sha256, status, error, params,
			counts_total, counts_pending, counts_running, counts_completed, counts_errored, counts_cancelled, cost_usd,
			judge_model, num_answered, num_judged, num_correct, num_partial, num_incorrect, score_sum, num_batches, judge_calls,
			judge_input_tokens, judge_output_tokens, created_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		r.TrainedModelID, r.Dataset, r.DatasetSHA256, string(r.Status), r.Error, params,
		r.Counts.Total, r.Counts.Pending, r.Counts.Running, r.Counts.Completed, r.Counts.Errored, r.Counts.Cancelled, r.CostUSD,
		r.JudgeModel, r.NumAnswered, r.NumJudged, r.NumCorrect, r.NumPartial, r.NumIncorrect, r.ScoreSum, r.NumBatches, r.JudgeCalls,
		r.JudgeInputTokens, r.JudgeOutputTokens, text(r.CreatedAt), textPtr(r.StartedAt), textPtr(r.FinishedAt)).Scan(&id); err != nil {
		return fmt.Errorf("create training audit run of trained model %d: %w", r.TrainedModelID, err)
	}
	r.ID = core.AuditRunID(id)
	return nil
}

// CreateAuditCaseRun inserts c into gb_training_audit_case_runs, as an example of the audit run c.RunID, and sets its ID,
// Status and CreatedAt.
func CreateAuditCaseRun(ctx context.Context, q db.Execer, c *core.AuditCaseRun) error {
	c.Status = status(c.Status)
	c.CreatedAt = fill(c.CreatedAt)
	var id int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_training_audit_case_runs (run_id, idx, status, error, instruction, input, reference, response, batch,
			judge_score, judge_reason, input_tokens, output_tokens, duration_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		int64(c.RunID), c.Idx, string(c.Status), c.Error, c.Instruction, c.Input, c.Reference, c.Response, c.Batch,
		c.JudgeScore, c.JudgeReason, c.InputTokens, c.OutputTokens, c.DurationMS, text(c.CreatedAt)).Scan(&id); err != nil {
		return fmt.Errorf("create training audit case run %d of audit run %d: %w", c.Idx, c.RunID, err)
	}
	c.ID = core.AuditCaseRunID(id)
	return nil
}
