package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/model"
)

//---------------------------------------------------------------------------------------------
// Deployments of trained models (gb_trained_model_deployments, see db_schema.go)

// InsertTrainedModelDeployment stores a new deployment and returns its ID.
func InsertTrainedModelDeployment(ctx context.Context, d *sql.DB, e model.TrainedModelDeployment) (int64, error) {
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_trained_model_deployments (trained_model_id, deployer, options, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		e.TrainedModelID, e.Deployer, e.OptionsJSON(), e.Status, rsqlite.FormatTime(rsqlite.Now()))
	if err != nil {
		return 0, fmt.Errorf("insert trained model deploy: %w", err)
	}
	return res.LastInsertId()
}

// SetTrainedModelDeployDir records the directory the weights of deploy id were fetched to.
func SetTrainedModelDeployDir(ctx context.Context, d *sql.DB, id int64, dir string) error {
	res, err := d.ExecContext(ctx, `UPDATE gb_trained_model_deployments SET dir = ? WHERE id = ?`, dir, id)
	if err != nil {
		return fmt.Errorf("set trained model deploy dir: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// FinishTrainedModelDeployment ends a deployment (see db.FinishTrainedModelDeployment).
func FinishTrainedModelDeployment(ctx context.Context, d *sql.DB, id int64, status, ref, detailsJSON, errText string, durationMS int64) error {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_trained_model_deployments SET status = ?, ref = ?, details = ?, error = ?, duration_ms = ?, completed_at = ? WHERE id = ?`,
		status, ref, detailsJSON, errText, durationMS, rsqlite.FormatTime(rsqlite.Now()), id)
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
	var options, details, created string
	var completed sql.NullString
	if err := s.Scan(&e.ID, &e.TrainedModelID, &e.Deployer, &options, &e.Status, &e.Dir, &e.Ref, &details, &e.Error,
		&e.DurationMS, &created, &completed); err != nil {
		return nil, err
	}
	e.SetStoredJSON(options, details)
	e.CreatedAt = parseStoredTime(created)
	if completed.Valid {
		if t := rsqlite.ParseTime(completed.String); t != nil {
			e.CompletedAt = t
		}
	}
	return &e, nil
}

// GetTrainedModelDeployment returns the deployment with that ID, or model.ErrNotFound.
func GetTrainedModelDeployment(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelDeployment, error) {
	e, err := scanTrainedModelDeployment(d.QueryRowContext(ctx, `SELECT `+trainedModelDeployColumns+` FROM gb_trained_model_deployments WHERE id = ?`, id))
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
		q += ` WHERE trained_model_id = ?`
		args = append(args, modelID)
	}
	q += ` ORDER BY id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
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
