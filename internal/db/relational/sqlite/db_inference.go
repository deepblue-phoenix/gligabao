package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Inferences (gb_inferences, see db_schema.go)

// InsertInference stores one inference and returns its ID. r.ModelID must already be stored in gb_models ("" for
// unknown); r.Reasoning must be valid.
func InsertInference(ctx context.Context, d *sql.DB, r model.Inference) (int64, error) {
	var modelID any
	if r.ModelID != "" {
		modelID = r.ModelID
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_inferences (type, stage, model_id, reasoning, input_tokens, output_tokens, cost_usd, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Type, r.Stage, modelID, r.Reasoning, r.InputTokens, r.OutputTokens, r.CostUSD, FormatTime(Now()))
	if err != nil {
		return 0, fmt.Errorf("insert inference: %w", err)
	}
	return res.LastInsertId()
}

// GetInference returns the inference with that ID (its model read from gb_models), or model.ErrNotFound.
func GetInference(ctx context.Context, d *sql.DB, id int64) (*model.Inference, error) {
	var r model.Inference
	var created string
	err := d.QueryRowContext(ctx, `
		SELECT r.id, r.type, r.stage, COALESCE(r.model_id, ''), COALESCE(m.name, ''), COALESCE(m.provider, ''), COALESCE(m.kind, ''),
			r.reasoning, r.input_tokens, r.output_tokens, r.cost_usd, r.created_at
		FROM gb_inferences r
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

// InferenceStats sums the recorded inferences by type.
func InferenceStats(ctx context.Context, d *sql.DB) (map[string]model.InferenceStat, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT type, COUNT(*), SUM(input_tokens), SUM(output_tokens), SUM(cost_usd)
		FROM gb_inferences
		GROUP BY type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.InferenceStat{}
	for rows.Next() {
		var typ string
		var s model.InferenceStat
		if err := rows.Scan(&typ, &s.Inferences, &s.InputTokens, &s.OutputTokens, &s.CostUSD); err != nil {
			return nil, err
		}
		out[typ] = s
	}
	return out, rows.Err()
}

// InferenceGroups sums all the inferences by type, stage, model and thinking level, largest cost first.
func InferenceGroups(ctx context.Context, d *sql.DB) ([]model.InferenceGroup, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT r.type, r.stage, COALESCE(r.model_id, ''), COALESCE(m.name, ''), r.reasoning,
			COUNT(*), SUM(r.input_tokens), SUM(r.output_tokens), SUM(r.cost_usd)
		FROM gb_inferences r
		LEFT JOIN gb_models m ON m.id = r.model_id
		GROUP BY r.type, r.stage, r.model_id, m.name, r.reasoning
		ORDER BY SUM(r.cost_usd) DESC, COUNT(*) DESC, r.type, r.stage`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.InferenceGroup{}
	for rows.Next() {
		var g model.InferenceGroup
		if err := rows.Scan(&g.Type, &g.Stage, &g.ModelID, &g.Model, &g.Reasoning, &g.Inferences, &g.InputTokens, &g.OutputTokens, &g.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListInferences returns the inferences that match f, newest first (at most limit, after skipping offset), and how many
// match f in all.
func ListInferences(ctx context.Context, d *sql.DB, f model.InferenceFilter, limit, offset int) ([]model.Inference, int, error) {

	var conds []string
	var args []any
	for _, c := range []struct{ col, val string }{{"r.type", f.Type}, {"r.stage", f.Stage}, {"r.model_id", f.ModelID}, {"r.reasoning", f.Reasoning}} {
		if c.val != "" {
			args = append(args, c.val)
			conds = append(conds, c.col+" = "+"?")
		}
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM gb_inferences r"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.QueryContext(ctx, `
		SELECT r.id, r.type, r.stage, COALESCE(r.model_id, ''), COALESCE(m.name, ''), COALESCE(m.provider, ''), COALESCE(m.kind, ''),
			r.reasoning, r.input_tokens, r.output_tokens, r.cost_usd, r.created_at
		FROM gb_inferences r
		LEFT JOIN gb_models m ON m.id = r.model_id`+where+`
		ORDER BY r.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.Inference{}
	for rows.Next() {
		var r model.Inference
		var created string
		if err := rows.Scan(&r.ID, &r.Type, &r.Stage, &r.ModelID, &r.Model, &r.Provider, &r.Kind,
			&r.Reasoning, &r.InputTokens, &r.OutputTokens, &r.CostUSD, &created); err != nil {
			return nil, 0, err
		}
		if t := ParseTime(created); t != nil {
			r.CreatedAt = *t
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}
