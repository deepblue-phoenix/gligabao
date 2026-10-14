package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

// seriesBatch is how many points one INSERT writes (3 parameters each, well under SQLite's limit).
const seriesBatch = 1000

// anchorColumn is the column of gb_series_anchors for an anchor type.
func anchorColumn(typ string) (string, error) {
	switch typ {
	case model.AnchorArtifact:
		return "artifact_id", nil
	case model.AnchorEntity:
		return "entity_id", nil
	case model.AnchorEdge:
		return "edge_id", nil
	}
	return "", fmt.Errorf("unknown series anchor type %q", typ)
}

func (t *Tx) FindEntity(ctx context.Context, dsID int64, nameKey, typ string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		SELECT id
		FROM gb_entities
		WHERE dataset_id = ? AND name_key = ? AND type = ?
		ORDER BY id
		LIMIT 1`, dsID, nameKey, typ).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return id, err
}

func (t *Tx) FindEdgeID(ctx context.Context, dsID int64, sourceKey, relation, targetKey string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		SELECT e.id
		FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id
		JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id = ? AND s.name_key = ? AND t.name_key = ? AND e.relation = ?
		ORDER BY (e.expired_at IS NULL) DESC, e.id DESC
		LIMIT 1`, dsID, sourceKey, targetKey, relation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return id, err
}

func (t *Tx) CheckArtifact(ctx context.Context, dsID, id int64) error {
	var one int
	err := t.tx.QueryRowContext(ctx, `SELECT 1 FROM gb_artifacts WHERE dataset_id = ? AND id = ?`, dsID, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrNotFound
	}
	return err
}

func (t *Tx) UpsertSeries(ctx context.Context, s model.NewSeries) (int64, bool, error) {
	now := rsqlite.FormatTime(s.CreatedAt)
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		SELECT id FROM gb_series WHERE dataset_id = ? AND provider = ? AND key = ? AND field = ?`,
		s.DatasetID, s.Provider, s.Key, s.Field).Scan(&id)
	switch {
	case err == nil:
		if _, err := t.tx.ExecContext(ctx, `
			UPDATE gb_series SET name = ?, unit = ?, frequency = ?, definition = ?, updated_at = ?
			WHERE id = ?`, s.Name, s.Unit, s.Frequency, s.Definition, now, id); err != nil {
			return 0, false, fmt.Errorf("update series %q: %w", s.Key, err)
		}
		return id, false, nil
	case errors.Is(err, sql.ErrNoRows):
		res, err := t.tx.ExecContext(ctx, `
			INSERT INTO gb_series (dataset_id, key, field, name, unit, frequency, source, provider, definition, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.DatasetID, s.Key, s.Field, s.Name, s.Unit, s.Frequency, s.Source, s.Provider, s.Definition, now, now)
		if err != nil {
			return 0, false, fmt.Errorf("insert series %q: %w", s.Key, err)
		}
		id, err = res.LastInsertId()
		return id, true, err
	}
	return 0, false, err
}

func (t *Tx) UpsertSeriesPoints(ctx context.Context, seriesID int64, pts []model.SeriesPoint) error {
	for start := 0; start < len(pts); start += seriesBatch {
		batch := pts[start:min(start+seriesBatch, len(pts))]
		var sb strings.Builder
		sb.WriteString(`INSERT INTO gb_series_points (series_id, ts, value) VALUES `)
		args := make([]any, 0, len(batch)*3)
		for i, p := range batch {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?,?)")
			args = append(args, seriesID, rsqlite.FormatTime(p.TS), p.Value)
		}
		sb.WriteString(` ON CONFLICT (series_id, ts) DO UPDATE SET value = excluded.value`)
		if _, err := t.tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("write points of series %d: %w", seriesID, err)
		}
	}
	return nil
}

func (t *Tx) ListSeriesPoints(ctx context.Context, seriesID int64) ([]model.SeriesPoint, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT ts, value FROM gb_series_points WHERE series_id = ? ORDER BY ts`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSeriesPoints(rows)
}

func scanSeriesPoints(rows *sql.Rows) ([]model.SeriesPoint, error) {
	var out []model.SeriesPoint
	for rows.Next() {
		var ts string
		var p model.SeriesPoint
		if err := rows.Scan(&ts, &p.Value); err != nil {
			return nil, err
		}
		if t := rsqlite.ParseTime(ts); t != nil {
			p.TS = *t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (t *Tx) UpdateSeriesSummary(ctx context.Context, seriesID int64, first, last *time.Time, count int, stats string, now time.Time) error {
	if _, err := t.tx.ExecContext(ctx, `
		UPDATE gb_series SET first_ts = ?, last_ts = ?, point_count = ?, stats = ?, updated_at = ?
		WHERE id = ?`, rsqlite.FormatTimePtr(first), rsqlite.FormatTimePtr(last), count, stats, rsqlite.FormatTime(now), seriesID); err != nil {
		return fmt.Errorf("update summary of series %d: %w", seriesID, err)
	}
	return nil
}

func (t *Tx) AnchorSeries(ctx context.Context, a model.NewSeriesAnchor) error {
	col, err := anchorColumn(a.Type)
	if err != nil {
		return err
	}
	ws, we := rsqlite.FormatTimePtr(a.WindowStart), rsqlite.FormatTimePtr(a.WindowEnd)
	var id int64
	err = t.tx.QueryRowContext(ctx, `SELECT id FROM gb_series_anchors WHERE series_id = ? AND `+col+` = ?`, a.SeriesID, a.ID).Scan(&id)
	switch {
	case err == nil:
		_, err = t.tx.ExecContext(ctx, `UPDATE gb_series_anchors SET role = ?, window_start = ?, window_end = ? WHERE id = ?`, a.Role, ws, we, id)
	case errors.Is(err, sql.ErrNoRows):
		_, err = t.tx.ExecContext(ctx, `
			INSERT INTO gb_series_anchors (series_id, `+col+`, role, window_start, window_end, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, a.SeriesID, a.ID, a.Role, ws, we, rsqlite.FormatTime(a.CreatedAt))
	}
	if err != nil {
		return fmt.Errorf("anchor series %d to %s %d: %w", a.SeriesID, a.Type, a.ID, err)
	}
	return nil
}

//---------------------------------------------------------------------------------------------
// Reading series

const seriesColumns = `id, dataset_id, key, field, name, unit, frequency, source, provider, definition,
	first_ts, last_ts, point_count, stats, created_at, updated_at`

func scanSeries(r scanner) (model.Series, error) {
	var s model.Series
	var first, last sql.NullString
	var created, updated string
	if err := r.Scan(&s.ID, &s.DatasetID, &s.Key, &s.Field, &s.Name, &s.Unit, &s.Frequency, &s.Source, &s.Provider, &s.Definition,
		&first, &last, &s.PointCount, &s.Stats, &created, &updated); err != nil {
		return s, err
	}
	s.FirstTS, s.LastTS = rsqlite.ParseTime(first.String), rsqlite.ParseTime(last.String)
	if t := rsqlite.ParseTime(created); t != nil {
		s.CreatedAt = *t
	}
	if t := rsqlite.ParseTime(updated); t != nil {
		s.UpdatedAt = *t
	}
	return s, nil
}

// ListSeries returns the series of the dataset by ID.
func ListSeries(ctx context.Context, d *sql.DB, dsID int64) ([]model.Series, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+seriesColumns+` FROM gb_series WHERE dataset_id = ? ORDER BY id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Series
	for rows.Next() {
		s, err := scanSeries(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSeries returns one series of the dataset.
func GetSeries(ctx context.Context, d *sql.DB, dsID, id int64) (*model.Series, error) {
	s, err := scanSeries(d.QueryRowContext(ctx, `SELECT `+seriesColumns+` FROM gb_series WHERE id = ? AND dataset_id = ?`, id, dsID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("series %d: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SeriesPoints returns the points of the series between from and to (both inclusive, nil for no bound), oldest first,
// at most limit (0 for all).
func SeriesPoints(ctx context.Context, d *sql.DB, seriesID int64, from, to *time.Time, limit int) ([]model.SeriesPoint, error) {
	q := `SELECT ts, value FROM gb_series_points WHERE series_id = ?`
	args := []any{seriesID}
	if from != nil {
		q += ` AND ts >= ?`
		args = append(args, rsqlite.FormatTime(*from))
	}
	if to != nil {
		q += ` AND ts <= ?`
		args = append(args, rsqlite.FormatTime(*to))
	}
	q += ` ORDER BY ts`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSeriesPoints(rows)
}

// ListSeriesAnchors returns the anchors of the series by ID.
func ListSeriesAnchors(ctx context.Context, d *sql.DB, seriesID int64) ([]model.SeriesAnchor, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, series_id, artifact_id, entity_id, edge_id, role, window_start, window_end, created_at
		FROM gb_series_anchors
		WHERE series_id = ?
		ORDER BY id`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SeriesAnchor
	for rows.Next() {
		var a model.SeriesAnchor
		var art, ent, edge sql.NullInt64
		var ws, we sql.NullString
		var created string
		if err := rows.Scan(&a.ID, &a.SeriesID, &art, &ent, &edge, &a.Role, &ws, &we, &created); err != nil {
			return nil, err
		}
		switch {
		case art.Valid:
			a.Type, a.TargetID = model.AnchorArtifact, art.Int64
		case ent.Valid:
			a.Type, a.TargetID = model.AnchorEntity, ent.Int64
		case edge.Valid:
			a.Type, a.TargetID = model.AnchorEdge, edge.Int64
		}
		a.WindowStart, a.WindowEnd = rsqlite.ParseTime(ws.String), rsqlite.ParseTime(we.String)
		if t := rsqlite.ParseTime(created); t != nil {
			a.CreatedAt = *t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
