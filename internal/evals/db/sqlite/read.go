package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
)

// The get functions read a record with the parts that belong to it (an eval def with its cases, a case def with its Meta, an
// experiment def with its eval IDs). They return db.ErrNotFound if it does not exist.

// parseTime reads a time written by text.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// GetEvalDefByName returns the eval def called name with its Cases, ordered by position, each with its Meta (a pointer to the
// struct of its type: *core.KnowledgeCaseDefMeta, *core.TrainingCaseDefMeta).
func GetEvalDefByName(ctx context.Context, q db.Execer, name string) (*core.EvalDef, error) {
	var d core.EvalDef
	var id int64
	var typ string
	var tags, params []byte
	var created, updated string
	err := q.QueryRowContext(ctx, `SELECT id, name, description, type, tags, params, created_at, updated_at FROM gb_eval_defs WHERE name = ?`, name).
		Scan(&id, &d.Name, &d.Description, &typ, &tags, &params, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("eval def %q: %w", name, db.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get eval def %q: %w", name, err)
	}
	d.ID, d.Type = core.EvalDefID(id), core.Type(typ)
	if err := db.Unmarshal(tags, &d.Tags); err != nil {
		return nil, err
	}
	if err := db.Unmarshal(params, &d.Params); err != nil {
		return nil, err
	}
	if len(d.Params) == 0 {
		d.Params = nil
	}
	if d.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if d.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, err
	}
	if d.Cases, err = listCaseDefs(ctx, q, d.ID); err != nil {
		return nil, err
	}
	return &d, nil
}

func listCaseDefs(ctx context.Context, q db.Execer, evalDefID core.EvalDefID) ([]core.CaseDef, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, type, name, position, description, tags, created_at, updated_at FROM gb_case_defs
		WHERE eval_def_id = ? ORDER BY position`, int64(evalDefID))
	if err != nil {
		return nil, fmt.Errorf("list case defs of eval def %d: %w", evalDefID, err)
	}
	defer rows.Close()
	var cases []core.CaseDef
	for rows.Next() {
		c := core.CaseDef{EvalDefID: evalDefID}
		var id int64
		var typ string
		var tags []byte
		var created, updated string
		if err := rows.Scan(&id, &typ, &c.Name, &c.Position, &c.Description, &tags, &created, &updated); err != nil {
			return nil, fmt.Errorf("list case defs of eval def %d: %w", evalDefID, err)
		}
		c.ID, c.Type = core.CaseDefID(id), core.Type(typ)
		if err := db.Unmarshal(tags, &c.Tags); err != nil {
			return nil, err
		}
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if c.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list case defs of eval def %d: %w", evalDefID, err)
	}
	rows.Close()
	for i := range cases {
		if cases[i].Meta, err = getCaseDefMeta(ctx, q, cases[i].ID, cases[i].Type); err != nil {
			return nil, err
		}
	}
	return cases, nil
}

// getCaseDefMeta reads the row of case def id in the table of typ.
func getCaseDefMeta(ctx context.Context, q db.Execer, id core.CaseDefID, typ core.Type) (core.CaseDefMeta, error) {
	switch typ {
	case core.TypeKnowledge:
		m := &core.KnowledgeCaseDefMeta{CaseDefID: id}
		var remember, contains, edges, series, models, reasoning []byte
		err := q.QueryRowContext(ctx, `
			SELECT remember, query, expect_contains, expect_edges, expect_min_chunks, expect_web_searches, expect_series, chunk_size, models, reasoning
			FROM gb_case_def_meta_knowledge WHERE case_def_id = ?`, int64(id)).
			Scan(&remember, &m.Query, &contains, &edges, &m.ExpectMinChunks, &m.ExpectWebSearches, &series, &m.ChunkSize, &models, &reasoning)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("knowledge case def meta of case def %d: %w", id, db.ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("get knowledge case def meta of case def %d: %w", id, err)
		}
		for _, f := range []struct {
			text []byte
			to   any
		}{{remember, &m.Remember}, {contains, &m.ExpectContains}, {edges, &m.ExpectEdges}, {series, &m.ExpectSeries}, {models, &m.Models}, {reasoning, &m.Reasoning}} {
			if err := db.Unmarshal(f.text, f.to); err != nil {
				return nil, err
			}
		}
		if len(m.Models) == 0 {
			m.Models = nil
		}
		if len(m.Reasoning) == 0 {
			m.Reasoning = nil
		}
		return m, nil
	case core.TypeTraining:
		m := &core.TrainingCaseDefMeta{CaseDefID: id}
		err := q.QueryRowContext(ctx, `SELECT instruction, input, output FROM gb_case_def_meta_training WHERE case_def_id = ?`, int64(id)).
			Scan(&m.Instruction, &m.Input, &m.Output)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("training case def meta of case def %d: %w", id, db.ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("get training case def meta of case def %d: %w", id, err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("case def %d: unknown type %q", id, typ)
}

// GetExperimentDefByName returns the experiment def called name, with its EvalDefIDs in the order the evals run.
func GetExperimentDefByName(ctx context.Context, q db.Execer, name string) (*core.ExperimentDef, error) {
	var d core.ExperimentDef
	var id int64
	var params []byte
	var created, updated string
	err := q.QueryRowContext(ctx, `SELECT id, name, description, params, created_at, updated_at FROM gb_experiment_defs WHERE name = ?`, name).
		Scan(&id, &d.Name, &d.Description, &params, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("experiment def %q: %w", name, db.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get experiment def %q: %w", name, err)
	}
	d.ID = core.ExperimentDefID(id)
	if err := db.Unmarshal(params, &d.Params); err != nil {
		return nil, err
	}
	if len(d.Params) == 0 {
		d.Params = nil
	}
	if d.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if d.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT eval_def_id FROM gb_experiment_def_evals WHERE experiment_def_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, fmt.Errorf("get the evals of experiment def %q: %w", name, err)
	}
	defer rows.Close()
	d.EvalDefIDs = []core.EvalDefID{}
	for rows.Next() {
		var e int64
		if err := rows.Scan(&e); err != nil {
			return nil, fmt.Errorf("get the evals of experiment def %q: %w", name, err)
		}
		d.EvalDefIDs = append(d.EvalDefIDs, core.EvalDefID(e))
	}
	return &d, rows.Err()
}
