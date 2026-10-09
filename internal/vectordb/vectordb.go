// Package vectordb stores and searches embeddings in a vector database.
//
// Store is the backend-neutral interface; Pinecone is the current implementation.
package vectordb

import "context"

// Vector is an embedding with an ID and optional metadata.
type Vector struct {
	ID       string
	Values   []float32
	Metadata map[string]any
}

// Match is a search result.
type Match struct {
	ID       string
	Score    float32 // similarity; higher is closer (the metric is defined by the index)
	Values   []float32
	Metadata map[string]any
}

// QueryOptions tunes a similarity search.
type QueryOptions struct {
	// TopK is the max number of matches to return (default 10).
	TopK int
	// Filter is an optional metadata filter in the backend's syntax
	// (Pinecone: https://docs.pinecone.io/guides/search/filter-by-metadata).
	Filter map[string]any
	// IncludeValues also returns the stored embeddings.
	IncludeValues bool
}

// Store is a vector database. Namespaces isolate vectors from each other; gligabao
// uses one namespace per dataset. An empty namespace is the backend's default one.
type Store interface {
	// Upsert inserts or overwrites vectors by ID.
	Upsert(ctx context.Context, namespace string, vectors []Vector) error
	// Query returns the vectors closest to the given embedding, best first.
	Query(ctx context.Context, namespace string, vector []float32, opts QueryOptions) ([]Match, error)
	// Delete removes vectors by ID. Unknown IDs are ignored.
	Delete(ctx context.Context, namespace string, ids []string) error
	// DeleteAll removes every vector in the namespace.
	DeleteAll(ctx context.Context, namespace string) error
}
