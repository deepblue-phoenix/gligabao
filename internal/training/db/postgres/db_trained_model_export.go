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
// Exports of trained models (see sqlite/db_trained_model_export.go for what the functions do)

// InsertTrainedModelExport stores a new export and returns its ID.
func InsertTrainedModelExport(ctx context.Context, d *sql.DB, e model.TrainedModelExport) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO gb_trained_model_exports (trained_model_id, exporter, options, status, created_at) VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		e.TrainedModelID, e.Exporter, e.OptionsJSON(), e.Status, rpostgres.Now()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert trained model export: %w", err)
	}
	return id, nil
}

// SetTrainedModelExportDir records the directory the weights of export id were fetched to.
func SetTrainedModelExportDir(ctx context.Context, d *sql.DB, id int64, dir string) error {
	res, err := d.ExecContext(ctx, `UPDATE gb_trained_model_exports SET dir = $1 WHERE id = $2`, dir, id)
	if err != nil {
		return fmt.Errorf("set trained model export dir: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// FinishTrainedModelExport ends an export.
func FinishTrainedModelExport(ctx context.Context, d *sql.DB, id int64, status, ref, detailsJSON, errText string, durationMS int64) error {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_trained_model_exports SET status = $1, ref = $2, details = $3, error = $4, duration_ms = $5, completed_at = $6 WHERE id = $7`,
		status, ref, detailsJSON, errText, durationMS, rpostgres.Now(), id)
	if err != nil {
		return fmt.Errorf("finish trained model export: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

const trainedModelExportColumns = `id, trained_model_id, exporter, options, status, dir, ref, details, error, duration_ms, created_at, completed_at`

func scanTrainedModelExport(s rowScanner) (*model.TrainedModelExport, error) {
	var e model.TrainedModelExport
	var options, details string
	var completed sql.NullTime
	if err := s.Scan(&e.ID, &e.TrainedModelID, &e.Exporter, &options, &e.Status, &e.Dir, &e.Ref, &details, &e.Error,
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

// GetTrainedModelExport returns the export with that ID, or model.ErrNotFound.
func GetTrainedModelExport(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelExport, error) {
	e, err := scanTrainedModelExport(d.QueryRowContext(ctx, `SELECT `+trainedModelExportColumns+` FROM gb_trained_model_exports WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// ListTrainedModelExports returns the newest exports first (of one trained model, or of all for modelID 0).
func ListTrainedModelExports(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]model.TrainedModelExport, error) {
	q := `SELECT ` + trainedModelExportColumns + ` FROM gb_trained_model_exports`
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
		return nil, fmt.Errorf("list trained model exports: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelExport{}
	for rows.Next() {
		e, err := scanTrainedModelExport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}
