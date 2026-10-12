package vector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/philippgille/chromem-go"
)

const (
	// chromemMemory is the path that selects a throwaway in-memory store.
	chromemMemory = ":memory:"
	// defaultCollection is the chromem collection used for the empty dataset name (chromem rejects empty names).
	defaultCollection = "_default"
	// dimensionsFile records the embedding size next to the persisted collections.
	dimensionsFile = "gligabao-dimensions"
	// metaKey holds the full JSON metadata of a vector; filterPrefix+key holds a string copy of each
	// metadata value, which is what chromem's equality filter runs against.
	metaKey      = "_meta"
	filterPrefix = "m:"
)

// Chromem is a Store backed by chromem-go (https://github.com/philippgille/chromem-go), a pure-Go
// (no CGO) embeddable vector database. Each dataset is one chromem collection (named after the dataset); with a directory
// the collections are persisted there (one file per vector) and loaded into memory on startup.
//
// Search is an exhaustive cosine-similarity scan of the dataset, and Match.Score is the cosine
// similarity, like a Pinecone cosine index. Stored vectors are normalized to unit length, so
// Match.Values are normalized too.
type Chromem struct {
	db   *chromem.DB
	dims int

	mu sync.Mutex // serializes collection creation/deletion
}

var _ Store = (*Chromem)(nil)

// NewChromem opens (creating if needed) the vector store persisted in the directory dir.
// dimensions is the embedding size; it is fixed once the store is created.
// Use ":memory:" for a throwaway in-memory store.
func NewChromem(dir string, dimensions int) (*Chromem, error) {
	if dimensions < 1 {
		return nil, fmt.Errorf("vector: invalid dimensions %d", dimensions)
	}
	if dir == chromemMemory {
		return &Chromem{db: chromem.NewDB(), dims: dimensions}, nil
	}
	if dir == "" {
		return nil, errors.New("vector: empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("vector: create dir: %w", err)
	}
	if err := checkDimensions(filepath.Join(dir, dimensionsFile), dimensions); err != nil {
		return nil, err
	}
	db, err := chromem.NewPersistentDB(dir, false)
	if err != nil {
		return nil, fmt.Errorf("vector: open chromem: %w", err)
	}
	return &Chromem{db: db, dims: dimensions}, nil
}

// checkDimensions verifies the size recorded in path, or records it if there is none yet.
func checkDimensions(path string, dims int) error {
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if stored := strings.TrimSpace(string(b)); stored != strconv.Itoa(dims) {
			return fmt.Errorf("vector: store has %s-dimensional vectors, configured embedding_dimensions is %d", stored, dims)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("vector: init: %w", err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(dims)+"\n"), 0o644); err != nil {
		return fmt.Errorf("vector: init: %w", err)
	}
	return nil
}

// Close is a no-op: chromem writes every change to disk immediately.
func (s *Chromem) Close() error { return nil }

func collectionName(dataset string) string {
	if dataset == "" {
		return defaultCollection
	}
	return dataset
}

// collection returns the dataset's collection, creating it if create is set (nil otherwise if missing).
// Embeddings are always supplied by the caller, so the collection's embedding function is a stub.
func (s *Chromem) collection(dataset string, create bool) (*chromem.Collection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := collectionName(dataset)
	if c := s.db.GetCollection(name, noEmbed); c != nil || !create {
		return c, nil
	}
	c, err := s.db.CreateCollection(name, nil, noEmbed)
	if err != nil {
		return nil, fmt.Errorf("vector: create dataset %q: %w", dataset, err)
	}
	return c, nil
}

func noEmbed(context.Context, string) ([]float32, error) {
	return nil, errors.New("vector: text embedding is not supported; supply embeddings")
}

//---------------------------------------------------------------------------------------------

// Upsert inserts or overwrites vectors by ID. All vectors are validated before any is written.
func (s *Chromem) Upsert(ctx context.Context, dataset string, vectors []Vector) error {
	docs := make([]chromem.Document, len(vectors))
	for i, v := range vectors {
		if v.ID == "" {
			return fmt.Errorf("vector: vector %d has no ID", i)
		}
		if len(v.Values) != s.dims {
			return fmt.Errorf("vector: vector %q has %d dimensions, want %d", v.ID, len(v.Values), s.dims)
		}
		if isZero(v.Values) {
			return fmt.Errorf("vector: vector %q is all zeros", v.ID)
		}
		meta, err := encodeMetadata(v.Metadata)
		if err != nil {
			return fmt.Errorf("vector: metadata of %q: %w", v.ID, err)
		}
		docs[i] = chromem.Document{ID: v.ID, Embedding: v.Values, Metadata: meta}
	}
	if len(docs) == 0 {
		return nil
	}
	c, err := s.collection(dataset, true)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := c.AddDocument(ctx, d); err != nil {
			return fmt.Errorf("vector: upsert %q: %w", d.ID, err)
		}
	}
	return nil
}

// Query returns the vectors closest to the given embedding, best first.
//
// opts.Filter supports equality only: {"key": value} or {"key": {"$eq": value}}, all of which
// must match (values are compared by their printed form). It is applied before the search, so
// up to TopK matches are returned whenever that many vectors match.
func (s *Chromem) Query(ctx context.Context, dataset string, vector []float32, opts QueryOptions) ([]Match, error) {
	if len(vector) != s.dims {
		return nil, fmt.Errorf("vector: query vector has %d dimensions, want %d", len(vector), s.dims)
	}
	if isZero(vector) {
		return nil, errors.New("vector: query vector is all zeros")
	}
	filter, err := parseEqFilter(opts.Filter)
	if err != nil {
		return nil, err
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	c, err := s.collection(dataset, false)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, nil
	}
	// chromem errors if asked for more results than the collection holds.
	n := c.Count()
	if n == 0 {
		return nil, nil
	}
	res, err := c.QueryEmbedding(ctx, vector, min(topK, n), filter, nil)
	if err != nil {
		return nil, fmt.Errorf("vector: query: %w", err)
	}
	out := make([]Match, 0, len(res))
	for _, r := range res {
		meta, err := decodeMetadata(r.Metadata)
		if err != nil {
			return nil, fmt.Errorf("vector: decode metadata of %q: %w", r.ID, err)
		}
		m := Match{ID: r.ID, Score: r.Similarity, Metadata: meta}
		if opts.IncludeValues {
			m.Values = r.Embedding
		}
		out = append(out, m)
	}
	return out, nil
}

// Delete removes vectors by ID. Unknown IDs are ignored.
func (s *Chromem) Delete(ctx context.Context, dataset string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	c, err := s.collection(dataset, false)
	if err != nil || c == nil {
		return err
	}
	if err := c.Delete(ctx, nil, nil, ids...); err != nil {
		return fmt.Errorf("vector: delete: %w", err)
	}
	return nil
}

// DeleteAll removes every vector in the dataset by deleting its collection.
func (s *Chromem) DeleteAll(_ context.Context, dataset string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.DeleteCollection(collectionName(dataset)); err != nil {
		return fmt.Errorf("vector: delete dataset: %w", err)
	}
	return nil
}

//---------------------------------------------------------------------------------------------

// encodeMetadata converts metadata to chromem's string map: the JSON of the whole thing (so types
// survive the round trip) plus a printed copy of each value for filtering.
func encodeMetadata(meta map[string]any) (map[string]string, error) {
	if len(meta) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(meta)+1)
	out[metaKey] = string(b)
	for k, v := range meta {
		out[filterPrefix+k] = fmt.Sprint(v)
	}
	return out, nil
}

func decodeMetadata(m map[string]string) (map[string]any, error) {
	raw, ok := m[metaKey]
	if !ok {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// parseEqFilter converts a Pinecone-style equality filter to a chromem "where" map.
func parseEqFilter(in map[string]any) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.HasPrefix(k, "$") {
			return nil, fmt.Errorf("vector: filter: unsupported operator %q (only equality)", k)
		}
		if op, ok := v.(map[string]any); ok {
			eq, only := op["$eq"]
			if !only || len(op) != 1 {
				return nil, fmt.Errorf("vector: filter on %q: only $eq is supported", k)
			}
			v = eq
		}
		out[filterPrefix+k] = fmt.Sprint(v)
	}
	return out, nil
}

func isZero(v []float32) bool {
	var sq float64
	for _, x := range v {
		sq += float64(x) * float64(x)
	}
	return sq == 0 || math.IsNaN(sq) || math.IsInf(sq, 0)
}
