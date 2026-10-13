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
	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/experiments"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/server"
)

//go:embed suites/*.json
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
}

// Artifact is one text to remember; ReferenceTime (RFC 3339) is when it was written. Kind is the artifact kind
// ("knowledge" or "episode"); empty uses the server default.
type Artifact struct {
	Kind          string `json:"kind"`
	Text          string `json:"text"`
	ReferenceTime string `json:"reference_time"`
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
func run(c Case, runID int64, database *db.DB, llm memory_core.LLM, emb memory_core.Embedder, verbose bool) (out *Outcome, err error) {
	out = &Outcome{TrajectoryDir: atifDir}

	srv := httptest.NewServer(server.NewHandler(database, llm, emb, true, false))
	defer srv.Close()

	dataset := datasetName(c, runID)
	defer func() {
		// Recorded with the result, so the graph can be read back from the dataset later (also after a failure).
		if id, err := relational.DatasetID(context.Background(), database.Relational, evalUser, dataset); err == nil {
			out.DatasetID = id
		}
	}()

	for i, m := range c.Remember {
		body := map[string]any{"user_id": evalUser, "text": m.Text, "dataset": dataset}
		if m.Kind != "" {
			body["kind"] = m.Kind
		}
		if m.ReferenceTime != "" {
			body["reference_time"] = m.ReferenceTime
		}
		var rem struct {
			ArtifactID int64            `json:"artifact_id"`
			Trajectory *atif.Trajectory `json:"trajectory"`
		}
		if err := post(srv.URL+"/v1/gb/eval/remember", body, &rem); err != nil {
			return out, fmt.Errorf("remember #%d: %w", i+1, err)
		}
		if rem.Trajectory == nil {
			return out, fmt.Errorf("remember #%d: response has no trajectory (is the server in eval mode?)", i+1)
		}
		out.Trajectories = append(out.Trajectories, rem.Trajectory)
		out.ArtifactIDs = append(out.ArtifactIDs, []int64{rem.ArtifactID})
	}
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
		// check records a held check (pass) or a failed one (fail).
		check := func(ok bool, pass, fail string) {
			if ok {
				out.PassedChecks = append(out.PassedChecks, pass)
			} else {
				out.Problems = append(out.Problems, fail)
			}
		}
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
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Post(url, "application/json", bytes.NewReader(b))
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

// runCases runs every case through llm (and embeds the entities and edges with emb, if not nil) in the eval database, prints PASS/FAIL per case and returns how many failed.
// The result of each case is recorded in the tracker's experiment run (kept in the same database).
func runCases(cases []Case, llm memory_core.LLM, emb memory_core.Embedder, verbose bool, database *db.DB, tracker *experiments.Tracker) (failed int) {
	for _, c := range cases {
		l := llm
		if verbose {
			fmt.Printf("=== %s\n", c.Name)
			l = &verboseLLM{inner: llm}
		}
		started := time.Now()
		out, err := run(c, tracker.Run.ID, database, l, emb, verbose)
		answer, problems := out.Answer, out.Problems
		cost, steps := out.Cost()
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
	flag.Parse()
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
	tracker, err := experiments.Start(context.Background(), database.Relational, experiments.Options{
		Name:        *experiment,
		Description: *experimentDesc,
		Params:      map[string]any{"suites_dir": *suitesDir, "cases": flag.Args(), "atif_dir": atifDir},
	})
	if err != nil {
		return fmt.Errorf("start experiment: %w", err)
	}

	failed := runCases(cases, llm, emb, *verbose, database, tracker)

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
