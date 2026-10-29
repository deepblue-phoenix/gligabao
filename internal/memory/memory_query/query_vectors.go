package memory_query

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
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

// EvidencePassage is a passage of a long text, a chunk of it, that was given to the LLM and that an answer cites.
type EvidencePassage struct {
	// ArtifactID is the gb_artifacts row of the chunk; ParentID the one of the whole text it is a chunk of, and Index the
	// position of the chunk in it, from 0.
	ArtifactID int64 `json:"artifact_id"`
	ParentID   int64 `json:"parent_id"`
	Index      int   `json:"index"`
	// Summary is the summary of the chunk, if the search found it. Text is the text of the chunk; only the best passages
	// have it.
	Summary string `json:"summary,omitempty"`
	Text    string `json:"text,omitempty"`
}

// vectorHits is what the similarity search found, best first in each list. Everything in it is still to be checked against
// the database: a vector can outlive its row.
type vectorHits struct {
	entities []int64 // entity IDs
	edges    []int64 // edge IDs
	chunks   []vectorHit
	// summaries are the artifact IDs of the chunk summaries found, with their scores.
	summaries []vectorHit
}

// vectorHit is a match of an index: the ID of the row it is the vector of (an entity, an edge, a chunk artifact or a
// summary artifact), with its cosine similarity to the question.
type vectorHit struct {
	ID    int64   `json:"id"`
	Score float32 `json:"score"`
}

// Retrieval by similarity: the question is embedded once and searched in the four vector indexes that remember fills
// (see docs/vectors.md). What it finds is added to what the keyword retrieval of retrieve finds:
//
//   - the entity index gives more seeds of the subgraph (entities the question names in other words);
//   - the edge index gives facts directly, with their endpoints, even if no entity of theirs matched;
//   - the chunk and summary indexes give passages: the text of the chunks of long documents that the question is close to.
//
// The vector store and the embedder are required: a failure of either fails the query. The scores are a first guess, to
// be tuned with the evals. Only live facts are retrieved (see graph.EdgesAround): a vector can outlive the validity of its edge.
const (
	vectorEntityTopK  = 10 // entities taken from the entity index
	vectorEdgeTopK    = 10 // facts taken from the edge index
	vectorPassageTopK = 5  // matches taken from each of the chunk and the summary index

	// The lowest cosine similarity of a match to count, per index: the same as remember asks of two names to be the same
	// entity (similarEntityMinScore). A question is not phrased like what it asks for, so this may turn out too strict for
	// some indexes: tune it per index with the evals.
	minEntityScore  = 0.75
	minEdgeScore    = 0.75
	minPassageScore = 0.75

	maxPassageChunks    = 3 // passages that are given with the text of the chunk (the most expensive part of the context)
	maxPassageSummaries = 5 // further passages that are given with their summary only
)

// StageEmbed is the stage of the inference of the embedding request of query (see gb_inferences.stage).
const StageEmbed = "embed"

// searchVectors embeds the question and searches the indexes of dataset dsID with it. The embedding request is recorded as
// an inference and as a step of the trajectory.
func searchVectors(ctx context.Context, d *sql.DB, co memory_core.CallOptions, tr *memory_core.Trace, dsID int64, question string) (*vectorHits, error) {
	embs, usage, err := co.Embedder.EmbedBatchUsage(ctx, []string{question})
	recordEmbedUsage(ctx, d, usage) // no transaction is open here
	if err != nil {
		return nil, fmt.Errorf("memory: embed the question: %w", err)
	}
	if len(embs) != 1 {
		return nil, fmt.Errorf("memory: embedder returned %d embeddings for 1 text", len(embs))
	}
	q := embs[0]

	h := &vectorHits{}
	search := func(ds string, topK int, min float32, kind string) ([]vectorHit, error) {
		ms, err := co.Vectors.Query(ctx, ds, q, vector.QueryOptions{TopK: topK})
		if err != nil {
			return nil, fmt.Errorf("memory: search the %s index: %w", kind, err)
		}
		var out []vectorHit
		for _, m := range ms {
			if id, ok := vectorRowID(m.ID, kind); ok && m.Score >= min {
				out = append(out, vectorHit{ID: id, Score: m.Score})
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
		return out, nil
	}
	ents, err := search(memory_dataset.EntityDataset(dsID), vectorEntityTopK, minEntityScore, memory.VectorKindEntity)
	if err != nil {
		return nil, err
	}
	edges, err := search(memory_dataset.EdgeDataset(dsID), vectorEdgeTopK, minEdgeScore, memory.VectorKindEdge)
	if err != nil {
		return nil, err
	}
	if h.chunks, err = search(memory_dataset.ChunkDataset(dsID), vectorPassageTopK, minPassageScore, memory.VectorKindChunk); err != nil {
		return nil, err
	}
	if h.summaries, err = search(memory_dataset.SummaryDataset(dsID), vectorPassageTopK, minPassageScore, memory.VectorKindSummary); err != nil {
		return nil, err
	}
	for _, e := range ents {
		h.entities = append(h.entities, e.ID)
	}
	for _, e := range edges {
		h.edges = append(h.edges, e.ID)
	}

	tr.EmbedStep("Embedded the question and searched the entity, edge, chunk and summary indexes with it.", "search_vectors",
		map[string]any{"question": question},
		map[string]any{"entities": ents, "edges": edges, "chunks": h.chunks, "summaries": h.summaries}, usage)
	return h, nil
}

// vectorRowID returns the row ID in the vector ID id of a vector of the given kind (see memory.EntityVectorID and the like).
func vectorRowID(id, kind string) (int64, bool) {
	rest, ok := strings.CutPrefix(id, kind+"-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	return n, err == nil
}

// recordEmbedUsage records the cost of the embedding request of a query as an inference, as it was spent. Detached from ctx
// so that a cancelled request still records money that was spent. It never fails the call.
func recordEmbedUsage(ctx context.Context, d *sql.DB, usage *ai.EmbedUsage) {
	if usage == nil {
		return
	}
	_, err := memory_core.RecordRun(context.WithoutCancel(ctx), d, relational.Inference{Type: CallQuery, Stage: StageEmbed,
		Model: usage.Model, Provider: usage.Provider, Kind: ai.KindEmbedding, Reasoning: ai.ReasoningNone,
		InputTokens: usage.InputTokens, CostUSD: usage.CostUSD})
	if err != nil {
		log.WithError(err).WithField("model", usage.Model).Error("record embedding call")
	}
}

//---------------------------------------------------------------------------------------------

// loadPassages turns the chunk and summary hits into the passages of the context: the chunks, best first, each with the
// summary of its own if that was found. The best maxPassageChunks have the text of the chunk too; the next ones, up to
// maxPassageSummaries more, are only given when their summary was found. A hit whose row is gone, or is not what the
// index says (a chunk, a summary of a chunk), is dropped.
func loadPassages(ctx context.Context, d *sql.DB, dsID int64, h *vectorHits) ([]EvidencePassage, error) {
	if len(h.chunks)+len(h.summaries) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(h.chunks)+len(h.summaries))
	for _, c := range h.chunks {
		ids = append(ids, c.ID)
	}
	for _, s := range h.summaries {
		ids = append(ids, s.ID)
	}
	arts, err := graph.ArtifactsByID(ctx, d, dsID, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]graph.Artifact, len(arts))
	for _, a := range arts {
		byID[a.ID] = a
	}

	// One candidate per chunk, scored by the better of its chunk and its summary.
	type cand struct {
		chunk   int64
		score   float32
		summary string
	}
	cands := map[int64]*cand{}
	get := func(chunk int64) *cand {
		if cands[chunk] == nil {
			cands[chunk] = &cand{chunk: chunk}
		}
		return cands[chunk]
	}
	for _, c := range h.chunks {
		if a, ok := byID[c.ID]; ok && a.ParentID != 0 && a.Kind != graph.ArtifactChunkSummary {
			if k := get(c.ID); c.Score > k.score {
				k.score = c.Score
			}
		}
	}
	for _, s := range h.summaries {
		if a, ok := byID[s.ID]; ok && a.Kind == graph.ArtifactChunkSummary && a.ParentID != 0 {
			k := get(a.ParentID)
			k.summary = a.Text
			if s.Score > k.score {
				k.score = s.Score
			}
		}
	}
	order := make([]*cand, 0, len(cands))
	for _, c := range cands {
		order = append(order, c)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].score != order[j].score {
			return order[i].score > order[j].score
		}
		return order[i].chunk < order[j].chunk
	})
	if len(order) > maxPassageChunks+maxPassageSummaries {
		order = order[:maxPassageChunks+maxPassageSummaries]
	}

	// The chunks themselves: the ones found by their summary only are not loaded yet.
	var missing []int64
	for _, c := range order {
		if _, ok := byID[c.chunk]; !ok {
			missing = append(missing, c.chunk)
		}
	}
	if len(missing) > 0 {
		more, err := graph.ArtifactsByID(ctx, d, dsID, missing)
		if err != nil {
			return nil, err
		}
		for _, a := range more {
			byID[a.ID] = a
		}
	}

	var out []EvidencePassage
	for _, c := range order {
		chunk, ok := byID[c.chunk]
		if !ok || chunk.Kind == graph.ArtifactChunkSummary {
			continue // the chunk is gone
		}
		p := EvidencePassage{ArtifactID: chunk.ID, ParentID: chunk.ParentID, Index: chunk.ChunkIndex, Summary: c.summary}
		switch {
		case len(out) < maxPassageChunks:
			p.Text = chunk.Text
		case c.summary == "":
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// renderPassages returns the <passages> section of the user message.
func renderPassages(ps []EvidencePassage) string {
	var sb strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&sb, "[passage %d] chunk %d of text %d\n", p.ArtifactID, p.Index+1, p.ParentID)
		if p.Summary != "" {
			sb.WriteString("summary: " + p.Summary + "\n")
		}
		if p.Text != "" {
			sb.WriteString("text: " + p.Text + "\n")
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}
