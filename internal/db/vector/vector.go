// Package vector stores and searches embeddings in a vector database.
//
// Store is the backend-neutral interface; Chromem (embedded, pure Go, the default) and Pinecone (hosted) are the implementations.
package vector

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
	// Filter is an optional metadata filter in the backend's syntax (Chromem: equality only)
	// (Pinecone: https://docs.pinecone.io/guides/search/filter-by-metadata).
	Filter map[string]any
	// IncludeValues also returns the stored embeddings.
	IncludeValues bool
}

// Store is a vector database. A dataset is gligabao's unit of vector isolation: vectors of different datasets
// never mix, and a query only sees the vectors of the dataset it names. Each backend maps it to its own isolation
// primitive: a chromem collection, a Pinecone namespace. An empty dataset name is the backend's default one.
type Store interface {
	// Upsert inserts or overwrites vectors by ID.
	Upsert(ctx context.Context, dataset string, vectors []Vector) error
	// Query returns the vectors closest to the given embedding, best first.
	Query(ctx context.Context, dataset string, vector []float32, opts QueryOptions) ([]Match, error)
	// Delete removes vectors by ID. Unknown IDs are ignored.
	Delete(ctx context.Context, dataset string, ids []string) error
	// DeleteAll removes every vector in the dataset (and the dataset itself, if the backend has such a thing).
	DeleteAll(ctx context.Context, dataset string) error
}

// Stats describes the vectors of one dataset.
type Stats struct {
	// Backend names the store ("chromem", "pinecone").
	Backend string `json:"backend"`
	// Count is the number of vectors in the dataset (0 if it does not exist).
	Count int `json:"count"`
	// Dimensions is the size of each vector.
	Dimensions int `json:"dimensions"`
	// Bytes is the size of the raw embeddings (Count * Dimensions * 4), not counting IDs, metadata or index overhead.
	Bytes int64 `json:"bytes"`
}

// Statter is implemented by stores that can report statistics about a dataset; both Chromem and Pinecone do.
type Statter interface {
	// Stats returns the statistics of the dataset. A dataset that does not exist has zero Count.
	Stats(ctx context.Context, dataset string) (Stats, error)
}
