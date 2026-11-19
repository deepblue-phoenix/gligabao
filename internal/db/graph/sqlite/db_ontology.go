package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// Ontology grounding (gb_ontology_events)

func (t *Tx) InsertOntologyEvents(ctx context.Context,
	events []model.OntologyEvent,
	createdAt time.Time) error {
	for _, e := range events {
		detail := string(e.Detail)
		if detail == "" {
			detail = "{}"
		}
		if _, err := t.tx.ExecContext(ctx,
			`
			INSERT INTO gb_ontology_events (
				dataset_id, artifact_id, run_id, ontology_id, ontology_version, mode, subject_kind, name, type, class,
				outcome, reason, detail, entity_id, edge_id, created_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.DatasetID,
			e.ArtifactID,
			nullIfZero(e.RunID),
			e.OntologyID,
			e.OntologyVersion,
			e.Mode,
			e.SubjectKind,
			e.Name,
			e.Type,
			e.Class,
			e.Outcome,
			e.Reason,
			detail,
			nullIfZero(e.EntityID),
			nullIfZero(e.EdgeID),
			rsqlite.FormatTime(createdAt)); err != nil {
			return fmt.Errorf("insert ontology event: %w", err)
		}
	}
	return nil
}

// ListOntologyEvents returns the events of the ontology, newest first.
func ListOntologyEvents(ctx context.Context,
	d *sql.DB,
	ontologyID int64,
	f model.OntologyEventFilter) ([]model.OntologyEvent, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx,
		`
		SELECT id, dataset_id, artifact_id, COALESCE(run_id, 0), ontology_id, ontology_version, mode, subject_kind, name, type, class,
			outcome, reason, detail, COALESCE(entity_id, 0), COALESCE(edge_id, 0), created_at
		FROM gb_ontology_events
		WHERE ontology_id = ?
			AND (? = 0 OR artifact_id = ?)
			AND (? = '' OR outcome = ?)
		ORDER BY id DESC
		LIMIT ?`,
		ontologyID,
		f.ArtifactID,
		f.ArtifactID,
		f.Outcome,
		f.Outcome,
		limit)
	if err != nil {
		return nil, fmt.Errorf("list ontology events: %w", err)
	}
	defer rows.Close()
	out := []model.OntologyEvent{}
	for rows.Next() {
		var e model.OntologyEvent
		var detail, created string
		if err := rows.Scan(&e.ID,
			&e.DatasetID,
			&e.ArtifactID,
			&e.RunID,
			&e.OntologyID,
			&e.OntologyVersion,
			&e.Mode,
			&e.SubjectKind,
			&e.Name,
			&e.Type,
			&e.Class,
			&e.Outcome,
			&e.Reason,
			&detail,
			&e.EntityID,
			&e.EdgeID,
			&created); err != nil {
			return nil, err
		}
		e.Detail = []byte(detail)
		if ts := rsqlite.ParseTime(created); ts != nil {
			e.CreatedAt = *ts
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// groundingColumns are the values of the ontology columns of a row: NULLs for a row that was not grounded.
func groundingColumns(g *model.Grounding) (version, class, valid, origin any) {
	if g == nil {
		return nil, nil, nil, nil
	}
	return g.Version, nullIfEmpty(g.Class), g.Valid, nullIfEmpty(g.OriginType)
}
