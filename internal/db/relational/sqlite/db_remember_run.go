package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Remember runs (gb_remember_runs and gb_remember_run_stages, see sql/schema_remember_runs.sql)

// rememberRunsSchema is the DDL of gb_remember_runs, gb_remember_run_stages and gb_remember_run_stage_outputs. A stage is a row of
// gb_remember_run_stages (its stage_type is the name of the stage), so a new stage needs no change of the schema.
//
//go:embed sql/schema_remember_runs.sql
var rememberRunsSchema string

// stageRow holds the columns of one stage in the wide layout of gb_query_runs, as they are scanned (see db_query_run.go).
type stageRow struct {
	status                 string
	err, started, finished sql.NullString
	runIDs                 string
}

// rememberRunColumns are the columns of a run but its ID, in the order of rememberRunValues and scanRememberRun.
var rememberRunColumns = []string{"user_id", "dataset", "kind", "params", "artifact_id", "status", "error", "started_at", "finished_at"}

// rememberStageColumns are the columns of a stage but its run and its type, in the order of stageValues and scanStage.
var rememberStageColumns = []string{"status", "error", "started_at", "finished_at", "inference_ids"}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// rememberRunValues returns the values of the columns of rememberRunColumns.
func rememberRunValues(r model.RememberRun) []any {
	return []any{r.UserID, r.Dataset, r.Kind, jsonObject(r.Params), r.ArtifactID, r.Status, nullableString(r.Error),
		FormatTime(r.StartedAt), FormatTimePtr(r.FinishedAt)}
}

// stageValues returns the values of the columns of rememberStageColumns.
func stageValues(st model.RememberStage) []any {
	if st.Status == "" {
		st.Status = model.RememberNotStarted
	}
	return []any{st.Status, nullableString(st.Error), FormatTimePtr(st.StartedAt), FormatTimePtr(st.FinishedAt), jsonIDs(st.InferenceIDs)}
}

// saveRememberStages writes every stage of model.RememberStages of the run (inserting or overwriting its row).
func saveRememberStages(ctx context.Context, tx *sql.Tx, runID int64, stages map[string]model.RememberStage) error {
	for _, s := range model.RememberStages {
		args := append([]any{runID, s.Name}, stageValues(stages[s.Name])...)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_remember_run_stages (run_id, stage_type, status, error, started_at, finished_at, inference_ids)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (run_id, stage_type) DO UPDATE SET
				status        = excluded.status,
				error         = excluded.error,
				started_at    = excluded.started_at,
				finished_at   = excluded.finished_at,
				inference_ids = excluded.inference_ids`, args...); err != nil {
			return fmt.Errorf("stage %s: %w", s.Name, err)
		}
	}
	return nil
}

// notStartedStages returns a stage map with every stage of model.RememberStages not started.
func notStartedStages() map[string]model.RememberStage {
	stages := make(map[string]model.RememberStage, len(model.RememberStages))
	for _, s := range model.RememberStages {
		stages[s.Name] = model.RememberStage{Status: model.RememberNotStarted, InferenceIDs: []int64{}}
	}
	return stages
}

func scanRememberRun(sc scanner) (*model.RememberRun, error) {
	var r model.RememberRun
	var params, started string
	var artifact sql.NullInt64
	var errText, finished sql.NullString
	if err := sc.Scan(&r.ID, &r.UserID, &r.Dataset, &r.Kind, &params, &artifact, &r.Status, &errText, &started, &finished); err != nil {
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
	r.Stages = notStartedStages()
	return &r, nil
}

// loadRememberStages fills in the stages of the runs (their Stages map) from gb_remember_run_stages. A run without a row for a
// stage keeps it not started.
func loadRememberStages(ctx context.Context, d *sql.DB, runs []model.RememberRun) error {
	if len(runs) == 0 {
		return nil
	}
	byID := make(map[int64]*model.RememberRun, len(runs))
	args := make([]any, 0, len(runs))
	marks := make([]string, 0, len(runs))
	for i := range runs {
		byID[runs[i].ID] = &runs[i]
		args = append(args, runs[i].ID)
		marks = append(marks, "?")
	}
	rows, err := d.QueryContext(ctx, `SELECT run_id, stage_type, status, error, started_at, finished_at, inference_ids
		FROM gb_remember_run_stages WHERE run_id IN (`+strings.Join(marks, ", ")+`)`, args...)
	if err != nil {
		return fmt.Errorf("load remember stages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID int64
		var name, status, ids string
		var errText, started, finished sql.NullString
		if err := rows.Scan(&runID, &name, &status, &errText, &started, &finished, &ids); err != nil {
			return fmt.Errorf("load remember stages: %w", err)
		}
		r := byID[runID]
		if r == nil {
			continue
		}
		st := model.RememberStage{Status: status, StartedAt: ParseTime(started.String), FinishedAt: ParseTime(finished.String),
			InferenceIDs: parseIDs(ids)}
		if errText.Valid {
			st.Error = &errText.String
		}
		r.Stages[name] = st
	}
	return rows.Err()
}

func rememberRunSelect() string {
	return "SELECT id, " + strings.Join(rememberRunColumns, ", ") + " FROM gb_remember_runs"
}

// InsertRememberRun stores a remember run with its stages (r.ID is ignored) and returns its ID.
func InsertRememberRun(ctx context.Context, d *sql.DB, r model.RememberRun) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO gb_remember_runs (`+strings.Join(rememberRunColumns, ", ")+`) VALUES (?`+strings.Repeat(", ?", len(rememberRunColumns)-1)+`)`,
		rememberRunValues(r)...)
	if err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	if err := saveRememberStages(ctx, tx, id, r.Stages); err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("insert remember run: %w", err)
	}
	return id, nil
}

// SaveRememberRun overwrites the stored remember run r.ID with r (the run and its stages). It returns model.ErrNotFound if there is
// none.
func SaveRememberRun(ctx context.Context, d *sql.DB, r model.RememberRun) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save remember run: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE gb_remember_runs SET `+strings.Join(rememberRunColumns, " = ?, ")+` = ? WHERE id = ?`,
		append(rememberRunValues(r), r.ID)...)
	if err != nil {
		return fmt.Errorf("save remember run: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return model.ErrNotFound
	}
	if err := saveRememberStages(ctx, tx, r.ID, r.Stages); err != nil {
		return fmt.Errorf("save remember run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save remember run: %w", err)
	}
	return nil
}

// GetRememberRun returns the remember run with that ID, or model.ErrNotFound.
func GetRememberRun(ctx context.Context, d *sql.DB, id int64) (*model.RememberRun, error) {
	r, err := scanRememberRun(d.QueryRowContext(ctx, rememberRunSelect()+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	runs := []model.RememberRun{*r}
	if err := loadRememberStages(ctx, d, runs); err != nil {
		return nil, err
	}
	return &runs[0], nil
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
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close() // the stages are read on the same connection pool
	if err := loadRememberStages(ctx, d, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
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
