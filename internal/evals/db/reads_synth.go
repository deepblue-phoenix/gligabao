package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
)

// The functions of the synthetic evals (tables gb_synth_*), written once for both backends like the other Reads. Every one takes the
// owner and refuses an eval def or generation of another owner (ErrNotFound).

const synthSettingsCols = `prompt, corpus_prompt, model, num_examples, mode, bulk_size`

func scanSynthSettings(s scanner) (core.SynthSettings, error) {
	var out core.SynthSettings
	var mode string
	err := s.Scan(&out.Prompt,
		&out.CorpusPrompt,
		&out.Model,
		&out.NumExamples,
		&mode,
		&out.BulkSize)
	out.Mode = core.SynthMode(mode)
	return out, err
}

func (r Reads) synthDefs(ctx context.Context,
	q Execer,
	tail string,
	args []any) ([]core.SynthDef, error) {
	rows, err := r.query(ctx,
		q,
		`SELECT eval_def_id, user_id, version, `+synthSettingsCols+`, created_at, updated_at FROM gb_synth_defs `+tail,
		args...)
	if err != nil {
		return nil, fmt.Errorf("list synthetic evals: %w", err)
	}
	defer rows.Close()
	out := []core.SynthDef{}
	for rows.Next() {
		var d core.SynthDef
		var id int64
		var mode string
		if err := rows.Scan(&id,
			&d.UserID,
			&d.Version,
			&d.Prompt,
			&d.CorpusPrompt,
			&d.Model,
			&d.NumExamples,
			&mode,
			&d.BulkSize,
			timeScan{&d.CreatedAt},
			timeScan{&d.UpdatedAt}); err != nil {
			return nil, fmt.Errorf("list synthetic evals: %w", err)
		}
		d.EvalDefID, d.Mode = core.EvalDefID(id), core.SynthMode(mode)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListSynthDefs returns the definitions of the synthetic evals of the owner, by eval def id (empty, not nil, if there are none).
func (r Reads) ListSynthDefs(ctx context.Context,
	q Execer,
	userID string) ([]core.SynthDef, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	return r.synthDefs(ctx,
		q,
		`WHERE user_id = ? ORDER BY eval_def_id`,
		[]any{userID})
}

// GetSynthDef returns the definition of the synthetic eval whose eval def is evalDefID, or ErrNotFound (also for a human eval).
func (r Reads) GetSynthDef(ctx context.Context,
	q Execer,
	userID string,
	evalDefID core.EvalDefID) (*core.SynthDef, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	defs, err := r.synthDefs(ctx,
		q,
		`WHERE user_id = ? AND eval_def_id = ?`,
		[]any{userID, int64(evalDefID)})
	if err != nil {
		return nil, err
	}
	if len(defs) == 0 {
		return nil, fmt.Errorf("synthetic eval %d: %w",
			evalDefID,
			ErrNotFound)
	}
	return &defs[0], nil
}

// CreateSynthDef makes the eval def d.EvalDefID (of the owner) a synthetic eval with the settings of d, at version 1, and records that
// version. It sets Version, CreatedAt and UpdatedAt. It is in one transaction when q can begin one.
func (r Reads) CreateSynthDef(ctx context.Context,
	q Execer,
	userID string,
	d *core.SynthDef) error {
	if err := CheckOwner(userID); err != nil {
		return err
	}
	return r.inTx(ctx,
		q,
		func(q Execer) error {
			d.UserID, d.Version = userID, 1
			if d.CreatedAt.IsZero() {
				d.CreatedAt = time.Now()
			}
			d.CreatedAt = d.CreatedAt.UTC().Truncate(time.Second)
			d.UpdatedAt = d.CreatedAt
			if _, err := q.ExecContext(ctx,
				r.d.Rebind(`
				INSERT INTO gb_synth_defs (eval_def_id, user_id, version, `+synthSettingsCols+`, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
				int64(d.EvalDefID),
				userID,
				d.Version,
				d.Prompt,
				d.CorpusPrompt,
				d.Model,
				d.NumExamples,
				string(d.Mode),
				d.BulkSize,
				r.d.Time(d.CreatedAt),
				r.d.Time(d.UpdatedAt)); err != nil {
				return fmt.Errorf("create synthetic eval %d: %w",
					d.EvalDefID,
					err)
			}
			return r.insertSynthVersion(ctx,
				q,
				d.EvalDefID,
				d.Version,
				d.SynthSettings,
				d.CreatedAt)
		})
}

func (r Reads) insertSynthVersion(ctx context.Context,
	q Execer,
	evalDefID core.EvalDefID,
	version int,
	s core.SynthSettings,
	at time.Time) error {
	_, err := q.ExecContext(ctx,
		r.d.Rebind(`
		INSERT INTO gb_synth_def_versions (eval_def_id, version, `+synthSettingsCols+`, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		int64(evalDefID),
		version,
		s.Prompt,
		s.CorpusPrompt,
		s.Model,
		s.NumExamples,
		string(s.Mode),
		s.BulkSize,
		r.d.Time(at))
	if err != nil {
		return fmt.Errorf("record version %d of synthetic eval %d: %w",
			version,
			evalDefID,
			err)
	}
	return nil
}

// UpdateSynthDef changes the settings of a synthetic eval of the owner. If they differ from the current ones the version is increased
// by one and the new version recorded; if they are the same nothing changes. It returns the definition as it is after the call.
func (r Reads) UpdateSynthDef(ctx context.Context,
	q Execer,
	userID string,
	evalDefID core.EvalDefID,
	s core.SynthSettings) (*core.SynthDef, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	var out *core.SynthDef
	err := r.inTx(ctx,
		q,
		func(q Execer) error {
			cur, err := r.GetSynthDef(ctx,
				q,
				userID,
				evalDefID)
			if err != nil {
				return err
			}
			if cur.SynthSettings.Equal(s) {
				out = cur
				return nil
			}
			cur.Version++
			cur.SynthSettings = s
			cur.UpdatedAt = time.Now().UTC().Truncate(time.Second)
			if _, err := q.ExecContext(ctx,
				r.d.Rebind(`
				UPDATE gb_synth_defs SET version = ?, prompt = ?, corpus_prompt = ?, model = ?, num_examples = ?, mode = ?, bulk_size = ?, updated_at = ?
				WHERE user_id = ? AND eval_def_id = ?`),
				cur.Version,
				s.Prompt,
				s.CorpusPrompt,
				s.Model,
				s.NumExamples,
				string(s.Mode),
				s.BulkSize,
				r.d.Time(cur.UpdatedAt),
				userID,
				int64(evalDefID)); err != nil {
				return fmt.Errorf("update synthetic eval %d: %w",
					evalDefID,
					err)
			}
			out = cur
			return r.insertSynthVersion(ctx,
				q,
				evalDefID,
				cur.Version,
				s,
				cur.UpdatedAt)
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListSynthVersions returns every version of the settings of a synthetic eval of the owner, newest first.
func (r Reads) ListSynthVersions(ctx context.Context,
	q Execer,
	userID string,
	evalDefID core.EvalDefID) ([]core.SynthVersion, error) {
	if _, err := r.GetSynthDef(ctx,
		q,
		userID,
		evalDefID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		q,
		`SELECT version, `+synthSettingsCols+`, created_at FROM gb_synth_def_versions WHERE eval_def_id = ? ORDER BY version DESC`,
		int64(evalDefID))
	if err != nil {
		return nil, fmt.Errorf("list versions of synthetic eval %d: %w",
			evalDefID,
			err)
	}
	defer rows.Close()
	out := []core.SynthVersion{}
	for rows.Next() {
		v := core.SynthVersion{
			EvalDefID: evalDefID,
		}
		var mode string
		if err := rows.Scan(&v.Version,
			&v.Prompt,
			&v.CorpusPrompt,
			&v.Model,
			&v.NumExamples,
			&mode,
			&v.BulkSize,
			timeScan{&v.CreatedAt}); err != nil {
			return nil, fmt.Errorf("list versions of synthetic eval %d: %w",
				evalDefID,
				err)
		}
		v.Mode = core.SynthMode(mode)
		out = append(out, v)
	}
	return out, rows.Err()
}

const synthGenerationCols = `id, user_id, eval_def_id, version, model, mode, status, error, requested, generated, kept, calls,
	input_tokens, output_tokens, cost_usd, created_at, started_at, finished_at, updated_at`

func scanSynthGeneration(s scanner) (core.SynthGeneration, error) {
	var g core.SynthGeneration
	var id, def int64
	var mode, status string
	err := s.Scan(&id,
		&g.UserID,
		&def,
		&g.Version,
		&g.Model,
		&mode,
		&status,
		&g.Error,
		&g.Requested,
		&g.Generated,
		&g.Kept,
		&g.Calls,
		&g.InputTokens,
		&g.OutputTokens,
		&g.CostUSD,
		timeScan{&g.CreatedAt},
		nullTimeScan{&g.StartedAt},
		nullTimeScan{&g.FinishedAt},
		timeScan{&g.UpdatedAt})
	g.ID, g.EvalDefID = core.SynthGenerationID(id), core.EvalDefID(def)
	g.Mode, g.Status = core.SynthMode(mode), core.SynthGenerationStatus(status)
	return g, err
}

func timeOrNil(d Dialect,
	t *time.Time) any {
	if t == nil {
		return nil
	}
	return d.Time(*t)
}

// CreateSynthGeneration records a generation of the synthetic eval g.EvalDefID of the owner (status pending unless set) and sets its ID
// and times. It returns core.ErrSynthConflict, and records nothing, if a generation of the eval is pending or running.
func (r Reads) CreateSynthGeneration(ctx context.Context,
	q Execer,
	userID string,
	g *core.SynthGeneration) error {
	if err := CheckOwner(userID); err != nil {
		return err
	}
	if g.Status == "" {
		g.Status = core.SynthPending
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	g.UserID = userID
	g.CreatedAt = g.CreatedAt.UTC().Truncate(time.Second)
	g.UpdatedAt = g.CreatedAt
	var active int
	switch err := r.queryRow(ctx,
		q,
		`SELECT 1 FROM gb_synth_generations WHERE eval_def_id = ? AND status IN ('pending', 'running') LIMIT 1`,
		int64(g.EvalDefID)).Scan(&active); {
	case err == nil:
		return core.ErrSynthConflict
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("create generation of synthetic eval %d: %w",
			g.EvalDefID,
			err)
	}
	var id int64
	err := r.queryRow(ctx,
		q,
		`
		INSERT INTO gb_synth_generations (user_id, eval_def_id, version, model, mode, status, error, requested, generated, kept, calls,
			input_tokens, output_tokens, cost_usd, created_at, started_at, finished_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		userID,
		int64(g.EvalDefID),
		g.Version,
		g.Model,
		string(g.Mode),
		string(g.Status),
		g.Error,
		g.Requested,
		g.Generated,
		g.Kept,
		g.Calls,
		g.InputTokens,
		g.OutputTokens,
		g.CostUSD,
		r.d.Time(g.CreatedAt),
		timeOrNil(r.d, g.StartedAt),
		timeOrNil(r.d, g.FinishedAt),
		r.d.Time(g.UpdatedAt)).Scan(&id)
	if err != nil {
		// the partial unique index: another request made an active generation between the check and the insert
		if m := err.Error(); strings.Contains(m, "UNIQUE constraint failed") || strings.Contains(m, "duplicate key") {
			return core.ErrSynthConflict
		}
		return fmt.Errorf("create generation of synthetic eval %d: %w",
			g.EvalDefID,
			err)
	}
	g.ID = core.SynthGenerationID(id)
	return nil
}

// UpdateSynthGeneration stores the progress of a generation of the owner (status, error, counts, cost, times), and sets UpdatedAt to now.
func (r Reads) UpdateSynthGeneration(ctx context.Context,
	q Execer,
	userID string,
	g *core.SynthGeneration) error {
	if err := CheckOwner(userID); err != nil {
		return err
	}
	g.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	res, err := q.ExecContext(ctx,
		r.d.Rebind(`
		UPDATE gb_synth_generations SET status = ?, error = ?, requested = ?, generated = ?, kept = ?, calls = ?, input_tokens = ?,
			output_tokens = ?, cost_usd = ?, started_at = ?, finished_at = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`),
		string(g.Status),
		g.Error,
		g.Requested,
		g.Generated,
		g.Kept,
		g.Calls,
		g.InputTokens,
		g.OutputTokens,
		g.CostUSD,
		timeOrNil(r.d, g.StartedAt),
		timeOrNil(r.d, g.FinishedAt),
		r.d.Time(g.UpdatedAt),
		userID,
		int64(g.ID))
	if err != nil {
		return fmt.Errorf("update generation %d: %w",
			g.ID,
			err)
	}
	return Affected(res, fmt.Sprintf("generation %d", g.ID))
}

// ListSynthGenerations returns the generations of a synthetic eval of the owner, newest first (limit <= 0: all).
func (r Reads) ListSynthGenerations(ctx context.Context,
	q Execer,
	userID string,
	evalDefID core.EvalDefID,
	limit int) ([]core.SynthGeneration, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	query := `SELECT ` + synthGenerationCols + ` FROM gb_synth_generations WHERE user_id = ? AND eval_def_id = ? ORDER BY id DESC`
	args := []any{userID, int64(evalDefID)}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := r.query(ctx,
		q,
		query,
		args...)
	if err != nil {
		return nil, fmt.Errorf("list generations of synthetic eval %d: %w",
			evalDefID,
			err)
	}
	defer rows.Close()
	out := []core.SynthGeneration{}
	for rows.Next() {
		g, err := scanSynthGeneration(rows)
		if err != nil {
			return nil, fmt.Errorf("list generations of synthetic eval %d: %w",
				evalDefID,
				err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetSynthGeneration returns a generation of the owner, or ErrNotFound.
func (r Reads) GetSynthGeneration(ctx context.Context,
	q Execer,
	userID string,
	id core.SynthGenerationID) (*core.SynthGeneration, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	g, err := scanSynthGeneration(r.queryRow(ctx,
		q,
		`SELECT `+synthGenerationCols+` FROM gb_synth_generations WHERE user_id = ? AND id = ?`,
		userID,
		int64(id)))
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("generation %d", id))
	}
	return &g, nil
}

// FailStaleSynthGenerations ends the generations that are pending or running and were not updated since cutoff: errored with reason.
// It returns how many (every owner: it is for the sweep of the server).
func (r Reads) FailStaleSynthGenerations(ctx context.Context,
	q Execer,
	cutoff time.Time,
	reason string,
	at time.Time) (int64, error) {
	res, err := q.ExecContext(ctx,
		r.d.Rebind(`
		UPDATE gb_synth_generations SET status = 'errored', error = ?, finished_at = ?, updated_at = ?
		WHERE status IN ('pending', 'running') AND updated_at < ?`),
		reason,
		r.d.Time(at),
		r.d.Time(at),
		r.d.Time(cutoff))
	if err != nil {
		return 0, fmt.Errorf("fail stale generations: %w", err)
	}
	return res.RowsAffected()
}

// ListSynthCases returns where the generated cases of a synthetic eval of the owner come from, by case name.
func (r Reads) ListSynthCases(ctx context.Context,
	q Execer,
	userID string,
	evalDefID core.EvalDefID) ([]core.SynthCase, error) {
	if _, err := r.GetSynthDef(ctx,
		q,
		userID,
		evalDefID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		q,
		`SELECT case_name, generation_id, version, content_hash, created_at FROM gb_synth_cases WHERE eval_def_id = ? ORDER BY case_name`,
		int64(evalDefID))
	if err != nil {
		return nil, fmt.Errorf("list cases of synthetic eval %d: %w",
			evalDefID,
			err)
	}
	defer rows.Close()
	out := []core.SynthCase{}
	for rows.Next() {
		c := core.SynthCase{
			EvalDefID: evalDefID,
		}
		var gen int64
		if err := rows.Scan(&c.CaseName,
			&gen,
			&c.Version,
			&c.ContentHash,
			timeScan{&c.CreatedAt}); err != nil {
			return nil, fmt.Errorf("list cases of synthetic eval %d: %w",
				evalDefID,
				err)
		}
		c.GenerationID = core.SynthGenerationID(gen)
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetSynthCases makes cases the whole list of generated cases of a synthetic eval of the owner (the rows of the eval that are not in it
// are deleted), in one transaction when q can begin one.
func (r Reads) SetSynthCases(ctx context.Context,
	q Execer,
	userID string,
	evalDefID core.EvalDefID,
	cases []core.SynthCase) error {
	if _, err := r.GetSynthDef(ctx,
		q,
		userID,
		evalDefID); err != nil {
		return err
	}
	return r.inTx(ctx,
		q,
		func(q Execer) error {
			if _, err := q.ExecContext(ctx,
				r.d.Rebind(`DELETE FROM gb_synth_cases WHERE eval_def_id = ?`),
				int64(evalDefID)); err != nil {
				return fmt.Errorf("set cases of synthetic eval %d: %w",
					evalDefID,
					err)
			}
			for _, c := range cases {
				created := c.CreatedAt
				if created.IsZero() {
					created = time.Now()
				}
				if _, err := q.ExecContext(ctx,
					r.d.Rebind(`
					INSERT INTO gb_synth_cases (eval_def_id, case_name, generation_id, version, content_hash, created_at)
					VALUES (?, ?, ?, ?, ?, ?)`),
					int64(evalDefID),
					c.CaseName,
					int64(c.GenerationID),
					c.Version,
					c.ContentHash,
					r.d.Time(created)); err != nil {
					return fmt.Errorf("set case %q of synthetic eval %d: %w",
						c.CaseName,
						evalDefID,
						err)
				}
			}
			return nil
		})
}

// inTx runs f in a transaction of its own when q can begin one, else in the caller's q.
func (r Reads) inTx(ctx context.Context,
	q Execer,
	f func(q Execer) error) error {
	b, ok := q.(beginner)
	if !ok {
		return f(q)
	}
	tx, err := b.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
