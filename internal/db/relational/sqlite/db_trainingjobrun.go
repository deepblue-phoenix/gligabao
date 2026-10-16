package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Training job runs and their events (gb_training_job_runs, gb_training_job_events, see db_schema.go)

// InsertTrainingJobRun stores one training job run and returns its ID. r.ID and r.CreatedAt are ignored, and so are the
// fields that only events set (last step and loss, final loss, error, finish time).
func InsertTrainingJobRun(ctx context.Context, d *sql.DB, r model.TrainingJobRun) (int64, error) {
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_training_job_runs (job_id, provider_job_id, status, model_size, dataset, num_train_steps, started_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.JobID, r.ProviderJobID, r.Status, r.ModelSize, r.Dataset, r.NumTrainSteps, FormatTime(r.StartedAt), FormatTime(Now()))
	if err != nil {
		return 0, fmt.Errorf("insert training job run: %w", err)
	}
	return res.LastInsertId()
}

const trainingJobRunColumns = `id, job_id, provider_job_id, status, model_size, dataset, num_train_steps, last_step, last_loss,
	final_loss, error, started_at, created_at, finished_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanTrainingJobRun(s rowScanner) (*model.TrainingJobRun, error) {
	var r model.TrainingJobRun
	var lastLoss, finalLoss sql.NullFloat64
	var started, created string
	var finished sql.NullString
	if err := s.Scan(&r.ID, &r.JobID, &r.ProviderJobID, &r.Status, &r.ModelSize, &r.Dataset, &r.NumTrainSteps, &r.LastStep,
		&lastLoss, &finalLoss, &r.Error, &started, &created, &finished); err != nil {
		return nil, err
	}
	if lastLoss.Valid {
		r.LastLoss = &lastLoss.Float64
	}
	if finalLoss.Valid {
		r.FinalLoss = &finalLoss.Float64
	}
	r.StartedAt = parseStoredTime(started)
	r.CreatedAt = parseStoredTime(created)
	if finished.Valid {
		t := parseStoredTime(finished.String)
		r.FinishedAt = &t
	}
	return &r, nil
}

// GetTrainingJobRun returns the training job run with that ID, or model.ErrNotFound.
func GetTrainingJobRun(ctx context.Context, d *sql.DB, id int64) (*model.TrainingJobRun, error) {
	r, err := scanTrainingJobRun(d.QueryRowContext(ctx, `SELECT `+trainingJobRunColumns+` FROM gb_training_job_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return r, err
}

// ListTrainingJobRuns returns the newest training job runs first, at most limit of them; with unfinished only the runs
// that have not completed or failed (all of them, oldest first, for limit <= 0).
func ListTrainingJobRuns(ctx context.Context, d *sql.DB, limit int, unfinished bool) ([]model.TrainingJobRun, error) {
	q := `SELECT ` + trainingJobRunColumns + ` FROM gb_training_job_runs`
	if unfinished {
		q += ` WHERE status NOT IN ('` + model.TrainingJobCompleted + `', '` + model.TrainingJobFailed + `')`
	}
	var args []any
	if limit > 0 {
		q += ` ORDER BY id DESC LIMIT ?`
		args = append(args, limit)
	} else {
		q += ` ORDER BY id`
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TrainingJobRun{}
	for rows.Next() {
		r, err := scanTrainingJobRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// UpdateTrainingJobRun writes the fields of u that are set to the run; model.ErrNotFound if there is no such run.
func UpdateTrainingJobRun(ctx context.Context, d *sql.DB, id int64, u model.TrainingJobRunUpdate) error {
	var sets []string
	var args []any
	set := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if u.Status != "" {
		set("status", u.Status)
	}
	if u.Step != nil {
		set("last_step", *u.Step)
		if u.Loss != nil {
			set("last_loss", *u.Loss)
		}
	}
	if u.FinalLoss != nil {
		set("final_loss", *u.FinalLoss)
	}
	if u.Error != "" {
		set("error", u.Error)
	}
	if u.FinishedAt != nil {
		set("finished_at", FormatTime(*u.FinishedAt))
	}
	if len(sets) == 0 {
		return nil
	}
	res, err := d.ExecContext(ctx, `UPDATE gb_training_job_runs SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...)
	if err != nil {
		return fmt.Errorf("update training job run: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// InsertTrainingJobEvent stores the event with index seq of a run. It reports whether it was new: an event with that
// seq already stored (the sidecar's stream replayed it) is left as it is.
func InsertTrainingJobEvent(ctx context.Context, d *sql.DB, runID int64, seq int, typ, data string) (bool, error) {
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_training_job_events (run_id, seq, type, data, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (run_id, seq) DO NOTHING`, runID, seq, typ, data, FormatTime(Now()))
	if err != nil {
		return false, fmt.Errorf("insert training job event: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ListTrainingJobEvents returns the events of a run with seq >= from, in order.
func ListTrainingJobEvents(ctx context.Context, d *sql.DB, runID int64, from int) ([]model.TrainingJobEvent, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT run_id, seq, type, data, created_at FROM gb_training_job_events
		WHERE run_id = ? AND seq >= ? ORDER BY seq`, runID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TrainingJobEvent{}
	for rows.Next() {
		var e model.TrainingJobEvent
		var created string
		if err := rows.Scan(&e.RunID, &e.Seq, &e.Type, &e.Data, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = parseStoredTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// NextTrainingJobEventSeq returns the seq the next event of a run would have: one past the newest stored (0 if none).
func NextTrainingJobEventSeq(ctx context.Context, d *sql.DB, runID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq) + 1, 0) FROM gb_training_job_events WHERE run_id = ?`, runID).Scan(&n)
	return n, err
}
