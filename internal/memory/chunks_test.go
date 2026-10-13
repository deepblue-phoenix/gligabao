package memory_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

var personRe = regexp.MustCompile(`Person(\d+) works at Acme`)

// personLLM extracts "PersonN works at Acme" for every such sentence of the text it is given (the <text> of an
// extraction request; the other requests, which compare facts, are not expected here) and counts its extractions.
type personLLM struct {
	calls  int
	failOn int // the 1-based call that fails; 0 = none
}

func (l *personLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	l.calls++
	if l.calls == l.failOn {
		return nil, errors.New("llm down")
	}
	if !strings.Contains(req.System, "extract a temporal knowledge graph") { // resolveEdges: nothing to flag
		return &ai.Response{StopReason: "end_turn", Text: `{"duplicates":[],"contradictions":[]}`}, nil
	}
	var ents, edges []string
	seen := map[string]bool{}
	for _, m := range personRe.FindAllStringSubmatch(req.Messages[0].Content, -1) {
		p := "Person" + m[1]
		if seen[p] {
			continue
		}
		seen[p] = true
		ents = append(ents, fmt.Sprintf(`{"name":%q,"type":"person"}`, p))
		edges = append(edges, fmt.Sprintf(`{"source":%q,"target":"Acme","relation":"works at"}`, p))
	}
	ents = append(ents, `{"name":"Acme","type":"organization"}`)
	return &ai.Response{StopReason: "end_turn", Text: `{"entities":[` + strings.Join(ents, ",") + `],"edges":[` + strings.Join(edges, ",") + `]}`}, nil
}

// document is n paragraphs, one fact each.
func document(n int) string {
	var ps []string
	for i := 0; i < n; i++ {
		ps = append(ps, fmt.Sprintf("Person%d works at Acme and enjoys long walks in the park on weekends.", i))
	}
	return strings.Join(ps, "\n\n")
}

func TestRememberChunksLongText(t *testing.T) { dbtest.Run(t, testRememberChunksLongText) }

func testRememberChunksLongText(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	vs, err := vector.NewChromem(":memory:", 2)
	if err != nil {
		t.Fatal(err)
	}
	cfg := chunk.Config{Size: 64, OverlapPercent: 40} // 25 tokens: one paragraph is repeated
	text := document(12)
	llm := &personLLM{}
	res, err := memory_remember.Remember(ctx, d, llm, "u1", "docs", "", text, relational.Now(),
		memory.WithVectors(vs, &fakeEmbedder{}), memory_core.WithChunking(cfg), memory_core.WithSessionID("s1"),
		memory_core.WithEval(), memory_core.WithTrajectoryDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	n := len(res.Chunks)
	if n < 3 || llm.calls < n {
		t.Fatalf("%d chunks, %d LLM calls", n, llm.calls)
	}

	// The document is the parent artifact; the chunks are artifacts that are its children and slice its text.
	dsID, _ := relational.DatasetID(ctx, d, "u1", "docs")
	arts, err := graph.ListArtifacts(ctx, d, dsID)
	if err != nil || len(arts) != n+1 {
		t.Fatalf("artifacts = %d, %v; want %d", len(arts), err, n+1)
	}
	if p := arts[0]; p.ID != res.ArtifactID || p.ParentID != 0 || p.Text != text || p.SessionID != "s1" {
		t.Errorf("parent = %+v", p)
	}
	isChunk := map[int64]bool{}
	for i, c := range arts[1:] {
		isChunk[c.ID] = true
		r := res.Chunks[i]
		if c.ParentID != res.ArtifactID || c.ChunkIndex != i || c.ID != r.ArtifactID || c.SessionID != "s1" || c.Kind != arts[0].Kind {
			t.Errorf("chunk %d = %+v", i, c)
		}
		if c.Text != text[c.StartOffset:c.EndOffset] || c.Tokens > cfg.Size || c.Tokens != r.Tokens {
			t.Errorf("chunk %d: text/offsets/tokens do not match: %+v", i, c)
		}
		if i > 0 && c.StartOffset >= arts[i].EndOffset {
			t.Errorf("chunk %d does not overlap the previous one", i)
		}
	}
	if c, err := graph.DatasetCounts(ctx, d, dsID); err != nil || c.Chunks != n || c.Artifacts != n+1 {
		t.Errorf("counts = %+v, %v", c, err)
	}

	// One fact per person even though the overlap makes two chunks state some of them: the repeat is merged into the
	// edge the first chunk stored, and so the person's entity is linked to both chunks, never to the parent.
	if len(res.Edges) != 12 || len(res.Entities) != 13 {
		t.Errorf("%d edges, %d entities", len(res.Edges), len(res.Entities))
	}
	links, err := graph.ListEntityArtifacts(ctx, d, dsID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if !isChunk[l.ArtifactID] {
			t.Errorf("entity %d is linked to the parent artifact", l.EntityID)
		}
	}
	elinks, _ := graph.ListEdgeArtifacts(ctx, d, dsID)
	shared := false
	perEdge := map[int64]int{}
	for _, l := range elinks {
		if !isChunk[l.ArtifactID] {
			t.Errorf("edge %d is linked to the parent artifact", l.EdgeID)
		}
		perEdge[l.EdgeID]++
		shared = shared || perEdge[l.EdgeID] > 1
	}
	if !shared {
		t.Error("no fact is backed by two overlapping chunks")
	}
	ses, _ := graph.ListEntitySessions(ctx, d, dsID)
	if len(ses) != 13 {
		t.Errorf("entity sessions = %d", len(ses))
	}

	// Every chunk is embedded, into the dedicated chunk index.
	if res.EmbeddedChunks != n {
		t.Errorf("embedded %d chunks, want %d", res.EmbeddedChunks, n)
	}
	got, err := vs.Query(ctx, memory_core.ChunkDataset(dsID), []float32{1, 1}, vector.QueryOptions{TopK: 100})
	if err != nil || len(got) != n {
		t.Fatalf("chunk vectors = %d, %v; want %d", len(got), err, n)
	}
	for _, m := range got {
		id := int64(m.Metadata["artifact_id"].(float64))
		if !isChunk[id] || m.ID != memory.ChunkVectorID(id) || m.Metadata["kind"] != memory.VectorKindChunk || int64(m.Metadata["parent_id"].(float64)) != res.ArtifactID {
			t.Errorf("chunk vector %s: %v", m.ID, m.Metadata)
		}
	}
	st, err := memory.VectorStats(ctx, d, vs, "u1", "docs")
	if err != nil {
		t.Fatal(err)
	}
	for _, ix := range st.Indexes {
		if ix.Index == memory_core.IndexChunks && (ix.Expected != n || ix.Count != n) {
			t.Errorf("chunk index stats = %+v", ix)
		}
	}

	// The trajectory shows the split, one extraction per chunk, and a store step per chunk.
	count := map[string]int{}
	for _, s := range res.Trajectory.Steps {
		for _, c := range s.ToolCalls {
			count[c.FunctionName]++
		}
	}
	if count["chunk_text"] != 1 || count["extract_graph"] != n || count["store_artifact"] != n || count["store_parent_artifact"] != 1 || count["embed"] != n {
		t.Errorf("tool steps = %v", count)
	}
}

func TestRememberShortTextIsNotChunked(t *testing.T) {
	dbtest.Run(t, testRememberShortTextIsNotChunked)
}

func testRememberShortTextIsNotChunked(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	vs, _ := vector.NewChromem(":memory:", 2)
	res, err := memory_remember.Remember(ctx, d, &personLLM{}, "u1", "docs", "", document(2), relational.Now(),
		memory.WithVectors(vs, &fakeEmbedder{}), memory_core.WithChunking(chunk.Config{Size: 512, OverlapPercent: 20}))
	if err != nil {
		t.Fatal(err)
	}
	dsID, _ := relational.DatasetID(ctx, d, "u1", "docs")
	if c, _ := graph.DatasetCounts(ctx, d, dsID); res.Chunks != nil || res.EmbeddedChunks != 0 || c.Artifacts != 1 || c.Chunks != 0 {
		t.Errorf("chunks = %v, counts = %+v", res.Chunks, c)
	}
	if got, _ := vs.Query(ctx, memory_core.ChunkDataset(dsID), []float32{1, 1}, vector.QueryOptions{TopK: 10}); len(got) != 0 {
		t.Errorf("%d chunk vectors for a short text", len(got))
	}
}

func TestRememberChunkExtractionFailureStoresNothing(t *testing.T) {
	dbtest.Run(t, testRememberChunkExtractionFailureStoresNothing)
}

func testRememberChunkExtractionFailureStoresNothing(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	llm := &personLLM{failOn: 2}
	_, err := memory_remember.Remember(ctx, d, llm, "u1", "docs", "", document(12), relational.Now(),
		memory_core.WithChunking(chunk.Config{Size: 64, OverlapPercent: 20}))
	if err == nil || !strings.Contains(err.Error(), "chunk 2 of") {
		t.Fatalf("err = %v", err)
	}
	if dsID, err := relational.DatasetID(ctx, d, "u1", "docs"); err == nil {
		if c, _ := graph.DatasetCounts(ctx, d, dsID); c != (graph.Counts{}) {
			t.Errorf("counts = %+v after a failed extraction", c)
		}
	}
}

func TestRememberInvalidChunking(t *testing.T) { dbtest.Run(t, testRememberInvalidChunking) }

func testRememberInvalidChunking(t *testing.T, d *sql.DB) {
	_, err := memory_remember.Remember(context.Background(), d, &personLLM{}, "u1", "docs", "", "x", relational.Now(),
		memory_core.WithChunking(chunk.Config{Size: 5}))
	if err == nil {
		t.Error("want an error for a chunk size below the minimum")
	}
}
