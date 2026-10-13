package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"

	log "github.com/sirupsen/logrus"
)

// Embedder turns texts into embeddings (see ai.Embedder), one per text and in the same order, and reports the usage.
type Embedder interface {
	EmbedBatchUsage(ctx context.Context, texts []string) ([][]float32, *ai.EmbedUsage, error)
}

// WithVectors makes Remember embed the entities and edges it stores and write the embeddings to store, into the
// dataset's vector datasets (one per index, see VectorDataset). Without it (or if either argument is nil)
// Remember does not touch vectors.
func WithVectors(store vector.Store, emb Embedder) Option {
	return func(o *callOptions) { o.vectors, o.embedder = store, emb }
}

// Values of the metadata key "kind" of the vectors written here (one per index).
const (
	VectorKindEntity = "entity"
	VectorKindEdge   = "edge"
)

// EntityVectorID is the vector ID of an entity in the entity index of its dataset.
func EntityVectorID(entityID int64) string {
	return VectorKindEntity + "-" + strconv.FormatInt(entityID, 10)
}

// EdgeVectorID is the vector ID of an edge in the edge index of its dataset.
func EdgeVectorID(edgeID int64) string { return VectorKindEdge + "-" + strconv.FormatInt(edgeID, 10) }

// entityText is the text that is embedded for an entity.
func entityText(e *graph.EvidenceEntity) string { return entityLabel(e.Name, e.Type, e.Description) }

func entityLabel(name, typ, description string) string {
	s := name + " (" + typ + ")"
	if description != "" {
		s += ": " + description
	}
	return s
}

// edgeText is the text that is embedded for an edge: the fact as a sentence.
func edgeText(e *Edge) string { return FactFromEdge(*e).Text() }

//---------------------------------------------------------------------------------------------

const (
	// similarEntityTopK is how many stored entities are fetched per endpoint of a new fact.
	similarEntityTopK = 3
	// similarEntityMinScore is the cosine similarity a stored entity needs to count as the same thing under another
	// name. A first guess: tune it with the evals.
	similarEntityMinScore = 0.75
)

// similarEntity is a stored entity found by similarity for an endpoint of a new fact.
type similarEntity struct {
	endpoint string // the name the new fact uses
	name     string // the stored entity's name
	typ      string
	score    float32
}

// findSimilarEntities looks for stored entities of dataset dsID that may be the endpoints of the new edges of ex under
// another name (aliases, spelling): each endpoint is embedded (like an entity is, see entityText) and searched in the
// dataset's entity index. Stored entities with the same normalized name as the endpoint are not reported (the exact
// lookup already covers them), and neither are those scoring below similarEntityMinScore.
//
// It returns the texts that were embedded (empty if no search was made: no vectors configured, or no endpoints) and
// the usage of the embedding request, which is already recorded as spent. It is only a hint for choosing candidate
// facts: the caller treats an error as "no matches".
func findSimilarEntities(ctx context.Context, d *sql.DB, o callOptions, dsID int64, ex *Extraction) (found []similarEntity, texts []string, usage *ai.EmbedUsage, err error) {
	if o.vectors == nil || o.embedder == nil {
		return nil, nil, nil, nil
	}
	typeOf := map[string]string{}
	descOf := map[string]string{}
	for _, e := range ex.Entities {
		k := key(e.Name)
		if _, ok := typeOf[k]; !ok {
			typeOf[k], descOf[k] = normType(e.Type), strings.TrimSpace(e.Description)
		}
	}
	var endpoints []string
	seen := map[string]bool{}
	for _, e := range ex.Edges {
		for _, n := range []string{e.Source, e.Target} {
			n = strings.TrimSpace(n)
			if k := key(n); k != "" && !seen[k] {
				seen[k] = true
				endpoints = append(endpoints, n)
				typ := typeOf[k]
				if typ == "" {
					typ = "other"
				}
				texts = append(texts, entityLabel(n, typ, descOf[k]))
			}
		}
	}
	if len(texts) == 0 {
		return nil, nil, nil, nil
	}

	embs, usage, err := o.embedder.EmbedBatchUsage(ctx, texts)
	recordEmbedUsage(ctx, d, usage) // no transaction is open here
	if err != nil {
		return nil, texts, usage, fmt.Errorf("memory: embed entities to search: %w", err)
	}
	if len(embs) != len(texts) {
		return nil, texts, usage, fmt.Errorf("memory: embedder returned %d embeddings for %d texts", len(embs), len(texts))
	}
	got := map[string]bool{}
	for i, ep := range endpoints {
		ms, err := o.vectors.Query(ctx, EntityDataset(dsID), embs[i], vector.QueryOptions{TopK: similarEntityTopK})
		if err != nil {
			return found, texts, usage, fmt.Errorf("memory: search similar entities: %w", err)
		}
		for _, m := range ms {
			name, _ := m.Metadata["name"].(string)
			typ, _ := m.Metadata["type"].(string)
			if name == "" || m.Score < similarEntityMinScore || key(name) == key(ep) || got[key(name)] {
				continue
			}
			got[key(name)] = true
			found = append(found, similarEntity{endpoint: ep, name: name, typ: typ, score: m.Score})
		}
	}
	return found, texts, usage, nil
}

//---------------------------------------------------------------------------------------------

// pendingVectors are the embeddings of a Store call, computed before its transaction commits and written after it.
type pendingVectors struct {
	store vector.Store
	// byDataset are the vectors to write, per vector store dataset (index).
	byDataset map[string][]vector.Vector
	entities  int
	edges     int
}

// prepareVectors embeds the entities (IDs, in order, without duplicates; read as stored through tx) and the edges
// of dataset dsID in one request. It returns nil if opts ask for no vectors or there is nothing to embed.
//
// The usage of the embedding request is returned (non-nil whenever the request was made, even if the call fails
// afterwards) for the caller to record with recordEmbedUsage once tx is finished: recording it here, through d,
// would block on the write lock tx holds (SQLITE_BUSY).
func prepareVectors(ctx context.Context, tx graph.Tx, o callOptions, dsID int64, entityIDs []int64, edges []Edge) (*pendingVectors, *ai.EmbedUsage, error) {
	if o.vectors == nil || o.embedder == nil || len(entityIDs)+len(edges) == 0 {
		return nil, nil, nil
	}
	ents := make([]*graph.EvidenceEntity, 0, len(entityIDs))
	texts := make([]string, 0, len(entityIDs)+len(edges))
	for _, id := range entityIDs {
		e, err := tx.GetEntity(ctx, dsID, id)
		if err != nil {
			return nil, nil, err
		}
		ents = append(ents, e)
		texts = append(texts, entityText(e))
	}
	for i := range edges {
		texts = append(texts, edgeText(&edges[i]))
	}

	embs, usage, err := o.embedder.EmbedBatchUsage(ctx, texts)
	if err != nil {
		return nil, nil, fmt.Errorf("memory: embed entities and edges: %w", err)
	}
	o.trace.embed(texts, len(entityIDs), len(edges), usage)
	if len(embs) != len(texts) {
		return nil, usage, fmt.Errorf("memory: embedder returned %d embeddings for %d texts", len(embs), len(texts))
	}

	p := &pendingVectors{store: o.vectors, byDataset: map[string][]vector.Vector{}, entities: len(ents), edges: len(edges)}
	for i, e := range ents {
		ns := EntityDataset(dsID)
		p.byDataset[ns] = append(p.byDataset[ns], vector.Vector{
			ID:     EntityVectorID(e.ID),
			Values: embs[i],
			Metadata: map[string]any{
				"kind":      VectorKindEntity,
				"entity_id": e.ID,
				"name":      e.Name,
				"type":      e.Type,
			},
		})
	}
	for i, e := range edges {
		ns := EdgeDataset(dsID)
		p.byDataset[ns] = append(p.byDataset[ns], vector.Vector{
			ID:     EdgeVectorID(e.ID),
			Values: embs[len(ents)+i],
			// Only what never changes: the validity of a fact does, so it is read from the database.
			Metadata: map[string]any{
				"kind":     VectorKindEdge,
				"edge_id":  e.ID,
				"source":   e.Source,
				"target":   e.Target,
				"relation": e.Relation,
			},
		})
	}
	return p, usage, nil
}

// recordEmbedUsage records the cost of an embedding request, as it was spent, like the LLM's. Detached from ctx so a
// cancelled request still records money that was spent. It must not run while a write transaction of d is open.
func recordEmbedUsage(ctx context.Context, d *sql.DB, usage *ai.EmbedUsage) {
	if usage == nil {
		return
	}
	if err := RecordCall(context.WithoutCancel(ctx), d, CallRemember, usage.Provider, usage.Model, usage.InputTokens, 0, usage.CostUSD); err != nil {
		log.WithError(err).WithField("model", usage.Model).Error("record embedding call")
	}
}

// write upserts the vectors into their datasets. Vectors are keyed by entity / edge ID, so writing them again
// (when an entity or fact is mentioned by a later artifact) overwrites rather than duplicates.
func (p *pendingVectors) write(ctx context.Context) error {
	if p == nil {
		return nil
	}
	for ns, vs := range p.byDataset {
		if err := p.store.Upsert(ctx, ns, vs); err != nil {
			return fmt.Errorf("memory: store vectors in dataset %s: %w", ns, err)
		}
	}
	return nil
}

//---------------------------------------------------------------------------------------------

// IndexStats is the state of one vector index of a dataset.
type IndexStats struct {
	Index VectorIndex `json:"index"`
	// VectorDataset is the dataset's name in the vector store.
	VectorDataset string `json:"vector_dataset"`
	// Expected is the number of rows (entities, edges) that should have a vector in this index.
	Expected int `json:"expected"`
	vector.Stats
}

// DatasetVectorStats is the state of all the vector indexes of a dataset.
type DatasetVectorStats struct {
	// Backend is "" if there is no vector store, or it cannot report statistics.
	Backend string       `json:"backend"`
	Indexes []IndexStats `json:"indexes"`
}

// VectorStats reports, for every vector index of userID's live dataset name, the vectors the store holds next to the
// number of rows that should have one. vectors may be nil (no indexes are listed then).
// It returns ErrDatasetNotFound if there is no such dataset.
func VectorStats(ctx context.Context, d *sql.DB, vectors vector.Store, userID, name string) (*DatasetVectorStats, error) {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	name, err = ResolveDatasetName(name)
	if err != nil {
		return nil, err
	}
	id, err := datasetID(ctx, d, userID, name)
	if err != nil {
		return nil, err
	}
	out := &DatasetVectorStats{Indexes: []IndexStats{}}
	statter, ok := vectors.(vector.Statter)
	if !ok {
		return out, nil
	}
	counts, err := graph.DatasetCounts(ctx, d, id)
	if err != nil {
		return nil, err
	}
	for _, idx := range VectorIndexes {
		ds := VectorDataset(id, idx)
		st, err := statter.Stats(ctx, ds)
		if err != nil {
			return nil, fmt.Errorf("memory: stats of vector dataset %s: %w", ds, err)
		}
		expected := counts.Entities
		if idx == IndexEdges {
			expected = counts.Edges
		}
		out.Backend = st.Backend
		out.Indexes = append(out.Indexes, IndexStats{Index: idx, VectorDataset: ds, Expected: expected, Stats: st})
	}
	return out, nil
}
