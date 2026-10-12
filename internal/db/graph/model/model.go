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
	ArtifactID  *int64 `json:"artifact_id,omitempty"`
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
	ArtifactID                    int64
}

// Artifact kinds.
const (
	// ArtifactEpisode is a Graphiti-style episode: a text anchored in time, written or observed at ValidAt.
	ArtifactEpisode = "episode"
	// ArtifactKnowledge is knowledge that is not strictly episodic (a reference text, a note, ...).
	ArtifactKnowledge = "knowledge"
)

// Artifact is a piece of data ingested to be remembered: the raw text, plus when it happened and when it was
// ingested. Every entity and edge of the graph is anchored to the artifact(s) it was inferred from.
type Artifact struct {
	ID        int64
	DatasetID int64
	Kind      string // ArtifactEpisode or ArtifactKnowledge
	Text      string
	ValidAt   time.Time
	CreatedAt time.Time
}

// EntityArtifact says that an entity was inferred from (mentioned in) an artifact.
type EntityArtifact struct {
	EntityID, ArtifactID int64
}

// EdgeArtifact says that an edge (a fact) was inferred from an artifact: the one that created it, or one that
// stated the same fact again.
type EdgeArtifact struct {
	EdgeID, ArtifactID int64
}

// Counts is the number of rows of each kind a dataset holds.
type Counts struct {
	Artifacts, Entities, Edges int
}

// OntologyClass is a class of an ontology (a node of the ontology graph).
type OntologyClass struct {
	IRI     string `json:"iri"`
	Name    string `json:"name"`
	Label   string `json:"label,omitempty"`
	Comment string `json:"comment,omitempty"`
	// Declared only matters to MergeOntology: true if the document defines the class itself, false if it merely
	// mentions it as an edge endpoint. It is not stored.
	Declared bool `json:"-"`
}

// OntologyCounts is the size of a stored ontology.
type OntologyCounts struct {
	Classes    int
	Properties int // distinct properties among the edges
	Edges      int
}

// OntologyEdge is a relationship between two classes of an ontology, referenced by IRI. Kind is "subclass_of",
// "equivalent_class", "disjoint_with" or "property"; Relation is the edge name (the property name for "property");
// IRI is the property or predicate IRI. Label, Comment and InverseOf describe a property.
type OntologyEdge struct {
	SourceIRI string `json:"source"`
	TargetIRI string `json:"target"`
	Relation  string `json:"relation"`
	Kind      string `json:"kind"`
	IRI       string `json:"iri"`
	Label     string `json:"label,omitempty"`
	Comment   string `json:"comment,omitempty"`
	InverseOf string `json:"inverse_of,omitempty"`
}

//---------------------------------------------------------------------------------------------

// Tx is a backend transaction covering all the graph writes of one Remember. All "not found" results are ErrNotFound.
type Tx interface {
	Commit() error
	// Rollback is safe to call after Commit.
	Rollback() error

	// InsertArtifact stores an artifact of the given kind (ArtifactEpisode or ArtifactKnowledge).
	InsertArtifact(ctx context.Context, dsID int64, kind, text string, validAt, createdAt time.Time) (int64, error)
	// UpsertEntity inserts the entity, or updates the one with the same (dataset, nameKey, type): a non-empty
	// description replaces the stored one. It returns the entity ID.
	UpsertEntity(ctx context.Context, dsID int64, e Entity, nameKey string) (int64, error)
	// LinkEntity records that the entity was inferred from the artifact. It is idempotent: linking the same pair
	// again is a no-op. Call it for every entity an artifact mentions, whether the entity was just created or
	// already existed (see the gb_entity_artifacts schema comment for why entities need a link table).
	LinkEntity(ctx context.Context, entityID, artifactID int64) error
	// LinkEdge records that the edge was inferred from the artifact. It is idempotent. Call it for the artifact
	// that created the edge and for every later artifact that states the same fact (a duplicate merged into the
	// stored edge), see the gb_edge_artifacts schema comment.
	LinkEdge(ctx context.Context, edgeID, artifactID int64) error
	// FindEntityID returns the oldest entity of the dataset with the given nameKey, of any type.
	FindEntityID(ctx context.Context, dsID int64, nameKey string) (int64, error)
	// GetEntity reads one entity (its stored state), inside the dataset only. It returns ErrNotFound if there is none.
	GetEntity(ctx context.Context, dsID, id int64) (*EvidenceEntity, error)
	// InsertEdge stores an edge and returns its ID.
	InsertEdge(ctx context.Context, e NewEdge) (int64, error)
	// MergeDuplicateEdge records the end of an already stored fact if it has none yet (invalidAt may be nil).
	MergeDuplicateEdge(ctx context.Context, dsID, id int64, invalidAt *time.Time) error
	// GetEdge reads one edge with its endpoint names, inside the dataset only.
	GetEdge(ctx context.Context, dsID, id int64) (*Edge, error)
	// InvalidateEdge sets the end of a fact (event time) and when we learned it (system time).
	InvalidateEdge(ctx context.Context, id int64, invalidAt, expiredAt time.Time) error
}
