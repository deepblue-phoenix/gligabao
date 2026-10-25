package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	rpostgres "github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/model"
)

//---------------------------------------------------------------------------------------------
// Deployments of trained models (see sqlite/db_trained_model_deploy.go for what the functions do)

// InsertTrainedModelDeployment stores a new deployment and returns its ID.
func InsertTrainedModelDeployment(ctx context.Context, d *sql.DB, e model.TrainedModelDeployment) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO gb_trained_model_deployments (trained_model_id, deployer, options, status, created_at) VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		e.TrainedModelID, e.Deployer, e.OptionsJSON(), e.Status, rpostgres.Now()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert trained model deploy: %w", err)
	}
	return id, nil
}

// SetTrainedModelDeployDir records the directory the weights of deploy id were fetched to.
func SetTrainedModelDeployDir(ctx context.Context, d *sql.DB, id int64, dir string) error {
	res, err := d.ExecContext(ctx, `UPDATE gb_trained_model_deployments SET dir = $1 WHERE id = $2`, dir, id)
	if err != nil {
		return fmt.Errorf("set trained model deploy dir: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// FinishTrainedModelDeployment ends a deployment.
func FinishTrainedModelDeployment(ctx context.Context, d *sql.DB, id int64, status, ref, detailsJSON, errText string, durationMS int64) error {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_trained_model_deployments SET status = $1, ref = $2, details = $3, error = $4, duration_ms = $5, completed_at = $6 WHERE id = $7`,
		status, ref, detailsJSON, errText, durationMS, rpostgres.Now(), id)
	if err != nil {
		return fmt.Errorf("finish trained model deploy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

const trainedModelDeployColumns = `id, trained_model_id, deployer, options, status, dir, ref, details, error, duration_ms, created_at, completed_at`

func scanTrainedModelDeployment(s rowScanner) (*model.TrainedModelDeployment, error) {
	var e model.TrainedModelDeployment
	var options, details string
	var completed sql.NullTime
	if err := s.Scan(&e.ID, &e.TrainedModelID, &e.Deployer, &options, &e.Status, &e.Dir, &e.Ref, &details, &e.Error,
		&e.DurationMS, &e.CreatedAt, &completed); err != nil {
		return nil, err
	}
	e.SetStoredJSON(options, details)
	e.CreatedAt = e.CreatedAt.UTC()
	if completed.Valid {
		t := completed.Time.UTC()
		e.CompletedAt = &t
	}
	return &e, nil
}

// GetTrainedModelDeployment returns the deployment with that ID, or model.ErrNotFound.
func GetTrainedModelDeployment(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelDeployment, error) {
	e, err := scanTrainedModelDeployment(d.QueryRowContext(ctx, `SELECT `+trainedModelDeployColumns+` FROM gb_trained_model_deployments WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// ListTrainedModelDeployments returns the newest deployments first (of one trained model, or of all for modelID 0).
func ListTrainedModelDeployments(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]model.TrainedModelDeployment, error) {
	q := `SELECT ` + trainedModelDeployColumns + ` FROM gb_trained_model_deployments`
	var args []any
	if modelID > 0 {
		args = append(args, modelID)
		q += fmt.Sprintf(` WHERE trained_model_id = $%d`, len(args))
	}
	q += ` ORDER BY id DESC`
	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf(` LIMIT $%d`, len(args))
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list trained model deployments: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelDeployment{}
	for rows.Next() {
		e, err := scanTrainedModelDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}
