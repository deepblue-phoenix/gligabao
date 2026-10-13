package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Models

// SyncModels makes models the supported models: it clears the supported flag of every stored model, then inserts
// each of models (or updates the provider and kind of the stored one) as supported. Models seen earlier but no
// longer supported stay as unsupported rows, because entities and edges may reference them. The ID of each model is
// derived from its name (model.ModelID); the Model.ID and Supported fields of models are ignored.
func SyncModels(ctx context.Context, d *sql.DB, models []model.Model) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sync models: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE gb_models SET supported = 0`); err != nil {
		return fmt.Errorf("sync models: %w", err)
	}
	for _, m := range models {
		id := model.ModelID(m.Name)
		if id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_models (id, name, provider, kind, supported)
			VALUES (?, ?, ?, ?, 1)
			ON CONFLICT (id) DO UPDATE SET
				provider  = excluded.provider,
				kind      = excluded.kind,
				supported = 1`, id, m.Name, m.Provider, kindOrLLM(m.Kind)); err != nil {
			return fmt.Errorf("sync model %q: %w", m.Name, err)
		}
	}
	return tx.Commit()
}

// EnsureModel inserts the model (as not supported) if it is not stored yet and returns its ID. A stored model is
// left unchanged. It returns "" for an empty name.
func EnsureModel(ctx context.Context, d *sql.DB, m model.Model) (string, error) {
	id := model.ModelID(m.Name)
	if id == "" {
		return "", nil
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO gb_models (id, name, provider, kind)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`, id, m.Name, m.Provider, kindOrLLM(m.Kind)); err != nil {
		return "", fmt.Errorf("ensure model %q: %w", m.Name, err)
	}
	return id, nil
}

// ListModels returns every stored model by name.
func ListModels(ctx context.Context, d *sql.DB) ([]model.Model, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, name, provider, kind, supported, created_at
		FROM gb_models
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()
	var out []model.Model
	for rows.Next() {
		var m model.Model
		var supported int
		var created string
		if err := rows.Scan(&m.ID, &m.Name, &m.Provider, &m.Kind, &supported, &created); err != nil {
			return nil, err
		}
		m.Supported = supported == 1
		if t := ParseTime(created); t != nil {
			m.CreatedAt = *t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func kindOrLLM(k string) string {
	if k == "" {
		return "llm"
	}
	return k
}
