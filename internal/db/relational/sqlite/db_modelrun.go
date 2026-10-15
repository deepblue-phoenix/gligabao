package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Model runs (gb_model_runs, see db_schema.go)

// InsertModelRun stores one model run and returns its ID. r.ModelID must already be stored in gb_models ("" for
// unknown); r.Reasoning must be valid.
func InsertModelRun(ctx context.Context, d *sql.DB, r model.ModelRun) (int64, error) {
	var modelID any
	if r.ModelID != "" {
		modelID = r.ModelID
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_model_runs (type, stage, model_id, reasoning, input_tokens, output_tokens, cost_usd, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Type, r.Stage, modelID, r.Reasoning, r.InputTokens, r.OutputTokens, r.CostUSD, FormatTime(Now()))
	if err != nil {
		return 0, fmt.Errorf("insert model run: %w", err)
	}
	return res.LastInsertId()
}

// GetModelRun returns the model run with that ID (its model read from gb_models), or model.ErrNotFound.
func GetModelRun(ctx context.Context, d *sql.DB, id int64) (*model.ModelRun, error) {
	var r model.ModelRun
	var created string
	err := d.QueryRowContext(ctx, `
		SELECT r.id, r.type, r.stage, COALESCE(r.model_id, ''), COALESCE(m.name, ''), COALESCE(m.provider, ''), COALESCE(m.kind, ''),
			r.reasoning, r.input_tokens, r.output_tokens, r.cost_usd, r.created_at
		FROM gb_model_runs r
		LEFT JOIN gb_models m ON m.id = r.model_id
		WHERE r.id = ?`, id).Scan(&r.ID, &r.Type, &r.Stage, &r.ModelID, &r.Model, &r.Provider, &r.Kind,
		&r.Reasoning, &r.InputTokens, &r.OutputTokens, &r.CostUSD, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if t := ParseTime(created); t != nil {
		r.CreatedAt = *t
	}
	return &r, nil
}

// ModelRunStats sums the recorded model runs by type.
func ModelRunStats(ctx context.Context, d *sql.DB) (map[string]model.ModelRunStat, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT type, COUNT(*), SUM(input_tokens), SUM(output_tokens), SUM(cost_usd)
		FROM gb_model_runs
		GROUP BY type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.ModelRunStat{}
	for rows.Next() {
		var typ string
		var s model.ModelRunStat
		if err := rows.Scan(&typ, &s.Runs, &s.InputTokens, &s.OutputTokens, &s.CostUSD); err != nil {
			return nil, err
		}
		out[typ] = s
	}
	return out, rows.Err()
}
