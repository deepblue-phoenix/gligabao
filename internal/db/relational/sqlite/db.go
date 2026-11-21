// Package sqlite manages the SQLite database: connection, the relational schema (datasets, inferences), the timestamp storage convention and the SQL for them. The graph tables live in internal/db/graph/sqlite.
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
	SELECT id, user_id, name, description, tags, created_at, deleted, deleted_at, kind, eval_run_id
	FROM gb_datasets`

type scanner interface{ Scan(dest ...any) error }

func scanDataset(r scanner) (*model.Dataset, error) {
	var ds model.Dataset
	var tags, created string
	var deleted int
	var deletedAt sql.NullString
	var evalRunID sql.NullInt64
	if err := r.Scan(&ds.ID,
		&ds.UserID,
		&ds.Name,
		&ds.Description,
		&tags,
		&created,
		&deleted,
		&deletedAt,
		&ds.Kind,
		&evalRunID); err != nil {
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
	if evalRunID.Valid {
		ds.EvalRunID = &evalRunID.Int64
	}
	return &ds, nil
}

// CreateDataset creates a regular dataset (kind 'data') owned by userID. It returns model.ErrExists if a live regular dataset with
// that name exists for the user.
func CreateDataset(ctx context.Context,
	d *sql.DB,
	userID,
	name,
	description string,
	tags []string) (*model.Dataset, error) {
	return createDataset(ctx,
		d,
		userID,
		name,
		description,
		tags,
		model.KindData,
		nil)
}

// CreateEvalDataset creates a dataset of kind 'eval' that belongs to the eval run evalRunID. It returns model.ErrExists if the user
// has a live eval dataset with that name.
func CreateEvalDataset(ctx context.Context,
	d *sql.DB,
	userID,
	name string,
	evalRunID int64) (*model.Dataset, error) {
	return createDataset(ctx,
		d,
		userID,
		name,
		"",
		nil,
		model.KindEval,
		&evalRunID)
}

func createDataset(ctx context.Context,
	d *sql.DB,
	userID,
	name,
	description string,
	tags []string,
	kind string,
	evalRunID *int64) (*model.Dataset, error) {
	tagsJSON, _ := json.Marshal(tags)
	var runID any
	if evalRunID != nil {
		runID = *evalRunID
	}
	res, err := d.ExecContext(ctx,
		`
		INSERT INTO gb_datasets (user_id, name, description, tags, created_at, kind, eval_run_id)
		SELECT ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1
			FROM gb_datasets
			WHERE user_id = ? AND name = ? AND kind = ? AND deleted = 0
		)`,
		userID,
		name,
		description,
		string(tagsJSON),
		FormatTime(Now()),
		kind,
		runID,
		userID,
		name,
		kind)
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
	return scanDataset(d.QueryRowContext(ctx,
		datasetSelect+`
		WHERE id = ?`,
		id))
}

// ListDatasets returns userID's regular datasets (kind 'data') oldest first; deleted ones only if includeDeleted.
func ListDatasets(ctx context.Context,
	d *sql.DB,
	userID string,
	includeDeleted bool) ([]model.Dataset, error) {
	q := datasetSelect + `
		WHERE user_id = ? AND kind = 'data'`
	if !includeDeleted {
		q += `
		AND deleted = 0`
	}
	return queryDatasets(ctx,
		d,
		q+`
		ORDER BY id`,
		"list datasets",
		userID)
}

// ListAllDatasets returns the regular datasets (kind 'data') of all users, oldest first; deleted ones only if includeDeleted.
func ListAllDatasets(ctx context.Context,
	d *sql.DB,
	includeDeleted bool) ([]model.Dataset, error) {
	q := datasetSelect + `
		WHERE kind = 'data'`
	if !includeDeleted {
		q += `
		AND deleted = 0`
	}
	return queryDatasets(ctx,
		d,
		q+`
		ORDER BY id`,
		"list all datasets")
}

// ListAllDatasetsAnyKind is ListAllDatasets over the datasets of both kinds ('data' and 'eval').
func ListAllDatasetsAnyKind(ctx context.Context,
	d *sql.DB,
	includeDeleted bool) ([]model.Dataset, error) {
	q := datasetSelect
	if !includeDeleted {
		q += `
		WHERE deleted = 0`
	}
	return queryDatasets(ctx,
		d,
		q+`
		ORDER BY id`,
		"list all datasets")
}

// ListEvalDatasets returns the live eval datasets of userID that belong to the eval run evalRunID, oldest first.
func ListEvalDatasets(ctx context.Context,
	d *sql.DB,
	userID string,
	evalRunID int64) ([]model.Dataset, error) {
	return queryDatasets(ctx,
		d,
		datasetSelect+`
		WHERE user_id = ? AND kind = 'eval' AND eval_run_id = ? AND deleted = 0
		ORDER BY id`,
		"list eval datasets",
		userID,
		evalRunID)
}

func queryDatasets(ctx context.Context,
	d *sql.DB,
	query,
	what string,
	args ...any) ([]model.Dataset, error) {
	rows, err := d.QueryContext(ctx,
		query,
		args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
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

// DeleteDataset soft-deletes userID's live regular dataset (kind 'data') called name. It returns model.ErrNotFound if there is none
// (an eval dataset is never deleted through this function).
func DeleteDataset(ctx context.Context,
	d *sql.DB,
	userID,
	name string) error {
	res, err := d.ExecContext(ctx,
		`
		UPDATE gb_datasets
		SET deleted = 1, deleted_at = ?
		WHERE user_id = ? AND name = ? AND kind = 'data' AND deleted = 0`,
		FormatTime(Now()),
		userID,
		name)
	if err != nil {
		return fmt.Errorf("delete dataset: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// DeleteEvalDatasets soft-deletes the live eval datasets of userID that belong to the eval run evalRunID, and returns their ids (the
// caller removes their vectors).
func DeleteEvalDatasets(ctx context.Context,
	d *sql.DB,
	userID string,
	evalRunID int64) ([]int64, error) {
	list, err := ListEvalDatasets(ctx,
		d,
		userID,
		evalRunID)
	if err != nil {
		return nil, err
	}
	if _, err := d.ExecContext(ctx,
		`
		UPDATE gb_datasets
		SET deleted = 1, deleted_at = ?
		WHERE user_id = ? AND kind = 'eval' AND eval_run_id = ? AND deleted = 0`,
		FormatTime(Now()),
		userID,
		evalRunID); err != nil {
		return nil, fmt.Errorf("delete eval datasets: %w", err)
	}
	ids := make([]int64, 0, len(list))
	for _, ds := range list {
		ids = append(ids, ds.ID)
	}
	return ids, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context,
		query string,
		args ...any) *sql.Row
}

func datasetID(ctx context.Context,
	q queryer,
	userID,
	name,
	kind string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		`
		SELECT id
		FROM gb_datasets
		WHERE user_id = ? AND name = ? AND kind = ? AND deleted = 0`,
		userID,
		name,
		kind).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return id, err
}

// DatasetID returns the ID of userID's live regular dataset (kind 'data') called name, or model.ErrNotFound.
func DatasetID(ctx context.Context,
	d *sql.DB,
	userID,
	name string) (int64, error) {
	return datasetID(ctx,
		d,
		userID,
		name,
		model.KindData)
}

// EvalDatasetID returns the ID of userID's live eval dataset called name that belongs to the eval run evalRunID, or model.ErrNotFound.
func EvalDatasetID(ctx context.Context,
	d *sql.DB,
	userID,
	name string,
	evalRunID int64) (int64, error) {
	id, err := datasetID(ctx,
		d,
		userID,
		name,
		model.KindEval)
	if err != nil {
		return 0, err
	}
	var owner sql.NullInt64
	if err := d.QueryRowContext(ctx,
		`SELECT eval_run_id FROM gb_datasets WHERE id = ?`,
		id).Scan(&owner); err != nil {
		return 0, err
	}
	if !owner.Valid || owner.Int64 != evalRunID {
		return 0, model.ErrNotFound
	}
	return id, nil
}

// EnsureDataset returns the ID of userID's live regular dataset (kind 'data') called name, creating it first if needed.
func EnsureDataset(ctx context.Context,
	d *sql.DB,
	userID,
	name string) (int64, error) {
	if _, err := createDataset(ctx,
		d,
		userID,
		name,
		"",
		nil,
		model.KindData,
		nil); err != nil && !errors.Is(err, model.ErrExists) {
		return 0, err
	}
	return datasetID(ctx,
		d,
		userID,
		name,
		model.KindData)
}

// EnsureEvalDataset returns the ID of userID's live eval dataset called name of the eval run evalRunID, creating it first if needed.
func EnsureEvalDataset(ctx context.Context,
	d *sql.DB,
	userID,
	name string,
	evalRunID int64) (int64, error) {
	if _, err := createDataset(ctx,
		d,
		userID,
		name,
		"",
		nil,
		model.KindEval,
		&evalRunID); err != nil && !errors.Is(err, model.ErrExists) {
		return 0, err
	}
	return EvalDatasetID(ctx,
		d,
		userID,
		name,
		evalRunID)
}

// DatasetSelect is the SELECT of the columns ScanDataset reads (add a WHERE clause), for the packages that join to datasets.
const DatasetSelect = datasetSelect

// ScanDataset reads a row selected with DatasetSelect.
func ScanDataset(r interface{ Scan(dest ...any) error }) (*model.Dataset, error) {
	return scanDataset(r)
}
