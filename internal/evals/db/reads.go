package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
)

// Dialect is what differs between the databases for the statements of Reads: the placeholders and how a time is passed.
type Dialect struct {
	// Rebind turns the ? placeholders of a statement into those of the database.
	Rebind func(query string) string
	// Time is a time as a parameter of a statement.
	Time func(t time.Time) any
}

// SQLiteDialect and PostgresDialect are the dialects of the two backends (the times of SQLite are UTC RFC 3339 text).
var (
	SQLiteDialect = Dialect{
		Rebind: func(q string) string { return q },
		Time:   func(t time.Time) any { return t.UTC().Truncate(time.Second).Format(time.RFC3339) },
	}
	PostgresDialect = Dialect{
		Rebind: func(q string) string {
			var b strings.Builder
			n := 0
			for _, r := range q {
				if r == '?' {
					n++
					b.WriteString("$" + strconv.Itoa(n))
					continue
				}
				b.WriteRune(r)
			}
			return b.String()
		},
		Time: func(t time.Time) any { return t.UTC().Truncate(time.Microsecond) },
	}
)

// Reads are the functions that read (and list) the records of the runs, written once for both backends: their statements are the
// same SQL, only the dialect differs. A backend's Store has them as method values. Every getter returns ErrNotFound if the record
// does not exist; the lists are empty (not nil) if there is nothing.
type Reads struct{ d Dialect }

// NewReads makes the Reads of a dialect.
func NewReads(d Dialect) Reads { return Reads{d} }

// TrajectoryRef is a knowledge trajectory with the case run it belongs to.
type TrajectoryRef struct {
	CaseRunID core.CaseRunID
	core.KnowledgeTrajectory
}

// CaseHistoryEntry is a case run of the history of one case: the run of the experiment it was in, and what happened.
type CaseHistoryEntry struct {
	ExperimentRunID core.ExperimentRunID
	// CaseRun has its Meta, without the Trajectories.
	CaseRun core.CaseRun
}

// rows is what *sql.Row and *sql.Rows share.
type scanner interface{ Scan(dest ...any) error }

// timeScan reads a time column: a time.Time (PostgreSQL, and SQLite when its driver parses the column) or the RFC 3339 text.
type timeScan struct{ t *time.Time }

func (s timeScan) Scan(v any) error {
	t, err := parseTime(v)
	if err != nil {
		return err
	}
	*s.t = t
	return nil
}

// nullTimeScan reads a time column that may be NULL.
type nullTimeScan struct{ t **time.Time }

func (s nullTimeScan) Scan(v any) error {
	if v == nil {
		*s.t = nil
		return nil
	}
	t, err := parseTime(v)
	if err != nil {
		return err
	}
	*s.t = &t
	return nil
}

func parseTime(v any) (time.Time, error) {
	switch v := v.(type) {
	case time.Time:
		return v.UTC(), nil
	case string:
		return parseTimeText(v)
	case []byte:
		return parseTimeText(string(v))
	}
	return time.Time{}, fmt.Errorf("parse time: unexpected %T", v)
}

func parseTimeText(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02 15:04:05.999999999-07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse time %q", s)
}

func (r Reads) query(ctx context.Context,
	q Execer,
	query string,
	args ...any) (*sql.Rows, error) {
	return q.QueryContext(ctx,
		r.d.Rebind(query),
		args...)
}

func (r Reads) queryRow(ctx context.Context,
	q Execer,
	query string,
	args ...any) *sql.Row {
	return q.QueryRowContext(ctx,
		r.d.Rebind(query),
		args...)
}

const experimentRunCols = `id, def_id, status, error, params, counts_total, counts_pending, counts_running, counts_completed,
	counts_errored, counts_cancelled, counts_passed, counts_failed, cost_usd, created_at, started_at, finished_at, user_id`

func scanExperimentRun(s scanner) (*core.ExperimentRun, error) {
	var x core.ExperimentRun
	var id, def int64
	var status string
	var params []byte
	c := &x.Counts
	err := s.Scan(&id,
		&def,
		&status,
		&x.Error,
		&params,
		&c.Total,
		&c.Pending,
		&c.Running,
		&c.Completed,
		&c.Errored,
		&c.Cancelled,
		&c.Passed,
		&c.Failed,
		&x.CostUSD,
		timeScan{&x.CreatedAt},
		nullTimeScan{&x.StartedAt},
		nullTimeScan{&x.FinishedAt},
		&x.UserID)
	if err != nil {
		return nil, err
	}
	x.ID, x.DefID, x.Status = core.ExperimentRunID(id), core.ExperimentDefID(def), core.Status(status)
	if err := unmarshalParams(params, &x.Params); err != nil {
		return nil, err
	}
	return &x, nil
}

const evalRunCols = `id, def_id, experiment_run_id, type, status, error, params, counts_total, counts_pending, counts_running,
	counts_completed, counts_errored, counts_cancelled, counts_passed, counts_failed, cost_usd, created_at, started_at, finished_at,
	user_id, runner_id, heartbeat_at, cancel_requested`

func scanEvalRun(s scanner) (*core.EvalRun, error) {
	var x core.EvalRun
	var id, def int64
	var exp sql.NullInt64
	var typ, status string
	var params []byte
	c := &x.Counts
	err := s.Scan(&id,
		&def,
		&exp,
		&typ,
		&status,
		&x.Error,
		&params,
		&c.Total,
		&c.Pending,
		&c.Running,
		&c.Completed,
		&c.Errored,
		&c.Cancelled,
		&c.Passed,
		&c.Failed,
		&x.CostUSD,
		timeScan{&x.CreatedAt},
		nullTimeScan{&x.StartedAt},
		nullTimeScan{&x.FinishedAt},
		&x.UserID,
		&x.RunnerID,
		nullTimeScan{&x.HeartbeatAt},
		&x.CancelRequested)
	if err != nil {
		return nil, err
	}
	x.ID, x.DefID, x.Type, x.Status = core.EvalRunID(id), core.EvalDefID(def), core.Type(typ), core.Status(status)
	if exp.Valid {
		e := core.ExperimentRunID(exp.Int64)
		x.ExperimentRunID = &e
	}
	if err := unmarshalParams(params, &x.Params); err != nil {
		return nil, err
	}
	return &x, nil
}

func unmarshalParams(text []byte,
	p *core.Params) error {
	if err := Unmarshal(text, p); err != nil {
		return err
	}
	if len(*p) == 0 {
		*p = nil
	}
	return nil
}

const caseRunCols = `id, def_id, eval_run_id, type, status, error, verdict, score, cost_usd, created_at, started_at, finished_at, user_id`

func scanCaseRun(s scanner) (*core.CaseRun, error) {
	var x core.CaseRun
	var id, def, run int64
	var typ, status, verdict string
	err := s.Scan(&id,
		&def,
		&run,
		&typ,
		&status,
		&x.Error,
		&verdict,
		&x.Score,
		&x.CostUSD,
		timeScan{&x.CreatedAt},
		nullTimeScan{&x.StartedAt},
		nullTimeScan{&x.FinishedAt},
		&x.UserID)
	if err != nil {
		return nil, err
	}
	x.ID, x.DefID, x.EvalRunID, x.Type = core.CaseRunID(id), core.CaseDefID(def), core.EvalRunID(run), core.Type(typ)
	x.Status, x.Verdict = core.Status(status), core.Verdict(verdict)
	return &x, nil
}

// notFound turns sql.ErrNoRows into ErrNotFound (wrapped with what).
func notFound(err error,
	what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w",
			what,
			ErrNotFound)
	}
	return fmt.Errorf("%s: %w",
		what,
		err)
}

//---------------------------------------------------------------------------------------------
// Defs

// ListExperimentDefs returns every experiment def, oldest first, each with its EvalDefIDs.
func (r Reads) ListExperimentDefs(ctx context.Context,
	q Execer,
	userID string) ([]core.ExperimentDef, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	return r.experimentDefs(ctx,
		q,
		userID,
		``,
		nil)
}

// GetExperimentDef returns the experiment def with the ID, with its EvalDefIDs.
func (r Reads) GetExperimentDef(ctx context.Context,
	q Execer,
	userID string,
	id core.ExperimentDefID) (*core.ExperimentDef, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	list, err := r.experimentDefs(ctx,
		q,
		userID,
		`AND id = ?`,
		[]any{int64(id)})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("experiment def %d: %w",
			id,
			ErrNotFound)
	}
	return &list[0], nil
}

func (r Reads) experimentDefs(ctx context.Context,
	q Execer,
	userID string,
	where string,
	args []any) ([]core.ExperimentDef, error) {
	rows, err := r.query(ctx,
		q,
		`SELECT id, name, description, params, created_at, updated_at FROM gb_experiment_defs WHERE user_id = ? `+where+` ORDER BY id`,
		append([]any{userID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("list experiment defs: %w", err)
	}
	defer rows.Close()
	out := []core.ExperimentDef{}
	byID := map[core.ExperimentDefID]int{}
	for rows.Next() {
		var d core.ExperimentDef
		var id int64
		var params []byte
		if err := rows.Scan(&id,
			&d.Name,
			&d.Description,
			&params,
			timeScan{&d.CreatedAt},
			timeScan{&d.UpdatedAt}); err != nil {
			return nil, fmt.Errorf("list experiment defs: %w", err)
		}
		d.ID, d.UserID, d.EvalDefIDs = core.ExperimentDefID(id), userID, []core.EvalDefID{}
		if err := unmarshalParams(params, &d.Params); err != nil {
			return nil, err
		}
		byID[d.ID] = len(out)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list experiment defs: %w", err)
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}
	links, err := r.query(ctx,
		q,
		`
		SELECT experiment_def_id, eval_def_id FROM gb_experiment_def_evals
		WHERE experiment_def_id IN (SELECT id FROM gb_experiment_defs WHERE user_id = ?)
		ORDER BY experiment_def_id, position`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("list the evals of the experiment defs: %w", err)
	}
	defer links.Close()
	for links.Next() {
		var x, e int64
		if err := links.Scan(&x, &e); err != nil {
			return nil, fmt.Errorf("list the evals of the experiment defs: %w", err)
		}
		if i, ok := byID[core.ExperimentDefID(x)]; ok {
			out[i].EvalDefIDs = append(out[i].EvalDefIDs, core.EvalDefID(e))
		}
	}
	return out, links.Err()
}

// GetEvalDef returns the eval def with the ID, without its Cases (see ListCaseDefs).
func (r Reads) GetEvalDef(ctx context.Context,
	q Execer,
	userID string,
	id core.EvalDefID) (*core.EvalDef, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	var d core.EvalDef
	var typ string
	var tags, params []byte
	err := r.queryRow(ctx,
		q,
		`SELECT name, description, type, tags, params, created_at, updated_at FROM gb_eval_defs WHERE user_id = ? AND id = ?`,
		userID,
		int64(id)).
		Scan(&d.Name,
			&d.Description,
			&typ,
			&tags,
			&params,
			timeScan{&d.CreatedAt},
			timeScan{&d.UpdatedAt})
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("eval def %d", id))
	}
	d.ID, d.UserID, d.Type = id, userID, core.Type(typ)
	if err := Unmarshal(tags, &d.Tags); err != nil {
		return nil, err
	}
	if err := unmarshalParams(params, &d.Params); err != nil {
		return nil, err
	}
	return &d, nil
}

// ListCaseDefs returns the case defs of an eval def ordered by position, without their Meta.
func (r Reads) ListCaseDefs(ctx context.Context,
	q Execer,
	userID string,
	evalDefID core.EvalDefID) ([]core.CaseDef, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		q,
		`
		SELECT id, type, name, position, description, tags, created_at, updated_at FROM gb_case_defs
		WHERE eval_def_id = ? AND eval_def_id IN (SELECT id FROM gb_eval_defs WHERE user_id = ?) ORDER BY position`,
		int64(evalDefID),
		userID)
	if err != nil {
		return nil, fmt.Errorf("list case defs of eval def %d: %w",
			evalDefID,
			err)
	}
	defer rows.Close()
	out := []core.CaseDef{}
	for rows.Next() {
		c := core.CaseDef{
			EvalDefID: evalDefID,
		}
		var id int64
		var typ string
		var tags []byte
		if err := rows.Scan(&id,
			&typ,
			&c.Name,
			&c.Position,
			&c.Description,
			&tags,
			timeScan{&c.CreatedAt},
			timeScan{&c.UpdatedAt}); err != nil {
			return nil, fmt.Errorf("list case defs of eval def %d: %w",
				evalDefID,
				err)
		}
		c.ID, c.Type = core.CaseDefID(id), core.Type(typ)
		if err := Unmarshal(tags, &c.Tags); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListExperimentCases returns the cases (eval and case name) that the runs of an experiment executed, ordered by eval and name.
func (r Reads) ListExperimentCases(ctx context.Context,
	q Execer,
	userID string,
	experimentDefID core.ExperimentDefID) ([]core.CaseRef, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		q,
		`
		SELECT DISTINCT ed.name, cd.name
		FROM gb_case_runs cr
		JOIN gb_eval_runs er ON er.id = cr.eval_run_id
		JOIN gb_experiment_runs xr ON xr.id = er.experiment_run_id
		JOIN gb_case_defs cd ON cd.id = cr.def_id
		JOIN gb_eval_defs ed ON ed.id = cd.eval_def_id
		WHERE xr.user_id = ? AND cr.user_id = ? AND xr.def_id = ?
		ORDER BY ed.name, cd.name`,
		userID,
		userID,
		int64(experimentDefID))
	if err != nil {
		return nil, fmt.Errorf("list the cases of experiment def %d: %w",
			experimentDefID,
			err)
	}
	defer rows.Close()
	out := []core.CaseRef{}
	for rows.Next() {
		var c core.CaseRef
		if err := rows.Scan(&c.Eval, &c.Name); err != nil {
			return nil, fmt.Errorf("list the cases of experiment def %d: %w",
				experimentDefID,
				err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

//---------------------------------------------------------------------------------------------
// Runs

// ListExperimentRuns returns the runs of an experiment def, oldest first.
func (r Reads) ListExperimentRuns(ctx context.Context,
	q Execer,
	userID string,
	defID core.ExperimentDefID) ([]core.ExperimentRun, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		q,
		`SELECT `+experimentRunCols+` FROM gb_experiment_runs WHERE user_id = ? AND def_id = ? ORDER BY id`,
		userID,
		int64(defID))
	if err != nil {
		return nil, fmt.Errorf("list runs of experiment def %d: %w",
			defID,
			err)
	}
	defer rows.Close()
	out := []core.ExperimentRun{}
	for rows.Next() {
		x, err := scanExperimentRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list runs of experiment def %d: %w",
				defID,
				err)
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// GetExperimentRun returns the experiment run with the ID (without its EvalRuns).
func (r Reads) GetExperimentRun(ctx context.Context,
	q Execer,
	userID string,
	id core.ExperimentRunID) (*core.ExperimentRun, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	x, err := scanExperimentRun(r.queryRow(ctx,
		q,
		`SELECT `+experimentRunCols+` FROM gb_experiment_runs WHERE user_id = ? AND id = ?`,
		userID,
		int64(id)))
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("experiment run %d", id))
	}
	return x, nil
}

// GetEvalRun returns the eval run with the ID with its Meta, without its CaseRuns (see ListCaseRuns).
func (r Reads) GetEvalRun(ctx context.Context,
	q Execer,
	userID string,
	id core.EvalRunID) (*core.EvalRun, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	x, err := scanEvalRun(r.queryRow(ctx,
		q,
		`SELECT `+evalRunCols+` FROM gb_eval_runs WHERE user_id = ? AND id = ?`,
		userID,
		int64(id)))
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("eval run %d", id))
	}
	if x.Meta, err = r.evalRunMeta(ctx,
		q,
		x.ID,
		x.Type); err != nil {
		return nil, err
	}
	return x, nil
}

// ListEvalRunsOfExperimentRun returns the eval runs of an experiment run, oldest first, with their Meta.
func (r Reads) ListEvalRunsOfExperimentRun(ctx context.Context,
	q Execer,
	userID string,
	id core.ExperimentRunID) ([]core.EvalRun, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	return r.evalRuns(ctx,
		q,
		`WHERE user_id = ? AND experiment_run_id = ? ORDER BY id`,
		[]any{userID, int64(id)})
}

// ListEvalRunsOfExperiment returns the eval runs of every run of an experiment def, oldest first, with their Meta.
func (r Reads) ListEvalRunsOfExperiment(ctx context.Context,
	q Execer,
	userID string,
	defID core.ExperimentDefID) ([]core.EvalRun, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	return r.evalRuns(ctx,
		q,
		`WHERE user_id = ? AND experiment_run_id IN (SELECT id FROM gb_experiment_runs WHERE user_id = ? AND def_id = ?) ORDER BY id`,
		[]any{userID, userID, int64(defID)})
}

// ListTrainingEvalRuns returns the training eval runs of a trained model, newest first, at most limit (all if 0), with their Meta.
func (r Reads) ListTrainingEvalRuns(ctx context.Context,
	q Execer,
	trainedModelID int64,
	limit int) ([]core.EvalRun, error) {
	where := `WHERE type = 'training' AND id IN (SELECT eval_run_id FROM gb_eval_runs_meta_training WHERE trained_model_id = ?) ORDER BY id DESC`
	args := []any{trainedModelID}
	if limit > 0 {
		where += ` LIMIT ?`
		args = append(args, limit)
	}
	return r.evalRuns(ctx,
		q,
		where,
		args)
}

func (r Reads) evalRuns(ctx context.Context,
	q Execer,
	tail string,
	args []any) ([]core.EvalRun, error) {
	rows, err := r.query(ctx,
		q,
		`SELECT `+evalRunCols+` FROM gb_eval_runs `+tail,
		args...)
	if err != nil {
		return nil, fmt.Errorf("list eval runs: %w", err)
	}
	defer rows.Close()
	out := []core.EvalRun{}
	for rows.Next() {
		x, err := scanEvalRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list eval runs: %w", err)
		}
		out = append(out, *x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list eval runs: %w", err)
	}
	rows.Close()
	for i := range out {
		if out[i].Meta, err = r.evalRunMeta(ctx,
			q,
			out[i].ID,
			out[i].Type); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// evalRunMeta reads the row of eval run id in the table of typ (a pointer to its struct).
func (r Reads) evalRunMeta(ctx context.Context,
	q Execer,
	id core.EvalRunID,
	typ core.Type) (core.EvalRunMeta, error) {
	switch typ {
	case core.TypeKnowledge:
		m := &core.KnowledgeEvalRunMeta{
			EvalRunID: id,
		}
		err := r.queryRow(ctx,
			q,
			`
			SELECT chunks, steps, remember_calls, query_calls, remember_cost_usd, query_cost_usd, checks_passed, checks_failed
			FROM gb_eval_runs_meta_knowledge WHERE eval_run_id = ?`,
			int64(id)).
			Scan(&m.Chunks,
				&m.Steps,
				&m.RememberCalls,
				&m.QueryCalls,
				&m.RememberCostUSD,
				&m.QueryCostUSD,
				&m.ChecksPassed,
				&m.ChecksFailed)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // an eval run may have been recorded without it
		}
		if err != nil {
			return nil, fmt.Errorf("get knowledge eval run meta of eval run %d: %w",
				id,
				err)
		}
		return m, nil
	case core.TypeTraining:
		m := &core.TrainingEvalRunMeta{
			EvalRunID: id,
		}
		err := r.queryRow(ctx,
			q,
			`
			SELECT trained_model_id, judge_model, num_answered, num_judged, num_correct, num_partial, num_incorrect, score_sum, num_batches,
				judge_calls, judge_input_tokens, judge_output_tokens, judge_cost_usd
			FROM gb_eval_runs_meta_training WHERE eval_run_id = ?`,
			int64(id)).
			Scan(&m.TrainedModelID,
				&m.JudgeModel,
				&m.NumAnswered,
				&m.NumJudged,
				&m.NumCorrect,
				&m.NumPartial,
				&m.NumIncorrect,
				&m.ScoreSum,
				&m.NumBatches,
				&m.JudgeCalls,
				&m.JudgeInputTokens,
				&m.JudgeOutputTokens,
				&m.JudgeCostUSD)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("get training eval run meta of eval run %d: %w",
				id,
				err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("eval run %d: unknown type %q",
		id,
		typ)
}

// ListCaseRuns returns the case runs of an eval run, in the order of their case defs, each with its Meta (a pointer to the struct
// of its type; a knowledge one with its Trajectories).
func (r Reads) ListCaseRuns(ctx context.Context,
	q Execer,
	userID string,
	evalRunID core.EvalRunID) ([]core.CaseRun, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		q,
		`
		SELECT `+prefixed("cr.", caseRunCols)+` FROM gb_case_runs cr JOIN gb_case_defs cd ON cd.id = cr.def_id
		WHERE cr.user_id = ? AND cr.eval_run_id = ? ORDER BY cd.position, cr.id`,
		userID,
		int64(evalRunID))
	if err != nil {
		return nil, fmt.Errorf("list case runs of eval run %d: %w",
			evalRunID,
			err)
	}
	defer rows.Close()
	out := []core.CaseRun{}
	for rows.Next() {
		x, err := scanCaseRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list case runs of eval run %d: %w",
				evalRunID,
				err)
		}
		out = append(out, *x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list case runs of eval run %d: %w",
			evalRunID,
			err)
	}
	rows.Close()
	for i := range out {
		if out[i].Meta, err = r.caseRunMeta(ctx,
			q,
			out[i].ID,
			out[i].Type,
			true); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// prefixed puts prefix before every column of a comma-separated list.
func prefixed(prefix,
	cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// GetCaseRun returns the case run with the ID with its Meta (a knowledge one with its Trajectories).
func (r Reads) GetCaseRun(ctx context.Context,
	q Execer,
	userID string,
	id core.CaseRunID) (*core.CaseRun, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	x, err := scanCaseRun(r.queryRow(ctx,
		q,
		`SELECT `+caseRunCols+` FROM gb_case_runs WHERE user_id = ? AND id = ?`,
		userID,
		int64(id)))
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("case run %d", id))
	}
	if x.Meta, err = r.caseRunMeta(ctx,
		q,
		x.ID,
		x.Type,
		true); err != nil {
		return nil, err
	}
	return x, nil
}

// caseRunMeta reads the row of case run id in the table of typ (nil if there is none).
func (r Reads) caseRunMeta(ctx context.Context,
	q Execer,
	id core.CaseRunID,
	typ core.Type,
	trajectories bool) (core.CaseRunMeta, error) {
	switch typ {
	case core.TypeKnowledge:
		m := &core.KnowledgeCaseRunMeta{
			CaseRunID: id,
		}
		var input, problems, passed []byte
		err := r.queryRow(ctx,
			q,
			`
			SELECT input, answer, problems, passed_checks, dataset_id, steps FROM gb_case_runs_meta_knowledge WHERE case_run_id = ?`,
			int64(id)).
			Scan(&input,
				&m.Answer,
				&problems,
				&passed,
				&m.DatasetID,
				&m.Steps)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("get knowledge case run meta of case run %d: %w",
				id,
				err)
		}
		for _, f := range []struct {
			text []byte
			to   any
		}{{input, &m.Input}, {problems, &m.Problems}, {passed, &m.PassedChecks}} {
			if err := Unmarshal(f.text, f.to); err != nil {
				return nil, err
			}
		}
		if trajectories {
			if m.Trajectories, err = r.trajectories(ctx,
				q,
				id); err != nil {
				return nil, err
			}
		}
		return m, nil
	case core.TypeTraining:
		m := &core.TrainingCaseRunMeta{
			CaseRunID: id,
		}
		err := r.queryRow(ctx,
			q,
			`
			SELECT instruction, input, reference, response, batch, judge_score, judge_reason, input_tokens, output_tokens, duration_ms
			FROM gb_case_runs_meta_training WHERE case_run_id = ?`,
			int64(id)).
			Scan(&m.Instruction,
				&m.Input,
				&m.Reference,
				&m.Response,
				&m.Batch,
				&m.JudgeScore,
				&m.JudgeReason,
				&m.InputTokens,
				&m.OutputTokens,
				&m.DurationMS)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("get training case run meta of case run %d: %w",
				id,
				err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("case run %d: unknown type %q",
		id,
		typ)
}

const trajectoryCols = `seq, kind, trajectory_id, agent_name, steps, cost_usd, path, artifact_ids, created_at`

func scanTrajectory(s scanner,
	t *core.KnowledgeTrajectory) error {
	var artifacts []byte
	if err := s.Scan(&t.Seq,
		&t.Kind,
		&t.TrajectoryID,
		&t.AgentName,
		&t.Steps,
		&t.CostUSD,
		&t.Path,
		&artifacts,
		timeScan{&t.CreatedAt}); err != nil {
		return err
	}
	return Unmarshal(artifacts, &t.ArtifactIDs)
}

func (r Reads) trajectories(ctx context.Context,
	q Execer,
	caseRunID core.CaseRunID) ([]core.KnowledgeTrajectory, error) {
	rows, err := r.query(ctx,
		q,
		`SELECT `+trajectoryCols+` FROM gb_case_runs_meta_knowledge_trajectories WHERE case_run_id = ? ORDER BY seq`,
		int64(caseRunID))
	if err != nil {
		return nil, fmt.Errorf("list trajectories of case run %d: %w",
			caseRunID,
			err)
	}
	defer rows.Close()
	var out []core.KnowledgeTrajectory
	for rows.Next() {
		var t core.KnowledgeTrajectory
		if err := scanTrajectory(rows, &t); err != nil {
			return nil, fmt.Errorf("list trajectories of case run %d: %w",
				caseRunID,
				err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// FindKnowledgeTrajectory returns the trajectory with the given ATIF trajectory ID and the case run it belongs to.
func (r Reads) FindKnowledgeTrajectory(ctx context.Context,
	q Execer,
	userID string,
	trajectoryID string) (*TrajectoryRef, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	var ref TrajectoryRef
	var caseRun int64
	var artifacts []byte
	t := &ref.KnowledgeTrajectory
	err := r.queryRow(ctx,
		q,
		`
		SELECT case_run_id, `+trajectoryCols+` FROM gb_case_runs_meta_knowledge_trajectories
		WHERE trajectory_id = ? AND case_run_id IN (SELECT id FROM gb_case_runs WHERE user_id = ?) ORDER BY id LIMIT 1`,
		trajectoryID,
		userID).
		Scan(&caseRun,
			&t.Seq,
			&t.Kind,
			&t.TrajectoryID,
			&t.AgentName,
			&t.Steps,
			&t.CostUSD,
			&t.Path,
			&artifacts,
			timeScan{&t.CreatedAt})
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("trajectory %q", trajectoryID))
	}
	ref.CaseRunID = core.CaseRunID(caseRun)
	if err := Unmarshal(artifacts, &t.ArtifactIDs); err != nil {
		return nil, err
	}
	return &ref, nil
}

// ListCaseHistory returns the case runs of one case (the eval's name and the case's name) over the runs of an experiment, oldest
// run first, each with its Meta (without the Trajectories).
func (r Reads) ListCaseHistory(ctx context.Context,
	q Execer,
	userID string,
	defID core.ExperimentDefID,
	eval,
	name string) ([]CaseHistoryEntry, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		q,
		`
		SELECT er.experiment_run_id, `+prefixed("cr.", caseRunCols)+`
		FROM gb_case_runs cr
		JOIN gb_eval_runs er ON er.id = cr.eval_run_id
		JOIN gb_experiment_runs xr ON xr.id = er.experiment_run_id
		JOIN gb_case_defs cd ON cd.id = cr.def_id
		JOIN gb_eval_defs ed ON ed.id = cd.eval_def_id
		WHERE cr.user_id = ? AND xr.user_id = ? AND xr.def_id = ? AND ed.name = ? AND cd.name = ?
		ORDER BY xr.id, cr.id`,
		userID,
		userID,
		int64(defID),
		eval,
		name)
	if err != nil {
		return nil, fmt.Errorf("list the history of case %s/%s: %w",
			eval,
			name,
			err)
	}
	defer rows.Close()
	out := []CaseHistoryEntry{}
	for rows.Next() {
		var e CaseHistoryEntry
		var xr int64
		var typ, status, verdict string
		var id, def, run int64
		c := &e.CaseRun
		if err := rows.Scan(&xr,
			&id,
			&def,
			&run,
			&typ,
			&status,
			&c.Error,
			&verdict,
			&c.Score,
			&c.CostUSD,
			timeScan{&c.CreatedAt},
			nullTimeScan{&c.StartedAt},
			nullTimeScan{&c.FinishedAt},
			&c.UserID); err != nil {
			return nil, fmt.Errorf("list the history of case %s/%s: %w",
				eval,
				name,
				err)
		}
		e.ExperimentRunID = core.ExperimentRunID(xr)
		c.ID, c.DefID, c.EvalRunID, c.Type = core.CaseRunID(id), core.CaseDefID(def), core.EvalRunID(run), core.Type(typ)
		c.Status, c.Verdict = core.Status(status), core.Verdict(verdict)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the history of case %s/%s: %w",
			eval,
			name,
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

// FailRunning ends the runs of the evals of a type that are still pending or running, as errored with reason: their eval runs and
// the case runs of those, and the experiment runs all of whose eval runs are of that type and ended that way (what a restart of the
// server leaves behind). A case run that was running counts as errored in its eval run. It returns the number of eval runs ended.
func (r Reads) FailRunning(ctx context.Context,
	q Execer,
	typ core.Type,
	reason string,
	at time.Time) (int64, error) {
	t := r.d.Time(at)
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
		WHERE status IN ('pending', 'running') AND eval_run_id IN (SELECT id FROM gb_eval_runs WHERE type = ? AND status IN ('pending', 'running'))`,
		reason,
		t,
		string(typ)); err != nil {
		return 0, fmt.Errorf("fail running case runs: %w", err)
	}
	n, err := run(`UPDATE gb_eval_runs SET status = 'errored', error = ?, finished_at = ?,
			counts_errored = counts_errored + counts_running, counts_running = 0
		WHERE type = ? AND status IN ('pending', 'running')`,
		reason,
		t,
		string(typ))
	if err != nil {
		return 0, fmt.Errorf("fail running eval runs: %w", err)
	}
	if _, err := run(`UPDATE gb_experiment_runs SET status = 'errored', error = ?, finished_at = ?
		WHERE status IN ('pending', 'running')
		AND EXISTS (SELECT 1 FROM gb_eval_runs e WHERE e.experiment_run_id = gb_experiment_runs.id)
		AND NOT EXISTS (SELECT 1 FROM gb_eval_runs e WHERE e.experiment_run_id = gb_experiment_runs.id AND (e.type <> ? OR e.status IN ('pending', 'running')))`,
		reason,
		t,
		string(typ)); err != nil {
		return 0, fmt.Errorf("fail running experiment runs: %w", err)
	}
	return n, nil
}

//---------------------------------------------------------------------------------------------
// Commentaries

// CreateCaseCommentary inserts c into gb_case_run_commentaries and sets its ID and CreatedAt (now, unless set). The statement is the
// same for both backends, so it is here with the reads. It returns ErrNotFound if the case run does not exist.
func (r Reads) CreateCaseCommentary(ctx context.Context,
	q Execer,
	userID string,
	c *core.CaseCommentary) error {
	if err := CheckOwner(userID); err != nil {
		return err
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	c.CreatedAt = c.CreatedAt.UTC().Truncate(time.Second)
	var exists int
	if err := r.queryRow(ctx,
		q,
		`SELECT 1 FROM gb_case_runs WHERE user_id = ? AND id = ?`,
		userID,
		int64(c.CaseRunID)).Scan(&exists); err != nil {
		return notFound(err, fmt.Sprintf("case run %d", c.CaseRunID))
	}
	if err := r.queryRow(ctx,
		q,
		`
		INSERT INTO gb_case_run_commentaries (case_run_id, model, text, input_tokens, output_tokens, cost_usd, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		int64(c.CaseRunID),
		c.Model,
		c.Text,
		c.InputTokens,
		c.OutputTokens,
		c.CostUSD,
		r.d.Time(c.CreatedAt)).Scan(&c.ID); err != nil {
		return fmt.Errorf("create commentary of case run %d: %w",
			c.CaseRunID,
			err)
	}
	return nil
}

// ListCaseCommentaries returns the commentaries of a case run, newest first (empty, not nil, if it has none).
func (r Reads) ListCaseCommentaries(ctx context.Context,
	q Execer,
	userID string,
	caseRunID core.CaseRunID) ([]core.CaseCommentary, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		q,
		`
		SELECT id, model, text, input_tokens, output_tokens, cost_usd, created_at FROM gb_case_run_commentaries
		WHERE case_run_id = ? AND case_run_id IN (SELECT id FROM gb_case_runs WHERE user_id = ?) ORDER BY id DESC`,
		int64(caseRunID),
		userID)
	if err != nil {
		return nil, fmt.Errorf("list commentaries of case run %d: %w",
			caseRunID,
			err)
	}
	defer rows.Close()
	out := []core.CaseCommentary{}
	for rows.Next() {
		c := core.CaseCommentary{
			CaseRunID: caseRunID,
		}
		if err := rows.Scan(&c.ID,
			&c.Model,
			&c.Text,
			&c.InputTokens,
			&c.OutputTokens,
			&c.CostUSD,
			timeScan{&c.CreatedAt}); err != nil {
			return nil, fmt.Errorf("list commentaries of case run %d: %w",
				caseRunID,
				err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

//---------------------------------------------------------------------------------------------
// Bookmarks

// bookmarkKey identifies the bookmarked object of b (unique per user).
func bookmarkKey(b *core.EvalBookmark) string {
	switch b.Kind {
	case core.BookmarkExperimentRun:
		return fmt.Sprintf("experiment_run:%d", b.ExperimentRunID)
	case core.BookmarkEvalRun:
		return fmt.Sprintf("eval_run:%d", b.EvalRunID)
	case core.BookmarkCaseDef:
		return fmt.Sprintf("case_def:%d", b.CaseDefID)
	case core.BookmarkCaseRun:
		return fmt.Sprintf("case_run:%d", b.CaseRunID)
	}
	return fmt.Sprintf("fact:%d:%d", b.CaseRunID, b.EdgeID)
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// CreateEvalBookmark stores the bookmark b of the owner. The caller sets Kind and the id of that kind (see core.EvalBookmark), plus the
// fact for a fact; the levels above are looked up and filled in b (an object that is not the owner's is ErrNotFound). An object is
// bookmarked once: bookmarking it again keeps the bookmark and updates its note. It sets ID and CreatedAt.
func (r Reads) CreateEvalBookmark(ctx context.Context,
	q Execer,
	userID string,
	b *core.EvalBookmark) error {
	if err := CheckOwner(userID); err != nil {
		return err
	}
	if !b.Kind.Valid() {
		return fmt.Errorf("evals: invalid bookmark kind %q", b.Kind)
	}
	// The levels above the object.
	switch b.Kind {
	case core.BookmarkExperimentRun:
		var id int64
		if err := r.queryRow(ctx,
			q,
			`SELECT id FROM gb_experiment_runs WHERE user_id = ? AND id = ?`,
			userID,
			int64(b.ExperimentRunID)).Scan(&id); err != nil {
			return notFound(err, fmt.Sprintf("experiment run %d", b.ExperimentRunID))
		}
	case core.BookmarkEvalRun:
		var exp sql.NullInt64
		if err := r.queryRow(ctx,
			q,
			`SELECT experiment_run_id FROM gb_eval_runs WHERE user_id = ? AND id = ?`,
			userID,
			int64(b.EvalRunID)).Scan(&exp); err != nil {
			return notFound(err, fmt.Sprintf("eval run %d", b.EvalRunID))
		}
		b.ExperimentRunID = core.ExperimentRunID(exp.Int64)
	case core.BookmarkCaseDef:
		var id int64
		if err := r.queryRow(ctx,
			q,
			`SELECT c.id FROM gb_case_defs c JOIN gb_eval_defs e ON e.id = c.eval_def_id WHERE e.user_id = ? AND c.id = ?`,
			userID,
			int64(b.CaseDefID)).Scan(&id); err != nil {
			return notFound(err, fmt.Sprintf("case %d", b.CaseDefID))
		}
	default: // case_run, fact
		var run, def int64
		var exp sql.NullInt64
		if err := r.queryRow(ctx,
			q,
			`
			SELECT cr.eval_run_id, cr.def_id, er.experiment_run_id FROM gb_case_runs cr JOIN gb_eval_runs er ON er.id = cr.eval_run_id
			WHERE cr.user_id = ? AND cr.id = ?`,
			userID,
			int64(b.CaseRunID)).Scan(&run, &def, &exp); err != nil {
			return notFound(err, fmt.Sprintf("case run %d", b.CaseRunID))
		}
		b.EvalRunID, b.CaseDefID, b.ExperimentRunID = core.EvalRunID(run), core.CaseDefID(def), core.ExperimentRunID(exp.Int64)
	}
	fact := "{}"
	if b.Kind == core.BookmarkFact {
		if b.EdgeID <= 0 || b.Fact == nil {
			return fmt.Errorf("evals: a fact bookmark needs the edge id and the fact")
		}
		j, err := JSON(b.Fact, "{}")
		if err != nil {
			return err
		}
		fact = j
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now()
	}
	b.CreatedAt = b.CreatedAt.UTC().Truncate(time.Second)
	if err := r.queryRow(ctx,
		q,
		`
		INSERT INTO gb_eval_bookmarks (user_id, kind, bookmark_key, experiment_run_id, eval_run_id, case_def_id, case_run_id, edge_id, fact, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, bookmark_key) DO UPDATE SET note = excluded.note
		RETURNING id`,
		userID,
		string(b.Kind),
		bookmarkKey(b),
		nullID(int64(b.ExperimentRunID)),
		nullID(int64(b.EvalRunID)),
		nullID(int64(b.CaseDefID)),
		nullID(int64(b.CaseRunID)),
		nullID(b.EdgeID),
		fact,
		b.Note,
		r.d.Time(b.CreatedAt)).Scan(&b.ID); err != nil {
		return fmt.Errorf("create bookmark %s: %w",
			bookmarkKey(b),
			err)
	}
	return nil
}

// ListEvalBookmarks returns the bookmarks of the owner, newest first (empty, not nil, if none), with the names of what they are about.
func (r Reads) ListEvalBookmarks(ctx context.Context,
	q Execer,
	userID string) ([]core.EvalBookmark, error) {
	if err := CheckOwner(userID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		q,
		`
		SELECT b.id, b.kind, b.experiment_run_id, b.eval_run_id, b.case_def_id, b.case_run_id, b.edge_id, b.fact, b.note, b.created_at,
			cd.name, ed.name, xd.name, cr.verdict, er.params
		FROM gb_eval_bookmarks b
		LEFT JOIN gb_case_defs cd ON cd.id = b.case_def_id
		LEFT JOIN gb_eval_runs er ON er.id = b.eval_run_id
		LEFT JOIN gb_eval_defs ed ON ed.id = COALESCE(cd.eval_def_id, er.def_id)
		LEFT JOIN gb_case_runs cr ON cr.id = b.case_run_id
		LEFT JOIN gb_experiment_runs xr ON xr.id = b.experiment_run_id
		LEFT JOIN gb_experiment_defs xd ON xd.id = xr.def_id
		WHERE b.user_id = ? ORDER BY b.id DESC`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	defer rows.Close()
	out := []core.EvalBookmark{}
	for rows.Next() {
		var b core.EvalBookmark
		var kind, fact string
		var exp, run, def, caseRun, edge sql.NullInt64
		var caseName, evalName, expName, verdict sql.NullString
		var params []byte
		if err := rows.Scan(&b.ID,
			&kind,
			&exp,
			&run,
			&def,
			&caseRun,
			&edge,
			&fact,
			&b.Note,
			timeScan{&b.CreatedAt},
			&caseName,
			&evalName,
			&expName,
			&verdict,
			&params); err != nil {
			return nil, fmt.Errorf("list bookmarks: %w", err)
		}
		b.Kind = core.BookmarkKind(kind)
		b.ExperimentRunID, b.EvalRunID = core.ExperimentRunID(exp.Int64), core.EvalRunID(run.Int64)
		b.CaseDefID, b.CaseRunID, b.EdgeID = core.CaseDefID(def.Int64), core.CaseRunID(caseRun.Int64), edge.Int64
		b.CaseName, b.EvalName, b.ExperimentName, b.Verdict = caseName.String, evalName.String, expName.String, core.Verdict(verdict.String)
		if b.Kind == core.BookmarkFact {
			b.Fact = &core.BookmarkedFact{}
			if err := Unmarshal([]byte(fact), b.Fact); err != nil {
				return nil, fmt.Errorf("list bookmarks: fact: %w", err)
			}
		}
		if len(params) > 0 {
			if err := Unmarshal(params, &b.Params); err != nil {
				return nil, fmt.Errorf("list bookmarks: params: %w", err)
			}
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteEvalBookmark deletes the bookmark of the owner. It returns ErrNotFound if there is none.
func (r Reads) DeleteEvalBookmark(ctx context.Context,
	q Execer,
	userID string,
	id int64) error {
	if err := CheckOwner(userID); err != nil {
		return err
	}
	res, err := q.ExecContext(ctx,
		r.d.Rebind(`DELETE FROM gb_eval_bookmarks WHERE user_id = ? AND id = ?`),
		userID,
		id)
	if err != nil {
		return fmt.Errorf("delete bookmark %d: %w",
			id,
			err)
	}
	return Affected(res, fmt.Sprintf("bookmark %d", id))
}
