package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Remember runs (see sqlite/db_rememberrun.go and sqlite/db_schema.go for what the table and the functions are)

// rememberRunsSchema is the DDL of gb_remember_runs, generated from model.RememberStages (see the sqlite backend).
func rememberRunsSchema() string {
	statuses := "'" + strings.Join(model.RememberStatuses, "', '") + "'"
	var b strings.Builder
	fmt.Fprintf(&b, `
CREATE TABLE IF NOT EXISTS gb_remember_runs (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	dataset     TEXT NOT NULL,
	kind        TEXT NOT NULL,
	params      JSONB NOT NULL DEFAULT '{}',
	artifact_id BIGINT,
	status      TEXT NOT NULL CHECK (status IN (%s)),
	error       TEXT,
	started_at  TIMESTAMPTZ NOT NULL,
	finished_at TIMESTAMPTZ`, statuses)
	for _, s := range model.RememberStages {
		fmt.Fprintf(&b, `,
	%[1]s_status      TEXT NOT NULL DEFAULT 'not_started' CHECK (%[1]s_status IN (%[2]s)),
	%[1]s_error       TEXT,
	%[1]s_started_at  TIMESTAMPTZ,
	%[1]s_finished_at TIMESTAMPTZ`, s.Name, statuses)
		if s.Model {
			fmt.Fprintf(&b, `,
	%s_model_run_ids  JSONB NOT NULL DEFAULT '[]'`, s.Name)
		}
	}
	b.WriteString(`
);
CREATE INDEX IF NOT EXISTS idx_gb_remember_runs_dataset ON gb_remember_runs(user_id, dataset);
CREATE INDEX IF NOT EXISTS idx_gb_remember_runs_status ON gb_remember_runs(status);
`)
	return b.String()
}

// rememberCol is a column of gb_remember_runs; jsonb ones are written with a cast and read as text.
type rememberCol struct {
	name  string
	jsonb bool
}

// rememberRunCols are the columns of a run but its ID, in the order of rememberRunValues and scanRememberRun.
func rememberRunCols() []rememberCol {
	cols := []rememberCol{{"user_id", false}, {"dataset", false}, {"kind", false}, {"params", true}, {"artifact_id", false},
		{"status", false}, {"error", false}, {"started_at", false}, {"finished_at", false}}
	for _, s := range model.RememberStages {
		cols = append(cols, rememberCol{s.Name + "_status", false}, rememberCol{s.Name + "_error", false},
			rememberCol{s.Name + "_started_at", false}, rememberCol{s.Name + "_finished_at", false})
		if s.Model {
			cols = append(cols, rememberCol{s.Name + "_model_run_ids", true})
		}
	}
	return cols
}

// placeholder is the parameter n (from 1) of column c.
func (c rememberCol) placeholder(n int) string {
	if c.jsonb {
		return "$" + strconv.Itoa(n) + "::jsonb"
	}
	return "$" + strconv.Itoa(n)
}

// selected is how the column is read.
func (c rememberCol) selected() string {
	if c.jsonb {
		return c.name + "::text"
	}
	return c.name
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func rememberRunValues(r model.RememberRun) []any {
	vals := []any{r.UserID, r.Dataset, r.Kind, jsonObject(r.Params), r.ArtifactID, r.Status, nullableString(r.Error),
		r.StartedAt, nullableTime(r.FinishedAt)}
	for _, s := range model.RememberStages {
		st := r.Stages[s.Name]
		if st.Status == "" {
			st.Status = model.RememberNotStarted
		}
		vals = append(vals, st.Status, nullableString(st.Error), nullableTime(st.StartedAt), nullableTime(st.FinishedAt))
		if s.Model {
			vals = append(vals, jsonIDs(st.ModelRunIDs))
		}
	}
	return vals
}

type stageRow struct {
	status            string
	err               sql.NullString
	started, finished sql.NullTime
	runIDs            string
}

func scanRememberRun(sc scanner) (*model.RememberRun, error) {
	var r model.RememberRun
	var params string
	var artifact sql.NullInt64
	var errText sql.NullString
	var finished sql.NullTime
	stages := make([]stageRow, len(model.RememberStages))
	dest := []any{&r.ID, &r.UserID, &r.Dataset, &r.Kind, &params, &artifact, &r.Status, &errText, &r.StartedAt, &finished}
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
	r.StartedAt = r.StartedAt.UTC()
	r.FinishedAt = utc(finished)
	r.Stages = make(map[string]model.RememberStage, len(stages))
	for i, s := range model.RememberStages {
		st := model.RememberStage{Status: stages[i].status, StartedAt: utc(stages[i].started), FinishedAt: utc(stages[i].finished),
			ModelRunIDs: parseIDs(stages[i].runIDs)}
		if stages[i].err.Valid {
			st.Error = &stages[i].err.String
		}
		r.Stages[s.Name] = st
	}
	return &r, nil
}

func rememberRunSelect() string {
	names := []string{"id"}
	for _, c := range rememberRunCols() {
		names = append(names, c.selected())
	}
	return "SELECT " + strings.Join(names, ", ") + " FROM gb_remember_runs"
}

// InsertRememberRun stores a remember run (r.ID is ignored) and returns its ID.
func InsertRememberRun(ctx context.Context, d *sql.DB, r model.RememberRun) (int64, error) {
	cols := rememberRunCols()
	names, marks := make([]string, len(cols)), make([]string, len(cols))
	for i, c := range cols {
		names[i], marks[i] = c.name, c.placeholder(i+1)
	}
	var id int64
	err := d.QueryRowContext(ctx, `INSERT INTO gb_remember_runs (`+strings.Join(names, ", ")+`) VALUES (`+strings.Join(marks, ", ")+`) RETURNING id`,
		rememberRunValues(r)...).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	return id, nil
}

// SaveRememberRun overwrites the stored remember run r.ID with r. It returns model.ErrNotFound if there is none.
func SaveRememberRun(ctx context.Context, d *sql.DB, r model.RememberRun) error {
	cols := rememberRunCols()
	sets := make([]string, len(cols))
	for i, c := range cols {
		sets[i] = c.name + " = " + c.placeholder(i+1)
	}
	res, err := d.ExecContext(ctx, `UPDATE gb_remember_runs SET `+strings.Join(sets, ", ")+` WHERE id = $`+strconv.Itoa(len(cols)+1),
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
	r, err := scanRememberRun(d.QueryRowContext(ctx, rememberRunSelect()+" WHERE id = $1", id))
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
			conds = append(conds, c.col+" = $"+strconv.Itoa(len(args)))
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
	n := len(args)
	rows, err := d.QueryContext(ctx, rememberRunSelect()+where+" ORDER BY id DESC LIMIT $"+strconv.Itoa(n+1)+" OFFSET $"+strconv.Itoa(n+2),
		append(args, limit, offset)...)
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
