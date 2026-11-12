package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Query runs (gb_query_runs, see sql/schema.sql)

// queryRunsSchema is the DDL of gb_query_runs: the columns of the run itself, then those of each stage in model.QueryStages. The stage
// columns are written out in the file; schema_test.go checks that they match model.QueryStages.
//
//go:embed sql/schema_query_runs.sql
var queryRunsSchema string

// queryRunColumns are the columns of a run but its ID, in the order of queryRunValues and scanQueryRun.
func queryRunColumns() []string {
	cols := []string{"user_id", "dataset", "question", "params", "session_id", "status", "error", "started_at", "finished_at"}
	for _, s := range model.QueryStages {
		cols = append(cols,
			s.Name+"_status",
			s.Name+"_error",
			s.Name+"_started_at",
			s.Name+"_finished_at")
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
		vals = append(vals,
			st.Status,
			nullableString(st.Error),
			FormatTimePtr(st.StartedAt),
			FormatTimePtr(st.FinishedAt))
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
		dest = append(dest,
			&stages[i].status,
			&stages[i].err,
			&stages[i].started,
			&stages[i].finished)
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
		st := model.QueryStage{
			Status:       stages[i].status,
			StartedAt:    ParseTime(stages[i].started.String),
			FinishedAt:   ParseTime(stages[i].finished.String),
			InferenceIDs: parseIDs(stages[i].runIDs),
		}
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
func InsertQueryRun(ctx context.Context,
	d *sql.DB,
	r model.QueryRun) (int64, error) {
	cols := queryRunColumns()
	res, err := d.ExecContext(ctx,
		`INSERT INTO gb_query_runs (`+strings.Join(cols, ", ")+`) VALUES (?`+strings.Repeat(", ?", len(cols)-1)+`)`,
		queryRunValues(r)...)
	if err != nil {
		return 0, fmt.Errorf("insert query run: %w", err)
	}
	return res.LastInsertId()
}

// SaveQueryRun overwrites the stored query run r.ID with r. It returns model.ErrNotFound if there is none.
func SaveQueryRun(ctx context.Context,
	d *sql.DB,
	r model.QueryRun) error {
	cols := queryRunColumns()
	res, err := d.ExecContext(ctx,
		`UPDATE gb_query_runs SET `+strings.Join(cols, " = ?, ")+` = ? WHERE id = ?`,
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
func GetQueryRun(ctx context.Context,
	d *sql.DB,
	id int64) (*model.QueryRun, error) {
	r, err := scanQueryRun(d.QueryRowContext(ctx,
		queryRunSelect()+" WHERE id = ?",
		id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return r, err
}

// ListQueryRuns returns the query runs that match f, newest first (at most limit, after skipping offset), and how many match f
// in all.
func ListQueryRuns(ctx context.Context,
	d *sql.DB,
	f model.QueryRunFilter,
	limit,
	offset int) ([]model.QueryRun, int, error) {
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
	if err := d.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM gb_query_runs"+where,
		args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.QueryContext(ctx,
		queryRunSelect()+where+" ORDER BY id DESC LIMIT ? OFFSET ?",
		append(args,
			limit,
			offset)...)
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
