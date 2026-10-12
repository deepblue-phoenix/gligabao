// Package postgres stores the knowledge graph (artifacts, entities, edges) in PostgreSQL tables and runs every SQL
// statement for it. The functions here are reached through the routing functions of package graph and mirror those
// of package sqlite, in the PostgreSQL dialect ($n placeholders, RETURNING, native types).
//
// The tables live in the same database as the relational data (they reference its datasets), so this package
// works on the *sql.DB opened by internal/db/relational/postgres.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
	rpostgres "github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
)

//---------------------------------------------------------------------------------------------

// Open applies the graph schema to d, a database opened by relational/postgres.Open.
func Open(d *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return initSchema(ctx, d)
}

// args collects query parameters and hands out their $n placeholders.
type args []any

func (a *args) add(v any) string {
	*a = append(*a, v)
	return fmt.Sprintf("$%d", len(*a))
}

type scanner interface{ Scan(dest ...any) error }

func utc(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time.UTC()
	return &t
}

//---------------------------------------------------------------------------------------------
// Edges

const edgeColumns = `
	e.id, s.name, t.name, e.relation, e.description,
	e.valid_at, e.invalid_at, e.created_at, e.expired_at`
const edgeFrom = `
	FROM gb_edges e
	JOIN gb_entities s ON s.id = e.source_id
	JOIN gb_entities t ON t.id = e.target_id`

func scanEdge(r scanner) (model.Edge, error) {
	var e model.Edge
	var valid, invalid, created, expired sql.NullTime
	if err := r.Scan(&e.ID, &e.Source, &e.Target, &e.Relation, &e.Description, &valid, &invalid, &created, &expired); err != nil {
		return e, err
	}
	e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = utc(valid), utc(invalid), utc(created), utc(expired)
	return e, nil
}

func collectEdges(rows *sql.Rows) ([]model.Edge, error) {
	var out []model.Edge
	for rows.Next() {
		e, err := scanEdge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LoadCandidateEdges returns live (not expired) edges of the dataset touching at least one entity whose name_key
// is in keys, newest first, at most limit.
func LoadCandidateEdges(ctx context.Context, d *sql.DB, dsID int64, keys []string, limit int) ([]model.Edge, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := d.QueryContext(ctx, `
		SELECT `+edgeColumns+edgeFrom+`
		WHERE e.dataset_id = $1
			AND e.expired_at IS NULL
			AND (s.name_key = ANY($2) OR t.name_key = ANY($2))
		ORDER BY e.id DESC
		LIMIT $3`, dsID, keys, limit)
	if err != nil {
		return nil, fmt.Errorf("load edge candidates: %w", err)
	}
	defer rows.Close()
	return collectEdges(rows)
}

// ListEdges returns every edge of userID's live dataset called name, by ID (expired ones included).
func ListEdges(ctx context.Context, d *sql.DB, userID, name string) ([]model.Edge, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+edgeColumns+edgeFrom+`
		JOIN gb_datasets ds ON ds.id = e.dataset_id
		WHERE ds.user_id = $1 AND ds.name = $2 AND NOT ds.deleted
		ORDER BY e.id`, userID, name)
	if err != nil {
		return nil, fmt.Errorf("list edges: %w", err)
	}
	defer rows.Close()
	return collectEdges(rows)
}

//---------------------------------------------------------------------------------------------
// Recall retrieval

// FallbackSeedEntities returns the IDs of the most recently updated entities of the dataset.
func FallbackSeedEntities(ctx context.Context, d *sql.DB, dsID int64, limit int) ([]int64, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id
		FROM gb_entities
		WHERE dataset_id = $1
		ORDER BY updated_at DESC, id DESC
		LIMIT $2`, dsID, limit)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// MatchEntities returns the IDs of the best entities whose name or description mention the query terms.
// queryKey is the normalized (lowercased) query; tokens are its distinct terms.
func MatchEntities(ctx context.Context, d *sql.DB, dsID int64, queryKey string, tokens []string, limit int) ([]int64, error) {
	var a args
	ds := a.add(dsID)
	// Score: 5 if the full entity name appears in the query; per token, 3 for a name hit and 1 for a description hit.
	qk := a.add(queryKey)
	conds := []string{"strpos(" + qk + "::text, name_key) > 0"}
	score := []string{"(CASE WHEN strpos(" + qk + "::text, name_key) > 0 THEN 5 ELSE 0 END)"}
	for _, t := range tokens {
		p := a.add(t)
		conds = append(conds, "strpos(name_key, "+p+"::text) > 0", "strpos(lower(description), "+p+"::text) > 0")
		score = append(score, "(CASE WHEN strpos(name_key, "+p+"::text) > 0 THEN 3 ELSE 0 END)", "(CASE WHEN strpos(lower(description), "+p+"::text) > 0 THEN 1 ELSE 0 END)")
	}
	lim := a.add(limit)
	rows, err := d.QueryContext(ctx, `
		SELECT id
		FROM gb_entities
		WHERE dataset_id = `+ds+`
			AND (
				`+strings.Join(conds, "\n\t\t\t\tOR ")+`
			)
		ORDER BY
			(
				`+strings.Join(score, "\n\t\t\t\t+ ")+`
			) DESC,
			updated_at DESC,
			id DESC
		LIMIT `+lim, []any(a)...)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// EdgesAround returns the edges of the dataset that start or end at one of the entity IDs, newest first.
func EdgesAround(ctx context.Context, d *sql.DB, dsID int64, entityIDs []int64, limit int) ([]model.EvidenceEdge, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT
			e.id, e.source_id, e.target_id, s.name, t.name, e.relation, e.description, e.artifact_id,
			e.valid_at, e.invalid_at, e.created_at, e.expired_at
		FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id
		JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id = $1
			AND (e.source_id = ANY($2) OR e.target_id = ANY($2))
		ORDER BY e.id DESC
		LIMIT $3`, dsID, entityIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EvidenceEdge
	for rows.Next() {
		var e model.EvidenceEdge
		var mem sql.NullInt64
		var valid, invalid, created, expired sql.NullTime
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.Source, &e.Target, &e.Relation, &e.Description, &mem,
			&valid, &invalid, &created, &expired); err != nil {
			return nil, err
		}
		e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = utc(valid), utc(invalid), utc(created), utc(expired)
		if mem.Valid {
			e.ArtifactID = &mem.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EntitiesByID returns the entities with the given IDs.
func EntitiesByID(ctx context.Context, d *sql.DB, ids []int64) ([]model.EvidenceEntity, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, name, type, description
		FROM gb_entities
		WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EvidenceEntity
	for rows.Next() {
		var e model.EvidenceEntity
		if err := rows.Scan(&e.ID, &e.Name, &e.Type, &e.Description); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

//---------------------------------------------------------------------------------------------
// Inspection (read-only summaries of what is stored; used to verify behaviour, e.g. in tests)

// DatasetCounts returns the number of artifacts, entities and edges in the dataset.
func DatasetCounts(ctx context.Context, d *sql.DB, dsID int64) (model.Counts, error) {
	var c model.Counts
	err := d.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM gb_artifacts WHERE dataset_id = $1),
			(SELECT COUNT(*) FROM gb_entities WHERE dataset_id = $1),
			(SELECT COUNT(*) FROM gb_edges WHERE dataset_id = $1)`, dsID).Scan(&c.Artifacts, &c.Entities, &c.Edges)
	return c, err
}

// ListArtifacts returns the artifacts of the dataset by ID.
func ListArtifacts(ctx context.Context, d *sql.DB, dsID int64) ([]model.Artifact, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, dataset_id, kind, text, valid_at, created_at
		FROM gb_artifacts
		WHERE dataset_id = $1
		ORDER BY id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Artifact
	for rows.Next() {
		var m model.Artifact
		var valid, created time.Time
		if err := rows.Scan(&m.ID, &m.DatasetID, &m.Kind, &m.Text, &valid, &created); err != nil {
			return nil, err
		}
		m.ValidAt, m.CreatedAt = valid.UTC(), created.UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListEdgeArtifacts returns the edge-to-artifact links of the dataset, by edge ID then artifact ID.
func ListEdgeArtifacts(ctx context.Context, d *sql.DB, dsID int64) ([]model.EdgeArtifact, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT ea.edge_id, ea.artifact_id
		FROM gb_edge_artifacts ea
		JOIN gb_edges e ON e.id = ea.edge_id
		WHERE e.dataset_id = $1
		ORDER BY ea.edge_id, ea.artifact_id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EdgeArtifact
	for rows.Next() {
		var l model.EdgeArtifact
		if err := rows.Scan(&l.EdgeID, &l.ArtifactID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListEntityArtifacts returns the entity-to-artifact links of the dataset, by entity ID then artifact ID.
func ListEntityArtifacts(ctx context.Context, d *sql.DB, dsID int64) ([]model.EntityArtifact, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT ea.entity_id, ea.artifact_id
		FROM gb_entity_artifacts ea
		JOIN gb_entities e ON e.id = ea.entity_id
		WHERE e.dataset_id = $1
		ORDER BY ea.entity_id, ea.artifact_id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EntityArtifact
	for rows.Next() {
		var l model.EntityArtifact
		if err := rows.Scan(&l.EntityID, &l.ArtifactID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListEntities returns the entities of the dataset by ID.
func ListEntities(ctx context.Context, d *sql.DB, dsID int64) ([]model.EvidenceEntity, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, name, type, description
		FROM gb_entities
		WHERE dataset_id = $1
		ORDER BY id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EvidenceEntity
	for rows.Next() {
		var e model.EvidenceEntity
		if err := rows.Scan(&e.ID, &e.Name, &e.Type, &e.Description); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListGraphEdges returns every edge of the dataset (expired ones included) with its endpoint IDs and names, by ID.
func ListGraphEdges(ctx context.Context, d *sql.DB, dsID int64) ([]model.EvidenceEdge, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT
			e.id, e.source_id, e.target_id, s.name, t.name, e.relation, e.description, e.artifact_id,
			e.valid_at, e.invalid_at, e.created_at, e.expired_at
		FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id
		JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id = $1
		ORDER BY e.id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EvidenceEdge
	for rows.Next() {
		var e model.EvidenceEdge
		var mem sql.NullInt64
		var valid, invalid, created, expired sql.NullTime
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.Source, &e.Target, &e.Relation, &e.Description, &mem,
			&valid, &invalid, &created, &expired); err != nil {
			return nil, err
		}
		e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = utc(valid), utc(invalid), utc(created), utc(expired)
		if mem.Valid {
			e.ArtifactID = &mem.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountCrossDatasetEdges returns the number of edges whose dataset differs from that of one of their endpoints
// (always 0 unless datasets leak into each other).
func CountCrossDatasetEdges(ctx context.Context, d *sql.DB) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id
		JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id <> s.dataset_id
			OR e.dataset_id <> t.dataset_id`).Scan(&n)
	return n, err
}

//---------------------------------------------------------------------------------------------
// Transactions

// Tx is a PostgreSQL transaction; it implements model.Tx.
type Tx struct{ tx *sql.Tx }

var _ model.Tx = (*Tx)(nil)

// Begin starts a transaction.
func Begin(ctx context.Context, d *sql.DB) (*Tx, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{tx}, nil
}

func (t *Tx) Commit() error   { return t.tx.Commit() }
func (t *Tx) Rollback() error { return t.tx.Rollback() }

func (t *Tx) InsertArtifact(ctx context.Context, dsID int64, kind, text string, validAt, createdAt time.Time) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		INSERT INTO gb_artifacts (dataset_id, kind, text, valid_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		dsID, kind, text, validAt.UTC(), createdAt.UTC()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert artifact: %w", err)
	}
	return id, nil
}

func (t *Tx) LinkEdge(ctx context.Context, edgeID, artifactID int64) error {
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_edge_artifacts (edge_id, artifact_id)
		VALUES ($1, $2)
		ON CONFLICT (edge_id, artifact_id) DO NOTHING`, edgeID, artifactID); err != nil {
		return fmt.Errorf("link edge %d to artifact %d: %w", edgeID, artifactID, err)
	}
	return nil
}

func (t *Tx) LinkEntity(ctx context.Context, entityID, artifactID int64) error {
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_entity_artifacts (entity_id, artifact_id)
		VALUES ($1, $2)
		ON CONFLICT (entity_id, artifact_id) DO NOTHING`, entityID, artifactID); err != nil {
		return fmt.Errorf("link entity %d to artifact %d: %w", entityID, artifactID, err)
	}
	return nil
}

func (t *Tx) UpsertEntity(ctx context.Context, dsID int64, e model.Entity, nameKey string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		INSERT INTO gb_entities (dataset_id, name, name_key, type, description)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (dataset_id, name_key, type) DO UPDATE SET
			description = CASE
				WHEN EXCLUDED.description <> '' THEN EXCLUDED.description
				ELSE gb_entities.description
			END,
			updated_at  = `+rpostgres.SQLNow+`
		RETURNING id`,
		dsID, e.Name, nameKey, e.Type, e.Description).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert entity %q: %w", e.Name, err)
	}
	return id, nil
}

func (t *Tx) FindEntityID(ctx context.Context, dsID int64, nameKey string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		SELECT id
		FROM gb_entities
		WHERE dataset_id = $1 AND name_key = $2
		ORDER BY id
		LIMIT 1`, dsID, nameKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return id, err
}

func (t *Tx) GetEntity(ctx context.Context, dsID, id int64) (*model.EvidenceEntity, error) {
	e := &model.EvidenceEntity{}
	err := t.tx.QueryRowContext(ctx, `
		SELECT id, name, type, description
		FROM gb_entities
		WHERE dataset_id = $1 AND id = $2`, dsID, id).Scan(&e.ID, &e.Name, &e.Type, &e.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get entity %d: %w", id, err)
	}
	return e, nil
}

func (t *Tx) InsertEdge(ctx context.Context, e model.NewEdge) (int64, error) {
	var invalid any
	if e.InvalidAt != nil {
		invalid = e.InvalidAt.UTC()
	}
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		INSERT INTO gb_edges (dataset_id, source_id, target_id, relation, description, valid_at, invalid_at, created_at, artifact_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`,
		e.DatasetID, e.SourceID, e.TargetID, e.Relation, e.Description, e.ValidAt.UTC(), invalid, e.CreatedAt.UTC(), e.ArtifactID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert edge: %w", err)
	}
	return id, nil
}

func (t *Tx) MergeDuplicateEdge(ctx context.Context, dsID, id int64, invalidAt *time.Time) error {
	var invalid any
	if invalidAt != nil {
		invalid = invalidAt.UTC()
	}
	if _, err := t.tx.ExecContext(ctx, `
		UPDATE gb_edges
		SET invalid_at = COALESCE(invalid_at, $1::timestamptz)
		WHERE id = $2 AND dataset_id = $3`,
		invalid, id, dsID); err != nil {
		return fmt.Errorf("merge duplicate edge: %w", err)
	}
	return nil
}

func (t *Tx) GetEdge(ctx context.Context, dsID, id int64) (*model.Edge, error) {
	e, err := scanEdge(t.tx.QueryRowContext(ctx, `
		SELECT `+edgeColumns+edgeFrom+`
		WHERE e.id = $1 AND e.dataset_id = $2`, id, dsID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load edge %d: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("load edge %d: %w", id, err)
	}
	return &e, nil
}

func (t *Tx) InvalidateEdge(ctx context.Context, id int64, invalidAt, expiredAt time.Time) error {
	if _, err := t.tx.ExecContext(ctx, `
		UPDATE gb_edges
		SET invalid_at = $1, expired_at = $2
		WHERE id = $3`,
		invalidAt.UTC(), expiredAt.UTC(), id); err != nil {
		return fmt.Errorf("invalidate edge: %w", err)
	}
	return nil
}
