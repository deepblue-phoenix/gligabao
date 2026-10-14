// Command evals runs end-to-end evals against the real LLM.
//
// Each suite in evals/suites/*.json is a JSON list of cases. The cases run in the eval database (-db), which isolates
// the evaluation data from the main database. It is never reset: every run of an experiment creates its own datasets
// (one per case, named after the case and the run, see datasetName), so runs do not interfere with each other and
// the graphs of all runs are kept. The experiments and their runs are recorded in the same eval database. The cases
// are run behind an in-process HTTP server in eval mode through the eval endpoints (/v1/gb/eval/remember and
// /v1/gb/eval/recall): remember (once per artifact, in order), then recall. These return the ATIF trajectory of
// every call along with its result.
// Besides the answer, the stored temporal facts (edges with valid_at / invalid_at) are checked in the database.
//
// Usage: go run ./evals [-v] [-suites dir] [case-name ...]   (requires ANTHROPIC_API_KEY)
// With -suites, suites are read from that directory instead of the embedded evals/suites (used by gligabao_int).
// With -v, every LLM call's input and output is printed.
// With -atif dir, the trajectories are stored there as JSON (default: an "atif" directory next to the -db file,
// since the experiments reference them).
//
// Every invocation is a run of an experiment (-experiment, default "default"), persisted in the eval database
// together with the evals (suite + case) it executed, their results and the IDs of their ATIF trajectories; see
// internal/experiments.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/experiments"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/server"
)

//go:embed suites/*.json suites/corpus/*.txt
var suitesFS embed.FS

// datasetName is the dataset a case remembers into and recalls from: one per case and run of the experiment
// ("<case>_run<runID>"), so runs are isolated from each other and the graph of every case of every run stays in the
// eval database (and can be viewed in the admin UI). Characters a dataset name does not allow become "_"; the case
// part is cut so the whole name fits the 64-byte limit.
func datasetName(c Case, runID int64) string {
	suffix := fmt.Sprintf("_run%d", runID)
	b := []byte(c.Name)
	for i, ch := range b {
		ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			i > 0 && (ch == '.' || ch == '_' || ch == '-')
		if !ok {
			b[i] = '_'
		}
	}
	if max := 64 - len(suffix); len(b) > max {
		b = b[:max]
	}
	return string(b) + suffix
}

// evalUser owns the eval dataset.
const evalUser = "eval"

//---------------------------------------------------------------------------------------------

// Case is one eval: remember some texts in order, then ask a question about them.
type Case struct {
	// Suite is the suite (file name without ".json") the case was loaded from; it is not part of the JSON.
	Suite          string       `json:"-"`
	Name           string       `json:"name"`
	Remember       []Artifact   `json:"remember"`
	Query          string       `json:"query"`
	ExpectContains []string     `json:"expect_contains"`
	ExpectEdges    []ExpectEdge `json:"expect_edges"`
	// ChunkSize overrides the chunk size (in tokens) the case runs with; 0 keeps the default (1024). A smaller one
	// lets a modest corpus need many chunks.
	ChunkSize int `json:"chunk_size"`
	// ExpectMinChunks, when > 0, turns on the chunking checks: the remembered texts must have been split into at
	// least this many chunks in total, and the chunks must be stored consistently (see checkChunks).
	ExpectMinChunks int `json:"expect_min_chunks"`
	// Models picks the LLM model of each stage of remember for this case ("extract_graph", "resolve_edges", "extract_image"), on top of the
	// runner's -models: a stage named here wins, a stage named by neither uses the default model.
	Models map[string]string `json:"models,omitempty"`
	// ExpectWebSearches, when > 0, turns on the web search checks: the remember entries that set "web_search" must have
	// stored at least this many web_search artifacts, each with its query, a parent in the dataset (the artifact the
	// searched entity came from) and entities of its own (see checkWebSearches).
	ExpectWebSearches int `json:"expect_web_searches"`
	// ExpectSeries are timeseries that the remember entries' "timeseries" must have stored (see checkSeries). A case
	// with no Query skips the recall (recall does not use timeseries yet).
	ExpectSeries []ExpectSeries `json:"expect_series"`
}

// ExpectSeries is a stored series to check, identified by Key and Field ("value" if empty). Points is the number of
// points it must have; First and Last, when given, are the values of its first and last point. AnchorEntity, when
// set, is the name (case-insensitive) of an entity it must be anchored to; AnchorArtifact says it must be anchored to
// an artifact.
type ExpectSeries struct {
	Key            string   `json:"key"`
	Field          string   `json:"field"`
	Points         int      `json:"points"`
	First          *float64 `json:"first,omitempty"`
	Last           *float64 `json:"last,omitempty"`
	AnchorEntity   string   `json:"anchor_entity,omitempty"`
	AnchorArtifact bool     `json:"anchor_artifact,omitempty"`
}

// Artifact is one text to remember; ReferenceTime (RFC 3339) is when it was written. Kind is the artifact kind
// ("knowledge" or "episode"); empty uses the server default. TextFile, instead of Text, names a file (relative to the
// suites directory, e.g. "corpus/doc.txt") whose content is the text; loadCases reads it into Text.
type Artifact struct {
	Kind          string `json:"kind"`
	Text          string `json:"text"`
	TextFile      string `json:"text_file,omitempty"`
	ReferenceTime string `json:"reference_time"`
	// WebSearch makes the remember call search the web for the latest world events about its key entities (see
	// docs/web-search.md). It needs the Anthropic API key the runner already requires.
	WebSearch bool `json:"web_search,omitempty"`
	// Timeseries is the "timeseries" of the remember request, passed as is (see docs/timeseries.md).
	Timeseries json.RawMessage `json:"timeseries,omitempty"`
}

// ExpectEdge is a temporal fact that must be stored between two entities (any relation, names case-insensitive).
// Empty ValidAt / InvalidAt are not checked. Ended says whether the fact must have an invalid_at.
type ExpectEdge struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	ValidAt   string `json:"valid_at"`
	InvalidAt string `json:"invalid_at"`
	Ended     bool   `json:"ended"`
}

func loadCases(only []string, suitesDir string) ([]Case, error) {
	var fsys fs.FS = suitesFS
	dir := "suites"
	if suitesDir != "" {
		fsys, dir = os.DirFS(suitesDir), "."
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var cases []Case
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var suite []Case
		if err := json.Unmarshal(data, &suite); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		suiteName := strings.TrimSuffix(e.Name(), ".json")
		for i, c := range suite {
			c.Suite = suiteName
			if c.Name == "" {
				return nil, fmt.Errorf("%s: case %d has no name", e.Name(), i)
			}
			if seen[c.Name] {
				return nil, fmt.Errorf("%s: duplicate case name %q", e.Name(), c.Name)
			}
			c.Remember = slices.Clone(c.Remember)
			for j, m := range c.Remember {
				if m.TextFile == "" {
					continue
				}
				if m.Text != "" {
					return nil, fmt.Errorf("%s: case %q remember #%d has both text and text_file", e.Name(), c.Name, j+1)
				}
				b, err := fs.ReadFile(fsys, path.Join(dir, m.TextFile))
				if err != nil {
					return nil, fmt.Errorf("%s: case %q remember #%d: %w", e.Name(), c.Name, j+1, err)
				}
				c.Remember[j].Text = string(b)
			}
			seen[c.Name] = true
			if len(only) == 0 || contains(only, c.Name) {
				cases = append(cases, c)
			}
		}
	}
	for _, name := range only {
		found := false
		for _, c := range cases {
			found = found || c.Name == name
		}
		if !found {
			return nil, fmt.Errorf("unknown case %q", name)
		}
	}
	return cases, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

//---------------------------------------------------------------------------------------------

// verboseLLM wraps an LLM and prints the input and output of every call.
type verboseLLM struct {
	inner memory_core.LLM
	calls int
}

func (v *verboseLLM) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	v.calls++
	n := v.calls
	fmt.Printf("  ┌─ LLM call #%d input (model=%q max_tokens=%d)\n", n, req.Model, req.MaxTokens)
	if req.System != "" {
		fmt.Printf("  │ [system]\n%s\n", indent(req.System))
	}
	for _, m := range req.Messages {
		fmt.Printf("  │ [%s]\n%s\n", m.Role, indent(m.Content))
	}
	resp, err := v.inner.Complete(ctx, req)
	if err != nil {
		fmt.Printf("  └─ LLM call #%d error: %v\n", n, err)
		return resp, err
	}
	fmt.Printf("  ├─ LLM call #%d output (model=%s stop=%s in=%d out=%d cost=$%.6f)\n%s\n  └─\n",
		n, resp.Model, resp.StopReason, resp.InputTokens, resp.OutputTokens, resp.CostUSD, indent(resp.Text))
	return resp, nil
}

func indent(s string) string {
	return "  │   " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  │   ")
}

//---------------------------------------------------------------------------------------------

// atifDir is where the server stores the trajectories (-atif). The experiments reference the files, so the default is
// next to the eval database.
var atifDir string

// Outcome is what one eval case produced.
type Outcome struct {
	Answer   string
	Problems []string // failed temporal-fact expectations
	// DatasetID is the dataset the case ran in (0 if it was never created); it holds the graph the case built.
	DatasetID int64
	// PassedChecks are the temporal-fact expectations that held.
	PassedChecks []string
	// ArtifactIDs[i] are the IDs, in DatasetID, of the artifacts of Trajectories[i]: the artifact a remember call
	// stored, the artifacts a recall's evidence was sourced from.
	ArtifactIDs [][]int64
	// Trajectories are the ATIF trajectories returned by the eval endpoints: one per remember, then the recall's.
	Trajectories []*atif.Trajectory
	// TrajectoryDir is where the trajectory files were stored.
	TrajectoryDir string
	// Chunks is the total number of chunks the remember calls reported (0 if no text was chunked).
	Chunks int
}

// Cost is the total LLM cost of all trajectories, in USD.
func (o *Outcome) Cost() (usd float64, steps int) {
	for _, t := range o.Trajectories {
		if t.FinalMetrics != nil {
			usd += t.FinalMetrics.TotalCostUSD
			steps += t.FinalMetrics.TotalSteps
		}
	}
	return usd, steps
}

// run executes one case of the experiment run runID in the eval database through the eval endpoints, in a dataset
// of its own (see datasetName). The outcome is returned even when err is set, holding whatever was collected
// before the failure.
func run(c Case, runID int64, database *db.DB, llm memory_core.LLM, emb memory_core.Embedder, ws memory_core.WebSearcher, verbose bool, defaults memory_core.Models) (out *Outcome, err error) {
	out = &Outcome{TrajectoryDir: atifDir}

	caseModels, err := memory_core.ResolveModels(c.Models)
	if err != nil {
		return out, fmt.Errorf("models: %w", err)
	}
	models := defaults.Merge(caseModels)

	if c.ChunkSize != 0 {
		// The handlers read the chunking settings from the DB they are given: use a copy with the case's size.
		cp := *database
		cp.Chunking = chunk.Config{Size: c.ChunkSize, OverlapPercent: chunk.DefaultOverlapPercent}
		if err := cp.Chunking.Validate(); err != nil {
			return out, fmt.Errorf("chunk_size: %w", err)
		}
		database = &cp
	}
	srv := httptest.NewServer(server.NewHandler(database, llm, emb, nil, ws, nil, true, false))
	defer srv.Close()

	dataset := datasetName(c, runID)
	defer func() {
		// Recorded with the result, so the graph can be read back from the dataset later (also after a failure).
		if id, err := relational.DatasetID(context.Background(), database.Relational, evalUser, dataset); err == nil {
			out.DatasetID = id
		}
	}()

	// check records a held check (pass) or a failed one (fail).
	check := func(ok bool, pass, fail string) {
		if ok {
			out.PassedChecks = append(out.PassedChecks, pass)
		} else {
			out.Problems = append(out.Problems, fail)
		}
	}
	var parents []int64            // artifact IDs of the remembered texts that were chunked
	var reported []rememberedChunk // the chunks the remember calls reported

	for i, m := range c.Remember {
		body := map[string]any{"user_id": evalUser, "text": m.Text, "dataset": dataset}
		if m.Kind != "" {
			body["kind"] = m.Kind
		}
		if m.ReferenceTime != "" {
			body["reference_time"] = m.ReferenceTime
		}
		if len(models) > 0 {
			body["models"] = models
		}
		if m.WebSearch {
			body["web_search"] = true
		}
		if len(m.Timeseries) > 0 {
			body["timeseries"] = m.Timeseries
		}
		var rem struct {
			ArtifactID int64             `json:"artifact_id"`
			Chunks     []rememberedChunk `json:"chunks"`
			Trajectory *atif.Trajectory  `json:"trajectory"`
		}
		if err := post(srv.URL+"/v1/gb/eval/remember", body, &rem); err != nil {
			return out, fmt.Errorf("remember #%d: %w", i+1, err)
		}
		if rem.Trajectory == nil {
			return out, fmt.Errorf("remember #%d: response has no trajectory (is the server in eval mode?)", i+1)
		}
		out.Trajectories = append(out.Trajectories, rem.Trajectory)
		out.ArtifactIDs = append(out.ArtifactIDs, []int64{rem.ArtifactID})
		if len(rem.Chunks) > 0 {
			parents = append(parents, rem.ArtifactID)
			reported = append(reported, rem.Chunks...)
		}
	}
	out.Chunks = len(reported)
	edges, err := graph.ListEdges(context.Background(), database.Graph, evalUser, dataset)
	if err != nil {
		return out, err
	}
	tstr := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return relational.FormatTime(*t)
	}
	if verbose {
		fmt.Println("  stored edges:")
		for _, e := range edges {
			fmt.Printf("    [%d] %s --%s--> %s valid_at=%s invalid_at=%q expired_at=%q\n",
				e.ID, e.Source, e.Relation, e.Target, tstr(e.ValidAt), tstr(e.InvalidAt), tstr(e.ExpiredAt))
		}
	}
	for _, want := range c.ExpectEdges {
		var found *graph.Edge
		for i, e := range edges {
			if strings.EqualFold(e.Source, want.Source) && strings.EqualFold(e.Target, want.Target) {
				found = &edges[i]
				break
			}
		}
		desc := want.Source + " -> " + want.Target
		if found == nil {
			check(false, "", desc+": no edge stored")
			continue
		}
		check(true, desc+": edge stored", "")
		if want.ValidAt != "" {
			check(tstr(found.ValidAt) == want.ValidAt, fmt.Sprintf("%s: valid_at = %s", desc, want.ValidAt),
				fmt.Sprintf("%s: valid_at = %s, want %s", desc, tstr(found.ValidAt), want.ValidAt))
		}
		if want.InvalidAt != "" {
			check(tstr(found.InvalidAt) == want.InvalidAt, fmt.Sprintf("%s: invalid_at = %s", desc, want.InvalidAt),
				fmt.Sprintf("%s: invalid_at = %q, want %s", desc, tstr(found.InvalidAt), want.InvalidAt))
		}
		check(want.Ended == (found.InvalidAt != nil), fmt.Sprintf("%s: ended = %v", desc, want.Ended),
			fmt.Sprintf("%s: ended = %v, want %v", desc, found.InvalidAt != nil, want.Ended))
		if want.Ended {
			check(found.ExpiredAt != nil, desc+": ended and expired_at is set", desc+": ended but expired_at is not set")
		}
	}

	if c.ExpectMinChunks > 0 {
		if err := checkChunks(context.Background(), database, dataset, c, parents, reported, check); err != nil {
			return out, fmt.Errorf("check chunks: %w", err)
		}
	}

	if c.ExpectWebSearches > 0 {
		if err := checkWebSearches(context.Background(), database, dataset, c.ExpectWebSearches, check); err != nil {
			return out, fmt.Errorf("check web searches: %w", err)
		}
	}

	if len(c.ExpectSeries) > 0 {
		if err := checkSeries(context.Background(), database, dataset, c.ExpectSeries, verbose, check); err != nil {
			return out, fmt.Errorf("check series: %w", err)
		}
	}

	if strings.TrimSpace(c.Query) == "" {
		return out, nil // ingestion only
	}

	var rec struct {
		Answer   string `json:"answer"`
		Evidence struct {
			Edges []struct {
				ID         int64  `json:"id"`
				ArtifactID *int64 `json:"artifact_id"`
			} `json:"edges"`
		} `json:"evidence"`
		Trajectory *atif.Trajectory `json:"trajectory"`
	}
	if err := post(srv.URL+"/v1/gb/eval/recall", map[string]any{"user_id": evalUser, "query": c.Query, "dataset": dataset}, &rec); err != nil {
		return out, fmt.Errorf("recall: %w", err)
	}
	if rec.Trajectory == nil {
		return out, errors.New("recall: response has no trajectory (is the server in eval mode?)")
	}
	out.Answer = rec.Answer
	out.Trajectories = append(out.Trajectories, rec.Trajectory)
	cited := map[int64]*int64{} // cited edge ID -> artifact that created it
	for _, e := range rec.Evidence.Edges {
		cited[e.ID] = e.ArtifactID
	}
	ids, err := sourceArtifactIDs(context.Background(), database, dataset, cited)
	if err != nil {
		// Not worth failing the eval for: the recall itself worked.
		fmt.Fprintf(os.Stderr, "warning: %s: collect the recall's source artifacts: %v\n", c.Name, err)
	}
	out.ArtifactIDs = append(out.ArtifactIDs, ids)
	return out, nil
}

// rememberedChunk is a chunk as reported by the remember response.
type rememberedChunk struct {
	ArtifactID  int64 `json:"artifact_id"`
	Index       int   `json:"index"`
	Tokens      int   `json:"tokens"`
	StartOffset int   `json:"start_offset"`
	EndOffset   int   `json:"end_offset"`
}

// checkChunks verifies the chunking of the case's texts, using the chunks the remember calls reported and what is
// stored in the dataset. None of it depends on what the LLM extracts, except the spread of the facts over the chunks:
//   - at least ExpectMinChunks chunks, none larger than the chunk size;
//   - each chunk is a stored child artifact of one of the texts, numbered from 0, and the stored ones are exactly the
//     reported ones (no chunk lost, none stored twice);
//   - the chunks of a text cover it: the first starts at 0, each one starts at or before the end of the previous one
//     (the overlap) and after its start, and the last ends at the end of the text;
//   - the facts were linked to the chunks (never to the parent), and to at least half of them: a corpus that spreads
//     facts evenly must produce edges from the whole text, not just from its start.
func checkChunks(ctx context.Context, database *db.DB, dataset string, c Case, parents []int64, reported []rememberedChunk,
	check func(ok bool, pass, fail string)) error {
	dsID, err := relational.DatasetID(ctx, database.Relational, evalUser, dataset)
	if err != nil {
		return err
	}
	arts, err := graph.ListArtifacts(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	byID := map[int64]graph.Artifact{}
	children := map[int64][]graph.Artifact{} // parent ID -> its chunks
	for _, a := range arts {
		byID[a.ID] = a
		if a.IsChunk() { // not the web searches that hang off an artifact
			children[a.ParentID] = append(children[a.ParentID], a)
		}
	}

	size := database.Chunking.WithDefaults().Size
	check(len(reported) >= c.ExpectMinChunks, fmt.Sprintf("chunking: %d chunks (at least %d)", len(reported), c.ExpectMinChunks),
		fmt.Sprintf("chunking: %d chunks, want at least %d", len(reported), c.ExpectMinChunks))
	largest := 0
	for _, r := range reported {
		largest = max(largest, r.Tokens)
	}
	check(largest <= size, fmt.Sprintf("chunking: largest chunk is %d tokens (chunk size %d)", largest, size),
		fmt.Sprintf("chunking: a chunk has %d tokens, over the chunk size %d", largest, size))

	stored := 0
	for _, p := range parents {
		stored += len(children[p])
	}
	check(stored == len(reported), fmt.Sprintf("chunking: %d chunk artifacts stored, as reported", stored),
		fmt.Sprintf("chunking: %d chunk artifacts stored, %d reported", stored, len(reported)))

	// Coverage, text by text.
	ok, problem := true, ""
	for _, p := range parents {
		parent, kids := byID[p], children[p]
		for i, k := range kids {
			var prevEnd int
			if i > 0 {
				prevEnd = kids[i-1].EndOffset
			}
			switch {
			case k.ChunkIndex != i:
				ok, problem = false, fmt.Sprintf("artifact %d: chunk %d has chunk_index %d", p, i, k.ChunkIndex)
			case i == 0 && k.StartOffset != 0:
				ok, problem = false, fmt.Sprintf("artifact %d: first chunk starts at %d, not 0", p, k.StartOffset)
			case i > 0 && (k.StartOffset > prevEnd || k.StartOffset <= kids[i-1].StartOffset):
				ok, problem = false, fmt.Sprintf("artifact %d: chunk %d starts at %d (previous: %d-%d)", p, i, k.StartOffset, kids[i-1].StartOffset, prevEnd)
			case k.EndOffset > len(parent.Text) || parent.Text[k.StartOffset:k.EndOffset] != k.Text:
				ok, problem = false, fmt.Sprintf("artifact %d: chunk %d is not text[%d:%d] of its parent", p, i, k.StartOffset, k.EndOffset)
			}
		}
		if len(kids) > 0 && kids[len(kids)-1].EndOffset != len(parent.Text) {
			ok, problem = false, fmt.Sprintf("artifact %d: the last chunk ends at %d, the text at %d", p, kids[len(kids)-1].EndOffset, len(parent.Text))
		}
	}
	check(ok, "chunking: the chunks are consecutive, overlap, and cover the whole text", "chunking: "+problem)

	// Provenance.
	links, err := graph.ListEdgeArtifacts(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	isParent := map[int64]bool{}
	for _, p := range parents {
		isParent[p] = true
	}
	linkedChunks := map[int64]bool{}
	toParent := 0
	for _, l := range links {
		if isParent[l.ArtifactID] {
			toParent++
		} else if byID[l.ArtifactID].IsChunk() {
			linkedChunks[l.ArtifactID] = true
		}
	}
	check(toParent == 0, "chunking: no edge is linked to the parent artifact", fmt.Sprintf("chunking: %d edge links point at the parent artifact, not at a chunk", toParent))
	want := (c.ExpectMinChunks + 1) / 2
	check(len(linkedChunks) >= want, fmt.Sprintf("chunking: edges come from %d chunks (at least %d)", len(linkedChunks), want),
		fmt.Sprintf("chunking: edges come from only %d chunks, want at least %d", len(linkedChunks), want))
	return nil
}

// checkWebSearches verifies what the web search of remember stored in the dataset. None of it depends on what the web
// says, only on how it was stored:
//   - at least min web_search artifacts;
//   - each saves the query that was issued, has text, and has as its parent an artifact of the dataset that is not itself a
//     web search: the artifact from which the searched entity came (a web search is never a chunk, and has no chunk index);
//   - each has entities of its own linked to it (the entities discovered on the web are stored like any other).
func checkWebSearches(ctx context.Context, database *db.DB, dataset string, min int, check func(ok bool, pass, fail string)) error {
	dsID, err := relational.DatasetID(ctx, database.Relational, evalUser, dataset)
	if err != nil {
		return err
	}
	arts, err := graph.ListArtifacts(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	links, err := graph.ListEntityArtifacts(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	linked := map[int64]int{} // artifact ID -> entities linked to it
	for _, l := range links {
		linked[l.ArtifactID]++
	}
	byID := map[int64]graph.Artifact{}
	for _, a := range arts {
		byID[a.ID] = a
	}
	var webs []graph.Artifact
	for _, a := range arts {
		if a.Kind == graph.ArtifactWebSearch {
			webs = append(webs, a)
		}
	}
	check(len(webs) >= min, fmt.Sprintf("web search: %d web_search artifacts (at least %d)", len(webs), min),
		fmt.Sprintf("web search: %d web_search artifacts, want at least %d", len(webs), min))
	ok, problem := true, ""
	for _, w := range webs {
		parent, hasParent := byID[w.ParentID]
		switch {
		case strings.TrimSpace(w.Query) == "":
			ok, problem = false, fmt.Sprintf("artifact %d: no query saved", w.ID)
		case strings.TrimSpace(w.Text) == "":
			ok, problem = false, fmt.Sprintf("artifact %d: no text", w.ID)
		case !hasParent || parent.Kind == graph.ArtifactWebSearch:
			ok, problem = false, fmt.Sprintf("artifact %d: its parent %d is not an artifact the entity came from", w.ID, w.ParentID)
		case linked[w.ID] == 0:
			ok, problem = false, fmt.Sprintf("artifact %d (%q): no entity is linked to it", w.ID, w.Query)
		}
	}
	check(ok, "web search: every web_search artifact has its query, text, a parent artifact and entities", "web search: "+problem)
	return nil
}

// checkSeries verifies the timeseries stored in the dataset against the expected ones: each must exist, have the
// expected number of points and first/last values, and the expected anchors.
func checkSeries(ctx context.Context, database *db.DB, dataset string, want []ExpectSeries, verbose bool, check func(ok bool, pass, fail string)) error {
	dsID, err := relational.DatasetID(ctx, database.Relational, evalUser, dataset)
	if err != nil {
		return err
	}
	stored, err := graph.ListSeries(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	entities, err := graph.ListEntities(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	entityName := map[int64]string{}
	for _, e := range entities {
		entityName[e.ID] = e.Name
	}
	if verbose {
		fmt.Println("  stored series:")
		for _, s := range stored {
			fmt.Printf("    [%d] %s/%s points=%d first=%v last=%v\n", s.ID, s.Key, s.Field, s.PointCount, s.FirstTS, s.LastTS)
		}
	}
	for _, w := range want {
		field := w.Field
		if field == "" {
			field = memory_core.DefaultSeriesField
		}
		desc := "series " + w.Key + "/" + field
		var found *graph.Series
		for i, s := range stored {
			if s.Key == w.Key && s.Field == field {
				found = &stored[i]
				break
			}
		}
		if found == nil {
			check(false, "", desc+": not stored")
			continue
		}
		check(true, desc+": stored", "")
		check(found.PointCount == w.Points, fmt.Sprintf("%s: %d points", desc, w.Points),
			fmt.Sprintf("%s: %d points, want %d", desc, found.PointCount, w.Points))
		pts, err := graph.SeriesPoints(ctx, database.Graph, found.ID, nil, nil, 0)
		if err != nil {
			return err
		}
		check(len(pts) == found.PointCount, desc+": point rows match point_count",
			fmt.Sprintf("%s: %d point rows, point_count %d", desc, len(pts), found.PointCount))
		if len(pts) > 0 {
			if w.First != nil {
				check(pts[0].Value == *w.First, fmt.Sprintf("%s: first value = %v", desc, *w.First),
					fmt.Sprintf("%s: first value = %v, want %v", desc, pts[0].Value, *w.First))
			}
			if w.Last != nil {
				check(pts[len(pts)-1].Value == *w.Last, fmt.Sprintf("%s: last value = %v", desc, *w.Last),
					fmt.Sprintf("%s: last value = %v, want %v", desc, pts[len(pts)-1].Value, *w.Last))
			}
		}
		if w.AnchorEntity == "" && !w.AnchorArtifact {
			continue
		}
		anchors, err := graph.ListSeriesAnchors(ctx, database.Graph, found.ID)
		if err != nil {
			return err
		}
		var onEntity, onArtifact bool
		for _, a := range anchors {
			switch a.Type {
			case graph.AnchorEntity:
				onEntity = onEntity || strings.EqualFold(entityName[a.TargetID], w.AnchorEntity)
			case graph.AnchorArtifact:
				onArtifact = true
			}
		}
		if w.AnchorEntity != "" {
			check(onEntity, fmt.Sprintf("%s: anchored to entity %s", desc, w.AnchorEntity),
				fmt.Sprintf("%s: not anchored to entity %s", desc, w.AnchorEntity))
		}
		if w.AnchorArtifact {
			check(onArtifact, desc+": anchored to an artifact", desc+": not anchored to an artifact")
		}
	}
	return nil
}

// sourceArtifactIDs returns the IDs of the artifacts of the dataset that back the given edges (provenance): the
// artifact that created each edge and every later one that restated the fact (gb_edge_artifacts), ascending.
func sourceArtifactIDs(ctx context.Context, database *db.DB, dataset string, edgeArtifact map[int64]*int64) ([]int64, error) {
	dsID, err := relational.DatasetID(ctx, database.Relational, evalUser, dataset)
	if err != nil {
		return nil, err
	}
	links, err := graph.ListEdgeArtifacts(ctx, database.Graph, dsID)
	if err != nil {
		return nil, err
	}
	want := map[int64]bool{}
	for edgeID, creator := range edgeArtifact {
		if creator != nil {
			want[*creator] = true
		}
		for _, l := range links {
			if l.EdgeID == edgeID {
				want[l.ArtifactID] = true
			}
		}
	}
	ids := make([]int64, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func post(url string, body, out any) error {
	b, _ := json.Marshal(body)
	// A chunked text is extracted chunk by chunk in one request, so it can take long.
	resp, err := (&http.Client{Timeout: 30 * time.Minute}).Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

//---------------------------------------------------------------------------------------------

// runCases runs every case through llm (and embeds the entities and edges with emb, if not nil; ws, if not nil, runs the web searches of the remember entries that ask for them) in the eval database, prints PASS/FAIL per case and returns how many failed.
// The result of each case is recorded in the tracker's experiment run (kept in the same database).
// models are the runner's default model per remember stage (-models); a case's own models win over them.
func runCases(cases []Case, llm memory_core.LLM, emb memory_core.Embedder, ws memory_core.WebSearcher, verbose bool, models memory_core.Models, database *db.DB, tracker *experiments.Tracker) (failed int) {
	for _, c := range cases {
		l := llm
		if verbose {
			fmt.Printf("=== %s\n", c.Name)
			l = &verboseLLM{inner: llm}
		}
		started := time.Now()
		out, err := run(c, tracker.Run.ID, database, l, emb, ws, verbose, models)
		answer, problems := out.Answer, out.Problems
		cost, steps := out.Cost()
		if verbose && out.Chunks > 0 {
			fmt.Printf("  chunks: %d\n", out.Chunks)
		}
		if verbose {
			fmt.Printf("  trajectories: %d, steps: %d, cost: $%.6f\n", len(out.Trajectories), steps, cost)
		}

		// Everything that makes the case fail: temporal-fact problems, then expected answer text that is missing.
		passed := append([]string(nil), out.PassedChecks...)
		var missing []string
		if err == nil {
			for _, want := range c.ExpectContains {
				if !strings.Contains(strings.ToLower(answer), strings.ToLower(want)) {
					missing = append(missing, want)
				} else {
					passed = append(passed, fmt.Sprintf("answer contains %q", want))
				}
			}
		}
		{
			res := experiments.EvalResult{
				Suite: c.Suite, Name: c.Name, Answer: answer, Err: err,
				Problems: problems, PassedChecks: passed,
				ArtifactIDs: out.ArtifactIDs, DatasetID: out.DatasetID,
				StartedAt: started, FinishedAt: time.Now(),
				Trajectories: out.Trajectories, TrajectoryDir: out.TrajectoryDir,
			}
			if len(missing) > 0 {
				res.Problems = append(append([]string(nil), problems...), fmt.Sprintf("answer missing %q", missing))
			}
			if _, rerr := tracker.Record(context.Background(), res); rerr != nil {
				fmt.Fprintf(os.Stderr, "warning: record %s/%s in experiment %q: %v\n", c.Suite, c.Name, tracker.Experiment.Name, rerr)
			}
		}

		if err != nil {
			fmt.Printf("FAIL %s: %v\n", c.Name, err)
			failed++
			continue
		}
		if len(problems) > 0 {
			fmt.Printf("FAIL %s: temporal facts:\n     %s\n     answer: %s\n", c.Name, strings.Join(problems, "\n     "), answer)
			failed++
			continue
		}
		if len(missing) > 0 {
			fmt.Printf("FAIL %s: answer missing %q\n     answer: %s\n", c.Name, missing, answer)
			failed++
			continue
		}
		fmt.Printf("PASS %s: %s\n", c.Name, answer)
	}

	return failed
}

//---------------------------------------------------------------------------------------------

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

func mainErr() error {
	verbose := flag.Bool("v", false, "print the input and output of every LLM call")
	suitesDir := flag.String("suites", "", "read suites from this directory instead of the embedded ones")
	flag.StringVar(&atifDir, "atif", "", `store every ATIF trajectory as JSON in this directory (default: "atif" next to the -db file)`)
	experiment := flag.String("experiment", "default", "name of the experiment this run belongs to (created if new)")
	experimentDesc := flag.String("experiment-desc", "", "description of the experiment, used when it is created")
	evalDBPath := flag.String("db", "bin/gligabao-eval.db", "SQLite file of the eval database: the datasets of the cases, the experiments and their runs (never reset; show it with ./run_server_local.sh)")
	modelsFlag := flag.String("models", "", `LLM model per remember stage, e.g. "extract_graph=claude-sonnet-4-5,resolve_edges=claude-haiku-5-5" (stages: `+strings.Join(memory_core.Stages, ", ")+`); a stage left out uses the default model; a case's "models" win`)
	flag.Parse()
	models, err := memory_core.ParseModels(*modelsFlag)
	if err != nil {
		return fmt.Errorf("-models: %w", err)
	}
	if *evalDBPath == "" {
		return errors.New("-db must not be empty")
	}
	if atifDir == "" {
		// The experiments reference the trajectories by ID, so keep their files next to the eval database.
		atifDir = filepath.Join(filepath.Dir(*evalDBPath), "atif")
	}
	memory_core.TrajectoryDir = atifDir

	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY must be set to run evals")
	}
	llm, err := ai.New(key)
	if err != nil {
		return err
	}
	// remember embeds the entities and edges it stores into the eval database's vector store (see
	// ../docs/vectors.md); without an embedder it stores none, so say so. emb stays a nil interface then.
	var emb memory_core.Embedder
	if e, err := ai.NewEmbedder(os.Getenv(config.EnvOpenAIKey), ai.WithEmbedDimensions(config.DefaultEmbeddingDimensions)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: no embedder (%v): entities and edges will NOT be vector indexed\n", err)
	} else {
		emb = e
	}

	cases, err := loadCases(flag.Args(), *suitesDir)
	if err != nil {
		return err
	}

	database, err := db.Open(db.Config{SQLitePath: *evalDBPath})
	if err != nil {
		return err
	}
	defer database.Close()
	params := map[string]any{"suites_dir": *suitesDir, "cases": flag.Args(), "atif_dir": atifDir}
	if len(models) > 0 {
		params["models"] = models
	}
	tracker, err := experiments.Start(context.Background(), database.Relational, experiments.Options{
		Name:        *experiment,
		Description: *experimentDesc,
		Params:      params,
	})
	if err != nil {
		return fmt.Errorf("start experiment: %w", err)
	}

	failed := runCases(cases, llm, emb, llm, *verbose, models, database, tracker)

	fmt.Printf("---\npassed: %d, failed: %d\n", len(cases)-failed, failed)
	if run, err := tracker.Finish(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: finish experiment run: %v\n", err)
	} else {
		fmt.Printf("experiment %q run %d: %s, cost $%.6f\n", tracker.Experiment.Name, run.ID, run.Status, run.CostUSD)
	}
	if failed > 0 {
		os.Exit(1)
	}
	return nil
}
