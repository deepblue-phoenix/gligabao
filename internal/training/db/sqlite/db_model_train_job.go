package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"

	"github.com/deepblue-phoenix/gligabao/internal/training/db/model"
)

//---------------------------------------------------------------------------------------------
// Trainings outside the sidecar (gb_model_train_jobs, see sqlite/sql/schema.sql)

// InsertModelTrainJob claims the training of a user: it makes the running row of j (the status and the times are set here) and returns
// its ID. A running row of the same user and model type whose last beat is older than staleBefore is marked lost first; if one that is
// alive remains, model.ErrModelTrainJobRunning is returned (the unique index of running rows decides, so two instances never both win).
func InsertModelTrainJob(ctx context.Context,
	d *sql.DB,
	j model.ModelTrainJob,
	staleBefore time.Time) (int64, error) {
	if _, err := ExpireModelTrainJobs(ctx,
		d,
		j.ModelType,
		j.UserID,
		staleBefore); err != nil {
		return 0, err
	}
	res, err := d.ExecContext(ctx,
		`
		INSERT INTO gb_model_train_jobs (job_id, model_type, user_id, instance_id, status, job, info, started_at, heartbeat_at)
		VALUES (?, ?, ?, ?, 'running', ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		j.JobID,
		j.ModelType,
		j.UserID,
		j.InstanceID,
		j.JobJSON(),
		j.InfoJSON(),
		rsqlite.FormatTime(rsqlite.Now()),
		rsqlite.FormatTime(rsqlite.Now()))
	if err != nil {
		return 0, fmt.Errorf("insert model train job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, model.ErrModelTrainJobRunning
	}
	return res.LastInsertId()
}

// BeatModelTrainJob records that instanceID is still following the running job and its latest state from sargo (jobJSON). It says
// whether the job is still running and owned by that instance (false: it was ended or marked lost by someone else).
func BeatModelTrainJob(ctx context.Context,
	d *sql.DB,
	jobID,
	instanceID,
	jobJSON string) (bool, error) {
	res, err := d.ExecContext(ctx,
		`UPDATE gb_model_train_jobs SET heartbeat_at = ?, job = ? WHERE job_id = ? AND instance_id = ? AND status = 'running'`,
		rsqlite.FormatTime(rsqlite.Now()),
		jobJSON,
		jobID,
		instanceID)
	if err != nil {
		return false, fmt.Errorf("beat model train job: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// EndModelTrainJob ends the job with status (done, failed or lost), the error, its last state from sargo and the model it made (nil
// if none). A job that was marked lost can still be ended by its instance, which really finished it.
func EndModelTrainJob(ctx context.Context,
	d *sql.DB,
	jobID,
	status,
	errText,
	jobJSON string,
	modelID *int64) error {
	var mid any
	if modelID != nil {
		mid = *modelID
	}
	_, err := d.ExecContext(ctx,
		`UPDATE gb_model_train_jobs SET status = ?, error = ?, job = ?, model_id = ?, finished_at = ?
		 WHERE job_id = ? AND status IN ('running', 'lost')`,
		status,
		errText,
		jobJSON,
		mid,
		rsqlite.FormatTime(rsqlite.Now()),
		jobID)
	if err != nil {
		return fmt.Errorf("end model train job: %w", err)
	}
	return nil
}

// ExpireModelTrainJobs marks the running jobs of the user whose last beat is older than staleBefore as lost; it returns how many.
func ExpireModelTrainJobs(ctx context.Context,
	d *sql.DB,
	modelType,
	userID string,
	staleBefore time.Time) (int64, error) {
	res, err := d.ExecContext(ctx,
		`UPDATE gb_model_train_jobs SET status = 'lost', error = ?, finished_at = ?
		 WHERE model_type = ? AND user_id = ? AND status = 'running' AND heartbeat_at < ?`,
		"the instance that ran the training stopped before it ended",
		rsqlite.FormatTime(rsqlite.Now()),
		modelType,
		userID,
		rsqlite.FormatTime(staleBefore))
	if err != nil {
		return 0, fmt.Errorf("expire model train jobs: %w", err)
	}
	return res.RowsAffected()
}

const modelTrainJobColumns = `id, job_id, model_type, user_id, instance_id, status, error, job, info, model_id, started_at, heartbeat_at, finished_at`

func scanModelTrainJob(s rowScanner) (*model.ModelTrainJob, error) {
	var j model.ModelTrainJob
	var job, info string
	var modelID sql.NullInt64
	var started, beat string
	var finished sql.NullString
	if err := s.Scan(&j.ID,
		&j.JobID,
		&j.ModelType,
		&j.UserID,
		&j.InstanceID,
		&j.Status,
		&j.Error,
		&job,
		&info,
		&modelID,
		&started,
		&beat,
		&finished); err != nil {
		return nil, err
	}
	if modelID.Valid {
		j.ModelID = &modelID.Int64
	}
	j.SetStoredJSON(job, info)
	j.StartedAt = parseStoredTime(started)
	j.HeartbeatAt = parseStoredTime(beat)
	if finished.Valid {
		t := parseStoredTime(finished.String)
		j.FinishedAt = &t
	}
	return &j, nil
}

// LatestModelTrainJob returns the newest training of the user of that model type, or model.ErrNotFound.
func LatestModelTrainJob(ctx context.Context,
	d *sql.DB,
	modelType,
	userID string) (*model.ModelTrainJob, error) {
	j, err := scanModelTrainJob(d.QueryRowContext(ctx,
		`SELECT `+modelTrainJobColumns+` FROM gb_model_train_jobs WHERE model_type = ? AND user_id = ? ORDER BY id DESC LIMIT 1`,
		modelType,
		userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return j, err
}
