package memory_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// classLLM is personLLM that also answers the classify_graph requests with answer, and keeps what it was asked.
type classLLM struct {
	personLLM
	answer   string
	prompts  []string
	classErr error
}

func (l *classLLM) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if strings.Contains(req.System, "You classify a knowledge graph") {
		l.prompts = append(l.prompts, req.Messages[0].Content)
		if l.classErr != nil {
			return nil, l.classErr
		}
		return &ai.Response{StopReason: "end_turn", Model: "class-model", Provider: "test", Text: l.answer, InputTokens: 10, OutputTokens: 5}, nil
	}
	return l.personLLM.Complete(ctx, req)
}

func TestRememberClassifiesGraph(t *testing.T) { dbtest.Run(t, testRememberClassifiesGraph) }

func testRememberClassifiesGraph(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	llm := &classLLM{answer: "```json\n{\"classes\": [\"Corporate Staff\", \"employment\", \"corporate_staff\", \"fourth\", \"fifth\"]}\n```"}
	res, err := memory_remember.Remember(ctx, d, llm, "u1", "work", "", "Person1 works at Acme.", relational.Now(),
		memory_core.WithClassifyGraph(true, nil), memory_core.WithReasoning(memory_core.Reasoning{memory_core.StageClassifyGraph: "low"}))
	if err != nil {
		t.Fatal(err)
	}
	// normalized, without repeats, at most 3
	want := []string{"corporate_staff", "employment", "fourth"}
	if len(res.Classes) != 1 || res.Classes[0].ArtifactID != res.ArtifactID || strings.Join(res.Classes[0].Classes, ",") != strings.Join(want, ",") || res.Classes[0].Error != "" {
		t.Fatalf("classes = %+v, want %v", res.Classes, want)
	}
	if len(llm.prompts) != 1 || !strings.Contains(llm.prompts[0], "Person1 (person)") || !strings.Contains(llm.prompts[0], "Person1 --works_at--> Acme") ||
		strings.Contains(llm.prompts[0], "existing_classes") {
		t.Fatalf("prompt = %q", llm.prompts)
	}

	// one record per class, all with the run of the one call; the run holds the model and the level it ran with
	dsID, _ := relational.DatasetID(ctx, d, "u1", "work")
	recs, err := graph.ListArtifactClasses(ctx, d, dsID)
	if err != nil || len(recs) != 3 {
		t.Fatalf("records %+v, %v", recs, err)
	}
	for i, r := range recs {
		if r.ArtifactID != res.ArtifactID || r.Class != want[i] || r.InferenceID == 0 || r.InferenceID != res.Classes[0].InferenceID {
			t.Errorf("record %d = %+v", i, r)
		}
	}
	run, err := relational.GetInference(ctx, d, recs[0].InferenceID)
	if err != nil || run.Stage != memory_core.StageClassifyGraph || run.Reasoning != "low" || run.Type != memory_remember.CallRemember {
		t.Fatalf("run = %+v, %v", run, err)
	}

	// the run log has the stage, with that run
	runs, _, err := relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{}, 10, 0)
	if err != nil || len(runs) != 1 {
		t.Fatal(runs, err)
	}
	st := runs[0].Stages[memory_core.StageClassifyGraph]
	if st.Status != relational.RememberSucceeded || len(st.InferenceIDs) != 1 || st.InferenceIDs[0] != recs[0].InferenceID {
		t.Fatalf("stage = %+v", st)
	}

	// the next call is told of the classes the dataset has, and may add classes to its own artifact
	llm.answer = `{"classes": ["employment"]}`
	res2, err := memory_remember.Remember(ctx, d, llm, "u1", "work", "", "Person2 works at Acme.", relational.Now(), memory_core.WithClassifyGraph(true, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := llm.prompts[1]; !strings.Contains(got, "<existing_classes>\ncorporate_staff\nemployment\nfourth\n</existing_classes>") && !strings.Contains(got, "<existing_classes>\nemployment") {
		t.Fatalf("second prompt lacks the existing classes: %q", got)
	}
	recs, _ = graph.ListArtifactClasses(ctx, d, dsID)
	if len(recs) != 4 || recs[3].ArtifactID != res2.ArtifactID || recs[3].InferenceID == recs[0].InferenceID {
		t.Fatalf("records %+v", recs)
	}

	// the classes reach the knowledge dataset the training hook gets
	ds, err := memory.LoadKnowledgeDataset(ctx, d, "u1", "work")
	if err != nil || len(ds.Graphs) != 2 || len(ds.Graphs[0].Classes) != 3 || len(ds.Graphs[1].Classes) != 1 || ds.Graphs[1].Classes[0].Class != "employment" ||
		ds.Graphs[0].Classes[0].InferenceID != recs[0].InferenceID {
		t.Fatalf("dataset graphs = %+v, %v", ds.Graphs, err)
	}

	// the knowledge dataset gives the top classes of the whole dataset (what the hook of an embedding program needs)
	top, err := ds.TopClasses(ctx, 1)
	if err != nil || len(top) != 1 || top[0].Count < 1 {
		t.Fatalf("top classes = %+v, %v", top, err)
	}
	if _, err := (memory_core.KnowledgeDataset{}).TopClasses(ctx, 1); err == nil {
		t.Fatal("a dataset not loaded by gligabao must say so")
	}
}

func TestRememberClassifiesGraphAllowed(t *testing.T) {
	dbtest.Run(t, testRememberClassifiesGraphAllowed)
}

func testRememberClassifiesGraphAllowed(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	allowed, err := memory_core.ResolveGraphClasses([]string{"Employment", "supply chain"})
	if err != nil {
		t.Fatal(err)
	}
	// only the allowed classes are kept
	llm := &classLLM{answer: `{"classes": ["travel", "Supply Chain"]}`}
	res, err := memory_remember.Remember(ctx, d, llm, "u1", "work", "", "Person1 works at Acme.", relational.Now(), memory_core.WithClassifyGraph(true, allowed))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Classes[0].Classes; len(got) != 1 || got[0] != "supply_chain" {
		t.Fatalf("classes = %v", got)
	}
	if !strings.Contains(llm.prompts[0], "<allowed_classes>\nemployment\nsupply_chain\n</allowed_classes>") {
		t.Fatalf("prompt = %q", llm.prompts[0])
	}

	// no allowed class chosen: the stage fails, the call does not, nothing is stored for the artifact, but the run is kept
	llm.answer = `{"classes": ["travel"]}`
	res, err = memory_remember.Remember(ctx, d, llm, "u1", "work", "", "Person2 works at Acme.", relational.Now(), memory_core.WithClassifyGraph(true, allowed))
	if err != nil {
		t.Fatalf("an unusable class answer must not fail the call: %v", err)
	}
	if len(res.Classes) != 1 || len(res.Classes[0].Classes) != 0 || res.Classes[0].Error == "" || res.Classes[0].InferenceID == 0 {
		t.Fatalf("classes = %+v", res.Classes)
	}
	runs, _, _ := relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{}, 10, 0)
	if st := runs[0].Stages[memory_core.StageClassifyGraph]; st.Status != relational.RememberFailed || st.Error == nil {
		t.Fatalf("stage = %+v", st)
	}
	dsID, _ := relational.DatasetID(ctx, d, "u1", "work")
	if recs, _ := graph.ListArtifactClasses(ctx, d, dsID); len(recs) != 1 {
		t.Fatalf("records %+v", recs)
	}

	// a failed LLM call fails the stage only
	llm.classErr = context.DeadlineExceeded
	if _, err := memory_remember.Remember(ctx, d, llm, "u1", "work", "", "Person3 works at Acme.", relational.Now(), memory_core.WithClassifyGraph(true, allowed)); err != nil {
		t.Fatalf("a failed class call must not fail the call: %v", err)
	}
}

func TestRememberClassifiesEachChunk(t *testing.T) { dbtest.Run(t, testRememberClassifiesEachChunk) }

func testRememberClassifiesEachChunk(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	llm := &classLLM{answer: `{"classes": ["staff"]}`}
	res, err := memory_remember.Remember(ctx, d, llm, "u1", "docs", "", document(12), relational.Now(),
		memory_core.WithChunking(memory_chunk.Config{Size: 64, OverlapPercent: 40}), memory_core.WithClassifyGraph(true, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) < 3 || len(res.Classes) != len(res.Chunks) {
		t.Fatalf("%d chunks, %d class results", len(res.Chunks), len(res.Classes))
	}
	for i, c := range res.Classes {
		if c.ArtifactID != res.Chunks[i].ArtifactID || len(c.Classes) != 1 {
			t.Errorf("class result %d = %+v, chunk %+v", i, c, res.Chunks[i])
		}
	}
	// the parent artifact has no graph and no class
	dsID, _ := relational.DatasetID(ctx, d, "u1", "docs")
	recs, _ := graph.ListArtifactClasses(ctx, d, dsID)
	for _, r := range recs {
		if r.ArtifactID == res.ArtifactID {
			t.Fatalf("the parent has a class: %+v", r)
		}
	}
	if len(recs) != len(res.Chunks) {
		t.Fatalf("%d records for %d chunks", len(recs), len(res.Chunks))
	}
}

func TestRememberWithoutClassifyGraph(t *testing.T) { dbtest.Run(t, testRememberWithoutClassifyGraph) }

func testRememberWithoutClassifyGraph(t *testing.T, d *sql.DB) {
	llm := &classLLM{answer: `{"classes": ["staff"]}`}
	res, err := memory_remember.Remember(context.Background(), d, llm, "u1", "work", "", "Person1 works at Acme.", relational.Now())
	if err != nil || len(llm.prompts) != 0 || len(res.Classes) != 0 {
		t.Fatalf("classes without being asked: %v, %d prompts, %+v", err, len(llm.prompts), res.Classes)
	}
}
