package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Model runs (see sqlite/db_modelrun.go for what the functions do)

// InsertModelRun stores one model run and returns its ID.
func InsertModelRun(ctx context.Context, d *sql.DB, r model.ModelRun) (int64, error) {
	var modelID any
	if r.ModelID != "" {
		modelID = r.ModelID
	}
	var id int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO gb_model_runs (type, stage, model_id, reasoning, input_tokens, output_tokens, cost_usd, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		r.Type, r.Stage, modelID, r.Reasoning, r.InputTokens, r.OutputTokens, r.CostUSD, Now()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert model run: %w", err)
	}
	return id, nil
}

// GetModelRun returns the model run with that ID, or model.ErrNotFound.
func GetModelRun(ctx context.Context, d *sql.DB, id int64) (*model.ModelRun, error) {
	var r model.ModelRun
	err := d.QueryRowContext(ctx, `
		SELECT r.id, r.type, r.stage, COALESCE(r.model_id, ''), COALESCE(m.name, ''), COALESCE(m.provider, ''), COALESCE(m.kind, ''),
			r.reasoning, r.input_tokens, r.output_tokens, r.cost_usd, r.created_at
		FROM gb_model_runs r
		LEFT JOIN gb_models m ON m.id = r.model_id
		WHERE r.id = $1`, id).Scan(&r.ID, &r.Type, &r.Stage, &r.ModelID, &r.Model, &r.Provider, &r.Kind,
		&r.Reasoning, &r.InputTokens, &r.OutputTokens, &r.CostUSD, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt = r.CreatedAt.UTC()
	return &r, nil
}

// ModelRunStats sums the recorded model runs by type.
func ModelRunStats(ctx context.Context, d *sql.DB) (map[string]model.ModelRunStat, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT type, COUNT(*), SUM(input_tokens)::bigint, SUM(output_tokens)::bigint, SUM(cost_usd)
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

// ModelRunGroups sums all the model runs by type, stage, model and thinking level, largest cost first.
func ModelRunGroups(ctx context.Context, d *sql.DB) ([]model.ModelRunGroup, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT r.type, r.stage, COALESCE(r.model_id, ''), COALESCE(m.name, ''), r.reasoning,
			COUNT(*), SUM(r.input_tokens)::bigint, SUM(r.output_tokens)::bigint, SUM(r.cost_usd)
		FROM gb_model_runs r
		LEFT JOIN gb_models m ON m.id = r.model_id
		GROUP BY r.type, r.stage, r.model_id, m.name, r.reasoning
		ORDER BY SUM(r.cost_usd) DESC, COUNT(*) DESC, r.type, r.stage`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ModelRunGroup{}
	for rows.Next() {
		var g model.ModelRunGroup
		if err := rows.Scan(&g.Type, &g.Stage, &g.ModelID, &g.Model, &g.Reasoning, &g.Runs, &g.InputTokens, &g.OutputTokens, &g.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListModelRuns returns the model runs that match f, newest first (at most limit, after skipping offset), and how many
// match f in all.
func ListModelRuns(ctx context.Context, d *sql.DB, f model.ModelRunFilter, limit, offset int) ([]model.ModelRun, int, error) {

	var conds []string
	var args []any
	for _, c := range []struct{ col, val string }{{"r.type", f.Type}, {"r.stage", f.Stage}, {"r.model_id", f.ModelID}, {"r.reasoning", f.Reasoning}} {
		if c.val != "" {
			args = append(args, c.val)
			conds = append(conds, c.col+" = "+"$"+strconv.Itoa(len(args)))
		}
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM gb_model_runs r"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	n := len(args)
	rows, err := d.QueryContext(ctx, `
		SELECT r.id, r.type, r.stage, COALESCE(r.model_id, ''), COALESCE(m.name, ''), COALESCE(m.provider, ''), COALESCE(m.kind, ''),
			r.reasoning, r.input_tokens, r.output_tokens, r.cost_usd, r.created_at
		FROM gb_model_runs r
		LEFT JOIN gb_models m ON m.id = r.model_id`+where+`
		ORDER BY r.id DESC LIMIT $`+strconv.Itoa(n+1)+` OFFSET $`+strconv.Itoa(n+2), append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.ModelRun{}
	for rows.Next() {
		var r model.ModelRun
		if err := rows.Scan(&r.ID, &r.Type, &r.Stage, &r.ModelID, &r.Model, &r.Provider, &r.Kind,
			&r.Reasoning, &r.InputTokens, &r.OutputTokens, &r.CostUSD, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		r.CreatedAt = r.CreatedAt.UTC()
		out = append(out, r)
	}
	return out, total, rows.Err()
}
