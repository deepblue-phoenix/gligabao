// Package sqlite manages the SQLite database: connection, schema, the timestamp storage convention and every
// SQL statement the application runs. The functions here are reached through the routing functions of package db.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ivan/gligabao/internal/db/model"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

//---------------------------------------------------------------------------------------------

// Open opens (creating if needed) the SQLite database at path and applies the schema.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	d, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := initSchema(d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

//---------------------------------------------------------------------------------------------
// Datasets

const datasetSelect = `SELECT id, user_id, name, description, tags, created_at, deleted, deleted_at FROM gb_datasets`

type scanner interface{ Scan(dest ...any) error }

func scanDataset(r scanner) (*model.Dataset, error) {
	var ds model.Dataset
	var tags, created string
	var deleted int
	var deletedAt sql.NullString
	if err := r.Scan(&ds.ID, &ds.UserID, &ds.Name, &ds.Description, &tags, &created, &deleted, &deletedAt); err != nil {
		return nil, err
	}
	ds.Tags = []string{}
	if err := json.Unmarshal([]byte(tags), &ds.Tags); err != nil || ds.Tags == nil {
		ds.Tags = []string{}
	}
	if t := ParseTime(created); t != nil {
		ds.CreatedAt = *t
	}
	ds.Deleted = deleted == 1
	ds.DeletedAt = ParseTime(deletedAt.String)
	return &ds, nil
}

// CreateDataset creates a dataset owned by userID. It returns model.ErrExists if a live dataset with that name
// exists for the user.
func CreateDataset(ctx context.Context, d *sql.DB, userID, name, description string, tags []string) (*model.Dataset, error) {
	tagsJSON, _ := json.Marshal(tags)
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_datasets (user_id, name, description, tags, created_at)
		SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM gb_datasets WHERE user_id = ? AND name = ? AND deleted = 0)`,
		userID, name, description, string(tagsJSON), FormatTime(Now()), userID, name)
	if err != nil {
		return nil, fmt.Errorf("create dataset: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, model.ErrExists
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanDataset(d.QueryRowContext(ctx, datasetSelect+` WHERE id = ?`, id))
}

// ListDatasets returns userID's datasets oldest first; deleted ones only if includeDeleted.
func ListDatasets(ctx context.Context, d *sql.DB, userID string, includeDeleted bool) ([]model.Dataset, error) {
	q := datasetSelect + ` WHERE user_id = ?`
	if !includeDeleted {
		q += ` AND deleted = 0`
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
	res, err := d.ExecContext(ctx, `UPDATE gb_datasets SET deleted = 1, deleted_at = ? WHERE user_id = ? AND name = ? AND deleted = 0`,
		FormatTime(Now()), userID, name)
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
	err := q.QueryRowContext(ctx, `SELECT id FROM gb_datasets WHERE user_id = ? AND name = ? AND deleted = 0`, userID, name).Scan(&id)
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
		INSERT INTO gb_llm_calls (type, provider, model, input_tokens, output_tokens, cost_usd) VALUES (?, ?, ?, ?, ?, ?)`,
		kind, provider, modelName, inputTokens, outputTokens, costUSD)
	return err
}

//---------------------------------------------------------------------------------------------
// Edges

const edgeColumns = `e.id, s.name, t.name, e.relation, e.description, e.valid_at, e.invalid_at, e.created_at, e.expired_at`
const edgeFrom = ` FROM gb_edges e JOIN gb_entities s ON s.id = e.source_id JOIN gb_entities t ON t.id = e.target_id`

func scanEdge(r scanner) (model.Edge, error) {
	var e model.Edge
	var valid, invalid, created, expired sql.NullString
	if err := r.Scan(&e.ID, &e.Source, &e.Target, &e.Relation, &e.Description, &valid, &invalid, &created, &expired); err != nil {
		return e, err
	}
	e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = ParseTime(valid.String), ParseTime(invalid.String), ParseTime(created.String), ParseTime(expired.String)
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
	rows, err := d.QueryContext(ctx, `SELECT `+edgeColumns+edgeFrom+`
		WHERE e.dataset_id = ? AND e.expired_at IS NULL AND (s.name_key IN (`+ph+`) OR t.name_key IN (`+ph+`))
		ORDER BY e.id DESC LIMIT ?`, args...)
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
		WHERE ds.user_id = ? AND ds.name = ? AND ds.deleted = 0 ORDER BY e.id`, userID, name)
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
	rows, err := d.QueryContext(ctx, `SELECT id FROM gb_entities WHERE dataset_id = ? ORDER BY updated_at DESC, id DESC LIMIT ?`, dsID, limit)
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
	rows, err := d.QueryContext(ctx, `SELECT id FROM gb_entities WHERE dataset_id = ? AND (`+strings.Join(conds, " OR ")+`)
		ORDER BY (`+strings.Join(score, " + ")+`) DESC, updated_at DESC, id DESC LIMIT ?`, args...)
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
		SELECT e.id, e.source_id, e.target_id, s.name, t.name, e.relation, e.description, e.memory_id,
			e.valid_at, e.invalid_at, e.created_at, e.expired_at
		FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id
		JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id = ? AND (e.source_id IN (`+ph+`) OR e.target_id IN (`+ph+`))
		ORDER BY e.id DESC LIMIT ?`, args...)
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
			&valid, &invalid, &created, &expired); err != nil {
			return nil, err
		}
		e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = ParseTime(valid.String), ParseTime(invalid.String), ParseTime(created.String), ParseTime(expired.String)
		if mem.Valid {
			e.MemoryID = &mem.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EntitiesByID returns the entities with the given IDs.
func EntitiesByID(ctx context.Context, d *sql.DB, ids []int64) ([]model.EvidenceEntity, error) {
	ph, args := placeholders(ids)
	rows, err := d.QueryContext(ctx, `SELECT id, name, type, description FROM gb_entities WHERE id IN (`+ph+`)`, args...)
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

// DatasetCounts returns the number of memories, entities and edges in the dataset.
func DatasetCounts(ctx context.Context, d *sql.DB, dsID int64) (model.DatasetCounts, error) {
	var c model.DatasetCounts
	err := d.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM gb_memories WHERE dataset_id = ?),
		(SELECT COUNT(*) FROM gb_entities WHERE dataset_id = ?),
		(SELECT COUNT(*) FROM gb_edges WHERE dataset_id = ?)`, dsID, dsID, dsID).Scan(&c.Memories, &c.Entities, &c.Edges)
	return c, err
}

// ListMemories returns the episodes of the dataset by ID.
func ListMemories(ctx context.Context, d *sql.DB, dsID int64) ([]model.Memory, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, dataset_id, text, valid_at, created_at FROM gb_memories WHERE dataset_id = ? ORDER BY id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Memory
	for rows.Next() {
		var m model.Memory
		var valid, created string
		if err := rows.Scan(&m.ID, &m.DatasetID, &m.Text, &valid, &created); err != nil {
			return nil, err
		}
		if t := ParseTime(valid); t != nil {
			m.ValidAt = *t
		}
		if t := ParseTime(created); t != nil {
			m.CreatedAt = *t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListEntities returns the entities of the dataset by ID.
func ListEntities(ctx context.Context, d *sql.DB, dsID int64) ([]model.EvidenceEntity, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, name, type, description FROM gb_entities WHERE dataset_id = ? ORDER BY id`, dsID)
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

// CountCrossDatasetEdges returns the number of edges whose dataset differs from that of one of their endpoints
// (always 0 unless datasets leak into each other).
func CountCrossDatasetEdges(ctx context.Context, d *sql.DB) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id != s.dataset_id OR e.dataset_id != t.dataset_id`).Scan(&n)
	return n, err
}

// LLMCallStats sums the logged LLM calls by type.
func LLMCallStats(ctx context.Context, d *sql.DB) (map[string]model.LLMCallStat, error) {
	rows, err := d.QueryContext(ctx, `SELECT type, COUNT(*), SUM(input_tokens), SUM(output_tokens), SUM(cost_usd) FROM gb_llm_calls GROUP BY type`)
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

func (t *Tx) EnsureDataset(ctx context.Context, userID, name string) (int64, error) {
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_datasets (user_id, name, created_at)
		SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM gb_datasets WHERE user_id = ? AND name = ? AND deleted = 0)`,
		userID, name, FormatTime(Now()), userID, name); err != nil {
		return 0, fmt.Errorf("create dataset: %w", err)
	}
	return datasetID(ctx, t.tx, userID, name)
}

func (t *Tx) InsertMemory(ctx context.Context, dsID int64, text string, validAt, createdAt time.Time) (int64, error) {
	res, err := t.tx.ExecContext(ctx, `INSERT INTO gb_memories (dataset_id, text, valid_at, created_at) VALUES (?, ?, ?, ?)`,
		dsID, text, FormatTime(validAt), FormatTime(createdAt))
	if err != nil {
		return 0, fmt.Errorf("insert memory: %w", err)
	}
	return res.LastInsertId()
}

func (t *Tx) UpsertEntity(ctx context.Context, dsID int64, e model.Entity, nameKey string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx, `
		INSERT INTO gb_entities (dataset_id, name, name_key, type, description) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (dataset_id, name_key, type) DO UPDATE SET
			description = CASE WHEN excluded.description != '' THEN excluded.description ELSE gb_entities.description END,
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
	err := t.tx.QueryRowContext(ctx, `SELECT id FROM gb_entities WHERE dataset_id = ? AND name_key = ? ORDER BY id LIMIT 1`, dsID, nameKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return id, err
}

func (t *Tx) InsertEdge(ctx context.Context, e model.NewEdge) (int64, error) {
	res, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_edges (dataset_id, source_id, target_id, relation, description, valid_at, invalid_at, created_at, memory_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.DatasetID, e.SourceID, e.TargetID, e.Relation, e.Description, FormatTime(e.ValidAt), FormatTimePtr(e.InvalidAt),
		FormatTime(e.CreatedAt), e.MemoryID)
	if err != nil {
		return 0, fmt.Errorf("insert edge: %w", err)
	}
	return res.LastInsertId()
}

func (t *Tx) MergeDuplicateEdge(ctx context.Context, dsID, id int64, invalidAt *time.Time) error {
	if _, err := t.tx.ExecContext(ctx, `UPDATE gb_edges SET invalid_at = COALESCE(invalid_at, ?) WHERE id = ? AND dataset_id = ?`,
		FormatTimePtr(invalidAt), id, dsID); err != nil {
		return fmt.Errorf("merge duplicate edge: %w", err)
	}
	return nil
}

func (t *Tx) GetEdge(ctx context.Context, dsID, id int64) (*model.Edge, error) {
	e, err := scanEdge(t.tx.QueryRowContext(ctx, `SELECT `+edgeColumns+edgeFrom+` WHERE e.id = ? AND e.dataset_id = ?`, id, dsID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load edge %d: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("load edge %d: %w", id, err)
	}
	return &e, nil
}

func (t *Tx) InvalidateEdge(ctx context.Context, id int64, invalidAt, expiredAt time.Time) error {
	if _, err := t.tx.ExecContext(ctx, `UPDATE gb_edges SET invalid_at = ?, expired_at = ? WHERE id = ?`,
		FormatTime(invalidAt), FormatTime(expiredAt), id); err != nil {
		return fmt.Errorf("invalidate edge: %w", err)
	}
	return nil
}
