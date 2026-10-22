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
// Exports of trained models (gb_trained_model_exports, see db_schema.go)

// InsertTrainedModelExport stores a new export and returns its ID.
func InsertTrainedModelExport(ctx context.Context, d *sql.DB, e model.TrainedModelExport) (int64, error) {
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_trained_model_exports (trained_model_id, exporter, options, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		e.TrainedModelID, e.Exporter, e.OptionsJSON(), e.Status, rsqlite.FormatTime(rsqlite.Now()))
	if err != nil {
		return 0, fmt.Errorf("insert trained model export: %w", err)
	}
	return res.LastInsertId()
}

// SetTrainedModelExportDir records the directory the weights of export id were fetched to.
func SetTrainedModelExportDir(ctx context.Context, d *sql.DB, id int64, dir string) error {
	res, err := d.ExecContext(ctx, `UPDATE gb_trained_model_exports SET dir = ? WHERE id = ?`, dir, id)
	if err != nil {
		return fmt.Errorf("set trained model export dir: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// FinishTrainedModelExport ends an export (see db.FinishTrainedModelExport).
func FinishTrainedModelExport(ctx context.Context, d *sql.DB, id int64, status, ref, detailsJSON, errText string, durationMS int64) error {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_trained_model_exports SET status = ?, ref = ?, details = ?, error = ?, duration_ms = ?, completed_at = ? WHERE id = ?`,
		status, ref, detailsJSON, errText, durationMS, rsqlite.FormatTime(rsqlite.Now()), id)
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
	var options, details, created string
	var completed sql.NullString
	if err := s.Scan(&e.ID, &e.TrainedModelID, &e.Exporter, &options, &e.Status, &e.Dir, &e.Ref, &details, &e.Error,
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

// GetTrainedModelExport returns the export with that ID, or model.ErrNotFound.
func GetTrainedModelExport(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelExport, error) {
	e, err := scanTrainedModelExport(d.QueryRowContext(ctx, `SELECT `+trainedModelExportColumns+` FROM gb_trained_model_exports WHERE id = ?`, id))
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
