package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Remember runs (gb_remember_runs, see db_schema.go)

// rememberRunsSchema is the DDL of gb_remember_runs: the columns of the run itself, then those of each stage in
// model.RememberStages, generated so that the stage list stays the only place that names them.
func rememberRunsSchema() string {
	statuses := "'" + strings.Join(model.RememberStatuses, "', '") + "'"
	var b strings.Builder
	fmt.Fprintf(&b, `
CREATE TABLE IF NOT EXISTS gb_remember_runs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id     TEXT NOT NULL,
	dataset     TEXT NOT NULL,
	kind        TEXT NOT NULL,
	params      TEXT NOT NULL DEFAULT '{}',
	artifact_id INTEGER,
	status      TEXT NOT NULL CHECK (status IN (%s)),
	error       TEXT,
	started_at  DATETIME NOT NULL,
	finished_at DATETIME`, statuses)
	for _, s := range model.RememberStages {
		fmt.Fprintf(&b, `,
	%[1]s_status      TEXT NOT NULL DEFAULT 'not_started' CHECK (%[1]s_status IN (%[2]s)),
	%[1]s_error       TEXT,
	%[1]s_started_at  DATETIME,
	%[1]s_finished_at DATETIME`, s.Name, statuses)
		if s.Model {
			fmt.Fprintf(&b, `,
	%s_inference_ids  TEXT NOT NULL DEFAULT '[]'`, s.Name)
		}
	}
	b.WriteString(`
);
CREATE INDEX IF NOT EXISTS idx_gb_remember_runs_dataset ON gb_remember_runs(user_id, dataset);
CREATE INDEX IF NOT EXISTS idx_gb_remember_runs_status ON gb_remember_runs(status);
`)
	fmt.Fprintf(&b, `
CREATE TABLE IF NOT EXISTS gb_remember_run_stage_outputs (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id     INTEGER NOT NULL REFERENCES gb_remember_runs(id) ON DELETE CASCADE,
	stage      TEXT NOT NULL CHECK (stage IN (%s)),
	name       TEXT NOT NULL,
	label      TEXT NOT NULL DEFAULT '',
	output     TEXT NOT NULL,
	created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_gb_remember_run_stage_outputs_run ON gb_remember_run_stage_outputs(run_id, stage);
CREATE INDEX IF NOT EXISTS idx_gb_remember_run_stage_outputs_created ON gb_remember_run_stage_outputs(created_at);
`, "'"+strings.Join(stageNames(), "', '")+"'")
	return b.String()
}

func stageNames() []string {
	names := make([]string, len(model.RememberStages))
	for i, st := range model.RememberStages {
		names[i] = st.Name
	}
	return names
}

// rememberRunColumns are the columns of a run but its ID, in the order of rememberRunValues and rememberRunScan.
func rememberRunColumns() []string {
	cols := []string{"user_id", "dataset", "kind", "params", "artifact_id", "status", "error", "started_at", "finished_at"}
	for _, s := range model.RememberStages {
		cols = append(cols, s.Name+"_status", s.Name+"_error", s.Name+"_started_at", s.Name+"_finished_at")
		if s.Model {
			cols = append(cols, s.Name+"_inference_ids")
		}
	}
	return cols
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// rememberRunValues returns the values of the columns of rememberRunColumns.
func rememberRunValues(r model.RememberRun) []any {
	vals := []any{r.UserID, r.Dataset, r.Kind, jsonObject(r.Params), r.ArtifactID, r.Status, nullableString(r.Error),
		FormatTime(r.StartedAt), FormatTimePtr(r.FinishedAt)}
	for _, s := range model.RememberStages {
		st := r.Stages[s.Name]
		if st.Status == "" {
			st.Status = model.RememberNotStarted
		}
		vals = append(vals, st.Status, nullableString(st.Error), FormatTimePtr(st.StartedAt), FormatTimePtr(st.FinishedAt))
		if s.Model {
			vals = append(vals, jsonIDs(st.InferenceIDs))
		}
	}
	return vals
}

// stageRow holds the columns of one stage as they are scanned.
type stageRow struct {
	status                 string
	err, started, finished sql.NullString
	runIDs                 string
}

func scanRememberRun(sc scanner) (*model.RememberRun, error) {
	var r model.RememberRun
	var params, started string
	var artifact sql.NullInt64
	var errText, finished sql.NullString
	stages := make([]stageRow, len(model.RememberStages))
	dest := []any{&r.ID, &r.UserID, &r.Dataset, &r.Kind, &params, &artifact, &r.Status, &errText, &started, &finished}
	for i, s := range model.RememberStages {
		dest = append(dest, &stages[i].status, &stages[i].err, &stages[i].started, &stages[i].finished)
		if s.Model {
			dest = append(dest, &stages[i].runIDs)
		}
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	r.Params = parseObject(params)
	if artifact.Valid {
		r.ArtifactID = &artifact.Int64
	}
	if errText.Valid {
		r.Error = &errText.String
	}
	r.StartedAt = parseStoredTime(started)
	r.FinishedAt = ParseTime(finished.String)
	r.Stages = make(map[string]model.RememberStage, len(stages))
	for i, s := range model.RememberStages {
		st := model.RememberStage{Status: stages[i].status, StartedAt: ParseTime(stages[i].started.String),
			FinishedAt: ParseTime(stages[i].finished.String), InferenceIDs: parseIDs(stages[i].runIDs)}
		if stages[i].err.Valid {
			st.Error = &stages[i].err.String
		}
		r.Stages[s.Name] = st
	}
	return &r, nil
}

func rememberRunSelect() string {
	return "SELECT id, " + strings.Join(rememberRunColumns(), ", ") + " FROM gb_remember_runs"
}

// InsertRememberRun stores a remember run (r.ID is ignored) and returns its ID.
func InsertRememberRun(ctx context.Context, d *sql.DB, r model.RememberRun) (int64, error) {
	cols := rememberRunColumns()
	res, err := d.ExecContext(ctx, `INSERT INTO gb_remember_runs (`+strings.Join(cols, ", ")+`) VALUES (?`+strings.Repeat(", ?", len(cols)-1)+`)`,
		rememberRunValues(r)...)
	if err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	return res.LastInsertId()
}

// SaveRememberRun overwrites the stored remember run r.ID with r. It returns model.ErrNotFound if there is none.
func SaveRememberRun(ctx context.Context, d *sql.DB, r model.RememberRun) error {
	cols := rememberRunColumns()
	res, err := d.ExecContext(ctx, `UPDATE gb_remember_runs SET `+strings.Join(cols, " = ?, ")+` = ? WHERE id = ?`,
		append(rememberRunValues(r), r.ID)...)
	if err != nil {
		return fmt.Errorf("save remember run: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// GetRememberRun returns the remember run with that ID, or model.ErrNotFound.
func GetRememberRun(ctx context.Context, d *sql.DB, id int64) (*model.RememberRun, error) {
	r, err := scanRememberRun(d.QueryRowContext(ctx, rememberRunSelect()+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return r, err
}

// ListRememberRuns returns the remember runs that match f, newest first (at most limit, after skipping offset), and how many
// match f in all.
func ListRememberRuns(ctx context.Context, d *sql.DB, f model.RememberRunFilter, limit, offset int) ([]model.RememberRun, int, error) {
	var conds []string
	var args []any
	for _, c := range []struct{ col, val string }{{"user_id", f.UserID}, {"dataset", f.Dataset}, {"status", f.Status}} {
		if c.val != "" {
			args = append(args, c.val)
			conds = append(conds, c.col+" = ?")
		}
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM gb_remember_runs"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.QueryContext(ctx, rememberRunSelect()+where+" ORDER BY id DESC LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.RememberRun{}
	for rows.Next() {
		r, err := scanRememberRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

//---------------------------------------------------------------------------------------------
// Stage outputs (gb_remember_run_stage_outputs)

// InsertRememberStageOutput stores an output of a stage of a run (o.ID is ignored) and returns its ID.
func InsertRememberStageOutput(ctx context.Context, d *sql.DB, o model.RememberStageOutput) (int64, error) {
	res, err := d.ExecContext(ctx, `INSERT INTO gb_remember_run_stage_outputs (run_id, stage, name, label, output, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		o.RunID, o.Stage, o.Name, o.Label, string(o.Output), FormatTime(o.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert remember stage output: %w", err)
	}
	return res.LastInsertId()
}

// ListRememberStageOutputs returns the outputs of the run, in the order they were made; only those of the stage if it is not "".
func ListRememberStageOutputs(ctx context.Context, d *sql.DB, runID int64, stage string) ([]model.RememberStageOutput, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, run_id, stage, name, label, output, created_at FROM gb_remember_run_stage_outputs WHERE run_id = ? AND (? = '' OR stage = ?) ORDER BY id`, runID, stage, stage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.RememberStageOutput{}
	for rows.Next() {
		var o model.RememberStageOutput
		var output, created string
		if err := rows.Scan(&o.ID, &o.RunID, &o.Stage, &o.Name, &o.Label, &output, &created); err != nil {
			return nil, err
		}
		o.Output, o.CreatedAt = []byte(output), parseStoredTime(created)
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeleteRememberStageOutputs deletes the outputs made before the time (all of them if it is zero) and returns how many.
func DeleteRememberStageOutputs(ctx context.Context, d *sql.DB, before time.Time) (int64, error) {
	var res sql.Result
	var err error
	if before.IsZero() {
		res, err = d.ExecContext(ctx, `DELETE FROM gb_remember_run_stage_outputs`)
	} else {
		res, err = d.ExecContext(ctx, `DELETE FROM gb_remember_run_stage_outputs WHERE created_at < ?`, FormatTime(before))
	}
	if err != nil {
		return 0, fmt.Errorf("delete remember stage outputs: %w", err)
	}
	return res.RowsAffected()
}
