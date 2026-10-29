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
// Query runs (gb_query_runs, see db_schema.go)

// queryRunsSchema is the DDL of gb_query_runs: the columns of the run itself, then those of each stage in model.QueryStages,
// generated so that the stage list stays the only place that names them.
func queryRunsSchema() string {
	statuses := "'" + strings.Join(model.RememberStatuses, "', '") + "'"
	var b strings.Builder
	fmt.Fprintf(&b, `
CREATE TABLE IF NOT EXISTS gb_query_runs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id     TEXT NOT NULL,
	dataset     TEXT NOT NULL,
	question    TEXT NOT NULL,
	params      TEXT NOT NULL DEFAULT '{}',
	session_id  TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL CHECK (status IN (%s)),
	error       TEXT,
	started_at  DATETIME NOT NULL,
	finished_at DATETIME`, statuses)
	for _, s := range model.QueryStages {
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
CREATE INDEX IF NOT EXISTS idx_gb_query_runs_dataset ON gb_query_runs(user_id, dataset);
CREATE INDEX IF NOT EXISTS idx_gb_query_runs_status ON gb_query_runs(status);
`)
	return b.String()
}

// queryRunColumns are the columns of a run but its ID, in the order of queryRunValues and scanQueryRun.
func queryRunColumns() []string {
	cols := []string{"user_id", "dataset", "question", "params", "session_id", "status", "error", "started_at", "finished_at"}
	for _, s := range model.QueryStages {
		cols = append(cols, s.Name+"_status", s.Name+"_error", s.Name+"_started_at", s.Name+"_finished_at")
		if s.Model {
			cols = append(cols, s.Name+"_inference_ids")
		}
	}
	return cols
}

// queryRunValues returns the values of the columns of queryRunColumns.
func queryRunValues(r model.QueryRun) []any {
	vals := []any{r.UserID, r.Dataset, r.Question, jsonObject(r.Params), r.SessionID, r.Status, nullableString(r.Error),
		FormatTime(r.StartedAt), FormatTimePtr(r.FinishedAt)}
	for _, s := range model.QueryStages {
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

func scanQueryRun(sc scanner) (*model.QueryRun, error) {
	var r model.QueryRun
	var params, started string
	var errText, finished sql.NullString
	stages := make([]stageRow, len(model.QueryStages))
	dest := []any{&r.ID, &r.UserID, &r.Dataset, &r.Question, &params, &r.SessionID, &r.Status, &errText, &started, &finished}
	for i, s := range model.QueryStages {
		dest = append(dest, &stages[i].status, &stages[i].err, &stages[i].started, &stages[i].finished)
		if s.Model {
			dest = append(dest, &stages[i].runIDs)
		}
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	r.Params = parseObject(params)
	if errText.Valid {
		r.Error = &errText.String
	}
	r.StartedAt = parseStoredTime(started)
	r.FinishedAt = ParseTime(finished.String)
	r.Stages = make(map[string]model.QueryStage, len(stages))
	for i, s := range model.QueryStages {
		st := model.QueryStage{Status: stages[i].status, StartedAt: ParseTime(stages[i].started.String),
			FinishedAt: ParseTime(stages[i].finished.String), InferenceIDs: parseIDs(stages[i].runIDs)}
		if stages[i].err.Valid {
			st.Error = &stages[i].err.String
		}
		r.Stages[s.Name] = st
	}
	return &r, nil
}

func queryRunSelect() string {
	return "SELECT id, " + strings.Join(queryRunColumns(), ", ") + " FROM gb_query_runs"
}

// InsertQueryRun stores a query run (r.ID is ignored) and returns its ID.
func InsertQueryRun(ctx context.Context, d *sql.DB, r model.QueryRun) (int64, error) {
	cols := queryRunColumns()
	res, err := d.ExecContext(ctx, `INSERT INTO gb_query_runs (`+strings.Join(cols, ", ")+`) VALUES (?`+strings.Repeat(", ?", len(cols)-1)+`)`,
		queryRunValues(r)...)
	if err != nil {
		return 0, fmt.Errorf("insert query run: %w", err)
	}
	return res.LastInsertId()
}

// SaveQueryRun overwrites the stored query run r.ID with r. It returns model.ErrNotFound if there is none.
func SaveQueryRun(ctx context.Context, d *sql.DB, r model.QueryRun) error {
	cols := queryRunColumns()
	res, err := d.ExecContext(ctx, `UPDATE gb_query_runs SET `+strings.Join(cols, " = ?, ")+` = ? WHERE id = ?`,
		append(queryRunValues(r), r.ID)...)
	if err != nil {
		return fmt.Errorf("save query run: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// GetQueryRun returns the query run with that ID, or model.ErrNotFound.
func GetQueryRun(ctx context.Context, d *sql.DB, id int64) (*model.QueryRun, error) {
	r, err := scanQueryRun(d.QueryRowContext(ctx, queryRunSelect()+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return r, err
}

// ListQueryRuns returns the query runs that match f, newest first (at most limit, after skipping offset), and how many match f
// in all.
func ListQueryRuns(ctx context.Context, d *sql.DB, f model.QueryRunFilter, limit, offset int) ([]model.QueryRun, int, error) {
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
	if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM gb_query_runs"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.QueryContext(ctx, queryRunSelect()+where+" ORDER BY id DESC LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.QueryRun{}
	for rows.Next() {
		r, err := scanQueryRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}
