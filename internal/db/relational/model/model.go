// Package model holds the types shared by package relational and its backends (sqlite, postgres): the records they
// read and write. It has no dependencies so that every backend can import it. Graph records live in
// internal/db/graph/model.
package model

import (
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

// Ontology is the metadata of a loaded ontology. The ontology itself (classes and relationships) is stored in the
// graph database (see internal/db/graph).
type Ontology struct {
	ID          int64     `json:"id"`
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	IRI         string    `json:"iri,omitempty"`   // the owl:Ontology IRI declared by the document
	Title       string    `json:"title,omitempty"` // its label
	Format      string    `json:"format"`          // serialization of the loaded document, e.g. "rdfxml"
	SizeBytes   int       `json:"size_bytes"`      // size of the loaded document
	SHA256      string    `json:"sha256"`          // hex digest of the loaded document
	Classes     int       `json:"classes"`
	Properties  int       `json:"properties"` // distinct properties that became edges
	Edges       int       `json:"edges"`      // relationships between classes, subclass_of ones included
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"` // when the ontology was last loaded or replaced; equals created_at until updated
}

// LLMCallStat sums the logged LLM calls of one type.
type LLMCallStat struct {
	Calls        int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}
