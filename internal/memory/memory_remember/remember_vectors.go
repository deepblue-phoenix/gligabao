package memory_remember

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_dataset"

	log "github.com/sirupsen/logrus"
)

// similarEntity is a stored entity found by similarity for an endpoint of a new fact.
type similarEntity struct {
	endpoint string // the name the new fact uses
	name     string // the stored entity's name
	typ      string
	score    float32
}

// pendingVectors are the embeddings of a Store call, computed before its transaction commits and written after it.
type pendingVectors struct {
	store vector.Store
	// byDataset are the vectors to write, per vector store dataset (index).
	byDataset map[string][]vector.Vector
	entities  int
	edges     int
	chunks    int
	summaries int
}

// chunkVector is the chunk whose text prepareVectors embeds next to the entities and edges of a Store call.
type chunkVector struct {
	artifactID, parentID int64
	index                int
	text                 string
}

// summaryVector is the summary of a chunk whose text prepareVectors embeds next to the chunk's.
type summaryVector struct {
	artifactID, chunkID, parentID int64
	index                         int
	text                          string
}

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
func edgeText(e *Edge) string { return memory_core.FactFromEdge(*e).Text() }

//---------------------------------------------------------------------------------------------

const (
	// similarEntityTopK is how many stored entities are fetched per endpoint of a new fact.
	similarEntityTopK = 3
	// similarEntityMinScore is the cosine similarity a stored entity needs to count as the same thing under another
	// name. A first guess: tune it with the evals.
	similarEntityMinScore = 0.75
)

// findSimilarEntities looks for stored entities of dataset dsID that may be the endpoints of the new edges of ex under
// another name (aliases, spelling): each endpoint is embedded (like an entity is, see entityText) and searched in the
// dataset's entity index. Stored entities with the same normalized name as the endpoint are not reported (the exact
// lookup already covers them), and neither are those scoring below similarEntityMinScore.
//
// run is told of the embedding request. It returns the texts that were embedded (empty if no search was made: no vectors configured, or no endpoints) and
// the usage of the embedding request, which is already recorded as spent. It is only a hint for choosing candidate
// facts: the caller treats an error as "no matches".
func findSimilarEntities(ctx context.Context, d *sql.DB, run *runLog, o memory_core.CallOptions, dsID int64, ex *Extraction) (found []similarEntity, texts []string, usage *ai.EmbedUsage, err error) {
	if o.Vectors == nil || o.Embedder == nil {
		return nil, nil, nil, nil
	}
	typeOf := map[string]string{}
	descOf := map[string]string{}
	for _, e := range ex.Entities {
		k := memory_core.Key(e.Name)
		if _, ok := typeOf[k]; !ok {
			typeOf[k], descOf[k] = normType(e.Type), strings.TrimSpace(e.Description)
		}
	}
	var endpoints []string
	seen := map[string]bool{}
	for _, e := range ex.Edges {
		for _, n := range []string{e.Source, e.Target} {
			n = strings.TrimSpace(n)
			if k := memory_core.Key(n); k != "" && !seen[k] {
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

	embs, usage, err := o.Embedder.EmbedBatchUsage(ctx, texts)
	run.AddInference(StageLoadCandidates, recordEmbedUsage(ctx, d, usage)) // no transaction is open here
	if err != nil {
		return nil, texts, usage, fmt.Errorf("memory: embed entities to search: %w", err)
	}
	if len(embs) != len(texts) {
		return nil, texts, usage, fmt.Errorf("memory: embedder returned %d embeddings for %d texts", len(embs), len(texts))
	}
	got := map[string]bool{}
	for i, ep := range endpoints {
		ms, err := o.Vectors.Query(ctx, memory_dataset.EntityDataset(dsID), embs[i], vector.QueryOptions{TopK: similarEntityTopK})
		if err != nil {
			return found, texts, usage, fmt.Errorf("memory: search similar entities: %w", err)
		}
		for _, m := range ms {
			name, _ := m.Metadata["name"].(string)
			typ, _ := m.Metadata["type"].(string)
			if name == "" || m.Score < similarEntityMinScore || memory_core.Key(name) == memory_core.Key(ep) || got[memory_core.Key(name)] {
				continue
			}
			got[memory_core.Key(name)] = true
			found = append(found, similarEntity{endpoint: ep, name: name, typ: typ, score: m.Score})
		}
	}
	return found, texts, usage, nil
}

//---------------------------------------------------------------------------------------------

// prepareVectors embeds the entities (IDs, in order, without duplicates; read as stored through tx), the edges and,
// if the stored artifact is a chunk, the chunk's text (chunk, else nil) and its summary (summary, else nil) of dataset dsID in
// one request. It returns nil
// if opts ask for no vectors or there is nothing to embed.
//
// The usage of the embedding request is returned (non-nil whenever the request was made, even if the call fails
// afterwards) for the caller to record with recordEmbedUsage once tx is finished: recording it here, through d,
// would block on the write lock tx holds (SQLITE_BUSY).
func prepareVectors(ctx context.Context, tx graph.Tx, o memory_core.CallOptions, dsID int64, entityIDs []int64, edges []Edge, chunk *chunkVector, summary *summaryVector) (*pendingVectors, *ai.EmbedUsage, error) {
	if o.Vectors == nil || o.Embedder == nil || (len(entityIDs)+len(edges) == 0 && chunk == nil && summary == nil) {
		return nil, nil, nil
	}
	ents := make([]*graph.EvidenceEntity, 0, len(entityIDs))
	texts := make([]string, 0, len(entityIDs)+len(edges)+1)
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
	if chunk != nil {
		texts = append(texts, chunk.text)
	}
	if summary != nil {
		texts = append(texts, summary.text)
	}

	embs, usage, err := o.Embedder.EmbedBatchUsage(ctx, texts)
	if err != nil {
		return nil, nil, fmt.Errorf("memory: embed entities and edges: %w", err)
	}
	traceEmbed(o.Trace, texts, len(entityIDs), len(edges), chunk != nil, summary != nil, usage)
	if len(embs) != len(texts) {
		return nil, usage, fmt.Errorf("memory: embedder returned %d embeddings for %d texts", len(embs), len(texts))
	}

	p := &pendingVectors{store: o.Vectors, byDataset: map[string][]vector.Vector{}, entities: len(ents), edges: len(edges)}
	for i, e := range ents {
		ns := memory_dataset.EntityDataset(dsID)
		p.byDataset[ns] = append(p.byDataset[ns], vector.Vector{
			ID:     memory.EntityVectorID(e.ID),
			Values: embs[i],
			Metadata: map[string]any{
				"kind":      memory.VectorKindEntity,
				"entity_id": e.ID,
				"name":      e.Name,
				"type":      e.Type,
			},
		})
	}
	for i, e := range edges {
		ns := memory_dataset.EdgeDataset(dsID)
		p.byDataset[ns] = append(p.byDataset[ns], vector.Vector{
			ID:     memory.EdgeVectorID(e.ID),
			Values: embs[len(ents)+i],
			// Only what never changes: the validity of a fact does, so it is read from the database.
			Metadata: map[string]any{
				"kind":     memory.VectorKindEdge,
				"edge_id":  e.ID,
				"source":   e.Source,
				"target":   e.Target,
				"relation": e.Relation,
			},
		})
	}
	if chunk != nil {
		ns := memory_dataset.ChunkDataset(dsID)
		p.chunks = 1
		p.byDataset[ns] = append(p.byDataset[ns], vector.Vector{
			ID:     memory.ChunkVectorID(chunk.artifactID),
			Values: embs[len(ents)+len(edges)],
			Metadata: map[string]any{
				"kind":        memory.VectorKindChunk,
				"artifact_id": chunk.artifactID,
				"parent_id":   chunk.parentID,
				"index":       chunk.index,
			},
		})
	}
	if summary != nil {
		i := len(ents) + len(edges)
		if chunk != nil {
			i++
		}
		ns := memory_dataset.SummaryDataset(dsID)
		p.summaries = 1
		p.byDataset[ns] = append(p.byDataset[ns], vector.Vector{
			ID:     memory.SummaryVectorID(summary.artifactID),
			Values: embs[i],
			Metadata: map[string]any{
				"kind":        memory.VectorKindSummary,
				"artifact_id": summary.artifactID,
				"chunk_id":    summary.chunkID,
				"parent_id":   summary.parentID,
				"index":       summary.index,
			},
		})
	}
	return p, usage, nil
}

// StageEmbed is the stage of the inferences of the embedding requests of remember.
const StageEmbed = "embed"

// recordEmbedUsage records the cost of an embedding request as an inference, as it was spent, like the LLM's. Detached from ctx so a
// cancelled request still records money that was spent. It must not run while a write transaction of d is open. It returns
// the ID of the inference, 0 if there is none (no request was made, or it could not be recorded).
func recordEmbedUsage(ctx context.Context, d *sql.DB, usage *ai.EmbedUsage) int64 {
	if usage == nil {
		return 0
	}
	id, err := memory_core.RecordRun(context.WithoutCancel(ctx), d, relational.Inference{Type: CallRemember, Stage: StageEmbed,
		Model: usage.Model, Provider: usage.Provider, Kind: ai.KindEmbedding, Reasoning: ai.ReasoningNone,
		InputTokens: usage.InputTokens, CostUSD: usage.CostUSD})
	if err != nil {
		log.WithError(err).WithField("model", usage.Model).Error("record embedding call")
	}
	return id
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
