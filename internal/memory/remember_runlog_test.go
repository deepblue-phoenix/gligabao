package memory_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// onlyRun returns the one remember run of the database.
func onlyRun(t *testing.T, d *sql.DB) *relational.RememberRun {
	t.Helper()
	runs, total, err := relational.ListRememberRuns(context.Background(), d, relational.RememberRunFilter{}, 10, 0)
	if err != nil || total != 1 || len(runs) != 1 {
		t.Fatalf("remember runs: %d, %v", total, err)
	}
	return &runs[0]
}

// checkInferences checks the run logs against gb_inferences: every inference of type remember is in exactly one stage of
// one of the logs, and no stage that runs no model has any.
func checkInferences(t *testing.T, d *sql.DB, runLogs ...*relational.RememberRun) {
	t.Helper()
	all, _, err := relational.ListInferences(context.Background(), d, relational.InferenceFilter{Type: memory_remember.CallRemember}, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	held := map[int64]int{}
	for _, run := range runLogs {
		for _, s := range relational.RememberStages {
			if ids := run.Stages[s.Name].InferenceIDs; !s.Model && len(ids) > 0 {
				t.Errorf("stage %s runs no model but has inferences %v", s.Name, ids)
			}
			for _, id := range run.Stages[s.Name].InferenceIDs {
				held[id]++
			}
		}
	}
	for _, r := range all {
		if held[r.ID] != 1 {
			t.Errorf("inference %d (stage %s) is in %d stages of the run logs", r.ID, r.Stage, held[r.ID])
		}
	}
	if len(held) != len(all) {
		t.Errorf("the run logs hold %d inferences, gb_inferences has %d", len(held), len(all))
	}
}

// stageRuns returns the inferences of each stage of the run log.
func stageRuns(t *testing.T, d *sql.DB, run *relational.RememberRun) map[string][]relational.Inference {
	t.Helper()
	out := map[string][]relational.Inference{}
	for name, st := range run.Stages {
		for _, id := range st.InferenceIDs {
			r, err := relational.GetInference(context.Background(), d, id)
			if err != nil {
				t.Fatal(err)
			}
			out[name] = append(out[name], *r)
		}
	}
	return out
}

func stageNames(run *relational.RememberRun, status string) []string {
	var out []string
	for _, s := range relational.RememberStages {
		if run.Stages[s.Name].Status == status {
			out = append(out, s.Name)
		}
	}
	sort.Strings(out)
	return out
}

func TestRememberRunLogText(t *testing.T) { dbtest.Run(t, testRememberRunLogText) }

// A text that is one chunk, with vectors: the stages it goes through succeeded with their times and inferences, and the others
// are not started.
func testRememberRunLogText(t *testing.T, d *sql.DB) {
	vs, err := vector.NewChromem(":memory:", 2)
	if err != nil {
		t.Fatal(err)
	}
	opts := []memory_core.Option{memory.WithVectors(vs, &fakeEmbedder{}), memory_core.WithSessionID("s9"),
		memory_core.WithModels(memory_core.Models{memory_core.StageExtractGraph: "m-x"})}
	// The second call finds a fact of the first about Acme, so it has candidates: it searches for similar entities.
	if _, err := memory_remember.Remember(context.Background(), d, &personLLM{}, "u1", "work", "episode", "Person2 works at Acme.", relational.Now(), opts...); err != nil {
		t.Fatal(err)
	}
	res, err := memory_remember.Remember(context.Background(), d, &personLLM{}, "u1", "work", "episode", "Person1 works at Acme.", relational.Now(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	runs, total, err := relational.ListRememberRuns(context.Background(), d, relational.RememberRunFilter{}, 10, 0)
	if err != nil || total != 2 {
		t.Fatalf("remember runs: %d, %v", total, err)
	}
	run := &runs[0] // the newest
	if res.RememberRunID != run.ID || run.UserID != "u1" || run.Dataset != "work" || run.Kind != "episode" || run.Status != relational.RememberSucceeded ||
		run.Error != nil || run.FinishedAt == nil || run.ArtifactID == nil || *run.ArtifactID != res.ArtifactID {
		t.Fatalf("run = %+v, result run id %d, artifact %d", run, res.RememberRunID, res.ArtifactID)
	}
	if run.Params["session_id"] != "s9" || run.Params["models"] == nil {
		t.Errorf("params = %v", run.Params)
	}
	want := []string{"extract_graph", "load_candidates", "resolve_edges", "store"}
	if got := stageNames(run, relational.RememberSucceeded); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("succeeded stages = %v, want %v", got, want)
	}
	for _, name := range want {
		st := run.Stages[name]
		if st.Error != nil || st.StartedAt == nil || st.FinishedAt == nil || st.FinishedAt.Before(*st.StartedAt) || st.StartedAt.Before(run.StartedAt) {
			t.Errorf("stage %s = %+v", name, st)
		}
	}
	// The stages the call does not use are skipped: nothing is left as not started in a run that succeeded.
	wantSkipped := []string{"chunk_text", "classify_complexity", "fetch_series", "observe_series", "resolve_geo", "store_series", "summarize_chunk", "web_search"}
	if got := stageNames(run, relational.RememberSkipped); strings.Join(got, ",") != strings.Join(wantSkipped, ",") {
		t.Errorf("skipped stages = %v, want %v", got, wantSkipped)
	}
	if got := stageNames(run, relational.RememberNotStarted); len(got) != 0 {
		t.Errorf("not started stages = %v", got)
	}
	checkInferences(t, d, &runs[1], run)
	runs1, runs2 := stageRuns(t, d, &runs[1]), stageRuns(t, d, run)
	// The first call has no candidates: no search for similar entities and no comparison of facts.
	if len(runs1["extract_graph"]) != 1 || len(runs1["load_candidates"]) != 0 || len(runs1["resolve_edges"]) != 0 || len(runs1["store"]) != 1 {
		t.Errorf("first call: %+v", runs1)
	}
	if g := runs2["extract_graph"]; len(g) != 1 || g[0].Stage != "extract_graph" || g[0].Model != "person-model" {
		t.Errorf("extract_graph runs = %+v", g)
	}
	// The embeddings belong to the stage that made them: the search for similar entities, and the storage.
	if len(runs2["load_candidates"]) != 1 || runs2["load_candidates"][0].Stage != memory_remember.StageEmbed ||
		len(runs2["store"]) != 1 || runs2["store"][0].Stage != memory_remember.StageEmbed ||
		len(runs2["resolve_edges"]) != 1 || runs2["resolve_edges"][0].Stage != memory_core.StageResolveEdges {
		t.Errorf("second call: %+v", runs2)
	}
}

// outputsOf returns the stage outputs of the run by stage and name.
func outputsOf(t *testing.T, d *sql.DB, run *relational.RememberRun) map[string][]relational.RememberStageOutput {
	t.Helper()
	all, err := relational.ListRememberStageOutputs(context.Background(), d, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]relational.RememberStageOutput{}
	for _, o := range all {
		if !json.Valid(o.Output) {
			t.Errorf("output %d (%s) is not JSON: %s", o.ID, o.Name, o.Output)
		}
		out[o.Stage+"/"+o.Name] = append(out[o.Stage+"/"+o.Name], o)
	}
	return out
}

func TestRememberRunLogOutputs(t *testing.T) { dbtest.Run(t, testRememberRunLogOutputs) }

// The stages file what they produced, for debugging: the extraction with the raw answer of the LLM, the candidates, the
// comparison and the storage; for a chunked text the ones of each chunk, labeled; and a failed stage files what it has.
func testRememberRunLogOutputs(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	opts := []memory_core.Option{memory_core.WithChunking(memory_chunk.Config{Size: 64, OverlapPercent: 40})}
	// The second call has a candidate (Acme's fact of the first), so the facts are compared.
	for _, text := range []string{"Person2 works at Acme.", "Person1 works at Acme."} {
		if _, err := memory_remember.Remember(ctx, d, &personLLM{}, "u1", "work", "", text, relational.Now(), opts...); err != nil {
			t.Fatal(err)
		}
	}
	runs, _, err := relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{}, 10, 0)
	if err != nil || len(runs) != 2 {
		t.Fatal(runs, err)
	}
	outs := outputsOf(t, d, &runs[0])
	for _, k := range []string{"extract_graph/extract_graph", "load_candidates/load_candidates", "resolve_edges/resolve_edges", "store/store"} {
		if len(outs[k]) != 1 || outs[k][0].Label != "" {
			t.Errorf("%s: %+v", k, outs[k])
		}
	}
	var ex struct {
		Entities []struct{ Name string }
		Edges    []any
		Raw      string
	}
	if err := json.Unmarshal(outs["extract_graph/extract_graph"][0].Output, &ex); err != nil || len(ex.Entities) == 0 || len(ex.Edges) == 0 || ex.Raw == "" {
		t.Errorf("extract_graph output %s: %v", outs["extract_graph/extract_graph"][0].Output, err)
	}
	var st struct {
		ArtifactID int64 `json:"artifact_id"`
	}
	if err := json.Unmarshal(outs["store/store"][0].Output, &st); err != nil || runs[0].ArtifactID == nil || st.ArtifactID != *runs[0].ArtifactID {
		t.Errorf("store output %s, run artifact %v", outs["store/store"][0].Output, runs[0].ArtifactID)
	}
	if got := outputsOf(t, d, &runs[1]); len(got["extract_graph/extract_graph"]) != 1 || len(got["resolve_edges/resolve_edges"]) != 1 {
		t.Errorf("first call: %v", got)
	}
	if len(outs) != 4 {
		t.Errorf("a text that is one chunk has outputs %v", outs)
	}

	// A chunked text: one of each per chunk, with the chunk as the label, and the chunking itself.
	res, err := memory_remember.Remember(ctx, d, &personLLM{}, "u2", "docs", "", document(8), relational.Now(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	n := len(res.Chunks)
	docRuns, _, err := relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{UserID: "u2"}, 10, 0)
	if err != nil || len(docRuns) != 1 {
		t.Fatal(docRuns, err)
	}
	outs = outputsOf(t, d, &docRuns[0])
	for _, k := range []string{"extract_graph/extract_graph", "summarize_chunk/summarize_chunk", "load_candidates/load_candidates", "resolve_edges/resolve_edges", "store/store"} {
		if len(outs[k]) != n || outs[k][0].Label != fmt.Sprintf("chunk 1 of %d", n) || outs[k][n-1].Label != fmt.Sprintf("chunk %d of %d", n, n) {
			t.Errorf("%s: %d outputs, want %d: %+v", k, len(outs[k]), n, outs[k])
		}
	}
	if len(outs["chunk_text/chunk_text"]) != 1 || len(outs["store/store_parent_artifact"]) != 1 {
		t.Errorf("chunk_text or parent: %v", outs)
	}

	// A call that fails files the raw answer that could not be parsed.
	if _, err := memory_remember.Remember(ctx, d, &badJSONLLM{}, "u3", "work", "", "Person1 works at Acme.", relational.Now()); err == nil {
		t.Fatal("Remember did not fail")
	}
	failed, _, err := relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{UserID: "u3"}, 10, 0)
	if err != nil || len(failed) != 1 {
		t.Fatal(failed, err)
	}
	fo := outputsOf(t, d, &failed[0])["extract_graph/extract_graph"]
	if len(fo) != 1 || !strings.Contains(string(fo[0].Output), "this is not json") {
		t.Errorf("failed extraction outputs: %+v", fo)
	}
}

func TestRememberRunLogChunks(t *testing.T) { dbtest.Run(t, testRememberRunLogChunks) }

// A chunked text: a stage that runs for each chunk spans them and lists the inference of each.
func testRememberRunLogChunks(t *testing.T, d *sql.DB) {
	res, err := memory_remember.Remember(context.Background(), d, &personLLM{}, "u1", "docs", "", document(8), relational.Now(),
		memory_core.WithChunking(memory_chunk.Config{Size: 64, OverlapPercent: 40}))
	if err != nil {
		t.Fatal(err)
	}
	n := len(res.Chunks)
	run := onlyRun(t, d)
	if n < 3 || run.Status != relational.RememberSucceeded || run.ArtifactID == nil || *run.ArtifactID != res.ArtifactID {
		t.Fatalf("%d chunks, run %+v", n, run)
	}
	want := []string{"chunk_text", "extract_graph", "load_candidates", "resolve_edges", "store", "summarize_chunk"}
	if got := stageNames(run, relational.RememberSucceeded); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("succeeded stages = %v, want %v", got, want)
	}
	checkInferences(t, d, run)
	runs := stageRuns(t, d, run)
	if len(runs["extract_graph"]) != n || len(runs["summarize_chunk"]) != n {
		t.Errorf("%d chunks, %d extract_graph and %d summarize_chunk runs", n, len(runs["extract_graph"]), len(runs["summarize_chunk"]))
	}
	for i, c := range res.Chunks {
		if c.Summary.InferenceID != run.Stages["summarize_chunk"].InferenceIDs[i] {
			t.Errorf("chunk %d: summary run %d, run log %v", i, c.Summary.InferenceID, run.Stages["summarize_chunk"].InferenceIDs)
		}
	}
}

func TestRememberRunLogFailure(t *testing.T) { dbtest.Run(t, testRememberRunLogFailure) }

// A call that fails: the run and the stage that failed say why, the stages it passed that the call does not use are skipped, and the
// stages that were never reached are not started.
func testRememberRunLogFailure(t *testing.T, d *sql.DB) {
	_, err := memory_remember.Remember(context.Background(), d, &personLLM{failOn: 1}, "u1", "work", "", "Person1 works at Acme.", relational.Now())
	if err == nil {
		t.Fatal("Remember did not fail")
	}
	run := onlyRun(t, d)
	if run.Status != relational.RememberFailed || run.Error == nil || *run.Error != "llm down" || run.FinishedAt == nil || run.ArtifactID != nil {
		t.Fatalf("run = %+v", run)
	}
	eg := run.Stages["extract_graph"]
	if eg.Status != relational.RememberFailed || eg.Error == nil || *eg.Error != "llm down" || eg.StartedAt == nil || eg.FinishedAt == nil || len(eg.InferenceIDs) != 0 {
		t.Errorf("extract_graph = %+v", eg)
	}
	if got := stageNames(run, relational.RememberFailed); len(got) != 1 {
		t.Errorf("failed stages = %v", got)
	}
	// The stages it passed that the call does not use are skipped; those it never reached are not started.
	if got := stageNames(run, relational.RememberSkipped); strings.Join(got, ",") != "chunk_text,classify_complexity" {
		t.Errorf("skipped stages = %v", got)
	}
	if got := stageNames(run, relational.RememberNotStarted); len(got) != len(relational.RememberStages)-3 {
		t.Errorf("not started stages = %v", got)
	}

	// The failure of a later stage, on the second chunk: the first is stored, the run and its stage say which failed.
	_, err = memory_remember.Remember(context.Background(), d, &personLLM{failOn: 3}, "u1", "docs", "", document(8), relational.Now(),
		memory_core.WithChunking(memory_chunk.Config{Size: 64, OverlapPercent: 40}))
	if err == nil {
		t.Fatal("Remember of the document did not fail")
	}
	runs, _, err := relational.ListRememberRuns(context.Background(), d, relational.RememberRunFilter{Dataset: "docs", Status: relational.RememberFailed}, 10, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("failed runs: %+v, %v", runs, err)
	}
	if got := runs[0].Stages["extract_graph"]; got.Status != relational.RememberFailed || got.Error == nil || runs[0].Stages["chunk_text"].Status != relational.RememberSucceeded {
		t.Errorf("stages = %+v", runs[0].Stages)
	}
}

func TestRememberRunLogSeries(t *testing.T) { dbtest.Run(t, testRememberRunLogSeries) }

// The series the caller supplies are a stage of their own, whether they are stored with a text that is one chunk (in its transaction) or
// after the chunks of a long one, and so are the observations made of them.
func testRememberRunLogSeries(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	series := []memory_core.SeriesSpec{{Key: "headcount", Points: []memory_core.SeriesPoint{{TS: at, Value: 10}, {TS: at.AddDate(0, 1, 0), Value: 12}}}}
	for i, text := range []string{"Person1 works at Acme.", document(8)} {
		user := fmt.Sprintf("u%d", i)
		if _, err := memory_remember.Remember(ctx, d, &personLLM{}, user, "work", "", text, relational.Now(),
			memory_core.WithSeries(series), memory_core.WithSeriesObservations(true), memory_core.WithChunking(memory_chunk.Config{Size: 64, OverlapPercent: 40})); err != nil {
			t.Fatal(err)
		}
		runs, _, err := relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{UserID: user}, 10, 0)
		if err != nil || len(runs) != 1 {
			t.Fatal(runs, err)
		}
		run := runs[0]
		if i == 1 && run.Stages["chunk_text"].Status != relational.RememberSucceeded {
			t.Errorf("the document was not chunked: %+v", run.Stages["chunk_text"])
		}
		for _, name := range []string{"store", "store_series", "observe_series"} {
			if st := run.Stages[name]; st.Status != relational.RememberSucceeded || st.Error != nil || st.StartedAt == nil || st.FinishedAt == nil {
				t.Errorf("text %d: stage %s = %+v", i, name, st)
			}
		}
		if n := len(run.Stages["observe_series"].InferenceIDs); n == 0 {
			t.Errorf("text %d: the observation has no inferences", i)
		}
	}
	all, _, err := relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{}, 10, 0)
	if err != nil || len(all) != 2 {
		t.Fatal(all, err)
	}
	checkInferences(t, d, &all[0], &all[1])
}

// searcher answers every web search with a briefing that the test LLM extracts a fact from.
type searcher struct{ fail bool }

func (s searcher) WebSearch(context.Context, ai.WebSearchRequest) (*ai.WebSearchResponse, error) {
	if s.fail {
		return nil, context.DeadlineExceeded
	}
	return &ai.WebSearchResponse{Text: "Person7 works at Acme in 2026.", Provider: ai.ProviderAnthropic, Model: "search-model", Searches: 1, InputTokens: 10, OutputTokens: 5, CostUSD: 0.02}, nil
}

func TestRememberRunLogWebSearch(t *testing.T) { dbtest.Run(t, testRememberRunLogWebSearch) }

// The web search is a stage of its own: it holds the inferences of the searches and of the extraction of their results, and a
// failed search is the stage's, while the call itself succeeds.
func testRememberRunLogWebSearch(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	res, err := memory_remember.Remember(ctx, d, &personLLM{}, "u1", "work", "", "Person1 works at Acme.", relational.Now(),
		memory_core.WithWebSearch(true), memory_core.WithWebSearcher(searcher{}))
	if err != nil {
		t.Fatal(err)
	}
	run := onlyRun(t, d)
	if len(res.WebSearches) == 0 || run.Stages["web_search"].Status != relational.RememberSucceeded {
		t.Fatalf("%d web searches, web_search stage %+v", len(res.WebSearches), run.Stages["web_search"])
	}
	checkInferences(t, d, run)
	runs := stageRuns(t, d, run)
	// A search, and the extraction of its briefing, per entity (their inferences are the stage's, not extract_graph's).
	if len(runs["web_search"]) < len(res.WebSearches)*2 || len(runs["extract_graph"]) != 1 {
		t.Errorf("web_search runs %d, extract_graph runs %d, %d searches", len(runs["web_search"]), len(runs["extract_graph"]), len(res.WebSearches))
	}
	// Its outputs: the entities picked, each search, and the steps of the briefings it stored (told apart by name and label).
	outs := outputsOf(t, d, run)
	for _, k := range []string{"web_search/select_web_search_entities", "web_search/web_search", "web_search/extract_graph", "web_search/store"} {
		if len(outs[k]) == 0 {
			t.Errorf("no output %s: %v", k, outs)
		}
	}
	if o := outs["web_search/extract_graph"]; len(o) > 0 && !strings.HasPrefix(o[0].Label, "web search for ") {
		t.Errorf("label of a briefing's extraction: %q", o[0].Label)
	}
	if len(outs["extract_graph/extract_graph"]) != 1 {
		t.Errorf("the text's extraction: %v", outs["extract_graph/extract_graph"])
	}
	for _, r := range runs["web_search"] {
		if r.Stage != memory_core.StageWebSearch && r.Stage != memory_core.StageExtractGraph && r.Stage != memory_core.StageResolveEdges {
			t.Errorf("web_search holds %+v", r)
		}
	}

	// A failing search fails the stage, not the call.
	if _, err := memory_remember.Remember(ctx, d, &personLLM{}, "u2", "work", "", "Person1 works at Acme.", relational.Now(),
		memory_core.WithWebSearch(true), memory_core.WithWebSearcher(searcher{fail: true})); err != nil {
		t.Fatal(err)
	}
	runs2, _, err := relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{UserID: "u2"}, 10, 0)
	if err != nil || len(runs2) != 1 {
		t.Fatal(runs2, err)
	}
	ws := runs2[0].Stages["web_search"]
	if runs2[0].Status != relational.RememberSucceeded || runs2[0].Error != nil || ws.Status != relational.RememberFailed || ws.Error == nil || !strings.Contains(*ws.Error, "search") {
		t.Errorf("run %s, web_search %+v", runs2[0].Status, ws)
	}
}

func TestRememberRunLogStages(t *testing.T) {
	// Every stage of the table that runs a model is a stage of gb_inferences, or one that holds the runs of another.
	known := map[string]bool{}
	for _, s := range memory_core.Stages {
		known[s] = true
	}
	for _, name := range []string{memory_remember.StageClassifyComplexity, memory_remember.StageEmbed} {
		known[name] = true
	}
	for _, s := range []string{memory_core.StageExtractGraph, memory_core.StageSummarizeChunk, memory_core.StageResolveEdges, memory_core.StageWebSearch,
		memory_remember.StageClassifyComplexity} {
		if def, ok := relational.RememberStageDefOf(s); !ok || !def.Model || !known[s] {
			t.Errorf("stage %s is not a model stage of the run log", s)
		}
	}
	for name, want := range map[string]string{memory_remember.StageChunkText: "chunk_text", memory_remember.StageLoadCandidates: "load_candidates",
		memory_remember.StageStore: "store", memory_remember.StageStoreSeries: "store_series", memory_remember.StageFetchSeries: "fetch_series",
		memory_remember.StageObserveSeries: "observe_series"} {
		if _, ok := relational.RememberStageDefOf(name); !ok || name != want {
			t.Errorf("stage %q is not the run log's %q", name, want)
		}
	}
}

// badJSONLLM answers with text that is not the JSON the extraction asks for.
type badJSONLLM struct{}

func (badJSONLLM) Complete(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{StopReason: "end_turn", Model: "bad-model", Text: "this is not json"}, nil
}
