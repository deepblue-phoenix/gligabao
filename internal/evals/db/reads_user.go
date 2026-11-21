package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
)

// ErrRunFinished means the run is already in a final status, so the operation (a cancel) does not apply.
var ErrRunFinished = errors.New("evals: run already finished")

// AdminFilter selects what the admin reads return. A zero UserID is every user: only the admin API uses these functions.
type AdminFilter struct {
	UserID string
}

// prefixScanner puts first in front of the destinations of a Scan, to read a row that has some columns before the ones a scan
// function reads.
type prefixScanner struct {
	s     scanner
	first []any
}

func (p prefixScanner) Scan(dest ...any) error {
	return p.s.Scan(append(append([]any{}, p.first...), dest...)...)
}

// beginner is what *sql.DB has: a q that can start a transaction.
type beginner interface {
	BeginTx(ctx context.Context,
		opts *sql.TxOptions) (*sql.Tx, error)
}

//---------------------------------------------------------------------------------------------
// Reads of the user API

const evalDefCols = `id, user_id, name, description, type, tags, params, created_at, updated_at`

func scanEvalDef(s scanner) (*core.EvalDef, error) {
	var d core.EvalDef
	var id int64
	var typ string
	var tags, params []byte
	if err := s.Scan(&id,
		&d.UserID,
		&d.Name,
		&d.Description,
		&typ,
		&tags,
		&params,
		timeScan{&d.CreatedAt},
		timeScan{&d.UpdatedAt}); err != nil {
		return nil, err
	}
	d.ID, d.Type = core.EvalDefID(id), core.Type(typ)
	if err := Unmarshal(tags, &d.Tags); err != nil {
		return nil, err
	}
	if err := unmarshalParams(params, &d.Params); err != nil {
		return nil, err
	}
	return &d, nil
}

func (r Reads) evalDefs(ctx context.Context,
	q Execer,
	tail string,
	args []any) ([]core.EvalDef, error) {
	rows, err := r.query(ctx,
		q,
		`SELECT `+evalDefCols+` FROM gb_eval_defs `+tail,
		args...)
	if err != nil {
		return nil, fmt.Errorf("list eval defs: %w", err)
	}
	defer rows.Close()
	out := []core.EvalDef{}
	for rows.Next() {
		d, err := scanEvalDef(rows)
		if err != nil {
			return nil, fmt.Errorf("list eval defs: %w", err)
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// ListEvalDefs returns the eval defs of the owner ordered by name, without their Cases.
func (r Reads) ListEvalDefs(ctx context.Context,
	q Execer,
	userID string) ([]core.EvalDef, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	return r.evalDefs(ctx,
		q,
		`WHERE user_id = ? ORDER BY name`,
		[]any{userID})
}

// ListEvalRunsOfDef returns the runs of an eval def of the owner, newest first, at most limit (all if 0), with their Meta.
func (r Reads) ListEvalRunsOfDef(ctx context.Context,
	q Execer,
	userID string,
	defID core.EvalDefID,
	limit int) ([]core.EvalRun, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	tail := `WHERE user_id = ? AND def_id = ? ORDER BY id DESC`
	args := []any{userID, int64(defID)}
	if limit > 0 {
		tail += ` LIMIT ?`
		args = append(args, limit)
	}
	return r.evalRuns(ctx,
		q,
		tail,
		args)
}

// CountActiveEvalRuns returns the number of pending or running eval runs of the owner (the concurrency limit).
func (r Reads) CountActiveEvalRuns(ctx context.Context,
	q Execer,
	userID string) (int, error) {
	if err := CheckOwner(userID); err != nil {
		return 0, err
	}
	var n int
	if err := r.queryRow(ctx,
		q,
		`SELECT COUNT(*) FROM gb_eval_runs WHERE user_id = ? AND status IN ('pending', 'running')`,
		userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active eval runs: %w", err)
	}
	return n, nil
}

// SumCostSince returns the LLM cost of the eval runs of the owner created at or after since (the daily cost cap).
func (r Reads) SumCostSince(ctx context.Context,
	q Execer,
	userID string,
	since time.Time) (float64, error) {
	if err := CheckOwner(userID); err != nil {
		return 0, err
	}
	var sum float64
	if err := r.queryRow(ctx,
		q,
		`SELECT COALESCE(SUM(cost_usd), 0) FROM gb_eval_runs WHERE user_id = ? AND created_at >= ?`,
		userID,
		r.d.Time(since)).Scan(&sum); err != nil {
		return 0, fmt.Errorf("sum the cost of eval runs: %w", err)
	}
	return sum, nil
}

// ListCaseHistoryOfEval returns the case runs of the case caseName of an eval def of the owner, over the runs of the eval, oldest
// run first, each with its Meta (without the Trajectories). ExperimentRunID is 0 for the runs that are not part of an experiment run.
func (r Reads) ListCaseHistoryOfEval(ctx context.Context,
	q Execer,
	userID string,
	defID core.EvalDefID,
	caseName string) ([]CaseHistoryEntry, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		q,
		`
		SELECT COALESCE(er.experiment_run_id, 0), `+prefixed("cr.", caseRunCols)+`
		FROM gb_case_runs cr
		JOIN gb_eval_runs er ON er.id = cr.eval_run_id
		JOIN gb_case_defs cd ON cd.id = cr.def_id
		WHERE cr.user_id = ? AND er.user_id = ? AND er.def_id = ? AND cd.name = ?
		ORDER BY er.id, cr.id`,
		userID,
		userID,
		int64(defID),
		caseName)
	if err != nil {
		return nil, fmt.Errorf("list the history of case %q of eval def %d: %w",
			caseName,
			defID,
			err)
	}
	defer rows.Close()
	out := []CaseHistoryEntry{}
	for rows.Next() {
		var xr int64
		c, err := scanCaseRun(prefixScanner{
			s:     rows,
			first: []any{&xr},
		})
		if err != nil {
			return nil, fmt.Errorf("list the history of case %q of eval def %d: %w",
				caseName,
				defID,
				err)
		}
		out = append(out, CaseHistoryEntry{
			ExperimentRunID: core.ExperimentRunID(xr),
			CaseRun:         *c,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the history of case %q of eval def %d: %w",
			caseName,
			defID,
			err)
	}
	rows.Close()
	for i := range out {
		c := &out[i].CaseRun
		if c.Meta, err = r.caseRunMeta(ctx,
			q,
			c.ID,
			c.Type,
			false); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DeleteEvalDef deletes an eval def of the owner with its runs (their case runs, metadata and commentaries go with them), in one
// transaction (its own, when q can begin one; else the caller's), and returns the IDs of the deleted runs, for the cleanup of their
// datasets. It returns ErrNotFound if the owner has no such eval, and the error of the database if an experiment still includes it.
func (r Reads) DeleteEvalDef(ctx context.Context,
	q Execer,
	userID string,
	defID core.EvalDefID) ([]core.EvalRunID, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	if b, ok := q.(beginner); ok {
		tx, err := b.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("delete eval def %d: %w",
				defID,
				err)
		}
		ids, err := r.deleteEvalDef(ctx,
			tx,
			userID,
			defID)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("delete eval def %d: %w",
				defID,
				err)
		}
		return ids, nil
	}
	return r.deleteEvalDef(ctx,
		q,
		userID,
		defID)
}

func (r Reads) deleteEvalDef(ctx context.Context,
	q Execer,
	userID string,
	defID core.EvalDefID) ([]core.EvalRunID, error) {
	var exists int
	if err := r.queryRow(ctx,
		q,
		`SELECT 1 FROM gb_eval_defs WHERE user_id = ? AND id = ?`,
		userID,
		int64(defID)).Scan(&exists); err != nil {
		return nil, notFound(err, fmt.Sprintf("eval def %d", defID))
	}
	rows, err := r.query(ctx,
		q,
		`SELECT id FROM gb_eval_runs WHERE user_id = ? AND def_id = ? ORDER BY id`,
		userID,
		int64(defID))
	if err != nil {
		return nil, fmt.Errorf("delete eval def %d: %w",
			defID,
			err)
	}
	defer rows.Close()
	ids := []core.EvalRunID{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("delete eval def %d: %w",
				defID,
				err)
		}
		ids = append(ids, core.EvalRunID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("delete eval def %d: %w",
			defID,
			err)
	}
	rows.Close()
	for _, stmt := range []string{
		`DELETE FROM gb_eval_runs WHERE user_id = ? AND def_id = ?`,
		`DELETE FROM gb_eval_defs WHERE user_id = ? AND id = ?`,
	} {
		if _, err := q.ExecContext(ctx,
			r.d.Rebind(stmt),
			userID,
			int64(defID)); err != nil {
			return nil, fmt.Errorf("delete eval def %d: %w",
				defID,
				err)
		}
	}
	return ids, nil
}

//---------------------------------------------------------------------------------------------
// Admin reads (all users, with an optional owner filter)

// AdminListEvalDefs returns the eval defs of every user (or of f.UserID), ordered by owner and name, without their Cases.
func (r Reads) AdminListEvalDefs(ctx context.Context,
	q Execer,
	f AdminFilter) ([]core.EvalDef, error) {
	where, args := adminWhere(f)
	return r.evalDefs(ctx,
		q,
		where+` ORDER BY user_id, name`,
		args)
}

// AdminListEvalRuns returns the eval runs of every user (or of f.UserID), newest first, at most limit (all if 0), with their Meta.
func (r Reads) AdminListEvalRuns(ctx context.Context,
	q Execer,
	f AdminFilter,
	limit int) ([]core.EvalRun, error) {
	where, args := adminWhere(f)
	tail := where + ` ORDER BY id DESC`
	if limit > 0 {
		tail += ` LIMIT ?`
		args = append(args, limit)
	}
	return r.evalRuns(ctx,
		q,
		tail,
		args)
}

func adminWhere(f AdminFilter) (string, []any) {
	if f.UserID == "" {
		return ``, []any{}
	}
	return `WHERE user_id = ?`, []any{f.UserID}
}

//---------------------------------------------------------------------------------------------
// Run bookkeeping

// ClaimRun marks a pending eval run as running on the server runnerID: it sets runner_id, heartbeat_at and started_at. It returns
// ErrNotFound if the run is not pending (or does not exist).
func (r Reads) ClaimRun(ctx context.Context,
	q Execer,
	runID core.EvalRunID,
	runnerID string,
	at time.Time) error {
	res, err := q.ExecContext(ctx,
		r.d.Rebind(`
		UPDATE gb_eval_runs SET status = 'running', runner_id = ?, heartbeat_at = ?, started_at = COALESCE(started_at, ?)
		WHERE id = ? AND status = 'pending'`),
		runnerID,
		r.d.Time(at),
		r.d.Time(at),
		int64(runID))
	if err != nil {
		return fmt.Errorf("claim eval run %d: %w",
			runID,
			err)
	}
	return Affected(res, fmt.Sprintf("claim eval run %d", runID))
}

// Heartbeat records that the server executing the run is alive, and returns whether a cancel was requested. It returns ErrNotFound if
// the run does not exist or is already final.
func (r Reads) Heartbeat(ctx context.Context,
	q Execer,
	runID core.EvalRunID,
	at time.Time) (bool, error) {
	res, err := q.ExecContext(ctx,
		r.d.Rebind(`UPDATE gb_eval_runs SET heartbeat_at = ? WHERE id = ? AND status IN ('pending', 'running')`),
		r.d.Time(at),
		int64(runID))
	if err != nil {
		return false, fmt.Errorf("heartbeat of eval run %d: %w",
			runID,
			err)
	}
	if err := Affected(res, fmt.Sprintf("heartbeat of eval run %d", runID)); err != nil {
		return false, err
	}
	var cancel bool
	if err := r.queryRow(ctx,
		q,
		`SELECT cancel_requested FROM gb_eval_runs WHERE id = ?`,
		int64(runID)).Scan(&cancel); err != nil {
		return false, notFound(err, fmt.Sprintf("eval run %d", runID))
	}
	return cancel, nil
}

// RequestCancel sets cancel_requested on a run of the owner that is pending or running. It returns ErrNotFound if the owner has no
// such run, and ErrRunFinished if it is already final.
func (r Reads) RequestCancel(ctx context.Context,
	q Execer,
	userID string,
	runID core.EvalRunID) error {
	if err := CheckOwner(userID); err != nil {
		return err
	}
	res, err := q.ExecContext(ctx,
		r.d.Rebind(`UPDATE gb_eval_runs SET cancel_requested = ? WHERE user_id = ? AND id = ? AND status IN ('pending', 'running')`),
		true,
		userID,
		int64(runID))
	if err != nil {
		return fmt.Errorf("cancel eval run %d: %w",
			runID,
			err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("cancel eval run %d: %w",
			runID,
			err)
	}
	if n > 0 {
		return nil
	}
	var exists int
	if err := r.queryRow(ctx,
		q,
		`SELECT 1 FROM gb_eval_runs WHERE user_id = ? AND id = ?`,
		userID,
		int64(runID)).Scan(&exists); err != nil {
		return notFound(err, fmt.Sprintf("eval run %d", runID))
	}
	return fmt.Errorf("cancel eval run %d: %w",
		runID,
		ErrRunFinished)
}

// FailStale ends as errored, with reason, the pending or running eval runs of a type that a server executes (runner_id is set; CLI
// runs have none and are never stale) and whose last heartbeat is older than cutoff (or that have none and were created before it),
// with their pending or running case runs, and the experiment runs that had one of them and have no eval run left going. It returns
// the number of eval runs ended.
func (r Reads) FailStale(ctx context.Context,
	q Execer,
	typ core.Type,
	cutoff time.Time,
	reason string,
	at time.Time) (int64, error) {
	t, c := r.d.Time(at), r.d.Time(cutoff)
	stale := `type = ? AND runner_id <> '' AND status IN ('pending', 'running')
		AND ((heartbeat_at IS NOT NULL AND heartbeat_at < ?) OR (heartbeat_at IS NULL AND created_at < ?))`
	run := func(query string,
		args ...any) (int64, error) {
		res, err := q.ExecContext(ctx,
			r.d.Rebind(query),
			args...)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	if _, err := run(`UPDATE gb_case_runs SET status = 'errored', error = ?, finished_at = ?
		WHERE status IN ('pending', 'running') AND eval_run_id IN (SELECT id FROM gb_eval_runs WHERE `+stale+`)`,
		reason,
		t,
		string(typ),
		c,
		c); err != nil {
		return 0, fmt.Errorf("fail stale case runs: %w", err)
	}
	n, err := run(`UPDATE gb_eval_runs SET status = 'errored', error = ?, finished_at = ?,
			counts_errored = counts_errored + counts_running, counts_running = 0
		WHERE `+stale,
		reason,
		t,
		string(typ),
		c,
		c)
	if err != nil {
		return 0, fmt.Errorf("fail stale eval runs: %w", err)
	}
	if _, err := run(`UPDATE gb_experiment_runs SET status = 'errored', error = ?, finished_at = ?
		WHERE status IN ('pending', 'running')
		AND EXISTS (SELECT 1 FROM gb_eval_runs e WHERE e.experiment_run_id = gb_experiment_runs.id AND e.error = ? AND e.finished_at = ?)
		AND NOT EXISTS (SELECT 1 FROM gb_eval_runs e WHERE e.experiment_run_id = gb_experiment_runs.id AND e.status IN ('pending', 'running'))`,
		reason,
		t,
		reason,
		t); err != nil {
		return 0, fmt.Errorf("fail stale experiment runs: %w", err)
	}
	return n, nil
}
