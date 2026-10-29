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
// Query runs (see sqlite/db_query_run.go and sqlite/db_schema.go for what the table and the functions are)

// queryRunsSchema is the DDL of gb_query_runs, generated from model.QueryStages (see the sqlite backend).
func queryRunsSchema() string {
	statuses := "'" + strings.Join(model.RememberStatuses, "', '") + "'"
	var b strings.Builder
	fmt.Fprintf(&b, `
CREATE TABLE IF NOT EXISTS gb_query_runs (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	dataset     TEXT NOT NULL,
	question    TEXT NOT NULL,
	params      JSONB NOT NULL DEFAULT '{}',
	session_id  TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL CHECK (status IN (%s)),
	error       TEXT,
	started_at  TIMESTAMPTZ NOT NULL,
	finished_at TIMESTAMPTZ`, statuses)
	for _, s := range model.QueryStages {
		fmt.Fprintf(&b, `,
	%[1]s_status      TEXT NOT NULL DEFAULT 'not_started' CHECK (%[1]s_status IN (%[2]s)),
	%[1]s_error       TEXT,
	%[1]s_started_at  TIMESTAMPTZ,
	%[1]s_finished_at TIMESTAMPTZ`, s.Name, statuses)
		if s.Model {
			fmt.Fprintf(&b, `,
	%s_inference_ids  JSONB NOT NULL DEFAULT '[]'`, s.Name)
		}
	}
	b.WriteString(`
);
CREATE INDEX IF NOT EXISTS idx_gb_query_runs_dataset ON gb_query_runs(user_id, dataset);
CREATE INDEX IF NOT EXISTS idx_gb_query_runs_status ON gb_query_runs(status);
`)
	return b.String()
}

// queryRunCols are the columns of a run but its ID, in the order of queryRunValues and scanQueryRun (see rememberCol).
func queryRunCols() []rememberCol {
	cols := []rememberCol{{"user_id", false}, {"dataset", false}, {"question", false}, {"params", true}, {"session_id", false},
		{"status", false}, {"error", false}, {"started_at", false}, {"finished_at", false}}
	for _, s := range model.QueryStages {
		cols = append(cols, rememberCol{s.Name + "_status", false}, rememberCol{s.Name + "_error", false},
			rememberCol{s.Name + "_started_at", false}, rememberCol{s.Name + "_finished_at", false})
		if s.Model {
			cols = append(cols, rememberCol{s.Name + "_inference_ids", true})
		}
	}
	return cols
}

// queryRunValues returns the values of the columns of queryRunCols.
func queryRunValues(r model.QueryRun) []any {
	vals := []any{r.UserID, r.Dataset, r.Question, jsonObject(r.Params), r.SessionID, r.Status, nullableString(r.Error),
		r.StartedAt, nullableTime(r.FinishedAt)}
	for _, s := range model.QueryStages {
		st := r.Stages[s.Name]
		if st.Status == "" {
			st.Status = model.RememberNotStarted
		}
		vals = append(vals, st.Status, nullableString(st.Error), nullableTime(st.StartedAt), nullableTime(st.FinishedAt))
		if s.Model {
			vals = append(vals, jsonIDs(st.InferenceIDs))
		}
	}
	return vals
}

func scanQueryRun(sc scanner) (*model.QueryRun, error) {
	var r model.QueryRun
	var params string
	var errText sql.NullString
	var finished sql.NullTime
	stages := make([]stageRow, len(model.QueryStages))
	dest := []any{&r.ID, &r.UserID, &r.Dataset, &r.Question, &params, &r.SessionID, &r.Status, &errText, &r.StartedAt, &finished}
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
	r.StartedAt = r.StartedAt.UTC()
	r.FinishedAt = utc(finished)
	r.Stages = make(map[string]model.QueryStage, len(stages))
	for i, s := range model.QueryStages {
		st := model.QueryStage{Status: stages[i].status, StartedAt: utc(stages[i].started), FinishedAt: utc(stages[i].finished),
			InferenceIDs: parseIDs(stages[i].runIDs)}
		if stages[i].err.Valid {
			st.Error = &stages[i].err.String
		}
		r.Stages[s.Name] = st
	}
	return &r, nil
}

func queryRunSelect() string {
	names := []string{"id"}
	for _, c := range queryRunCols() {
		names = append(names, c.selected())
	}
	return "SELECT " + strings.Join(names, ", ") + " FROM gb_query_runs"
}

// InsertQueryRun stores a query run (r.ID is ignored) and returns its ID.
func InsertQueryRun(ctx context.Context, d *sql.DB, r model.QueryRun) (int64, error) {
	cols := queryRunCols()
	names, marks := make([]string, len(cols)), make([]string, len(cols))
	for i, c := range cols {
		names[i], marks[i] = c.name, c.placeholder(i+1)
	}
	var id int64
	err := d.QueryRowContext(ctx, `INSERT INTO gb_query_runs (`+strings.Join(names, ", ")+`) VALUES (`+strings.Join(marks, ", ")+`) RETURNING id`,
		queryRunValues(r)...).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert query run: %w", err)
	}
	return id, nil
}

// SaveQueryRun overwrites the stored query run r.ID with r. It returns model.ErrNotFound if there is none.
func SaveQueryRun(ctx context.Context, d *sql.DB, r model.QueryRun) error {
	cols := queryRunCols()
	sets := make([]string, len(cols))
	for i, c := range cols {
		sets[i] = c.name + " = " + c.placeholder(i+1)
	}
	res, err := d.ExecContext(ctx, `UPDATE gb_query_runs SET `+strings.Join(sets, ", ")+` WHERE id = $`+strconv.Itoa(len(cols)+1),
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
	r, err := scanQueryRun(d.QueryRowContext(ctx, queryRunSelect()+" WHERE id = $1", id))
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
			conds = append(conds, c.col+" = $"+strconv.Itoa(len(args)))
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
	n := len(args)
	rows, err := d.QueryContext(ctx, queryRunSelect()+where+" ORDER BY id DESC LIMIT $"+strconv.Itoa(n+1)+" OFFSET $"+strconv.Itoa(n+2),
		append(args, limit, offset)...)
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
