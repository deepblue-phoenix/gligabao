package vectordb

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3" // CGO SQLite driver; sqlite-vec is loaded into it via Auto()
)

// maxKNN is sqlite-vec's limit on the k of a KNN query.
const maxKNN = 4096

var registerVec sync.Once

// SQLite is a Store backed by a SQLite database using the sqlite-vec extension (CGO).
//
// All vectors live in one vec0 virtual table, with the namespace as a partition key so that
// searches only scan their own namespace. Distances use the cosine metric, and Match.Score is
// the cosine similarity (1 - distance), like a Pinecone cosine index.
type SQLite struct {
	db   *sql.DB
	dims int
}

var _ Store = (*SQLite)(nil)

// NewSQLite opens (creating if needed) the SQLite vector database at path.
// dimensions is the embedding size; it is fixed once the database is created.
// Use ":memory:" for a throwaway in-memory store.
func NewSQLite(path string, dimensions int) (*SQLite, error) {
	if dimensions < 1 {
		return nil, fmt.Errorf("vectordb: invalid dimensions %d", dimensions)
	}
	registerVec.Do(sqlitevec.Auto)

	dsn := path
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("vectordb: create dir: %w", err)
		}
		dsn = "file:" + path + "?_busy_timeout=5000&_journal_mode=WAL"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("vectordb: open sqlite: %w", err)
	}
	// One connection: serializes writes (no SQLITE_BUSY) and keeps ":memory:" a single database.
	db.SetMaxOpenConns(1)

	s := &SQLite{db: db, dims: dimensions}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) init() error {
	var ver string
	if err := s.db.QueryRow(`SELECT vec_version()`).Scan(&ver); err != nil {
		return fmt.Errorf("vectordb: sqlite-vec not available: %w", err)
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS gb_vector_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("vectordb: init: %w", err)
	}
	var stored string
	err := s.db.QueryRow(`SELECT value FROM gb_vector_meta WHERE key = 'dimensions'`).Scan(&stored)
	switch {
	case err == nil:
		if stored != strconv.Itoa(s.dims) {
			return fmt.Errorf("vectordb: database has %s-dimensional vectors, configured embedding_dimensions is %d", stored, s.dims)
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("vectordb: init: %w", err)
	}
	// vkey is "<namespace>/<id>" (see rowKey); id and metadata are auxiliary (stored, not indexed) columns.
	_, err = s.db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS gb_vectors USING vec0(
		vkey TEXT PRIMARY KEY,
		namespace TEXT PARTITION KEY,
		embedding FLOAT[%d] distance_metric=cosine,
		+id TEXT,
		+metadata TEXT
	)`, s.dims))
	if err != nil {
		return fmt.Errorf("vectordb: create vector table: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO gb_vector_meta (key, value) VALUES ('dimensions', ?)`, strconv.Itoa(s.dims)); err != nil {
		return fmt.Errorf("vectordb: init: %w", err)
	}
	return nil
}

// rowKey builds the unique primary key of a vector; the length prefix keeps it unambiguous.
func rowKey(namespace, id string) string {
	return strconv.Itoa(len(namespace)) + ":" + namespace + ":" + id
}

//---------------------------------------------------------------------------------------------

// Upsert inserts or overwrites vectors by ID, atomically.
func (s *SQLite) Upsert(ctx context.Context, namespace string, vectors []Vector) error {
	type row struct {
		key, id string
		blob    []byte
		meta    []byte
	}
	rows := make([]row, len(vectors))
	for i, v := range vectors {
		if v.ID == "" {
			return fmt.Errorf("vectordb: vector %d has no ID", i)
		}
		if len(v.Values) != s.dims {
			return fmt.Errorf("vectordb: vector %q has %d dimensions, want %d", v.ID, len(v.Values), s.dims)
		}
		blob, err := sqlitevec.SerializeFloat32(v.Values)
		if err != nil {
			return fmt.Errorf("vectordb: serialize %q: %w", v.ID, err)
		}
		meta := []byte("{}")
		if len(v.Metadata) > 0 {
			if meta, err = json.Marshal(v.Metadata); err != nil {
				return fmt.Errorf("vectordb: marshal metadata of %q: %w", v.ID, err)
			}
		}
		rows[i] = row{key: rowKey(namespace, v.ID), id: v.ID, blob: blob, meta: meta}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("vectordb: begin: %w", err)
	}
	defer tx.Rollback()
	// vec0 tables don't support INSERT OR REPLACE, so replace = delete + insert.
	del, err := tx.PrepareContext(ctx, `DELETE FROM gb_vectors WHERE vkey = ?`)
	if err != nil {
		return fmt.Errorf("vectordb: prepare: %w", err)
	}
	defer del.Close()
	ins, err := tx.PrepareContext(ctx, `INSERT INTO gb_vectors (vkey, namespace, embedding, id, metadata) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("vectordb: prepare: %w", err)
	}
	defer ins.Close()
	for _, r := range rows {
		if _, err := del.ExecContext(ctx, r.key); err != nil {
			return fmt.Errorf("vectordb: upsert %q: %w", r.id, err)
		}
		if _, err := ins.ExecContext(ctx, r.key, namespace, r.blob, r.id, string(r.meta)); err != nil {
			return fmt.Errorf("vectordb: upsert %q: %w", r.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("vectordb: commit: %w", err)
	}
	return nil
}

// Query returns the vectors closest to the given embedding, best first.
//
// opts.Filter supports equality only: {"key": value} or {"key": {"$eq": value}}, all of which
// must match. It is applied after the nearest-neighbour search over the whole namespace
// (up to 4096 candidates), so it never returns more than TopK matches.
func (s *SQLite) Query(ctx context.Context, namespace string, vector []float32, opts QueryOptions) ([]Match, error) {
	if len(vector) != s.dims {
		return nil, fmt.Errorf("vectordb: query vector has %d dimensions, want %d", len(vector), s.dims)
	}
	filter, err := parseEqFilter(opts.Filter)
	if err != nil {
		return nil, err
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	k := min(topK, maxKNN)
	if len(filter) > 0 {
		k = maxKNN
	}
	blob, err := sqlitevec.SerializeFloat32(vector)
	if err != nil {
		return nil, fmt.Errorf("vectordb: serialize query: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, metadata, distance, embedding FROM gb_vectors
		WHERE embedding MATCH ? AND k = ? AND namespace = ? ORDER BY distance`, blob, k, namespace)
	if err != nil {
		return nil, fmt.Errorf("vectordb: query: %w", err)
	}
	defer rows.Close()

	var out []Match
	for rows.Next() && len(out) < topK {
		var (
			id, metaJSON string
			dist         float64
			emb          []byte
		)
		if err := rows.Scan(&id, &metaJSON, &dist, &emb); err != nil {
			return nil, fmt.Errorf("vectordb: scan: %w", err)
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
			return nil, fmt.Errorf("vectordb: decode metadata of %q: %w", id, err)
		}
		if !filter.match(meta) {
			continue
		}
		m := Match{ID: id, Score: float32(1 - dist), Metadata: meta}
		if opts.IncludeValues {
			m.Values = deserializeFloat32(emb)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vectordb: query: %w", err)
	}
	return out, nil
}

// Delete removes vectors by ID. Unknown IDs are ignored.
func (s *SQLite) Delete(ctx context.Context, namespace string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("vectordb: begin: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gb_vectors WHERE vkey = ?`, rowKey(namespace, id)); err != nil {
			return fmt.Errorf("vectordb: delete %q: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("vectordb: commit: %w", err)
	}
	return nil
}

// DeleteAll removes every vector in the namespace.
func (s *SQLite) DeleteAll(ctx context.Context, namespace string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gb_vectors WHERE namespace = ?`, namespace); err != nil {
		return fmt.Errorf("vectordb: delete namespace: %w", err)
	}
	return nil
}

//---------------------------------------------------------------------------------------------

type eqFilter map[string]any

func (f eqFilter) match(meta map[string]any) bool {
	for k, want := range f {
		got, ok := meta[k]
		if !ok || fmt.Sprint(got) != fmt.Sprint(want) {
			return false
		}
	}
	return true
}

func parseEqFilter(in map[string]any) (eqFilter, error) {
	out := make(eqFilter, len(in))
	for k, v := range in {
		if strings.HasPrefix(k, "$") {
			return nil, fmt.Errorf("vectordb: sqlite filter: unsupported operator %q (only equality)", k)
		}
		if op, ok := v.(map[string]any); ok {
			eq, only := op["$eq"]
			if !only || len(op) != 1 {
				return nil, fmt.Errorf("vectordb: sqlite filter on %q: only $eq is supported", k)
			}
			v = eq
		}
		out[k] = v
	}
	return out, nil
}

func deserializeFloat32(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}
