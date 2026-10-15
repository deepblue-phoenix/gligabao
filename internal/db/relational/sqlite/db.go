// Package sqlite manages the SQLite database: connection, the relational schema (datasets, model runs), the timestamp storage convention and the SQL for them. The graph tables live in internal/db/graph/sqlite.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

//---------------------------------------------------------------------------------------------

// Open opens (creating if needed) the SQLite database at path and applies the schema.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	d, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := initSchema(d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

//---------------------------------------------------------------------------------------------
// Datasets

const datasetSelect = `
	SELECT id, user_id, name, description, tags, created_at, deleted, deleted_at
	FROM gb_datasets`

type scanner interface{ Scan(dest ...any) error }

func scanDataset(r scanner) (*model.Dataset, error) {
	var ds model.Dataset
	var tags, created string
	var deleted int
	var deletedAt sql.NullString
	if err := r.Scan(&ds.ID, &ds.UserID, &ds.Name, &ds.Description, &tags, &created, &deleted, &deletedAt); err != nil {
		return nil, err
	}
	ds.Tags = []string{}
	if err := json.Unmarshal([]byte(tags), &ds.Tags); err != nil || ds.Tags == nil {
		ds.Tags = []string{}
	}
	if t := ParseTime(created); t != nil {
		ds.CreatedAt = *t
	}
	ds.Deleted = deleted == 1
	ds.DeletedAt = ParseTime(deletedAt.String)
	return &ds, nil
}

// CreateDataset creates a dataset owned by userID. It returns model.ErrExists if a live dataset with that name
// exists for the user.
func CreateDataset(ctx context.Context, d *sql.DB, userID, name, description string, tags []string) (*model.Dataset, error) {
	tagsJSON, _ := json.Marshal(tags)
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_datasets (user_id, name, description, tags, created_at)
		SELECT ?, ?, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1
			FROM gb_datasets
			WHERE user_id = ? AND name = ? AND deleted = 0
		)`,
		userID, name, description, string(tagsJSON), FormatTime(Now()), userID, name)
	if err != nil {
		return nil, fmt.Errorf("create dataset: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, model.ErrExists
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanDataset(d.QueryRowContext(ctx, datasetSelect+`
		WHERE id = ?`, id))
}

// ListDatasets returns userID's datasets oldest first; deleted ones only if includeDeleted.
func ListDatasets(ctx context.Context, d *sql.DB, userID string, includeDeleted bool) ([]model.Dataset, error) {
	q := datasetSelect + `
		WHERE user_id = ?`
	if !includeDeleted {
		q += `
		AND deleted = 0`
	}
	rows, err := d.QueryContext(ctx, q+`
		ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list datasets: %w", err)
	}
	defer rows.Close()
	out := []model.Dataset{}
	for rows.Next() {
		ds, err := scanDataset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ds)
	}
	return out, rows.Err()
}

// ListAllDatasets returns the datasets of all users, oldest first; deleted ones only if includeDeleted.
func ListAllDatasets(ctx context.Context, d *sql.DB, includeDeleted bool) ([]model.Dataset, error) {
	q := datasetSelect
	if !includeDeleted {
		q += `
		WHERE deleted = 0`
	}
	rows, err := d.QueryContext(ctx, q+`
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list all datasets: %w", err)
	}
	defer rows.Close()
	out := []model.Dataset{}
	for rows.Next() {
		ds, err := scanDataset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ds)
	}
	return out, rows.Err()
}

// DeleteDataset soft-deletes userID's live dataset called name. It returns model.ErrNotFound if there is none.
func DeleteDataset(ctx context.Context, d *sql.DB, userID, name string) error {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_datasets
		SET deleted = 1, deleted_at = ?
		WHERE user_id = ? AND name = ? AND deleted = 0`,
		FormatTime(Now()), userID, name)
	if err != nil {
		return fmt.Errorf("delete dataset: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func datasetID(ctx context.Context, q queryer, userID, name string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `
		SELECT id
		FROM gb_datasets
		WHERE user_id = ? AND name = ? AND deleted = 0`, userID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return id, err
}

// DatasetID returns the ID of userID's live dataset called name, or model.ErrNotFound.
func DatasetID(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	return datasetID(ctx, d, userID, name)
}

// EnsureDataset returns the ID of userID's live dataset called name, creating it first if needed.
func EnsureDataset(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	if _, err := d.ExecContext(ctx, `
		INSERT INTO gb_datasets (user_id, name, created_at)
		SELECT ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1
			FROM gb_datasets
			WHERE user_id = ? AND name = ? AND deleted = 0
		)`,
		userID, name, FormatTime(Now()), userID, name); err != nil {
		return 0, fmt.Errorf("create dataset: %w", err)
	}
	return datasetID(ctx, d, userID, name)
}
