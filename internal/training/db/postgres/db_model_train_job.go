package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	rpostgres "github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"

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
	var id int64
	err := d.QueryRowContext(ctx,
		`
		INSERT INTO gb_model_train_jobs (job_id, model_type, user_id, instance_id, status, job, info, started_at, heartbeat_at)
		VALUES ($1, $2, $3, $4, 'running', $5, $6, $7, $7)
		ON CONFLICT DO NOTHING
		RETURNING id`,
		j.JobID,
		j.ModelType,
		j.UserID,
		j.InstanceID,
		j.JobJSON(),
		j.InfoJSON(),
		rpostgres.Now()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrModelTrainJobRunning
	}
	if err != nil {
		return 0, fmt.Errorf("insert model train job: %w", err)
	}
	return id, nil
}

// BeatModelTrainJob records that instanceID is still following the running job and its latest state from sargo (jobJSON). It says
// whether the job is still running and owned by that instance (false: it was ended or marked lost by someone else).
func BeatModelTrainJob(ctx context.Context,
	d *sql.DB,
	jobID,
	instanceID,
	jobJSON string) (bool, error) {
	res, err := d.ExecContext(ctx,
		`UPDATE gb_model_train_jobs SET heartbeat_at = $1, job = $2 WHERE job_id = $3 AND instance_id = $4 AND status = 'running'`,
		rpostgres.Normalize(rpostgres.Now()),
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
		`UPDATE gb_model_train_jobs SET status = $1, error = $2, job = $3, model_id = $4, finished_at = $5
		 WHERE job_id = $6 AND status IN ('running', 'lost')`,
		status,
		errText,
		jobJSON,
		mid,
		rpostgres.Normalize(rpostgres.Now()),
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
		`UPDATE gb_model_train_jobs SET status = 'lost', error = $1, finished_at = $2
		 WHERE model_type = $3 AND user_id = $4 AND status = 'running' AND heartbeat_at < $5`,
		"the instance that ran the training stopped before it ended",
		rpostgres.Normalize(rpostgres.Now()),
		modelType,
		userID,
		rpostgres.Normalize(staleBefore))
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
	var finished sql.NullTime
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
		&j.StartedAt,
		&j.HeartbeatAt,
		&finished); err != nil {
		return nil, err
	}
	if modelID.Valid {
		j.ModelID = &modelID.Int64
	}
	j.SetStoredJSON(job, info)
	if finished.Valid {
		j.FinishedAt = &finished.Time
	}
	return &j, nil
}

// LatestModelTrainJob returns the newest training of the user of that model type, or model.ErrNotFound.
func LatestModelTrainJob(ctx context.Context,
	d *sql.DB,
	modelType,
	userID string) (*model.ModelTrainJob, error) {
	j, err := scanModelTrainJob(d.QueryRowContext(ctx,
		`SELECT `+modelTrainJobColumns+` FROM gb_model_train_jobs WHERE model_type = $1 AND user_id = $2 ORDER BY id DESC LIMIT 1`,
		modelType,
		userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return j, err
}
