// Package sqlite stores the knowledge graph (artifacts, entities, edges) in SQLite tables and runs every SQL
// statement for it. The functions here are reached through the routing functions of package graph.
//
// The tables live in the same database as the relational data (they reference its datasets), so this package
// works on the *sql.DB opened by internal/db/relational/sqlite and shares its timestamp convention.
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

//---------------------------------------------------------------------------------------------

// Open applies the graph schema to d, a database opened by relational/sqlite.Open.
func Open(d *sql.DB) error { return initSchema(d) }

type scanner interface{ Scan(dest ...any) error }

// nullIfEmpty is how an unknown model is stored: NULL, since model_id references gb_models.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

//---------------------------------------------------------------------------------------------
// Edges

const edgeColumns = `
	e.id, s.name, t.name, e.relation, e.description,
	e.valid_at, e.invalid_at, e.created_at, e.expired_at, COALESCE(e.model_id, '')`
const edgeFrom = `
	FROM gb_edges e
	JOIN gb_entities s ON s.id = e.source_id
	JOIN gb_entities t ON t.id = e.target_id`

func scanEdge(r scanner) (model.Edge, error) {
	var e model.Edge
	var valid, invalid, created, expired sql.NullString
	if err := r.Scan(&e.ID, &e.Source, &e.Target, &e.Relation, &e.Description, &valid, &invalid, &created, &expired, &e.ModelID); err != nil {
		return e, err
	}
	e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = rsqlite.ParseTime(valid.String), rsqlite.ParseTime(invalid.String), rsqlite.ParseTime(created.String), rsqlite.ParseTime(expired.String)
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
	ph := qmarks(len(keys))
	args := []any{dsID}
	for range 2 {
		for _, k := range keys {
			args = append(args, k)
		}
	}
	args = append(args, limit)
	rows, err := d.QueryContext(ctx, `
		SELECT `+edgeColumns+edgeFrom+`
		WHERE e.dataset_id = ?
			AND e.expired_at IS NULL
			AND (s.name_key IN (`+ph+`) OR t.name_key IN (`+ph+`))
		ORDER BY e.id DESC
		LIMIT ?`, args...)
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
		WHERE ds.user_id = ? AND ds.name = ? AND ds.deleted = 0
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
		WHERE dataset_id = ?
		ORDER BY updated_at DESC, id DESC
		LIMIT ?`, dsID, limit)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// MatchEntities returns the IDs of the best entities whose name or description mention the query terms.
// queryKey is the normalized (lowercased) query; tokens are its distinct terms.
func MatchEntities(ctx context.Context, d *sql.DB, dsID int64, queryKey string, tokens []string, limit int) ([]int64, error) {
	// Score: 5 if the full entity name appears in the query; per token, 3 for a name hit and 1 for a description hit.
	conds := []string{"instr(?, name_key) > 0"}
	whereArgs := []any{queryKey}
	score := []string{"(CASE WHEN instr(?, name_key) > 0 THEN 5 ELSE 0 END)"}
	scoreArgs := []any{queryKey}
	for _, t := range tokens {
		conds = append(conds, "instr(name_key, ?) > 0", "instr(lower(description), ?) > 0")
		whereArgs = append(whereArgs, t, t)
		score = append(score, "(CASE WHEN instr(name_key, ?) > 0 THEN 3 ELSE 0 END)", "(CASE WHEN instr(lower(description), ?) > 0 THEN 1 ELSE 0 END)")
		scoreArgs = append(scoreArgs, t, t)
	}
	// Placeholder order in the SQL text: WHERE, ORDER BY, LIMIT.
	args := append(append(append([]any{dsID}, whereArgs...), scoreArgs...), limit)
	rows, err := d.QueryContext(ctx, `
		SELECT id
		FROM gb_entities
		WHERE dataset_id = ?
			AND (
				`+strings.Join(conds, "\n\t\t\t\tOR ")+`
			)
		ORDER BY
			(
				`+strings.Join(score, "\n\t\t\t\t+ ")+`
			) DESC,
			updated_at DESC,
			id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// EdgesAround returns the edges of the dataset that start or end at one of the entity IDs, newest first.
func EdgesAround(ctx context.Context, d *sql.DB, dsID int64, entityIDs []int64, limit int) ([]model.EvidenceEdge, error) {
	ph, ids := placeholders(entityIDs)
	args := append(append([]any{dsID}, ids...), ids...)
	args = append(args, limit)
	rows, err := d.QueryContext(ctx, `
		SELECT
			e.id, e.source_id, e.target_id, s.name, t.name, e.relation, e.description, e.artifact_id,
			e.valid_at, e.invalid_at, e.created_at, e.expired_at, COALESCE(e.model_id, '')
		FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id
		JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id = ?
			AND (e.source_id IN (`+ph+`) OR e.target_id IN (`+ph+`))
		ORDER BY e.id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EvidenceEdge
	for rows.Next() {
		var e model.EvidenceEdge
		var mem sql.NullInt64
		var valid, invalid, created, expired sql.NullString
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.Source, &e.Target, &e.Relation, &e.Description, &mem,
			&valid, &invalid, &created, &expired, &e.ModelID); err != nil {
			return nil, err
		}
		e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = rsqlite.ParseTime(valid.String), rsqlite.ParseTime(invalid.String), rsqlite.ParseTime(created.String), rsqlite.ParseTime(expired.String)
		if mem.Valid {
			e.ArtifactID = &mem.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EntitiesByID returns the entities with the given IDs.
func EntitiesByID(ctx context.Context, d *sql.DB, ids []int64) ([]model.EvidenceEntity, error) {
	ph, args := placeholders(ids)
	rows, err := d.QueryContext(ctx, `
		SELECT id, name, type, description, COALESCE(model_id, '')
		FROM gb_entities
		WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EvidenceEntity
	for rows.Next() {
		var e model.EvidenceEntity
		if err := rows.Scan(&e.ID, &e.Name, &e.Type, &e.Description, &e.ModelID); err != nil {
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

func placeholders(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return qmarks(len(ids)), args
}

func qmarks(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

//---------------------------------------------------------------------------------------------
// Inspection (read-only summaries of what is stored; used to verify behaviour, e.g. in tests)

// DatasetCounts returns the number of artifacts (and of those that are chunks), entities and edges in the dataset.
func DatasetCounts(ctx context.Context, d *sql.DB, dsID int64) (model.Counts, error) {
	var c model.Counts
	err := d.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM gb_artifacts WHERE dataset_id = ?),
			(SELECT COUNT(*) FROM gb_entities WHERE dataset_id = ?),
			(SELECT COUNT(*) FROM gb_edges WHERE dataset_id = ?),
			(SELECT COUNT(*) FROM gb_artifacts WHERE dataset_id = ? AND chunk_index IS NOT NULL)`,
		dsID, dsID, dsID, dsID).Scan(&c.Artifacts, &c.Entities, &c.Edges, &c.Chunks)
	return c, err
}

// ListArtifacts returns the artifacts of the dataset by ID.
func ListArtifacts(ctx context.Context, d *sql.DB, dsID int64) ([]model.Artifact, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, dataset_id, kind, text, COALESCE(session_id, ''), valid_at, created_at,
			COALESCE(parent_id, 0), COALESCE(chunk_index, 0), COALESCE(start_offset, 0), COALESCE(end_offset, 0), COALESCE(tokens, 0), COALESCE(query, '')
		FROM gb_artifacts
		WHERE dataset_id = ?
		ORDER BY id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Artifact
	for rows.Next() {
		var m model.Artifact
		var valid, created string
		if err := rows.Scan(&m.ID, &m.DatasetID, &m.Kind, &m.Text, &m.SessionID, &valid, &created,
			&m.ParentID, &m.ChunkIndex, &m.StartOffset, &m.EndOffset, &m.Tokens, &m.Query); err != nil {
			return nil, err
		}
		if t := rsqlite.ParseTime(valid); t != nil {
			m.ValidAt = *t
		}
		if t := rsqlite.ParseTime(created); t != nil {
			m.CreatedAt = *t
		}
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
		WHERE e.dataset_id = ?
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
		WHERE e.dataset_id = ?
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

// ListEntitySessions returns the entity-to-session links of the dataset, by entity ID then session ID.
func ListEntitySessions(ctx context.Context, d *sql.DB, dsID int64) ([]model.EntitySession, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT es.entity_id, es.session_id
		FROM gb_entity_sessions es
		JOIN gb_entities e ON e.id = es.entity_id
		WHERE e.dataset_id = ?
		ORDER BY es.entity_id, es.session_id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EntitySession
	for rows.Next() {
		var l model.EntitySession
		if err := rows.Scan(&l.EntityID, &l.SessionID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListEdgeSessions returns the edge-to-session links of the dataset, by edge ID then session ID.
func ListEdgeSessions(ctx context.Context, d *sql.DB, dsID int64) ([]model.EdgeSession, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT es.edge_id, es.session_id
		FROM gb_edge_sessions es
		JOIN gb_edges e ON e.id = es.edge_id
		WHERE e.dataset_id = ?
		ORDER BY es.edge_id, es.session_id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EdgeSession
	for rows.Next() {
		var l model.EdgeSession
		if err := rows.Scan(&l.EdgeID, &l.SessionID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListEntities returns the entities of the dataset by ID.
func ListEntities(ctx context.Context, d *sql.DB, dsID int64) ([]model.EvidenceEntity, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, name, type, description, COALESCE(model_id, '')
		FROM gb_entities
		WHERE dataset_id = ?
		ORDER BY id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EvidenceEntity
	for rows.Next() {
		var e model.EvidenceEntity
		if err := rows.Scan(&e.ID, &e.Name, &e.Type, &e.Description, &e.ModelID); err != nil {
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
			e.valid_at, e.invalid_at, e.created_at, e.expired_at, COALESCE(e.model_id, '')
		FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id
		JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id = ?
		ORDER BY e.id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EvidenceEdge
	for rows.Next() {
		var e model.EvidenceEdge
		var mem sql.NullInt64
		var valid, invalid, created, expired sql.NullString
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.Source, &e.Target, &e.Relation, &e.Description, &mem,
			&valid, &invalid, &created, &expired, &e.ModelID); err != nil {
			return nil, err
		}
		e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = rsqlite.ParseTime(valid.String), rsqlite.ParseTime(invalid.String), rsqlite.ParseTime(created.String), rsqlite.ParseTime(expired.String)
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
		WHERE e.dataset_id != s.dataset_id
			OR e.dataset_id != t.dataset_id`).Scan(&n)
	return n, err
}

//---------------------------------------------------------------------------------------------
// Transactions

// Tx is a SQLite transaction; it implements model.Tx.
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

func (t *Tx) InsertArtifact(ctx context.Context, dsID int64, kind, text, sessionID string, validAt, createdAt time.Time) (int64, error) {
	res, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_artifacts (dataset_id, kind, text, session_id, valid_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		dsID, kind, text, nullIfEmpty(sessionID), rsqlite.FormatTime(validAt), rsqlite.FormatTime(createdAt))
	if err != nil {
		return 0, fmt.Errorf("insert artifact: %w", err)
	}
	return res.LastInsertId()
}

func (t *Tx) InsertChunkArtifact(ctx context.Context, c model.NewChunkArtifact) (int64, error) {
	res, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_artifacts (dataset_id, kind, text, session_id, parent_id, chunk_index, start_offset, end_offset, tokens, valid_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.DatasetID, c.Kind, c.Text, nullIfEmpty(c.SessionID), c.ParentID, c.ChunkIndex, c.StartOffset, c.EndOffset, c.Tokens,
		rsqlite.FormatTime(c.ValidAt), rsqlite.FormatTime(c.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert chunk %d of artifact %d: %w", c.ChunkIndex, c.ParentID, err)
	}
	return res.LastInsertId()
}

func (t *Tx) InsertWebSearchArtifact(ctx context.Context, w model.NewWebSearchArtifact) (int64, error) {
	res, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_artifacts (dataset_id, kind, text, session_id, parent_id, query, valid_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		w.DatasetID, model.ArtifactWebSearch, w.Text, nullIfEmpty(w.SessionID), w.ParentID, w.Query,
		rsqlite.FormatTime(w.ValidAt), rsqlite.FormatTime(w.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert web search of artifact %d: %w", w.ParentID, err)
	}
	return res.LastInsertId()
}

func (t *Tx) LinkEdge(ctx context.Context, edgeID, artifactID int64) error {
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_edge_artifacts (edge_id, artifact_id)
		VALUES (?, ?)
		ON CONFLICT (edge_id, artifact_id) DO NOTHING`, edgeID, artifactID); err != nil {
		return fmt.Errorf("link edge %d to artifact %d: %w", edgeID, artifactID, err)
	}
	return nil
}

func (t *Tx) LinkEntity(ctx context.Context, entityID, artifactID int64) error {
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_entity_artifacts (entity_id, artifact_id)
		VALUES (?, ?)
		ON CONFLICT (entity_id, artifact_id) DO NOTHING`, entityID, artifactID); err != nil {
		return fmt.Errorf("link entity %d to artifact %d: %w", entityID, artifactID, err)
	}
	return nil
}

func (t *Tx) LinkEdgeSession(ctx context.Context, edgeID int64, sessionID string) error {
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_edge_sessions (edge_id, session_id)
		VALUES (?, ?)
		ON CONFLICT (edge_id, session_id) DO NOTHING`, edgeID, sessionID); err != nil {
		return fmt.Errorf("link edge %d to session %q: %w", edgeID, sessionID, err)
	}
	return nil
}

func (t *Tx) LinkEntitySession(ctx context.Context, entityID int64, sessionID string) error {
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_entity_sessions (entity_id, session_id)
		VALUES (?, ?)
		ON CONFLICT (entity_id, session_id) DO NOTHING`, entityID, sessionID); err != nil {
		return fmt.Errorf("link entity %d to session %q: %w", entityID, sessionID, err)
	}
	return nil
}

func (t *Tx) UpsertEntity(ctx context.Context, dsID int64, e model.Entity, nameKey string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		INSERT INTO gb_entities (dataset_id, name, name_key, type, description, model_id)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (dataset_id, name_key, type) DO UPDATE SET
			-- model_id follows the description: it is the model that wrote the stored state.
			model_id    = CASE
				WHEN excluded.description != '' OR gb_entities.model_id IS NULL THEN excluded.model_id
				ELSE gb_entities.model_id
			END,
			description = CASE
				WHEN excluded.description != '' THEN excluded.description
				ELSE gb_entities.description
			END,
			updated_at  = `+rsqlite.SQLNow+`
		RETURNING id`,
		dsID, e.Name, nameKey, e.Type, e.Description, nullIfEmpty(e.ModelID)).Scan(&id)
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
		WHERE dataset_id = ? AND name_key = ?
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
		SELECT id, name, type, description, COALESCE(model_id, '')
		FROM gb_entities
		WHERE dataset_id = ? AND id = ?`, dsID, id).Scan(&e.ID, &e.Name, &e.Type, &e.Description, &e.ModelID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get entity %d: %w", id, err)
	}
	return e, nil
}

func (t *Tx) InsertEdge(ctx context.Context, e model.NewEdge) (int64, error) {
	res, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_edges (dataset_id, source_id, target_id, relation, description, valid_at, invalid_at, created_at, artifact_id, model_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.DatasetID, e.SourceID, e.TargetID, e.Relation, e.Description, rsqlite.FormatTime(e.ValidAt), rsqlite.FormatTimePtr(e.InvalidAt),
		rsqlite.FormatTime(e.CreatedAt), e.ArtifactID, nullIfEmpty(e.ModelID))
	if err != nil {
		return 0, fmt.Errorf("insert edge: %w", err)
	}
	return res.LastInsertId()
}

func (t *Tx) MergeDuplicateEdge(ctx context.Context, dsID, id int64, invalidAt *time.Time) error {
	if _, err := t.tx.ExecContext(ctx, `
		UPDATE gb_edges
		SET invalid_at = COALESCE(invalid_at, ?)
		WHERE id = ? AND dataset_id = ?`,
		rsqlite.FormatTimePtr(invalidAt), id, dsID); err != nil {
		return fmt.Errorf("merge duplicate edge: %w", err)
	}
	return nil
}

func (t *Tx) GetEdge(ctx context.Context, dsID, id int64) (*model.Edge, error) {
	e, err := scanEdge(t.tx.QueryRowContext(ctx, `
		SELECT `+edgeColumns+edgeFrom+`
		WHERE e.id = ? AND e.dataset_id = ?`, id, dsID))
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
		SET invalid_at = ?, expired_at = ?
		WHERE id = ?`,
		rsqlite.FormatTime(invalidAt), rsqlite.FormatTime(expiredAt), id); err != nil {
		return fmt.Errorf("invalidate edge: %w", err)
	}
	return nil
}
