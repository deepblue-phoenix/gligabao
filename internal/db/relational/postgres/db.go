// Package postgres manages the PostgreSQL database: connection, the relational schema (datasets, LLM call log),
// the timestamp convention and the SQL for them. The functions here are reached through the routing functions of
// package relational and mirror those of package sqlite, in the PostgreSQL dialect ($n placeholders, RETURNING,
// native types). The graph tables live in internal/db/graph/postgres.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ivan/gligabao/internal/db/relational/model"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver, registered as "pgx"
)

//---------------------------------------------------------------------------------------------

// Open connects to the PostgreSQL database at dsn (a URL such as postgres://user:pass@host:5432/dbname, or a
// key=value connection string) and applies the schema.
func Open(dsn string) (*sql.DB, error) {
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.PingContext(ctx); err != nil {
		d.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := initSchema(ctx, d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func utc(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time.UTC()
	return &t
}

//---------------------------------------------------------------------------------------------
// Datasets

const datasetColumns = `id, user_id, name, description, tags::text, created_at, deleted, deleted_at`
const datasetSelect = `SELECT ` + datasetColumns + ` FROM gb_datasets`

type scanner interface{ Scan(dest ...any) error }

func scanDataset(r scanner) (*model.Dataset, error) {
	var ds model.Dataset
	var tags string
	var deletedAt sql.NullTime
	if err := r.Scan(&ds.ID, &ds.UserID, &ds.Name, &ds.Description, &tags, &ds.CreatedAt, &ds.Deleted, &deletedAt); err != nil {
		return nil, err
	}
	ds.Tags = []string{}
	if err := json.Unmarshal([]byte(tags), &ds.Tags); err != nil || ds.Tags == nil {
		ds.Tags = []string{}
	}
	ds.CreatedAt = ds.CreatedAt.UTC()
	ds.DeletedAt = utc(deletedAt)
	return &ds, nil
}

// CreateDataset creates a dataset owned by userID. It returns model.ErrExists if a live dataset with that name
// exists for the user.
func CreateDataset(ctx context.Context, d *sql.DB, userID, name, description string, tags []string) (*model.Dataset, error) {
	tagsJSON, _ := json.Marshal(tags)
	ds, err := scanDataset(d.QueryRowContext(ctx, `
		INSERT INTO gb_datasets (user_id, name, description, tags, created_at) VALUES ($1, $2, $3, $4::jsonb, $5)
		ON CONFLICT (user_id, name) WHERE NOT deleted DO NOTHING
		RETURNING `+datasetColumns, userID, name, description, string(tagsJSON), Now()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrExists
	}
	if err != nil {
		return nil, fmt.Errorf("create dataset: %w", err)
	}
	return ds, nil
}

// ListDatasets returns userID's datasets oldest first; deleted ones only if includeDeleted.
func ListDatasets(ctx context.Context, d *sql.DB, userID string, includeDeleted bool) ([]model.Dataset, error) {
	q := datasetSelect + ` WHERE user_id = $1`
	if !includeDeleted {
		q += ` AND NOT deleted`
	}
	rows, err := d.QueryContext(ctx, q+` ORDER BY id`, userID)
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

// DeleteDataset soft-deletes userID's live dataset called name. It returns model.ErrNotFound if there is none.
func DeleteDataset(ctx context.Context, d *sql.DB, userID, name string) error {
	res, err := d.ExecContext(ctx, `UPDATE gb_datasets SET deleted = TRUE, deleted_at = $1 WHERE user_id = $2 AND name = $3 AND NOT deleted`,
		Now(), userID, name)
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
	err := q.QueryRowContext(ctx, `SELECT id FROM gb_datasets WHERE user_id = $1 AND name = $2 AND NOT deleted`, userID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return id, err
}

// DatasetID returns the ID of userID's live dataset called name, or model.ErrNotFound.
func DatasetID(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	return datasetID(ctx, d, userID, name)
}

//---------------------------------------------------------------------------------------------
// LLM call log

// RecordLLMCall stores one LLM/embedding API call.
func RecordLLMCall(ctx context.Context, d *sql.DB, kind, provider, modelName string, inputTokens, outputTokens int, costUSD float64) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO gb_llm_calls (type, provider, model, input_tokens, output_tokens, cost_usd) VALUES ($1, $2, $3, $4, $5, $6)`,
		kind, provider, modelName, inputTokens, outputTokens, costUSD)
	return err
}

// LLMCallStats sums the logged LLM calls by type.
func LLMCallStats(ctx context.Context, d *sql.DB) (map[string]model.LLMCallStat, error) {
	rows, err := d.QueryContext(ctx, `SELECT type, COUNT(*), SUM(input_tokens)::bigint, SUM(output_tokens)::bigint, SUM(cost_usd) FROM gb_llm_calls GROUP BY type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.LLMCallStat{}
	for rows.Next() {
		var typ string
		var s model.LLMCallStat
		if err := rows.Scan(&typ, &s.Calls, &s.InputTokens, &s.OutputTokens, &s.CostUSD); err != nil {
			return nil, err
		}
		out[typ] = s
	}
	return out, rows.Err()
}

// EnsureDataset returns the ID of userID's live dataset called name, creating it first if needed.
func EnsureDataset(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	if _, err := d.ExecContext(ctx, `
		INSERT INTO gb_datasets (user_id, name, created_at) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, name) WHERE NOT deleted DO NOTHING`,
		userID, name, Now()); err != nil {
		return 0, fmt.Errorf("create dataset: %w", err)
	}
	return datasetID(ctx, d, userID, name)
}
