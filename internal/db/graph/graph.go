// Package graph stores and queries the knowledge graph (artifacts, entities and bi-temporal edges) of the datasets.
// Every graph operation goes through the functions here, which route to the backend in use. Today the backends
// (graph/sqlite, graph/postgres) keep the graph in tables of the relational database opened by package relational;
// they belong here, and not in package relational, because they are graph operations that merely happen to be
// implemented on a relational store. Dedicated graph databases are meant to be added as further backends of this
// package. All SQL lives in the backends; other packages only call the functions here and never write SQL.
package graph

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph/sqlite"
)

// Artifact kinds (see model.Artifact).
const (
	ArtifactEpisode   = model.ArtifactEpisode
	ArtifactKnowledge = model.ArtifactKnowledge
)

// Types shared with the backends (see package model).
type (
	Entity             = model.Entity
	Edge               = model.Edge
	EvidenceEntity     = model.EvidenceEntity
	EvidenceEdge       = model.EvidenceEdge
	NewEdge            = model.NewEdge
	EntityArtifact     = model.EntityArtifact
	EdgeArtifact       = model.EdgeArtifact
	Artifact           = model.Artifact
	NewImage           = model.NewImage
	NewImageExtraction = model.NewImageExtraction
	ImageTextBlock     = model.ImageTextBlock
	ImageArtifact      = model.ImageArtifact
	ImageExtraction    = model.ImageExtraction
	Counts             = model.Counts
	Tx                 = model.Tx
	OntologyClass      = model.OntologyClass
	OntologyEdge       = model.OntologyEdge
	OntologyCounts     = model.OntologyCounts
)

// ErrNotFound is returned (wrapped) when a graph record does not exist.
var ErrNotFound = model.ErrNotFound

//---------------------------------------------------------------------------------------------

// active is the backend selected by the last successful Open.
var active atomic.Value // string

func backend() string {
	if b, _ := active.Load().(string); b != "" {
		return b
	}
	return config.DBBackendSQLite
}

func unsupported() error { return fmt.Errorf("unknown graph backend %q", backend()) }

// Open prepares the graph store of the given backend (config.DBBackendSQLite or config.DBBackendPostgres; empty
// means sqlite) and makes it the backend all other functions of this package route to. The relational backends
// keep the graph in the database d they were opened with, so call it with the *sql.DB returned by
// relational.Open (whose schema the graph tables reference), using the same backend name.
func Open(backendName string, d *sql.DB) error {
	if backendName == "" {
		backendName = config.DBBackendSQLite
	}
	var err error
	switch backendName {
	case config.DBBackendSQLite:
		err = sqlite.Open(d)
	case config.DBBackendPostgres:
		err = postgres.Open(d)
	default:
		return fmt.Errorf("unknown graph backend %q", backendName)
	}
	if err != nil {
		return err
	}
	active.Store(backendName)
	return nil
}

//---------------------------------------------------------------------------------------------
// Writing a memory

// Begin starts a transaction on the backend in use. The returned Tx is that backend's implementation.
func Begin(ctx context.Context, d *sql.DB) (Tx, error) {
	switch backend() {
	case config.DBBackendSQLite:
		tx, err := sqlite.Begin(ctx, d)
		if err != nil {
			return nil, err
		}
		return tx, nil
	case config.DBBackendPostgres:
		tx, err := postgres.Begin(ctx, d)
		if err != nil {
			return nil, err
		}
		return tx, nil
	}
	return nil, unsupported()
}

//---------------------------------------------------------------------------------------------
// Images (the image pipeline; independent of the text graph above)

// InsertImage stores an image artifact and its extraction (with the text blocks) in one transaction and returns
// their IDs.
func InsertImage(ctx context.Context, d *sql.DB, img NewImage) (artifactID, extractionID int64, err error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertImage(ctx, d, img)
	case config.DBBackendPostgres:
		return postgres.InsertImage(ctx, d, img)
	}
	return 0, 0, unsupported()
}

// ListImageArtifacts returns the image artifacts of the dataset by ID.
func ListImageArtifacts(ctx context.Context, d *sql.DB, dsID int64) ([]ImageArtifact, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListImageArtifacts(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.ListImageArtifacts(ctx, d, dsID)
	}
	return nil, unsupported()
}

// FindImageBySHA256 returns the newest image artifact of the dataset whose content has the given hex SHA-256, with
// its newest extraction. It returns ErrNotFound if the dataset has no such image (or none with an extraction).
func FindImageBySHA256(ctx context.Context, d *sql.DB, dsID int64, sha string) (*ImageArtifact, *ImageExtraction, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FindImageBySHA256(ctx, d, dsID, sha)
	case config.DBBackendPostgres:
		return postgres.FindImageBySHA256(ctx, d, dsID, sha)
	}
	return nil, nil, unsupported()
}

// GetImageArtifact returns one image artifact of the dataset. It returns ErrNotFound if there is none.
func GetImageArtifact(ctx context.Context, d *sql.DB, dsID, artifactID int64) (*ImageArtifact, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetImageArtifact(ctx, d, dsID, artifactID)
	case config.DBBackendPostgres:
		return postgres.GetImageArtifact(ctx, d, dsID, artifactID)
	}
	return nil, unsupported()
}

// ListImageExtractions returns the image extractions of the dataset, with their text blocks, by ID.
func ListImageExtractions(ctx context.Context, d *sql.DB, dsID int64) ([]ImageExtraction, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListImageExtractions(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.ListImageExtractions(ctx, d, dsID)
	}
	return nil, unsupported()
}

//---------------------------------------------------------------------------------------------
// Reading edges and the graph

// LoadCandidateEdges returns live (not expired) edges of the dataset touching at least one entity whose normalized
// name is in nameKeys, newest first, at most limit.
func LoadCandidateEdges(ctx context.Context, d *sql.DB, dsID int64, nameKeys []string, limit int) ([]Edge, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.LoadCandidateEdges(ctx, d, dsID, nameKeys, limit)
	case config.DBBackendPostgres:
		return postgres.LoadCandidateEdges(ctx, d, dsID, nameKeys, limit)
	}
	return nil, unsupported()
}

// ListEdges returns every edge (expired ones included) of userID's live dataset called name, by ID.
func ListEdges(ctx context.Context, d *sql.DB, userID, name string) ([]Edge, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListEdges(ctx, d, userID, name)
	case config.DBBackendPostgres:
		return postgres.ListEdges(ctx, d, userID, name)
	}
	return nil, unsupported()
}

// FallbackSeedEntities returns the IDs of the most recently updated entities of the dataset.
func FallbackSeedEntities(ctx context.Context, d *sql.DB, dsID int64, limit int) ([]int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FallbackSeedEntities(ctx, d, dsID, limit)
	case config.DBBackendPostgres:
		return postgres.FallbackSeedEntities(ctx, d, dsID, limit)
	}
	return nil, unsupported()
}

// MatchEntities returns the IDs of the best entities whose name or description mention the query terms, best
// first. queryKey is the normalized query; tokens are its distinct terms.
func MatchEntities(ctx context.Context, d *sql.DB, dsID int64, queryKey string, tokens []string, limit int) ([]int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.MatchEntities(ctx, d, dsID, queryKey, tokens, limit)
	case config.DBBackendPostgres:
		return postgres.MatchEntities(ctx, d, dsID, queryKey, tokens, limit)
	}
	return nil, unsupported()
}

// EdgesAround returns the edges of the dataset that start or end at one of the entity IDs, newest first.
func EdgesAround(ctx context.Context, d *sql.DB, dsID int64, entityIDs []int64, limit int) ([]EvidenceEdge, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.EdgesAround(ctx, d, dsID, entityIDs, limit)
	case config.DBBackendPostgres:
		return postgres.EdgesAround(ctx, d, dsID, entityIDs, limit)
	}
	return nil, unsupported()
}

// EntitiesByID returns the entities with the given IDs.
func EntitiesByID(ctx context.Context, d *sql.DB, ids []int64) ([]EvidenceEntity, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.EntitiesByID(ctx, d, ids)
	case config.DBBackendPostgres:
		return postgres.EntitiesByID(ctx, d, ids)
	}
	return nil, unsupported()
}

//---------------------------------------------------------------------------------------------
// Inspection (read-only summaries of what is stored; used to verify behaviour, e.g. in tests)

// DatasetCounts returns the number of artifacts, entities and edges in the dataset.
func DatasetCounts(ctx context.Context, d *sql.DB, dsID int64) (Counts, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.DatasetCounts(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.DatasetCounts(ctx, d, dsID)
	}
	return Counts{}, unsupported()
}

// ListArtifacts returns the artifacts of the dataset by ID.
func ListArtifacts(ctx context.Context, d *sql.DB, dsID int64) ([]Artifact, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListArtifacts(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.ListArtifacts(ctx, d, dsID)
	}
	return nil, unsupported()
}

// ListEntityArtifacts returns the entity-to-artifact links (which artifacts each entity was inferred from) of the
// dataset, by entity ID then artifact ID.
func ListEntityArtifacts(ctx context.Context, d *sql.DB, dsID int64) ([]EntityArtifact, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListEntityArtifacts(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.ListEntityArtifacts(ctx, d, dsID)
	}
	return nil, unsupported()
}

// ListEdgeArtifacts returns the edge-to-artifact links (every artifact each edge was inferred from) of the dataset,
// by edge ID then artifact ID.
func ListEdgeArtifacts(ctx context.Context, d *sql.DB, dsID int64) ([]EdgeArtifact, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListEdgeArtifacts(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.ListEdgeArtifacts(ctx, d, dsID)
	}
	return nil, unsupported()
}

// ListEntities returns the entities of the dataset by ID.
func ListEntities(ctx context.Context, d *sql.DB, dsID int64) ([]EvidenceEntity, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListEntities(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.ListEntities(ctx, d, dsID)
	}
	return nil, unsupported()
}

// ListGraphEdges returns every edge of the dataset (expired ones included) with its endpoint IDs and names, by ID.
func ListGraphEdges(ctx context.Context, d *sql.DB, dsID int64) ([]EvidenceEdge, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListGraphEdges(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.ListGraphEdges(ctx, d, dsID)
	}
	return nil, unsupported()
}

// CountCrossDatasetEdges returns the number of edges whose dataset differs from that of one of their endpoints
// (always 0 unless datasets leak into each other).
func CountCrossDatasetEdges(ctx context.Context, d *sql.DB) (int, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.CountCrossDatasetEdges(ctx, d)
	case config.DBBackendPostgres:
		return postgres.CountCrossDatasetEdges(ctx, d)
	}
	return 0, unsupported()
}

//---------------------------------------------------------------------------------------------
// Ontologies

// LoadOntology stores the classes and edges of the ontology whose metadata row is ontologyID (see package
// relational), replacing any it had stored, in one transaction: either the new ones replace the old or nothing changes. Edges refer to classes by IRI, so every
// endpoint must be in classes.
func LoadOntology(ctx context.Context, d *sql.DB, ontologyID int64, classes []OntologyClass, edges []OntologyEdge) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.LoadOntology(ctx, d, ontologyID, classes, edges)
	case config.DBBackendPostgres:
		return postgres.LoadOntology(ctx, d, ontologyID, classes, edges)
	}
	return unsupported()
}

// MergeOntology merges classes and edges into the ontology whose metadata row is ontologyID, in one transaction
// (all of it is applied or nothing is), and returns the size of the ontology afterwards. Nothing is deleted.
//
// Classes are identified by IRI, and edges by (source IRI, target IRI, kind, IRI):
//   - a class or edge that is not stored yet is added;
//   - an edge that is stored already is overwritten: its relation, label, comment and inverse_of are replaced by
//     the supplied ones, even when those are empty;
//   - a declared class that is stored already is overwritten in the same way (name, label, comment). A class that
//     is not declared (see OntologyClass.Declared) is only added when missing and never changes a stored one, so that
//     mentioning a class as the endpoint of an edge cannot erase its description;
//   - stored classes and edges that are not supplied are left alone.
//
// Overwriting keeps the row (its ID), so the edges attached to a class stay attached.
func MergeOntology(ctx context.Context, d *sql.DB, ontologyID int64, classes []OntologyClass, edges []OntologyEdge) (OntologyCounts, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.MergeOntology(ctx, d, ontologyID, classes, edges)
	case config.DBBackendPostgres:
		return postgres.MergeOntology(ctx, d, ontologyID, classes, edges)
	}
	return OntologyCounts{}, unsupported()
}

// ListOntologyClasses returns the classes of the ontology, in the order they were stored.
func ListOntologyClasses(ctx context.Context, d *sql.DB, ontologyID int64) ([]OntologyClass, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListOntologyClasses(ctx, d, ontologyID)
	case config.DBBackendPostgres:
		return postgres.ListOntologyClasses(ctx, d, ontologyID)
	}
	return nil, unsupported()
}

// ListOntologyEdges returns the edges of the ontology, in the order they were stored.
func ListOntologyEdges(ctx context.Context, d *sql.DB, ontologyID int64) ([]OntologyEdge, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListOntologyEdges(ctx, d, ontologyID)
	case config.DBBackendPostgres:
		return postgres.ListOntologyEdges(ctx, d, ontologyID)
	}
	return nil, unsupported()
}
