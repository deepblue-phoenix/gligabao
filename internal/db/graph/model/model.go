// Package model holds the types shared by package graph and its backends (sqlite, postgres, ...): the graph
// records they read and write, and the transaction interface. It has no dependencies so that every backend can
// import it.
package model

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound means the requested graph record does not exist.
var ErrNotFound = errors.New("not found")

//---------------------------------------------------------------------------------------------

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

// Counts is the number of rows of each kind a dataset holds.
type Counts struct {
	Memories, Entities, Edges int
}

//---------------------------------------------------------------------------------------------

// Tx is a backend transaction covering all the graph writes of one Remember. All "not found" results are ErrNotFound.
type Tx interface {
	Commit() error
	// Rollback is safe to call after Commit.
	Rollback() error

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
