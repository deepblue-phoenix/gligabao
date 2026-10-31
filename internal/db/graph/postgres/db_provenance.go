package postgres

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
)

// Provenance by session: what a session (a user or agent, e.g. a research agent's "research-<run id>") put in the knowledge base.

// ListArtifactsBySessions returns the artifacts, of any dataset, whose session_id is one of sessionIDs, by ID.
func ListArtifactsBySessions(ctx context.Context, d *sql.DB, sessionIDs []string) ([]model.Artifact, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT`+artifactColumns+`
		FROM gb_artifacts a
		WHERE a.session_id = ANY($1)
		ORDER BY a.id`, sessionIDs)
	if err != nil {
		return nil, err
	}
	return scanArtifacts(rows)
}

// ListEntitiesBySessions returns the entities discovered in one of sessionIDs, by ID, with the sessions (of those) each was discovered in.
func ListEntitiesBySessions(ctx context.Context, d *sql.DB, sessionIDs []string) ([]model.SessionEntity, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT e.id, e.dataset_id, e.name, e.type, e.description, COALESCE(e.model_id, ''), es.session_id
		FROM gb_entity_sessions es
		JOIN gb_entities e ON e.id = es.entity_id
		WHERE es.session_id = ANY($1)
		ORDER BY e.id, es.session_id`, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SessionEntity
	for rows.Next() {
		var e model.SessionEntity
		var session string
		if err := rows.Scan(&e.ID, &e.DatasetID, &e.Name, &e.Type, &e.Description, &e.ModelID, &session); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].ID == e.ID {
			out[n-1].Sessions = append(out[n-1].Sessions, session)
			continue
		}
		e.Sessions = []string{session}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListEdgesBySessions returns the edges discovered in one of sessionIDs, by ID, with the sessions (of those) each was discovered in.
func ListEdgesBySessions(ctx context.Context, d *sql.DB, sessionIDs []string) ([]model.SessionEdge, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT
			e.id, e.dataset_id, e.source_id, e.target_id, s.name, t.name, e.relation, e.description, e.artifact_id,
			e.valid_at, e.invalid_at, e.created_at, e.expired_at, COALESCE(e.model_id, ''), es.session_id
		FROM gb_edge_sessions es
		JOIN gb_edges e ON e.id = es.edge_id
		JOIN gb_entities s ON s.id = e.source_id
		JOIN gb_entities t ON t.id = e.target_id
		WHERE es.session_id = ANY($1)
		ORDER BY e.id, es.session_id`, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SessionEdge
	for rows.Next() {
		var e model.SessionEdge
		var mem sql.NullInt64
		var valid, invalid, created, expired sql.NullTime
		var session string
		if err := rows.Scan(&e.ID, &e.DatasetID, &e.SourceID, &e.TargetID, &e.Source, &e.Target, &e.Relation, &e.Description, &mem,
			&valid, &invalid, &created, &expired, &e.ModelID, &session); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].ID == e.ID {
			out[n-1].Sessions = append(out[n-1].Sessions, session)
			continue
		}
		e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = utc(valid), utc(invalid), utc(created), utc(expired)
		if mem.Valid {
			e.ArtifactID = &mem.Int64
		}
		e.Sessions = []string{session}
		out = append(out, e)
	}
	return out, rows.Err()
}
