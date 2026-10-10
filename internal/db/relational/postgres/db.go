// Package postgres manages the PostgreSQL database: connection, schema, the timestamp convention and every SQL
// statement the application runs. The functions here are reached through the routing functions of package db and
// mirror those of package sqlite, in the PostgreSQL dialect ($n placeholders, RETURNING, native types).
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ivan/gligabao/internal/db/relational/model"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver, registered as "pgx"
)

//---------------------------------------------------------------------------------------------

// Open connects to the PostgreSQL database at dsn (a URL such as postgres://user:pass@host:5432/dbname, or a
// key=value connection string) and applies the schema.
func Open(dsn string) (*sql.DB, error) {
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.PingContext(ctx); err != nil {
		d.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := initSchema(ctx, d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// args collects query parameters and hands out their $n placeholders.
type args []any

func (a *args) add(v any) string {
	*a = append(*a, v)
	return fmt.Sprintf("$%d", len(*a))
}

func utc(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time.UTC()
	return &t
}

//---------------------------------------------------------------------------------------------
// Datasets

const datasetColumns = `id, user_id, name, description, tags::text, created_at, deleted, deleted_at`
const datasetSelect = `SELECT ` + datasetColumns + ` FROM gb_datasets`

type scanner interface{ Scan(dest ...any) error }

func scanDataset(r scanner) (*model.Dataset, error) {
	var ds model.Dataset
	var tags string
	var deletedAt sql.NullTime
	if err := r.Scan(&ds.ID, &ds.UserID, &ds.Name, &ds.Description, &tags, &ds.CreatedAt, &ds.Deleted, &deletedAt); err != nil {
		return nil, err
	}
	ds.Tags = []string{}
	if err := json.Unmarshal([]byte(tags), &ds.Tags); err != nil || ds.Tags == nil {
		ds.Tags = []string{}
	}
	ds.CreatedAt = ds.CreatedAt.UTC()
	ds.DeletedAt = utc(deletedAt)
	return &ds, nil
}

// CreateDataset creates a dataset owned by userID. It returns model.ErrExists if a live dataset with that name
// exists for the user.
func CreateDataset(ctx context.Context, d *sql.DB, userID, name, description string, tags []string) (*model.Dataset, error) {
	tagsJSON, _ := json.Marshal(tags)
	ds, err := scanDataset(d.QueryRowContext(ctx, `
		INSERT INTO gb_datasets (user_id, name, description, tags, created_at) VALUES ($1, $2, $3, $4::jsonb, $5)
		ON CONFLICT (user_id, name) WHERE NOT deleted DO NOTHING
		RETURNING `+datasetColumns, userID, name, description, string(tagsJSON), Now()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrExists
	}
	if err != nil {
		return nil, fmt.Errorf("create dataset: %w", err)
	}
	return ds, nil
}

// ListDatasets returns userID's datasets oldest first; deleted ones only if includeDeleted.
func ListDatasets(ctx context.Context, d *sql.DB, userID string, includeDeleted bool) ([]model.Dataset, error) {
	q := datasetSelect + ` WHERE user_id = $1`
	if !includeDeleted {
		q += ` AND NOT deleted`
	}
	rows, err := d.QueryContext(ctx, q+` ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list datasets: %w", err)
	}
	defer rows.Close()
	out := []model.Dataset{}
	for rows.Next() {
		ds, err := scanDataset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ds)
	}
	return out, rows.Err()
}

// DeleteDataset soft-deletes userID's live dataset called name. It returns model.ErrNotFound if there is none.
func DeleteDataset(ctx context.Context, d *sql.DB, userID, name string) error {
	res, err := d.ExecContext(ctx, `UPDATE gb_datasets SET deleted = TRUE, deleted_at = $1 WHERE user_id = $2 AND name = $3 AND NOT deleted`,
		Now(), userID, name)
	if err != nil {
		return fmt.Errorf("delete dataset: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func datasetID(ctx context.Context, q queryer, userID, name string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM gb_datasets WHERE user_id = $1 AND name = $2 AND NOT deleted`, userID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return id, err
}

// DatasetID returns the ID of userID's live dataset called name, or model.ErrNotFound.
func DatasetID(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	return datasetID(ctx, d, userID, name)
}

//---------------------------------------------------------------------------------------------
// LLM call log

// RecordLLMCall stores one LLM/embedding API call.
func RecordLLMCall(ctx context.Context, d *sql.DB, kind, provider, modelName string, inputTokens, outputTokens int, costUSD float64) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO gb_llm_calls (type, provider, model, input_tokens, output_tokens, cost_usd) VALUES ($1, $2, $3, $4, $5, $6)`,
		kind, provider, modelName, inputTokens, outputTokens, costUSD)
	return err
}

//---------------------------------------------------------------------------------------------
// Edges

const edgeColumns = `e.id, s.name, t.name, e.relation, e.description, e.valid_at, e.invalid_at, e.created_at, e.expired_at`
const edgeFrom = ` FROM gb_edges e JOIN gb_entities s ON s.id = e.source_id JOIN gb_entities t ON t.id = e.target_id`

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
	rows, err := d.QueryContext(ctx, `SELECT `+edgeColumns+edgeFrom+`
		WHERE e.dataset_id = $1 AND e.expired_at IS NULL AND (s.name_key = ANY($2) OR t.name_key = ANY($2))
		ORDER BY e.id DESC LIMIT $3`, dsID, keys, limit)
	if err != nil {
		return nil, fmt.Errorf("load edge candidates: %w", err)
	}
	defer rows.Close()
	return collectEdges(rows)
}

// ListEdges returns every edge of userID's live dataset called name, by ID (expired ones included).
func ListEdges(ctx context.Context, d *sql.DB, userID, name string) ([]model.Edge, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+edgeColumns+edgeFrom+`
		JOIN gb_datasets ds ON ds.id = e.dataset_id
		WHERE ds.user_id = $1 AND ds.name = $2 AND NOT ds.deleted ORDER BY e.id`, userID, name)
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
	rows, err := d.QueryContext(ctx, `SELECT id FROM gb_entities WHERE dataset_id = $1 ORDER BY updated_at DESC, id DESC LIMIT $2`, dsID, limit)
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
	rows, err := d.QueryContext(ctx, `SELECT id FROM gb_entities WHERE dataset_id = `+ds+` AND (`+strings.Join(conds, " OR ")+`)
		ORDER BY (`+strings.Join(score, " + ")+`) DESC, updated_at DESC, id DESC LIMIT `+lim, []any(a)...)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// EdgesAround returns the edges of the dataset that start or end at one of the entity IDs, newest first.
func EdgesAround(ctx context.Context, d *sql.DB, dsID int64, entityIDs []int64, limit int) ([]model.EvidenceEdge, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT e.id, e.source_id, e.target_id, s.name, t.name, e.relation, e.description, e.memory_id,
			e.valid_at, e.invalid_at, e.created_at, e.expired_at
		FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id
		JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id = $1 AND (e.source_id = ANY($2) OR e.target_id = ANY($2))
		ORDER BY e.id DESC LIMIT $3`, dsID, entityIDs, limit)
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
			e.MemoryID = &mem.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EntitiesByID returns the entities with the given IDs.
func EntitiesByID(ctx context.Context, d *sql.DB, ids []int64) ([]model.EvidenceEntity, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, name, type, description FROM gb_entities WHERE id = ANY($1)`, ids)
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

// DatasetCounts returns the number of memories, entities and edges in the dataset.
func DatasetCounts(ctx context.Context, d *sql.DB, dsID int64) (model.DatasetCounts, error) {
	var c model.DatasetCounts
	err := d.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM gb_memories WHERE dataset_id = $1),
		(SELECT COUNT(*) FROM gb_entities WHERE dataset_id = $1),
		(SELECT COUNT(*) FROM gb_edges WHERE dataset_id = $1)`, dsID).Scan(&c.Memories, &c.Entities, &c.Edges)
	return c, err
}

// ListMemories returns the episodes of the dataset by ID.
func ListMemories(ctx context.Context, d *sql.DB, dsID int64) ([]model.Memory, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, dataset_id, text, valid_at, created_at FROM gb_memories WHERE dataset_id = $1 ORDER BY id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Memory
	for rows.Next() {
		var m model.Memory
		var valid, created time.Time
		if err := rows.Scan(&m.ID, &m.DatasetID, &m.Text, &valid, &created); err != nil {
			return nil, err
		}
		m.ValidAt, m.CreatedAt = valid.UTC(), created.UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListEntities returns the entities of the dataset by ID.
func ListEntities(ctx context.Context, d *sql.DB, dsID int64) ([]model.EvidenceEntity, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, name, type, description FROM gb_entities WHERE dataset_id = $1 ORDER BY id`, dsID)
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
		SELECT e.id, e.source_id, e.target_id, s.name, t.name, e.relation, e.description, e.memory_id,
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
			e.MemoryID = &mem.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountCrossDatasetEdges returns the number of edges whose dataset differs from that of one of their endpoints
// (always 0 unless datasets leak into each other).
func CountCrossDatasetEdges(ctx context.Context, d *sql.DB) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id <> s.dataset_id OR e.dataset_id <> t.dataset_id`).Scan(&n)
	return n, err
}

// LLMCallStats sums the logged LLM calls by type.
func LLMCallStats(ctx context.Context, d *sql.DB) (map[string]model.LLMCallStat, error) {
	rows, err := d.QueryContext(ctx, `SELECT type, COUNT(*), SUM(input_tokens)::bigint, SUM(output_tokens)::bigint, SUM(cost_usd) FROM gb_llm_calls GROUP BY type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.LLMCallStat{}
	for rows.Next() {
		var typ string
		var s model.LLMCallStat
		if err := rows.Scan(&typ, &s.Calls, &s.InputTokens, &s.OutputTokens, &s.CostUSD); err != nil {
			return nil, err
		}
		out[typ] = s
	}
	return out, rows.Err()
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

func (t *Tx) EnsureDataset(ctx context.Context, userID, name string) (int64, error) {
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_datasets (user_id, name, created_at) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, name) WHERE NOT deleted DO NOTHING`,
		userID, name, Now()); err != nil {
		return 0, fmt.Errorf("create dataset: %w", err)
	}
	return datasetID(ctx, t.tx, userID, name)
}

func (t *Tx) InsertMemory(ctx context.Context, dsID int64, text string, validAt, createdAt time.Time) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `INSERT INTO gb_memories (dataset_id, text, valid_at, created_at) VALUES ($1, $2, $3, $4) RETURNING id`,
		dsID, text, validAt.UTC(), createdAt.UTC()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert memory: %w", err)
	}
	return id, nil
}

func (t *Tx) UpsertEntity(ctx context.Context, dsID int64, e model.Entity, nameKey string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		INSERT INTO gb_entities (dataset_id, name, name_key, type, description) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (dataset_id, name_key, type) DO UPDATE SET
			description = CASE WHEN EXCLUDED.description <> '' THEN EXCLUDED.description ELSE gb_entities.description END,
			updated_at  = `+SQLNow+`
		RETURNING id`,
		dsID, e.Name, nameKey, e.Type, e.Description).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert entity %q: %w", e.Name, err)
	}
	return id, nil
}

func (t *Tx) FindEntityID(ctx context.Context, dsID int64, nameKey string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `SELECT id FROM gb_entities WHERE dataset_id = $1 AND name_key = $2 ORDER BY id LIMIT 1`, dsID, nameKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return id, err
}

func (t *Tx) InsertEdge(ctx context.Context, e model.NewEdge) (int64, error) {
	var invalid any
	if e.InvalidAt != nil {
		invalid = e.InvalidAt.UTC()
	}
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		INSERT INTO gb_edges (dataset_id, source_id, target_id, relation, description, valid_at, invalid_at, created_at, memory_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		e.DatasetID, e.SourceID, e.TargetID, e.Relation, e.Description, e.ValidAt.UTC(), invalid, e.CreatedAt.UTC(), e.MemoryID).Scan(&id)
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
	if _, err := t.tx.ExecContext(ctx, `UPDATE gb_edges SET invalid_at = COALESCE(invalid_at, $1::timestamptz) WHERE id = $2 AND dataset_id = $3`,
		invalid, id, dsID); err != nil {
		return fmt.Errorf("merge duplicate edge: %w", err)
	}
	return nil
}

func (t *Tx) GetEdge(ctx context.Context, dsID, id int64) (*model.Edge, error) {
	e, err := scanEdge(t.tx.QueryRowContext(ctx, `SELECT `+edgeColumns+edgeFrom+` WHERE e.id = $1 AND e.dataset_id = $2`, id, dsID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load edge %d: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("load edge %d: %w", id, err)
	}
	return &e, nil
}

func (t *Tx) InvalidateEdge(ctx context.Context, id int64, invalidAt, expiredAt time.Time) error {
	if _, err := t.tx.ExecContext(ctx, `UPDATE gb_edges SET invalid_at = $1, expired_at = $2 WHERE id = $3`,
		invalidAt.UTC(), expiredAt.UTC(), id); err != nil {
		return fmt.Errorf("invalidate edge: %w", err)
	}
	return nil
}
