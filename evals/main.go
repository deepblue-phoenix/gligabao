// Command evals runs end-to-end evals against the real LLM.
//
// Each suite in evals/suites/*.json is a JSON list of cases. The cases run in the eval database (-db) behind an
// in-process HTTP server running in eval mode. That database is reset at the start of every invocation (unless
// -keep-db), so the artifacts, entities and edges the cases create do not pile up between runs. The experiments
// are not kept there: they live in the main database (-main-db), which is never reset, so they survive the resets. The case is run through the eval endpoints (/v1/gb/eval/remember and
// /v1/gb/eval/recall): remember (once per artifact, in order), then recall. These return the ATIF trajectory of
// every call along with its result.
// Each case remembers into its own dataset, named after the case (see datasetName), so every case's graph is kept. The
// dataset is deleted at the start of the case, so it is recreated fresh (matters with -keep-db, where it may exist).
// Besides the answer, the stored temporal facts (edges with valid_at / invalid_at) are checked in the database.
//
// Usage: go run ./evals [-v] [-suites dir] [case-name ...]   (requires ANTHROPIC_API_KEY)
// With -suites, suites are read from that directory instead of the embedded evals/suites (used by gligabao_int).
// With -v, every LLM call's input and output is printed.
// With -atif dir, the trajectories are stored there as JSON (default: an "atif" directory next to the -main-db file,
// since the experiments reference them; with -main-db "" only the in-memory copies are used).
//
// Every invocation is a run of an experiment (-experiment, default "default"), persisted in the main database
// together with the evals (suite + case) it executed, their results and the IDs of their ATIF trajectories; see
// internal/experiments. With -main-db "" nothing is persisted.
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
	"github.com/deepblue-phoenix/gligabao/internal/experiments"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/server"
)

//go:embed suites/*.json
var suitesFS embed.FS

// datasetName is the dataset a case remembers into and recalls from: one per case, named after it, so the graph of
// every case stays in the eval database after the run (and can be viewed in the admin UI) instead of being replaced by
// the next case. Characters a dataset name does not allow become "_"; the name is cut to the 64-byte limit.
func datasetName(c Case) string {
	b := []byte(c.Name)
	for i, ch := range b {
		ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			i > 0 && (ch == '.' || ch == '_' || ch == '-')
		if !ok {
			b[i] = '_'
		}
	}
	if len(b) > 64 {
		b = b[:64]
	}
	return string(b)
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

// evalDBPath (-db) is the SQLite file all cases run in ("" = a throwaway database per case). It is reset at the start
// of each invocation (see resetEvalDB), and cases stay independent because each one has its own dataset (named after
// the case), which is soft-deleted first so remember starts from a new, empty one; the graph of every case stays. It holds no experiments, those are in mainDBPath.
var evalDBPath string

// mainDBPath (-main-db) is the SQLite file of the main (non-eval) database, where the experiments and their runs are
// recorded ("" = nothing is recorded). It is never reset.
var mainDBPath string

// resetEvalDB deletes the eval database at path (SQLite file and its -wal/-shm/-journal companions) together with its
// chromem vector directory, so the next open starts empty. Missing files are fine. It refuses to touch the main
// database, which holds the experiments.
func resetEvalDB(path, mainPath string) error {
	if path == "" {
		return nil
	}
	abs := func(p string) string {
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return filepath.Clean(p)
	}
	if mainPath != "" && abs(path) == abs(mainPath) {
		return fmt.Errorf("the eval database %q is the main database, which holds the experiments and is never reset", path)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// The default vector directory of db.Open: next to the database file.
	return os.RemoveAll(strings.TrimSuffix(path, filepath.Ext(path)) + ".vectors")
}

// atifDir is where the server stores the trajectories (-atif); empty = a throwaway per-case directory. The experiments
// reference the files, so the default is next to the main database, not the (reset) eval one.
var atifDir string

// Outcome is what one eval case produced.
type Outcome struct {
	Answer   string
	Problems []string // failed temporal-fact expectations
	// Trajectories are the ATIF trajectories returned by the eval endpoints: one per remember, then the recall's.
	Trajectories []*atif.Trajectory
	// TrajectoryDir is where the trajectory files were stored ("" if they were throwaway).
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

// run executes one case against a fresh database through the eval endpoints. The outcome is returned even
// when err is set, holding whatever was collected before the failure.
func run(c Case, llm memory.LLM, verbose bool) (out *Outcome, err error) {
	out = &Outcome{TrajectoryDir: atifDir}
	dir, err := os.MkdirTemp("", "gligabao-eval-*")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(dir)
	if atifDir == "" { // the trajectories come back in the responses, so the stored copies are throwaway
		memory.TrajectoryDir = filepath.Join(dir, "atif")
	}

	// The eval database (-db), which the server's admin UI can show (reset at the start of the invocation);
	// throwaway if -db is empty.
	dbPath := evalDBPath
	if dbPath == "" {
		dbPath = filepath.Join(dir, "eval.db")
	}
	database, err := db.Open(db.Config{SQLitePath: dbPath})
	if err != nil {
		return out, err
	}
	defer database.Close()

	srv := httptest.NewServer(server.NewHandler(database, llm, true, false))
	defer srv.Close()

	// Start from a clean dataset: soft-delete any previous one of this case (404 just means there was none; only
	// happens with -keep-db); remember below recreates it with a new id and an empty graph.
	dataset := datasetName(c)
	if err := deleteDataset(srv.URL, dataset); err != nil {
		return out, fmt.Errorf("delete dataset %q: %w", dataset, err)
	}

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
	if err := post(srv.URL+"/v1/gb/eval/recall", map[string]any{"user_id": evalUser, "query": c.Query, "dataset": dataset}, &rec); err != nil {
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

// runCases runs every case through llm, prints PASS/FAIL per case and returns how many failed. If tracker is not
// nil, the result of each case is recorded in its experiment run.
func runCases(cases []Case, llm memory.LLM, verbose bool, tracker *experiments.Tracker) (failed int) {
	for _, c := range cases {
		l := llm
		if verbose {
			fmt.Printf("=== %s\n", c.Name)
			l = &verboseLLM{inner: llm}
		}
		started := time.Now()
		out, err := run(c, l, verbose)
		answer, problems := out.Answer, out.Problems
		cost, steps := out.Cost()
		if verbose {
			fmt.Printf("  trajectories: %d, steps: %d, cost: $%.6f\n", len(out.Trajectories), steps, cost)
		}

		// Everything that makes the case fail: temporal-fact problems, then expected answer text that is missing.
		var missing []string
		if err == nil && len(problems) == 0 {
			for _, want := range c.ExpectContains {
				if !strings.Contains(strings.ToLower(answer), strings.ToLower(want)) {
					missing = append(missing, want)
				}
			}
		}
		if tracker != nil {
			res := experiments.EvalResult{
				Suite: c.Suite, Name: c.Name, Answer: answer, Err: err,
				Problems:  problems,
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
	flag.StringVar(&atifDir, "atif", "", `store every ATIF trajectory as JSON in this directory (default: "atif" next to the -main-db file)`)
	experiment := flag.String("experiment", "default", "name of the experiment this run belongs to (created if new; needs -main-db)")
	experimentDesc := flag.String("experiment-desc", "", "description of the experiment, used when it is created")
	flag.StringVar(&evalDBPath, "db", "bin/gligabao-eval.db", `SQLite file the cases run in (show it with ./run_server_local.sh); reset at the start of every run; "" = a throwaway database per case`)
	flag.StringVar(&mainDBPath, "main-db", db.DefaultSQLitePath, `SQLite file of the main database, where the experiments and their runs are recorded (never reset); "" = record nothing`)
	keepDB := flag.Bool("keep-db", false, "do not reset the eval database (-db) before running")
	flag.Parse()
	if atifDir == "" && mainDBPath != "" {
		// The experiments reference the trajectories by ID, so keep their files next to the main database.
		atifDir = filepath.Join(filepath.Dir(mainDBPath), "atif")
	}
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

	// Start from an empty eval database: what the previous run's cases stored there is dropped.
	if !*keepDB {
		if err := resetEvalDB(evalDBPath, mainDBPath); err != nil {
			return fmt.Errorf("reset eval database: %w", err)
		}
	}

	// The experiment run is recorded in the main database, which outlives the eval database resets; without
	// -main-db there is nowhere to keep it.
	var tracker *experiments.Tracker
	if mainDBPath != "" {
		trackDB, err := db.Open(db.Config{SQLitePath: mainDBPath})
		if err != nil {
			return err
		}
		defer trackDB.Close()
		tracker, err = experiments.Start(context.Background(), trackDB.Relational, experiments.Options{
			Name:        *experiment,
			Description: *experimentDesc,
			Params:      map[string]any{"suites_dir": *suitesDir, "cases": flag.Args(), "atif_dir": atifDir},
		})
		if err != nil {
			return fmt.Errorf("start experiment: %w", err)
		}
	}

	failed := runCases(cases, llm, *verbose, tracker)

	fmt.Printf("---\npassed: %d, failed: %d\n", len(cases)-failed, failed)
	if tracker != nil {
		if run, err := tracker.Finish(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: finish experiment run: %v\n", err)
		} else {
			fmt.Printf("experiment %q run %d: %s, cost $%.6f\n", tracker.Experiment.Name, run.ID, run.Status, run.CostUSD)
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
	return nil
}
