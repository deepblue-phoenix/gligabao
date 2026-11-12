package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

// CountInContext writes a row to gb_graph_in_ctx_counters for every entity and edge given, as described by ev (see
// model.InContextEvent). An ID that is not in the database (a vector can outlive its entity) is skipped, and a repeated ID gets one row.
func CountInContext(ctx context.Context,
	d *sql.DB,
	ev model.InContextEvent,
	entityIDs,
	edgeIDs []int64) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("count in context: %w", err)
	}
	defer tx.Rollback()
	now := rsqlite.FormatTime(rsqlite.Now())
	for _, c := range []struct {
		kind, table string
		ids         []int64
	}{{model.CounterEntity, "gb_entities", entityIDs}, {model.CounterEdge, "gb_edges", edgeIDs}} {
		ids := distinct(c.ids)
		if len(ids) == 0 {
			continue
		}
		ph, args := placeholders(ids)
		if _, err := tx.ExecContext(ctx,
			`
			INSERT INTO gb_graph_in_ctx_counters (dataset_id, kind, item_id, pipeline, stage, run_id, created_at)
			SELECT dataset_id, ?, id, ?, ?, ?, ? FROM `+c.table+` WHERE id IN (`+ph+`)`,
			append([]any{c.kind, ev.Pipeline, ev.Stage, nullIfZero(ev.RunID), now}, args...)...); err != nil {
			return fmt.Errorf("count %ss in context: %w",
				c.kind,
				err)
		}
	}
	return tx.Commit()
}

// distinct returns ids without the repeats, in order.
func distinct(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64,
		0,
		len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ListInContextEvents returns the in-context events of the dataset, newest first.
func ListInContextEvents(ctx context.Context,
	d *sql.DB,
	dsID int64) ([]model.InContextEvent, error) {
	rows, err := d.QueryContext(ctx,
		`
		SELECT id, dataset_id, kind, item_id, pipeline, stage, COALESCE(run_id, 0), created_at
		FROM gb_graph_in_ctx_counters
		WHERE dataset_id = ?
		ORDER BY id DESC`,
		dsID)
	if err != nil {
		return nil, fmt.Errorf("list in-context events: %w", err)
	}
	defer rows.Close()
	out := []model.InContextEvent{}
	for rows.Next() {
		var e model.InContextEvent
		var created string
		if err := rows.Scan(&e.ID,
			&e.DatasetID,
			&e.Kind,
			&e.ItemID,
			&e.Pipeline,
			&e.Stage,
			&e.RunID,
			&created); err != nil {
			return nil, err
		}
		if t := rsqlite.ParseTime(created); t != nil {
			e.CreatedAt = *t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// InContextCounts returns how many times each entity and edge of the dataset went into a context, per pipeline (see
// model.InContextCount); items that never did are not listed.
func InContextCounts(ctx context.Context,
	d *sql.DB,
	dsID int64) ([]model.InContextCount, error) {
	rows, err := d.QueryContext(ctx,
		`
		SELECT kind, item_id,
			COUNT(CASE WHEN pipeline = 'query' THEN 1 END),
			COUNT(CASE WHEN pipeline = 'remember' THEN 1 END)
		FROM gb_graph_in_ctx_counters
		WHERE dataset_id = ?
		GROUP BY kind, item_id
		ORDER BY kind, item_id`,
		dsID)
	if err != nil {
		return nil, fmt.Errorf("count in-context events: %w", err)
	}
	defer rows.Close()
	out := []model.InContextCount{}
	for rows.Next() {
		var c model.InContextCount
		if err := rows.Scan(&c.Kind,
			&c.ItemID,
			&c.Query,
			&c.Remember); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
