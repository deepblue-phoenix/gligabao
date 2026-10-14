package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
)

// seriesBatch is how many points one INSERT writes (3 parameters each, well under PostgreSQL's 65535).
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

// timePtr is how a nullable time is passed to the driver.
func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func (t *Tx) FindEntity(ctx context.Context, dsID int64, nameKey, typ string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		SELECT id
		FROM gb_entities
		WHERE dataset_id = $1 AND name_key = $2 AND type = $3
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
		WHERE e.dataset_id = $1 AND s.name_key = $2 AND t.name_key = $3 AND e.relation = $4
		ORDER BY (e.expired_at IS NULL) DESC, e.id DESC
		LIMIT 1`, dsID, sourceKey, targetKey, relation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return id, err
}

func (t *Tx) CheckArtifact(ctx context.Context, dsID, id int64) error {
	var one int
	err := t.tx.QueryRowContext(ctx, `SELECT 1 FROM gb_artifacts WHERE dataset_id = $1 AND id = $2`, dsID, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrNotFound
	}
	return err
}

func (t *Tx) UpsertSeries(ctx context.Context, s model.NewSeries) (int64, bool, error) {
	now := s.CreatedAt.UTC()
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		SELECT id FROM gb_series WHERE dataset_id = $1 AND provider = $2 AND key = $3 AND field = $4`,
		s.DatasetID, s.Provider, s.Key, s.Field).Scan(&id)
	switch {
	case err == nil:
		if _, err := t.tx.ExecContext(ctx, `
			UPDATE gb_series SET name = $1, unit = $2, frequency = $3, definition = $4, updated_at = $5
			WHERE id = $6`, s.Name, s.Unit, s.Frequency, s.Definition, now, id); err != nil {
			return 0, false, fmt.Errorf("update series %q: %w", s.Key, err)
		}
		return id, false, nil
	case errors.Is(err, sql.ErrNoRows):
		err := t.tx.QueryRowContext(ctx, `
			INSERT INTO gb_series (dataset_id, key, field, name, unit, frequency, source, provider, definition, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
			RETURNING id`,
			s.DatasetID, s.Key, s.Field, s.Name, s.Unit, s.Frequency, s.Source, s.Provider, s.Definition, now).Scan(&id)
		if err != nil {
			return 0, false, fmt.Errorf("insert series %q: %w", s.Key, err)
		}
		return id, true, nil
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
			fmt.Fprintf(&sb, "($%d,$%d,$%d)", i*3+1, i*3+2, i*3+3)
			args = append(args, seriesID, p.TS.UTC(), p.Value)
		}
		sb.WriteString(` ON CONFLICT (series_id, ts) DO UPDATE SET value = EXCLUDED.value`)
		if _, err := t.tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("write points of series %d: %w", seriesID, err)
		}
	}
	return nil
}

func (t *Tx) ListSeriesPoints(ctx context.Context, seriesID int64) ([]model.SeriesPoint, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT ts, value FROM gb_series_points WHERE series_id = $1 ORDER BY ts`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSeriesPoints(rows)
}

func scanSeriesPoints(rows *sql.Rows) ([]model.SeriesPoint, error) {
	var out []model.SeriesPoint
	for rows.Next() {
		var p model.SeriesPoint
		if err := rows.Scan(&p.TS, &p.Value); err != nil {
			return nil, err
		}
		p.TS = p.TS.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (t *Tx) UpdateSeriesSummary(ctx context.Context, seriesID int64, first, last *time.Time, count int, stats string, now time.Time) error {
	if _, err := t.tx.ExecContext(ctx, `
		UPDATE gb_series SET first_ts = $1, last_ts = $2, point_count = $3, stats = $4, updated_at = $5
		WHERE id = $6`, timePtr(first), timePtr(last), count, stats, now.UTC(), seriesID); err != nil {
		return fmt.Errorf("update summary of series %d: %w", seriesID, err)
	}
	return nil
}

func (t *Tx) AnchorSeries(ctx context.Context, a model.NewSeriesAnchor) error {
	col, err := anchorColumn(a.Type)
	if err != nil {
		return err
	}
	var id int64
	err = t.tx.QueryRowContext(ctx, `SELECT id FROM gb_series_anchors WHERE series_id = $1 AND `+col+` = $2`, a.SeriesID, a.ID).Scan(&id)
	switch {
	case err == nil:
		_, err = t.tx.ExecContext(ctx, `UPDATE gb_series_anchors SET role = $1, window_start = $2, window_end = $3 WHERE id = $4`,
			a.Role, timePtr(a.WindowStart), timePtr(a.WindowEnd), id)
	case errors.Is(err, sql.ErrNoRows):
		_, err = t.tx.ExecContext(ctx, `
			INSERT INTO gb_series_anchors (series_id, `+col+`, role, window_start, window_end, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, a.SeriesID, a.ID, a.Role, timePtr(a.WindowStart), timePtr(a.WindowEnd), a.CreatedAt.UTC())
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
	var first, last sql.NullTime
	var created, updated time.Time
	if err := r.Scan(&s.ID, &s.DatasetID, &s.Key, &s.Field, &s.Name, &s.Unit, &s.Frequency, &s.Source, &s.Provider, &s.Definition,
		&first, &last, &s.PointCount, &s.Stats, &created, &updated); err != nil {
		return s, err
	}
	s.FirstTS, s.LastTS = utc(first), utc(last)
	s.CreatedAt, s.UpdatedAt = created.UTC(), updated.UTC()
	return s, nil
}

// ListSeries returns the series of the dataset by ID.
func ListSeries(ctx context.Context, d *sql.DB, dsID int64) ([]model.Series, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+seriesColumns+` FROM gb_series WHERE dataset_id = $1 ORDER BY id`, dsID)
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
	s, err := scanSeries(d.QueryRowContext(ctx, `SELECT `+seriesColumns+` FROM gb_series WHERE id = $1 AND dataset_id = $2`, id, dsID))
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
	q := `SELECT ts, value FROM gb_series_points WHERE series_id = $1`
	args := []any{seriesID}
	if from != nil {
		args = append(args, from.UTC())
		q += fmt.Sprintf(` AND ts >= $%d`, len(args))
	}
	if to != nil {
		args = append(args, to.UTC())
		q += fmt.Sprintf(` AND ts <= $%d`, len(args))
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
		WHERE series_id = $1
		ORDER BY id`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SeriesAnchor
	for rows.Next() {
		var a model.SeriesAnchor
		var art, ent, edge sql.NullInt64
		var ws, we sql.NullTime
		var created time.Time
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
		a.WindowStart, a.WindowEnd = utc(ws), utc(we)
		a.CreatedAt = created.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}
