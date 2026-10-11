// Command evals runs end-to-end evals against the real LLM.
//
// Each suite in evals/suites/*.json is a JSON list of cases. Each case gets a fresh temporary database and an in-process
// HTTP server running in eval mode. The case is run through the eval endpoints (/v1/gb/eval/remember and
// /v1/gb/eval/recall): remember (once per artifact, in order), then recall. These return the ATIF trajectory of
// every call along with its result.
// All artifacts go to the dataset "test", which is deleted at the start of each case so it is recreated fresh.
// Besides the answer, the stored temporal facts (edges with valid_at / invalid_at) are checked in the database.
//
// Usage: go run ./evals [-v] [-suites dir] [case-name ...]   (requires ANTHROPIC_API_KEY)
// With -suites, suites are read from that directory instead of the embedded evals/suites (used by gligabao_int).
// With -v, every LLM call's input and output is printed.
// With -atif dir, the trajectories are also stored there as JSON (default: only the in-memory copies are used).
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
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/server"
)

//go:embed suites/*.json
var suitesFS embed.FS

// evalDataset is the dataset every eval remembers into and recalls from.
const evalDataset = "test"

// evalUser owns the eval dataset.
const evalUser = "eval"

//---------------------------------------------------------------------------------------------

// Case is one eval: remember some texts in order, then ask a question about them.
type Case struct {
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
		for i, c := range suite {
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
	inner memory.LLM
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

// atifDir is where the server also stores the trajectories (-atif); empty = a throwaway per-case directory.
var atifDir string

// Outcome is what one eval case produced.
type Outcome struct {
	Answer   string
	Problems []string // failed temporal-fact expectations
	// Trajectories are the ATIF trajectories returned by the eval endpoints: one per remember, then the recall's.
	Trajectories []*atif.Trajectory
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

// run executes one case against a fresh database through the eval endpoints. The outcome is returned even
// when err is set, holding whatever was collected before the failure.
func run(c Case, llm memory.LLM, verbose bool) (out *Outcome, err error) {
	out = &Outcome{}
	dir, err := os.MkdirTemp("", "gligabao-eval-*")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(dir)
	if atifDir == "" { // the trajectories come back in the responses, so the stored copies are throwaway
		memory.TrajectoryDir = filepath.Join(dir, "atif")
	}

	database, err := db.Open(db.Config{SQLitePath: filepath.Join(dir, "eval.db")})
	if err != nil {
		return out, err
	}
	defer database.Close()

	srv := httptest.NewServer(server.NewHandler(database, llm, true, false))
	defer srv.Close()

	// Start from a clean dataset: soft-delete any previous "test" (404 just means there was none); remember
	// below recreates it with a new id and an empty graph.
	if err := deleteDataset(srv.URL, evalDataset); err != nil {
		return out, fmt.Errorf("delete dataset %q: %w", evalDataset, err)
	}

	for i, m := range c.Remember {
		body := map[string]any{"user_id": evalUser, "text": m.Text, "dataset": evalDataset}
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
	}
	edges, err := graph.ListEdges(context.Background(), database.Graph, evalUser, evalDataset)
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
		switch {
		case found == nil:
			out.Problems = append(out.Problems, desc+": no edge stored")
		default:
			if want.ValidAt != "" && tstr(found.ValidAt) != want.ValidAt {
				out.Problems = append(out.Problems, fmt.Sprintf("%s: valid_at = %s, want %s", desc, tstr(found.ValidAt), want.ValidAt))
			}
			if want.InvalidAt != "" && tstr(found.InvalidAt) != want.InvalidAt {
				out.Problems = append(out.Problems, fmt.Sprintf("%s: invalid_at = %q, want %s", desc, tstr(found.InvalidAt), want.InvalidAt))
			}
			if want.Ended != (found.InvalidAt != nil) {
				out.Problems = append(out.Problems, fmt.Sprintf("%s: ended = %v, want %v", desc, found.InvalidAt != nil, want.Ended))
			}
			if want.Ended && found.ExpiredAt == nil {
				out.Problems = append(out.Problems, desc+": ended but expired_at is not set")
			}
		}
	}

	var rec struct {
		Answer     string           `json:"answer"`
		Trajectory *atif.Trajectory `json:"trajectory"`
	}
	if err := post(srv.URL+"/v1/gb/eval/recall", map[string]any{"user_id": evalUser, "query": c.Query, "dataset": evalDataset}, &rec); err != nil {
		return out, fmt.Errorf("recall: %w", err)
	}
	if rec.Trajectory == nil {
		return out, errors.New("recall: response has no trajectory (is the server in eval mode?)")
	}
	out.Answer = rec.Answer
	out.Trajectories = append(out.Trajectories, rec.Trajectory)
	return out, nil
}

// deleteDataset soft-deletes a dataset through the API; a dataset that does not exist is not an error.
func deleteDataset(baseURL, name string) error {
	req, err := http.NewRequest(http.MethodDelete, baseURL+"/v1/gb/datasets/"+name+"?user_id="+url.QueryEscape(evalUser), nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
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

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

func mainErr() error {
	verbose := flag.Bool("v", false, "print the input and output of every LLM call")
	suitesDir := flag.String("suites", "", "read suites from this directory instead of the embedded ones")
	flag.StringVar(&atifDir, "atif", "", "also store every ATIF trajectory as JSON in this directory")
	flag.Parse()
	if atifDir != "" {
		memory.TrajectoryDir = atifDir
	}

	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY must be set to run evals")
	}
	llm, err := ai.New(key)
	if err != nil {
		return err
	}
	cases, err := loadCases(flag.Args(), *suitesDir)
	if err != nil {
		return err
	}

	failed := 0
	for _, c := range cases {
		var l memory.LLM = llm
		if *verbose {
			fmt.Printf("=== %s\n", c.Name)
			l = &verboseLLM{inner: llm}
		}
		out, err := run(c, l, *verbose)
		answer, problems := out.Answer, out.Problems
		cost, steps := out.Cost()
		if *verbose {
			fmt.Printf("  trajectories: %d, steps: %d, cost: $%.6f\n", len(out.Trajectories), steps, cost)
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
		var missing []string
		for _, want := range c.ExpectContains {
			if !strings.Contains(strings.ToLower(answer), strings.ToLower(want)) {
				missing = append(missing, want)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("FAIL %s: answer missing %q\n     answer: %s\n", c.Name, missing, answer)
			failed++
			continue
		}
		fmt.Printf("PASS %s: %s\n", c.Name, answer)
	}

	fmt.Printf("---\npassed: %d, failed: %d\n", len(cases)-failed, failed)
	if failed > 0 {
		os.Exit(1)
	}
	return nil
}
