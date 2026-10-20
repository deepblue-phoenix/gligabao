package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
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

func (r Reads) query(ctx context.Context, q Execer, query string, args ...any) (*sql.Rows, error) {
	return q.QueryContext(ctx, r.d.Rebind(query), args...)
}

func (r Reads) queryRow(ctx context.Context, q Execer, query string, args ...any) *sql.Row {
	return q.QueryRowContext(ctx, r.d.Rebind(query), args...)
}

const experimentRunCols = `id, def_id, status, error, params, counts_total, counts_pending, counts_running, counts_completed,
	counts_errored, counts_cancelled, counts_passed, counts_failed, cost_usd, created_at, started_at, finished_at`

func scanExperimentRun(s scanner) (*core.ExperimentRun, error) {
	var x core.ExperimentRun
	var id, def int64
	var status string
	var params []byte
	c := &x.Counts
	err := s.Scan(&id, &def, &status, &x.Error, &params, &c.Total, &c.Pending, &c.Running, &c.Completed, &c.Errored, &c.Cancelled,
		&c.Passed, &c.Failed, &x.CostUSD, timeScan{&x.CreatedAt}, nullTimeScan{&x.StartedAt}, nullTimeScan{&x.FinishedAt})
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
	counts_completed, counts_errored, counts_cancelled, counts_passed, counts_failed, cost_usd, created_at, started_at, finished_at`

func scanEvalRun(s scanner) (*core.EvalRun, error) {
	var x core.EvalRun
	var id, def int64
	var exp sql.NullInt64
	var typ, status string
	var params []byte
	c := &x.Counts
	err := s.Scan(&id, &def, &exp, &typ, &status, &x.Error, &params, &c.Total, &c.Pending, &c.Running, &c.Completed, &c.Errored,
		&c.Cancelled, &c.Passed, &c.Failed, &x.CostUSD, timeScan{&x.CreatedAt}, nullTimeScan{&x.StartedAt}, nullTimeScan{&x.FinishedAt})
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

func unmarshalParams(text []byte, p *core.Params) error {
	if err := Unmarshal(text, p); err != nil {
		return err
	}
	if len(*p) == 0 {
		*p = nil
	}
	return nil
}

const caseRunCols = `id, def_id, eval_run_id, type, status, error, verdict, score, cost_usd, created_at, started_at, finished_at`

func scanCaseRun(s scanner) (*core.CaseRun, error) {
	var x core.CaseRun
	var id, def, run int64
	var typ, status, verdict string
	err := s.Scan(&id, &def, &run, &typ, &status, &x.Error, &verdict, &x.Score, &x.CostUSD,
		timeScan{&x.CreatedAt}, nullTimeScan{&x.StartedAt}, nullTimeScan{&x.FinishedAt})
	if err != nil {
		return nil, err
	}
	x.ID, x.DefID, x.EvalRunID, x.Type = core.CaseRunID(id), core.CaseDefID(def), core.EvalRunID(run), core.Type(typ)
	x.Status, x.Verdict = core.Status(status), core.Verdict(verdict)
	return &x, nil
}

// notFound turns sql.ErrNoRows into ErrNotFound (wrapped with what).
func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}

//---------------------------------------------------------------------------------------------
// Defs

// ListExperimentDefs returns every experiment def, oldest first, each with its EvalDefIDs.
func (r Reads) ListExperimentDefs(ctx context.Context, q Execer) ([]core.ExperimentDef, error) {
	return r.experimentDefs(ctx, q, ``, nil)
}

// GetExperimentDef returns the experiment def with the ID, with its EvalDefIDs.
func (r Reads) GetExperimentDef(ctx context.Context, q Execer, id core.ExperimentDefID) (*core.ExperimentDef, error) {
	list, err := r.experimentDefs(ctx, q, `WHERE id = ?`, []any{int64(id)})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("experiment def %d: %w", id, ErrNotFound)
	}
	return &list[0], nil
}

func (r Reads) experimentDefs(ctx context.Context, q Execer, where string, args []any) ([]core.ExperimentDef, error) {
	rows, err := r.query(ctx, q, `SELECT id, name, description, params, created_at, updated_at FROM gb_neo_experiment_defs `+where+` ORDER BY id`, args...)
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
		if err := rows.Scan(&id, &d.Name, &d.Description, &params, timeScan{&d.CreatedAt}, timeScan{&d.UpdatedAt}); err != nil {
			return nil, fmt.Errorf("list experiment defs: %w", err)
		}
		d.ID, d.EvalDefIDs = core.ExperimentDefID(id), []core.EvalDefID{}
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
	links, err := r.query(ctx, q, `SELECT experiment_def_id, eval_def_id FROM gb_neo_experiment_def_evals ORDER BY experiment_def_id, position`)
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
func (r Reads) GetEvalDef(ctx context.Context, q Execer, id core.EvalDefID) (*core.EvalDef, error) {
	var d core.EvalDef
	var typ string
	var tags, params []byte
	err := r.queryRow(ctx, q, `SELECT name, description, type, tags, params, created_at, updated_at FROM gb_neo_eval_defs WHERE id = ?`, int64(id)).
		Scan(&d.Name, &d.Description, &typ, &tags, &params, timeScan{&d.CreatedAt}, timeScan{&d.UpdatedAt})
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("eval def %d", id))
	}
	d.ID, d.Type = id, core.Type(typ)
	if err := Unmarshal(tags, &d.Tags); err != nil {
		return nil, err
	}
	if err := unmarshalParams(params, &d.Params); err != nil {
		return nil, err
	}
	return &d, nil
}

// ListCaseDefs returns the case defs of an eval def ordered by position, without their Meta.
func (r Reads) ListCaseDefs(ctx context.Context, q Execer, evalDefID core.EvalDefID) ([]core.CaseDef, error) {
	rows, err := r.query(ctx, q, `
		SELECT id, type, name, position, description, tags, created_at, updated_at FROM gb_neo_case_defs
		WHERE eval_def_id = ? ORDER BY position`, int64(evalDefID))
	if err != nil {
		return nil, fmt.Errorf("list case defs of eval def %d: %w", evalDefID, err)
	}
	defer rows.Close()
	out := []core.CaseDef{}
	for rows.Next() {
		c := core.CaseDef{EvalDefID: evalDefID}
		var id int64
		var typ string
		var tags []byte
		if err := rows.Scan(&id, &typ, &c.Name, &c.Position, &c.Description, &tags, timeScan{&c.CreatedAt}, timeScan{&c.UpdatedAt}); err != nil {
			return nil, fmt.Errorf("list case defs of eval def %d: %w", evalDefID, err)
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
func (r Reads) ListExperimentCases(ctx context.Context, q Execer, experimentDefID core.ExperimentDefID) ([]core.CaseRef, error) {
	rows, err := r.query(ctx, q, `
		SELECT DISTINCT ed.name, cd.name
		FROM gb_neo_case_runs cr
		JOIN gb_neo_eval_runs er ON er.id = cr.eval_run_id
		JOIN gb_neo_experiment_runs xr ON xr.id = er.experiment_run_id
		JOIN gb_neo_case_defs cd ON cd.id = cr.def_id
		JOIN gb_neo_eval_defs ed ON ed.id = cd.eval_def_id
		WHERE xr.def_id = ?
		ORDER BY ed.name, cd.name`, int64(experimentDefID))
	if err != nil {
		return nil, fmt.Errorf("list the cases of experiment def %d: %w", experimentDefID, err)
	}
	defer rows.Close()
	out := []core.CaseRef{}
	for rows.Next() {
		var c core.CaseRef
		if err := rows.Scan(&c.Eval, &c.Name); err != nil {
			return nil, fmt.Errorf("list the cases of experiment def %d: %w", experimentDefID, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

//---------------------------------------------------------------------------------------------
// Runs

// ListExperimentRuns returns the runs of an experiment def, oldest first.
func (r Reads) ListExperimentRuns(ctx context.Context, q Execer, defID core.ExperimentDefID) ([]core.ExperimentRun, error) {
	rows, err := r.query(ctx, q, `SELECT `+experimentRunCols+` FROM gb_neo_experiment_runs WHERE def_id = ? ORDER BY id`, int64(defID))
	if err != nil {
		return nil, fmt.Errorf("list runs of experiment def %d: %w", defID, err)
	}
	defer rows.Close()
	out := []core.ExperimentRun{}
	for rows.Next() {
		x, err := scanExperimentRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list runs of experiment def %d: %w", defID, err)
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// GetExperimentRun returns the experiment run with the ID (without its EvalRuns).
func (r Reads) GetExperimentRun(ctx context.Context, q Execer, id core.ExperimentRunID) (*core.ExperimentRun, error) {
	x, err := scanExperimentRun(r.queryRow(ctx, q, `SELECT `+experimentRunCols+` FROM gb_neo_experiment_runs WHERE id = ?`, int64(id)))
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("experiment run %d", id))
	}
	return x, nil
}

// GetEvalRun returns the eval run with the ID with its Meta, without its CaseRuns (see ListCaseRuns).
func (r Reads) GetEvalRun(ctx context.Context, q Execer, id core.EvalRunID) (*core.EvalRun, error) {
	x, err := scanEvalRun(r.queryRow(ctx, q, `SELECT `+evalRunCols+` FROM gb_neo_eval_runs WHERE id = ?`, int64(id)))
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("eval run %d", id))
	}
	if x.Meta, err = r.evalRunMeta(ctx, q, x.ID, x.Type); err != nil {
		return nil, err
	}
	return x, nil
}

// ListEvalRunsOfExperimentRun returns the eval runs of an experiment run, oldest first, with their Meta.
func (r Reads) ListEvalRunsOfExperimentRun(ctx context.Context, q Execer, id core.ExperimentRunID) ([]core.EvalRun, error) {
	return r.evalRuns(ctx, q, `WHERE experiment_run_id = ? ORDER BY id`, []any{int64(id)})
}

// ListEvalRunsOfExperiment returns the eval runs of every run of an experiment def, oldest first, with their Meta.
func (r Reads) ListEvalRunsOfExperiment(ctx context.Context, q Execer, defID core.ExperimentDefID) ([]core.EvalRun, error) {
	return r.evalRuns(ctx, q, `WHERE experiment_run_id IN (SELECT id FROM gb_neo_experiment_runs WHERE def_id = ?) ORDER BY id`, []any{int64(defID)})
}

// ListTrainingEvalRuns returns the training eval runs of a trained model, newest first, at most limit (all if 0), with their Meta.
func (r Reads) ListTrainingEvalRuns(ctx context.Context, q Execer, trainedModelID int64, limit int) ([]core.EvalRun, error) {
	where := `WHERE type = 'training' AND id IN (SELECT eval_run_id FROM gb_neo_eval_runs_meta_training WHERE trained_model_id = ?) ORDER BY id DESC`
	args := []any{trainedModelID}
	if limit > 0 {
		where += ` LIMIT ?`
		args = append(args, limit)
	}
	return r.evalRuns(ctx, q, where, args)
}

func (r Reads) evalRuns(ctx context.Context, q Execer, tail string, args []any) ([]core.EvalRun, error) {
	rows, err := r.query(ctx, q, `SELECT `+evalRunCols+` FROM gb_neo_eval_runs `+tail, args...)
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
		if out[i].Meta, err = r.evalRunMeta(ctx, q, out[i].ID, out[i].Type); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// evalRunMeta reads the row of eval run id in the table of typ (a pointer to its struct).
func (r Reads) evalRunMeta(ctx context.Context, q Execer, id core.EvalRunID, typ core.Type) (core.EvalRunMeta, error) {
	switch typ {
	case core.TypeKnowledge:
		m := &core.KnowledgeEvalRunMeta{EvalRunID: id}
		err := r.queryRow(ctx, q, `
			SELECT chunks, steps, remember_calls, recall_calls, remember_cost_usd, recall_cost_usd, checks_passed, checks_failed
			FROM gb_neo_eval_runs_meta_knowledge WHERE eval_run_id = ?`, int64(id)).
			Scan(&m.Chunks, &m.Steps, &m.RememberCalls, &m.RecallCalls, &m.RememberCostUSD, &m.RecallCostUSD, &m.ChecksPassed, &m.ChecksFailed)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // an eval run may have been recorded without it
		}
		if err != nil {
			return nil, fmt.Errorf("get knowledge eval run meta of eval run %d: %w", id, err)
		}
		return m, nil
	case core.TypeTraining:
		m := &core.TrainingEvalRunMeta{EvalRunID: id}
		err := r.queryRow(ctx, q, `
			SELECT trained_model_id, judge_model, num_answered, num_judged, num_correct, num_partial, num_incorrect, score_sum, num_batches,
				judge_calls, judge_input_tokens, judge_output_tokens, judge_cost_usd
			FROM gb_neo_eval_runs_meta_training WHERE eval_run_id = ?`, int64(id)).
			Scan(&m.TrainedModelID, &m.JudgeModel, &m.NumAnswered, &m.NumJudged, &m.NumCorrect, &m.NumPartial, &m.NumIncorrect, &m.ScoreSum,
				&m.NumBatches, &m.JudgeCalls, &m.JudgeInputTokens, &m.JudgeOutputTokens, &m.JudgeCostUSD)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("get training eval run meta of eval run %d: %w", id, err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("eval run %d: unknown type %q", id, typ)
}

// ListCaseRuns returns the case runs of an eval run, in the order of their case defs, each with its Meta (a pointer to the struct
// of its type; a knowledge one with its Trajectories).
func (r Reads) ListCaseRuns(ctx context.Context, q Execer, evalRunID core.EvalRunID) ([]core.CaseRun, error) {
	rows, err := r.query(ctx, q, `
		SELECT `+prefixed("cr.", caseRunCols)+` FROM gb_neo_case_runs cr JOIN gb_neo_case_defs cd ON cd.id = cr.def_id
		WHERE cr.eval_run_id = ? ORDER BY cd.position, cr.id`, int64(evalRunID))
	if err != nil {
		return nil, fmt.Errorf("list case runs of eval run %d: %w", evalRunID, err)
	}
	defer rows.Close()
	out := []core.CaseRun{}
	for rows.Next() {
		x, err := scanCaseRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list case runs of eval run %d: %w", evalRunID, err)
		}
		out = append(out, *x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list case runs of eval run %d: %w", evalRunID, err)
	}
	rows.Close()
	for i := range out {
		if out[i].Meta, err = r.caseRunMeta(ctx, q, out[i].ID, out[i].Type, true); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// prefixed puts prefix before every column of a comma-separated list.
func prefixed(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// GetCaseRun returns the case run with the ID with its Meta (a knowledge one with its Trajectories).
func (r Reads) GetCaseRun(ctx context.Context, q Execer, id core.CaseRunID) (*core.CaseRun, error) {
	x, err := scanCaseRun(r.queryRow(ctx, q, `SELECT `+caseRunCols+` FROM gb_neo_case_runs WHERE id = ?`, int64(id)))
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("case run %d", id))
	}
	if x.Meta, err = r.caseRunMeta(ctx, q, x.ID, x.Type, true); err != nil {
		return nil, err
	}
	return x, nil
}

// caseRunMeta reads the row of case run id in the table of typ (nil if there is none).
func (r Reads) caseRunMeta(ctx context.Context, q Execer, id core.CaseRunID, typ core.Type, trajectories bool) (core.CaseRunMeta, error) {
	switch typ {
	case core.TypeKnowledge:
		m := &core.KnowledgeCaseRunMeta{CaseRunID: id}
		var input, problems, passed []byte
		err := r.queryRow(ctx, q, `
			SELECT input, answer, problems, passed_checks, dataset_id, steps FROM gb_neo_case_runs_meta_knowledge WHERE case_run_id = ?`, int64(id)).
			Scan(&input, &m.Answer, &problems, &passed, &m.DatasetID, &m.Steps)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("get knowledge case run meta of case run %d: %w", id, err)
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
			if m.Trajectories, err = r.trajectories(ctx, q, id); err != nil {
				return nil, err
			}
		}
		return m, nil
	case core.TypeTraining:
		m := &core.TrainingCaseRunMeta{CaseRunID: id}
		err := r.queryRow(ctx, q, `
			SELECT instruction, input, reference, response, batch, judge_score, judge_reason, input_tokens, output_tokens, duration_ms
			FROM gb_neo_case_runs_meta_training WHERE case_run_id = ?`, int64(id)).
			Scan(&m.Instruction, &m.Input, &m.Reference, &m.Response, &m.Batch, &m.JudgeScore, &m.JudgeReason, &m.InputTokens, &m.OutputTokens, &m.DurationMS)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("get training case run meta of case run %d: %w", id, err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("case run %d: unknown type %q", id, typ)
}

const trajectoryCols = `seq, kind, trajectory_id, agent_name, steps, cost_usd, path, artifact_ids, created_at`

func scanTrajectory(s scanner, t *core.KnowledgeTrajectory) error {
	var artifacts []byte
	if err := s.Scan(&t.Seq, &t.Kind, &t.TrajectoryID, &t.AgentName, &t.Steps, &t.CostUSD, &t.Path, &artifacts, timeScan{&t.CreatedAt}); err != nil {
		return err
	}
	return Unmarshal(artifacts, &t.ArtifactIDs)
}

func (r Reads) trajectories(ctx context.Context, q Execer, caseRunID core.CaseRunID) ([]core.KnowledgeTrajectory, error) {
	rows, err := r.query(ctx, q, `SELECT `+trajectoryCols+` FROM gb_neo_case_runs_meta_knowledge_trajectories WHERE case_run_id = ? ORDER BY seq`, int64(caseRunID))
	if err != nil {
		return nil, fmt.Errorf("list trajectories of case run %d: %w", caseRunID, err)
	}
	defer rows.Close()
	var out []core.KnowledgeTrajectory
	for rows.Next() {
		var t core.KnowledgeTrajectory
		if err := scanTrajectory(rows, &t); err != nil {
			return nil, fmt.Errorf("list trajectories of case run %d: %w", caseRunID, err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// FindKnowledgeTrajectory returns the trajectory with the given ATIF trajectory ID and the case run it belongs to.
func (r Reads) FindKnowledgeTrajectory(ctx context.Context, q Execer, trajectoryID string) (*TrajectoryRef, error) {
	var ref TrajectoryRef
	var caseRun int64
	var artifacts []byte
	t := &ref.KnowledgeTrajectory
	err := r.queryRow(ctx, q, `
		SELECT case_run_id, `+trajectoryCols+` FROM gb_neo_case_runs_meta_knowledge_trajectories WHERE trajectory_id = ? ORDER BY id LIMIT 1`, trajectoryID).
		Scan(&caseRun, &t.Seq, &t.Kind, &t.TrajectoryID, &t.AgentName, &t.Steps, &t.CostUSD, &t.Path, &artifacts, timeScan{&t.CreatedAt})
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
func (r Reads) ListCaseHistory(ctx context.Context, q Execer, defID core.ExperimentDefID, eval, name string) ([]CaseHistoryEntry, error) {
	rows, err := r.query(ctx, q, `
		SELECT er.experiment_run_id, `+prefixed("cr.", caseRunCols)+`
		FROM gb_neo_case_runs cr
		JOIN gb_neo_eval_runs er ON er.id = cr.eval_run_id
		JOIN gb_neo_experiment_runs xr ON xr.id = er.experiment_run_id
		JOIN gb_neo_case_defs cd ON cd.id = cr.def_id
		JOIN gb_neo_eval_defs ed ON ed.id = cd.eval_def_id
		WHERE xr.def_id = ? AND ed.name = ? AND cd.name = ?
		ORDER BY xr.id, cr.id`, int64(defID), eval, name)
	if err != nil {
		return nil, fmt.Errorf("list the history of case %s/%s: %w", eval, name, err)
	}
	defer rows.Close()
	out := []CaseHistoryEntry{}
	for rows.Next() {
		var e CaseHistoryEntry
		var xr int64
		var typ, status, verdict string
		var id, def, run int64
		c := &e.CaseRun
		if err := rows.Scan(&xr, &id, &def, &run, &typ, &status, &c.Error, &verdict, &c.Score, &c.CostUSD,
			timeScan{&c.CreatedAt}, nullTimeScan{&c.StartedAt}, nullTimeScan{&c.FinishedAt}); err != nil {
			return nil, fmt.Errorf("list the history of case %s/%s: %w", eval, name, err)
		}
		e.ExperimentRunID = core.ExperimentRunID(xr)
		c.ID, c.DefID, c.EvalRunID, c.Type = core.CaseRunID(id), core.CaseDefID(def), core.EvalRunID(run), core.Type(typ)
		c.Status, c.Verdict = core.Status(status), core.Verdict(verdict)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the history of case %s/%s: %w", eval, name, err)
	}
	rows.Close()
	for i := range out {
		c := &out[i].CaseRun
		if c.Meta, err = r.caseRunMeta(ctx, q, c.ID, c.Type, false); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// FailRunning ends the runs of the evals of a type that are still pending or running, as errored with reason: their eval runs and
// the case runs of those, and the experiment runs all of whose eval runs are of that type and ended that way (what a restart of the
// server leaves behind). A case run that was running counts as errored in its eval run. It returns the number of eval runs ended.
func (r Reads) FailRunning(ctx context.Context, q Execer, typ core.Type, reason string, at time.Time) (int64, error) {
	t := r.d.Time(at)
	run := func(query string, args ...any) (int64, error) {
		res, err := q.ExecContext(ctx, r.d.Rebind(query), args...)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	if _, err := run(`UPDATE gb_neo_case_runs SET status = 'errored', error = ?, finished_at = ?
		WHERE status IN ('pending', 'running') AND eval_run_id IN (SELECT id FROM gb_neo_eval_runs WHERE type = ? AND status IN ('pending', 'running'))`,
		reason, t, string(typ)); err != nil {
		return 0, fmt.Errorf("fail running case runs: %w", err)
	}
	n, err := run(`UPDATE gb_neo_eval_runs SET status = 'errored', error = ?, finished_at = ?,
			counts_errored = counts_errored + counts_running, counts_running = 0
		WHERE type = ? AND status IN ('pending', 'running')`, reason, t, string(typ))
	if err != nil {
		return 0, fmt.Errorf("fail running eval runs: %w", err)
	}
	if _, err := run(`UPDATE gb_neo_experiment_runs SET status = 'errored', error = ?, finished_at = ?
		WHERE status IN ('pending', 'running')
		AND EXISTS (SELECT 1 FROM gb_neo_eval_runs e WHERE e.experiment_run_id = gb_neo_experiment_runs.id)
		AND NOT EXISTS (SELECT 1 FROM gb_neo_eval_runs e WHERE e.experiment_run_id = gb_neo_experiment_runs.id AND (e.type <> ? OR e.status IN ('pending', 'running')))`,
		reason, t, string(typ)); err != nil {
		return 0, fmt.Errorf("fail running experiment runs: %w", err)
	}
	return n, nil
}
