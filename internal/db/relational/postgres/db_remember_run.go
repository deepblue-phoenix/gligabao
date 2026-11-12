package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Remember runs (see sqlite/db_remember_run.go and sqlite/sql/schema.sql for what the table and the functions are)

// rememberRunsSchema is the DDL of gb_remember_runs, gb_remember_run_stages and gb_remember_run_stage_outputs. A stage is a row of
// gb_remember_run_stages (its stage_type is the name of the stage), so a new stage needs no change of the schema.
//
//go:embed sql/schema_remember_runs.sql
var rememberRunsSchema string

// rememberCol is a column of gb_query_runs (the wide layout); jsonb ones are written with a cast and read as text.
type rememberCol struct {
	name  string
	jsonb bool
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

// rememberRunColumns are the columns of a run but its ID, in the order of rememberRunValues and scanRememberRun.
var rememberRunColumns = []string{"user_id", "dataset", "kind", "params", "artifact_id", "status", "error", "started_at", "finished_at"}

// rememberStageColumns are the columns of a stage but its run and its type, in the order of stageValues.
var rememberStageColumns = []string{"status", "error", "started_at", "finished_at", "inference_ids"}

// rememberRunValues returns the values of the columns of rememberRunColumns.
func rememberRunValues(r model.RememberRun) []any {
	return []any{r.UserID, r.Dataset, r.Kind, jsonObject(r.Params), r.ArtifactID, r.Status, nullableString(r.Error),
		r.StartedAt, nullableTime(r.FinishedAt)}
}

// stageValues returns the values of the columns of rememberStageColumns.
func stageValues(st model.RememberStage) []any {
	if st.Status == "" {
		st.Status = model.RememberNotStarted
	}
	return []any{st.Status, nullableString(st.Error), nullableTime(st.StartedAt), nullableTime(st.FinishedAt), jsonIDs(st.InferenceIDs)}
}

// saveRememberStages writes every stage of model.RememberStages of the run (inserting or overwriting its row).
func saveRememberStages(ctx context.Context,
	tx *sql.Tx,
	runID int64,
	stages map[string]model.RememberStage) error {
	for _, s := range model.RememberStages {
		args := append([]any{runID, s.Name}, stageValues(stages[s.Name])...)
		if _, err := tx.ExecContext(ctx,
			`
			INSERT INTO gb_remember_run_stages (run_id, stage_type, status, error, started_at, finished_at, inference_ids)
			VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
			ON CONFLICT (run_id, stage_type) DO UPDATE SET
				status        = EXCLUDED.status,
				error         = EXCLUDED.error,
				started_at    = EXCLUDED.started_at,
				finished_at   = EXCLUDED.finished_at,
				inference_ids = EXCLUDED.inference_ids`,
			args...); err != nil {
			return fmt.Errorf("stage %s: %w",
				s.Name,
				err)
		}
	}
	return nil
}

// notStartedStages returns a stage map with every stage of model.RememberStages not started.
func notStartedStages() map[string]model.RememberStage {
	stages := make(map[string]model.RememberStage, len(model.RememberStages))
	for _, s := range model.RememberStages {
		stages[s.Name] = model.RememberStage{
			Status:       model.RememberNotStarted,
			InferenceIDs: []int64{},
		}
	}
	return stages
}

// stageRow holds the columns of one stage in the wide layout of gb_query_runs, as they are scanned (see db_query_run.go).
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
	if err := sc.Scan(&r.ID,
		&r.UserID,
		&r.Dataset,
		&r.Kind,
		&params,
		&artifact,
		&r.Status,
		&errText,
		&r.StartedAt,
		&finished); err != nil {
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
	r.Stages = notStartedStages()
	return &r, nil
}

// loadRememberStages fills in the stages of the runs (their Stages map) from gb_remember_run_stages. A run without a row for a
// stage keeps it not started.
func loadRememberStages(ctx context.Context,
	d *sql.DB,
	runs []model.RememberRun) error {
	if len(runs) == 0 {
		return nil
	}
	byID := make(map[int64]*model.RememberRun, len(runs))
	args := make([]any,
		0,
		len(runs))
	marks := make([]string,
		0,
		len(runs))
	for i := range runs {
		byID[runs[i].ID] = &runs[i]
		args = append(args, runs[i].ID)
		marks = append(marks, "$"+strconv.Itoa(i+1))
	}
	rows, err := d.QueryContext(ctx,
		`SELECT run_id, stage_type, status, error, started_at, finished_at, inference_ids::text
		FROM gb_remember_run_stages WHERE run_id IN (`+strings.Join(marks, ", ")+`)`,
		args...)
	if err != nil {
		return fmt.Errorf("load remember stages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID int64
		var name, status, ids string
		var errText sql.NullString
		var started, finished sql.NullTime
		if err := rows.Scan(&runID,
			&name,
			&status,
			&errText,
			&started,
			&finished,
			&ids); err != nil {
			return fmt.Errorf("load remember stages: %w", err)
		}
		r := byID[runID]
		if r == nil {
			continue
		}
		st := model.RememberStage{
			Status:       status,
			StartedAt:    utc(started),
			FinishedAt:   utc(finished),
			InferenceIDs: parseIDs(ids),
		}
		if errText.Valid {
			st.Error = &errText.String
		}
		r.Stages[name] = st
	}
	return rows.Err()
}

func rememberRunSelect() string {
	return "SELECT id, user_id, dataset, kind, params::text, artifact_id, status, error, started_at, finished_at FROM gb_remember_runs"
}

// InsertRememberRun stores a remember run with its stages (r.ID is ignored) and returns its ID.
func InsertRememberRun(ctx context.Context,
	d *sql.DB,
	r model.RememberRun) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	defer tx.Rollback()
	marks := make([]string, len(rememberRunColumns))
	for i, c := range rememberRunColumns {
		marks[i] = rememberCol{c, c == "params"}.placeholder(i + 1)
	}
	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO gb_remember_runs (`+strings.Join(rememberRunColumns, ", ")+`) VALUES (`+strings.Join(marks, ", ")+`) RETURNING id`,
		rememberRunValues(r)...).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	if err := saveRememberStages(ctx,
		tx,
		id,
		r.Stages); err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	return id, nil
}

// SaveRememberRun overwrites the stored remember run r.ID with r (the run and its stages). It returns model.ErrNotFound if there is
// none.
func SaveRememberRun(ctx context.Context,
	d *sql.DB,
	r model.RememberRun) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save remember run: %w", err)
	}
	defer tx.Rollback()
	sets := make([]string, len(rememberRunColumns))
	for i, c := range rememberRunColumns {
		sets[i] = c + " = " + rememberCol{c, c == "params"}.placeholder(i+1)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE gb_remember_runs SET `+strings.Join(sets, ", ")+` WHERE id = $`+strconv.Itoa(len(rememberRunColumns)+1),
		append(rememberRunValues(r), r.ID)...)
	if err != nil {
		return fmt.Errorf("save remember run: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return model.ErrNotFound
	}
	if err := saveRememberStages(ctx,
		tx,
		r.ID,
		r.Stages); err != nil {
		return fmt.Errorf("save remember run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save remember run: %w", err)
	}
	return nil
}

// GetRememberRun returns the remember run with that ID, or model.ErrNotFound.
func GetRememberRun(ctx context.Context,
	d *sql.DB,
	id int64) (*model.RememberRun, error) {
	r, err := scanRememberRun(d.QueryRowContext(ctx,
		rememberRunSelect()+" WHERE id = $1",
		id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	runs := []model.RememberRun{*r}
	if err := loadRememberStages(ctx,
		d,
		runs); err != nil {
		return nil, err
	}
	return &runs[0], nil
}

// ListRememberRuns returns the remember runs that match f, newest first (at most limit, after skipping offset), and how many
// match f in all.
func ListRememberRuns(ctx context.Context,
	d *sql.DB,
	f model.RememberRunFilter,
	limit,
	offset int) ([]model.RememberRun, int, error) {
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
	if err := d.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM gb_remember_runs"+where,
		args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	n := len(args)
	rows, err := d.QueryContext(ctx,
		rememberRunSelect()+where+" ORDER BY id DESC LIMIT $"+strconv.Itoa(n+1)+" OFFSET $"+strconv.Itoa(n+2),
		append(args,
			limit,
			offset)...)
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
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close() // the stages are read on the same connection pool
	if err := loadRememberStages(ctx,
		d,
		out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

//---------------------------------------------------------------------------------------------
// Stage outputs (gb_remember_run_stage_outputs)

// InsertRememberStageOutput stores an output of a stage of a run (o.ID is ignored) and returns its ID.
func InsertRememberStageOutput(ctx context.Context,
	d *sql.DB,
	o model.RememberStageOutput) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx,
		`INSERT INTO gb_remember_run_stage_outputs (run_id, stage, name, label, output, created_at) VALUES ($1, $2, $3, $4, $5::jsonb, $6) RETURNING id`,
		o.RunID,
		o.Stage,
		o.Name,
		o.Label,
		string(o.Output),
		o.CreatedAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert remember stage output: %w", err)
	}
	return id, nil
}

// ListRememberStageOutputs returns the outputs of the run, in the order they were made; only those of the stage if it is not "".
func ListRememberStageOutputs(ctx context.Context,
	d *sql.DB,
	runID int64,
	stage string) ([]model.RememberStageOutput, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT id, run_id, stage, name, label, output::text, created_at FROM gb_remember_run_stage_outputs WHERE run_id = $1 AND ($2 = '' OR stage = $2) ORDER BY id`,
		runID,
		stage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.RememberStageOutput{}
	for rows.Next() {
		var o model.RememberStageOutput
		var output string
		if err := rows.Scan(&o.ID,
			&o.RunID,
			&o.Stage,
			&o.Name,
			&o.Label,
			&output,
			&o.CreatedAt); err != nil {
			return nil, err
		}
		o.Output, o.CreatedAt = []byte(output), o.CreatedAt.UTC()
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeleteRememberStageOutputs deletes the outputs made before the time (all of them if it is zero) and returns how many.
func DeleteRememberStageOutputs(ctx context.Context,
	d *sql.DB,
	before time.Time) (int64, error) {
	var res sql.Result
	var err error
	if before.IsZero() {
		res, err = d.ExecContext(ctx, `DELETE FROM gb_remember_run_stage_outputs`)
	} else {
		res, err = d.ExecContext(ctx,
			`DELETE FROM gb_remember_run_stage_outputs WHERE created_at < $1`,
			before)
	}
	if err != nil {
		return 0, fmt.Errorf("delete remember stage outputs: %w", err)
	}
	return res.RowsAffected()
}
