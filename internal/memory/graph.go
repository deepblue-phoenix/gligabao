package memory

import (
	"context"
	"database/sql"
	"errors"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// ArtifactGraphExport is the graph of one artifact: the entities it mentions and the edges (facts) it stated.
type ArtifactGraphExport struct {
	Dataset    string `json:"dataset"`
	ArtifactID int64  `json:"artifact_id"`
	// WithChunks says that the graph also covers the chunks of the artifact (the extraction runs on the chunks of a long text).
	WithChunks bool                   `json:"include_children"`
	Entities   []graph.EvidenceEntity `json:"entities"`
	Edges      []graph.EvidenceEdge   `json:"edges"`
	Models     []relational.Model     `json:"models"`
}

// GraphExport is a whole dataset graph: its entities (nodes) and every edge (facts, expired ones included).
type GraphExport struct {
	Dataset  string                 `json:"dataset"`
	Entities []graph.EvidenceEntity `json:"entities"`
	Edges    []graph.EvidenceEdge   `json:"edges"`
	// Models are the models (gb_models) that inferred the entities and edges, which refer to them by model_id.
	Models []relational.Model `json:"models"`
	// DatasetID is the ID of the dataset (not in the JSON), for callers that add more data about it (the admin UI's in-context counts).
	DatasetID int64 `json:"-"`
}

// ExportGraph returns the full graph of userID's live dataset called name (empty means the default dataset).
// It returns ErrDatasetNotFound if there is no such dataset.
func ExportGraph(ctx context.Context,
	d *sql.DB,
	userID,
	name string) (*GraphExport, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	name, err = memory_core.ResolveDatasetName(name)
	if err != nil {
		return nil, err
	}
	dsID, err := memory_core.DatasetID(ctx,
		d,
		userID,
		name)
	if err != nil {
		return nil, err
	}
	return exportGraph(ctx,
		d,
		dsID,
		name)
}

// ExportDatasetGraph returns the full graph of the dataset with the given ID and name, which may be a deleted one
// (its rows are kept). It is meant for admin tooling that follows a stored dataset ID, such as an eval run's.
func ExportDatasetGraph(ctx context.Context,
	d *sql.DB,
	ds *relational.Dataset) (*GraphExport, error) {
	return exportGraph(ctx,
		d,
		ds.ID,
		ds.Name)
}

func exportGraph(ctx context.Context,
	d *sql.DB,
	dsID int64,
	name string) (*GraphExport, error) {
	entities, err := graph.ListEntities(ctx,
		d,
		dsID)
	if err != nil {
		return nil, err
	}
	edges, err := graph.ListGraphEdges(ctx,
		d,
		dsID)
	if err != nil {
		return nil, err
	}
	if entities == nil {
		entities = []graph.EvidenceEntity{}
	}
	if edges == nil {
		edges = []graph.EvidenceEdge{}
	}
	models, err := referencedModels(ctx,
		d,
		entities,
		edges)
	if err != nil {
		return nil, err
	}
	return &GraphExport{
		Dataset:   name,
		Entities:  entities,
		Edges:     edges,
		Models:    models,
		DatasetID: dsID,
	}, nil
}

// referencedModels returns the stored models that the entities and edges refer to, by name.
func referencedModels(ctx context.Context,
	d *sql.DB,
	entities []graph.EvidenceEntity,
	edges []graph.EvidenceEdge) ([]relational.Model, error) {
	used := map[string]bool{}
	for _, e := range entities {
		used[e.ModelID] = true
	}
	for _, e := range edges {
		used[e.ModelID] = true
	}
	all, err := relational.ListModels(ctx, d)
	if err != nil {
		return nil, err
	}
	out := []relational.Model{}
	for _, m := range all {
		if used[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}

// TopClasses returns the n classes most given to the artifact graphs (the subgraphs deduced from an artifact) of userID's live
// dataset called name (empty means the default dataset), each with the number of artifacts it was given to, the most frequent first.
// It returns ErrDatasetNotFound if there is no such dataset.
func TopClasses(ctx context.Context,
	d *sql.DB,
	userID,
	name string,
	n int) ([]graph.ClassCount, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	name, err = memory_core.ResolveDatasetName(name)
	if err != nil {
		return nil, err
	}
	dsID, err := memory_core.DatasetID(ctx,
		d,
		userID,
		name)
	if err != nil {
		return nil, err
	}
	return graph.TopArtifactClasses(ctx,
		d,
		dsID,
		n)
}

// ErrArtifactNotFound is returned when the artifact does not exist in the dataset.
var ErrArtifactNotFound = errors.New("artifact not found")

// ExportArtifactGraph returns the graph of the artifact of userID's live dataset called name: the entities linked to it and
// the edges (expired ones included) it created or restated, and with withChunks those of its chunks too. It returns
// ErrDatasetNotFound if there is no such dataset and ErrArtifactNotFound if the dataset has no such artifact.
func ExportArtifactGraph(ctx context.Context,
	d *sql.DB,
	userID,
	name string,
	artifactID int64,
	withChunks bool) (*ArtifactGraphExport, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	name, err = memory_core.ResolveDatasetName(name)
	if err != nil {
		return nil, err
	}
	dsID, err := memory_core.DatasetID(ctx,
		d,
		userID,
		name)
	if err != nil {
		return nil, err
	}
	found, err := graph.ArtifactsByID(ctx,
		d,
		dsID,
		[]int64{artifactID})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, ErrArtifactNotFound
	}
	entities, err := graph.ArtifactEntities(ctx,
		d,
		dsID,
		artifactID,
		withChunks)
	if err != nil {
		return nil, err
	}
	edges, err := graph.ArtifactEdges(ctx,
		d,
		dsID,
		artifactID,
		withChunks)
	if err != nil {
		return nil, err
	}
	if entities == nil {
		entities = []graph.EvidenceEntity{}
	}
	if edges == nil {
		edges = []graph.EvidenceEdge{}
	}
	models, err := referencedModels(ctx,
		d,
		entities,
		edges)
	if err != nil {
		return nil, err
	}
	return &ArtifactGraphExport{
		Dataset:    name,
		ArtifactID: artifactID,
		WithChunks: withChunks,
		Entities:   entities,
		Edges:      edges,
		Models:     models,
	}, nil
}
