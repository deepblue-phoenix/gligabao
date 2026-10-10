// Package model holds the types shared by the db package and its backends (sqlite, postgres): the records they
// read and write, and the transaction interface. It has no dependencies so that every backend can import it.
package model

import (
	"context"
	"errors"
	"time"
)

// Errors returned by the backends.
var (
	// ErrNotFound means the requested row does not exist (or is not visible, e.g. soft-deleted).
	ErrNotFound = errors.New("not found")
	// ErrExists means a live row with that name already exists.
	ErrExists = errors.New("already exists")
)

//---------------------------------------------------------------------------------------------

// Dataset describes a dataset.
type Dataset struct {
	ID          int64      `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Tags        []string   `json:"tags"`
	CreatedAt   time.Time  `json:"created_at"`
	Deleted     bool       `json:"deleted"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

// Entity is a thing mentioned in the text (person, place, project, concept, ...).
type Entity struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// Edge is a fact relating two entities (referenced by name), with its bi-temporal validity.
// Description is the fact as a sentence, e.g. "Alice works at Acme."
type Edge struct {
	ID          int64      `json:"id,omitempty"`
	Source      string     `json:"source"`
	Target      string     `json:"target"`
	Relation    string     `json:"relation"`
	Description string     `json:"description,omitempty"`
	ValidAt     *time.Time `json:"valid_at,omitempty"`   // when the fact became true
	InvalidAt   *time.Time `json:"invalid_at,omitempty"` // when it stopped being true; nil while it is still true
	CreatedAt   *time.Time `json:"created_at,omitempty"` // when we learned about it
	ExpiredAt   *time.Time `json:"expired_at,omitempty"` // when we learned it was superseded
}

// EvidenceEntity is a graph entity cited by an answer.
type EvidenceEntity struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// EvidenceEdge is a graph edge cited by an answer.
type EvidenceEdge struct {
	ID          int64  `json:"id"`
	SourceID    int64  `json:"source_id"`
	TargetID    int64  `json:"target_id"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	Relation    string `json:"relation"`
	Description string `json:"description,omitempty"`
	MemoryID    *int64 `json:"memory_id,omitempty"`
	// Bi-temporal validity of the fact.
	ValidAt   *time.Time `json:"valid_at,omitempty"`
	InvalidAt *time.Time `json:"invalid_at,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	ExpiredAt *time.Time `json:"expired_at,omitempty"`
}

// NewEdge is an edge to insert; endpoints are entity IDs.
type NewEdge struct {
	DatasetID, SourceID, TargetID int64
	Relation, Description         string
	ValidAt                       time.Time
	InvalidAt                     *time.Time
	CreatedAt                     time.Time
	MemoryID                      int64
}

// Memory is a stored episode (the raw text plus when it happened and when it was ingested).
type Memory struct {
	ID        int64
	DatasetID int64
	Text      string
	ValidAt   time.Time
	CreatedAt time.Time
}

// DatasetCounts is the number of rows a dataset holds.
type DatasetCounts struct {
	Memories, Entities, Edges int
}

// LLMCallStat sums the logged LLM calls of one type.
type LLMCallStat struct {
	Calls        int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

//---------------------------------------------------------------------------------------------

// Tx is a backend transaction covering everything one Remember writes. All "not found" results are ErrNotFound.
type Tx interface {
	Commit() error
	// Rollback is safe to call after Commit.
	Rollback() error

	// EnsureDataset returns the ID of userID's live dataset called name, creating it first if needed.
	EnsureDataset(ctx context.Context, userID, name string) (int64, error)
	// InsertMemory stores an episode.
	InsertMemory(ctx context.Context, dsID int64, text string, validAt, createdAt time.Time) (int64, error)
	// UpsertEntity inserts the entity, or updates the one with the same (dataset, nameKey, type): a non-empty
	// description replaces the stored one. It returns the entity ID.
	UpsertEntity(ctx context.Context, dsID int64, e Entity, nameKey string) (int64, error)
	// FindEntityID returns the oldest entity of the dataset with the given nameKey, of any type.
	FindEntityID(ctx context.Context, dsID int64, nameKey string) (int64, error)
	// InsertEdge stores an edge and returns its ID.
	InsertEdge(ctx context.Context, e NewEdge) (int64, error)
	// MergeDuplicateEdge records the end of an already stored fact if it has none yet (invalidAt may be nil).
	MergeDuplicateEdge(ctx context.Context, dsID, id int64, invalidAt *time.Time) error
	// GetEdge reads one edge with its endpoint names, inside the dataset only.
	GetEdge(ctx context.Context, dsID, id int64) (*Edge, error)
	// InvalidateEdge sets the end of a fact (event time) and when we learned it (system time).
	InvalidateEdge(ctx context.Context, id int64, invalidAt, expiredAt time.Time) error
}
