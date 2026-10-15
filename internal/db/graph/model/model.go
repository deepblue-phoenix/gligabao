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
	// ModelID is the ID of the model (gb_models) that inferred the entity; "" if unknown. The pipeline sets it, never
	// the LLM output. See sqlite/db_schema.go for what it means for an entity shared between artifacts.
	ModelID string `json:"model_id,omitempty"`
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
	ModelID     string     `json:"model_id,omitempty"`   // ID of the model (gb_models) that inferred the fact
}

// EvidenceEntity is a graph entity cited by an answer.
type EvidenceEntity struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	ModelID     string `json:"model_id,omitempty"` // ID of the model (gb_models) that inferred the entity
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
	ModelID     string `json:"model_id,omitempty"` // ID of the model (gb_models) that inferred the fact
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
	ModelID                       string // ID of the model (gb_models) that inferred the fact; "" if unknown; the model must be stored
}

// Artifact kinds.
const (
	// ArtifactEpisode is a Graphiti-style episode: a text anchored in time, written or observed at ValidAt.
	ArtifactEpisode = "episode"
	// ArtifactKnowledge is knowledge that is not strictly episodic (a reference text, a note, ...).
	ArtifactKnowledge = "knowledge"
	// ArtifactWebSearch is the result of a web search that Remember made for an entity of another artifact (see
	// package memory_remember, remember_websearch.go). Callers cannot create one: it is not a valid kind of the
	// remember request.
	ArtifactWebSearch = "web_search"
	// ArtifactSeriesObservation is a derived artifact that Remember creates for a timeseries: a text generated
	// deterministically from the series' stats over a window (see package memory_remember, remember_observations.go).
	// Callers cannot create one: it is not a valid kind of the remember request.
	ArtifactSeriesObservation = "series_observation"
	// ArtifactChunkSummary is the summary of a chunk of a long text: an artifact that Remember derives with an LLM, a
	// child of the chunk (see package memory_remember, remember_summaries.go). Callers cannot create one: it is not a
	// valid kind of the remember request.
	ArtifactChunkSummary = "chunk_summary"
)

// Artifact is a piece of data ingested to be remembered: the raw text, plus when it happened and when it was
// ingested. Every entity and edge of the graph is anchored to the artifact(s) it was inferred from.
type Artifact struct {
	ID        int64
	DatasetID int64
	Kind      string // ArtifactEpisode, ArtifactKnowledge, ArtifactWebSearch, ArtifactSeriesObservation or ArtifactChunkSummary
	Text      string
	SessionID string // the user/agent session that remembered it; "" if none
	ValidAt   time.Time
	CreatedAt time.Time

	// ParentID is the artifact this one hangs off; 0 for an artifact that is nobody's child. There are two kinds of
	// child, told apart by Kind:
	//   - a chunk (Kind is that of its parent): the chunks of a long text are artifacts of their own, children of the
	//     artifact that holds the whole text (see Tx.InsertChunkArtifact);
	//   - a web search (Kind is ArtifactWebSearch): the child of the artifact the searched entity came from (see
	//     Tx.InsertWebSearchArtifact);
	//   - a series observation (Kind is ArtifactSeriesObservation): the child of the artifact that anchors the series
	//     (see Tx.InsertSeriesObservationArtifact);
	//   - a chunk summary (Kind is ArtifactChunkSummary): the child of the chunk it summarizes (see
	//     Tx.InsertChunkSummaryArtifact).
	ParentID int64
	// Only set for a chunk: its position among its siblings from 0, the byte range of its text in the parent's text
	// (overlap included) and its size in tokens.
	ChunkIndex, StartOffset, EndOffset, Tokens int
	// Query is the web search query that was issued for the artifact; only set for a web search (ArtifactWebSearch).
	Query string
	// SeriesID is the series an observation is about (gb_series.id); only set for a series observation
	// (ArtifactSeriesObservation), and 0 again if the series was deleted.
	SeriesID int64
	// ModelRunID is only set for a chunk summary (ArtifactChunkSummary): the run (gb_model_runs) of the model that wrote
	// it. The run holds the model, the reasoning level, the tokens and the cost (see relational.GetModelRun); the
	// artifact keeps nothing of it itself.
	ModelRunID int64
}

// IsDerived says whether Remember derived the artifact from something else (a web search, a series observation, a chunk summary), as
// opposed to a text the caller sent (or a chunk of it).
func (a Artifact) IsDerived() bool {
	return a.Kind == ArtifactWebSearch || a.Kind == ArtifactSeriesObservation || a.Kind == ArtifactChunkSummary
}

// IsChunk says whether the artifact is a chunk of its parent (and not, say, a web search that hangs off it).
func (a Artifact) IsChunk() bool { return a.ParentID != 0 && !a.IsDerived() }

// NewSeriesObservationArtifact is a series observation to insert: an artifact of kind ArtifactSeriesObservation that is
// a child of ParentID, the artifact that anchors the series, and records the series it describes.
type NewSeriesObservationArtifact struct {
	DatasetID int64
	// ParentID is the artifact that anchors the series. It must be in the same dataset and already stored.
	ParentID int64
	// SeriesID is the series the observation describes. It must be in the same dataset.
	SeriesID int64
	// Query says what the observation is of: the series and the window it covers.
	Query              string
	Text               string
	SessionID          string // that of the Remember call; "" if none
	ValidAt, CreatedAt time.Time
}

// NewChunkSummaryArtifact is the summary of a chunk to insert: an artifact of kind ArtifactChunkSummary that is a child
// of ParentID, the chunk it summarizes.
type NewChunkSummaryArtifact struct {
	DatasetID int64
	// ParentID is the chunk artifact that was summarized. It must be in the same dataset and already stored.
	ParentID           int64
	Text               string // the summary
	ModelRunID         int64  // the run (gb_model_runs) of the model that wrote it, which holds the model and the reasoning level; 0 if unknown
	SessionID          string // that of the chunk; "" if none
	ValidAt, CreatedAt time.Time
}

// NewWebSearchArtifact is the result of a web search to insert: an artifact of kind ArtifactWebSearch that is a child
// of ParentID, the artifact the searched entity came from.
type NewWebSearchArtifact struct {
	DatasetID int64
	// ParentID is the artifact from which the entity that was searched for came. It must be in the same dataset and
	// already stored.
	ParentID int64
	// Query is the web search query that was issued; it is what the artifact is the result of.
	Query              string
	Text               string
	SessionID          string // that of the Remember call; "" if none
	ValidAt, CreatedAt time.Time
}

// NewChunkArtifact is a chunk to insert: an artifact that is the child of the artifact ParentID, holding the part
// text[StartOffset:EndOffset] of the parent's text.
type NewChunkArtifact struct {
	DatasetID, ParentID int64
	Kind                string // that of the parent
	Text                string
	SessionID           string // that of the parent; "" if none
	ChunkIndex          int
	StartOffset         int
	EndOffset           int
	Tokens              int
	ValidAt, CreatedAt  time.Time
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

// EntitySession says that an entity was inferred from (mentioned in) an artifact of a session, whether or not that
// artifact created the entity.
type EntitySession struct {
	EntityID  int64
	SessionID string
}

// EdgeSession says that an edge (a fact) was inferred from an artifact of a session: the one that created it, or one
// that stated the same fact again.
type EdgeSession struct {
	EdgeID    int64
	SessionID string
}

// Counts is the number of rows of each kind a dataset holds.
type Counts struct {
	// Artifacts are all the artifact rows, chunks and chunk summaries included; Chunks is how many of them are chunks of
	// another artifact, and Summaries how many are chunk summaries.
	Artifacts, Entities, Edges, Chunks, Summaries int
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

	// InsertArtifact stores an artifact of the given kind (ArtifactEpisode, ArtifactKnowledge), attached to the
	// session sessionID ("" for none).
	InsertArtifact(ctx context.Context, dsID int64, kind, text, sessionID string, validAt, createdAt time.Time) (int64, error)
	// InsertChunkArtifact stores a chunk: an artifact that is a child of the artifact c.ParentID (which must be in the
	// same dataset and already stored), with its position, byte range and token count in the parent. The children of
	// an artifact are deleted with it.
	InsertChunkArtifact(ctx context.Context, c NewChunkArtifact) (int64, error)
	// InsertWebSearchArtifact stores the result of a web search: an artifact of kind ArtifactWebSearch that records
	// the query and is a child of the artifact w.ParentID (the one the searched entity came from; which must be in the
	// same dataset and already stored). It is not a chunk. The children of an artifact are deleted with it.
	InsertWebSearchArtifact(ctx context.Context, w NewWebSearchArtifact) (int64, error)
	// InsertSeriesObservationArtifact stores an observation of a series: an artifact of kind ArtifactSeriesObservation
	// that records the series (o.SeriesID) and the query, and is a child of the artifact o.ParentID (the one that anchors
	// the series; which must be in the same dataset and already stored). It is not a chunk. The children of an artifact
	// are deleted with it.
	InsertSeriesObservationArtifact(ctx context.Context, o NewSeriesObservationArtifact) (int64, error)
	// InsertChunkSummaryArtifact stores the summary of a chunk: an artifact of kind ArtifactChunkSummary that is a child
	// of the chunk artifact s.ParentID (which must be in the same dataset and already stored). It is not a chunk. The
	// children of an artifact are deleted with it.
	InsertChunkSummaryArtifact(ctx context.Context, s NewChunkSummaryArtifact) (int64, error)
	// UpsertEntity inserts the entity, or updates the one with the same (dataset, nameKey, type): a non-empty
	// description replaces the stored one (and e.ModelID the stored model_id, which follows the description; an empty
	// stored model_id is always filled). It returns the entity ID.
	UpsertEntity(ctx context.Context, dsID int64, e Entity, nameKey string) (int64, error)
	// LinkEntity records that the entity was inferred from the artifact. It is idempotent: linking the same pair
	// again is a no-op. Call it for every entity an artifact mentions, whether the entity was just created or
	// already existed (see the gb_entity_artifacts schema comment for why entities need a link table).
	LinkEntity(ctx context.Context, entityID, artifactID int64) error
	// LinkEdge records that the edge was inferred from the artifact. It is idempotent. Call it for the artifact
	// that created the edge and for every later artifact that states the same fact (a duplicate merged into the
	// stored edge), see the gb_edge_artifacts schema comment.
	LinkEdge(ctx context.Context, edgeID, artifactID int64) error
	// LinkEntitySession records that the entity was inferred from an artifact of the session. It is idempotent. Call
	// it for every entity an artifact of the session mentions, whether the entity was just created or already
	// existed: this is how the sessions an entity was discovered in are kept even when the entity is a duplicate
	// that is not written again (see the gb_entity_sessions schema comment).
	LinkEntitySession(ctx context.Context, entityID int64, sessionID string) error
	// LinkEdgeSession records that the edge was inferred from an artifact of the session. It is idempotent. Call it
	// for the session of the artifact that created the edge and of every later artifact that states the same fact
	// (a duplicate merged into the stored edge), see the gb_edge_sessions schema comment.
	LinkEdgeSession(ctx context.Context, edgeID int64, sessionID string) error
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

	// FindEntity returns the oldest entity of the dataset with the given nameKey and type.
	FindEntity(ctx context.Context, dsID int64, nameKey, typ string) (int64, error)
	// FindEdgeID returns the newest edge of the dataset between the entities with the normalized names sourceKey and
	// targetKey, with the given relation; a live (not expired) one is preferred to an expired one.
	FindEdgeID(ctx context.Context, dsID int64, sourceKey, relation, targetKey string) (int64, error)
	// CheckArtifact returns ErrNotFound unless the dataset has an artifact with the ID.
	CheckArtifact(ctx context.Context, dsID, id int64) error

	// UpsertSeries inserts the series, or updates the name, unit, frequency and definition of the one with the same
	// (dataset, provider, key, field). created says which. It returns the series ID.
	UpsertSeries(ctx context.Context, s NewSeries) (id int64, created bool, err error)
	// UpsertSeriesPoints writes the points: a point at a time the series already has replaces its value. The times
	// of the points must be distinct.
	UpsertSeriesPoints(ctx context.Context, seriesID int64, pts []SeriesPoint) error
	// ListSeriesPoints returns every point of the series, oldest first.
	ListSeriesPoints(ctx context.Context, seriesID int64) ([]SeriesPoint, error)
	// UpdateSeriesSummary stores what describes all the points of the series: the first and last time, the number of
	// points and the stats JSON.
	UpdateSeriesSummary(ctx context.Context, seriesID int64, first, last *time.Time, count int, stats string, now time.Time) error
	// AnchorSeries links the series to an artifact, entity or edge of its dataset (idempotent: it replaces the role
	// and window of an existing anchor).
	AnchorSeries(ctx context.Context, a NewSeriesAnchor) error
}
