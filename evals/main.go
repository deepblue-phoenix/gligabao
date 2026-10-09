// Command evals runs end-to-end evals against the real LLM.
//
// Each case in evals/cases/*.json gets a fresh temporary database and an in-process
// HTTP server. The case is run through the public API: remember (once per memory, in order), then recall.
// Besides the answer, the stored temporal facts (edges with valid_at / invalid_at) are checked in the database.
//
// Usage: go run ./evals [-v] [case-name ...]   (requires ANTHROPIC_API_KEY)
// With -v, every LLM call's input and output is printed.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ivan/gligabao/internal/ai"
	"github.com/ivan/gligabao/internal/db"
	"github.com/ivan/gligabao/internal/memory"
	"github.com/ivan/gligabao/internal/server"
)

//go:embed cases/*.json
var casesFS embed.FS

//---------------------------------------------------------------------------------------------

// Case is one eval: remember some texts in order, then ask a question about them.
type Case struct {
	Name           string       `json:"name"`
	Remember       []Memory     `json:"remember"`
	Query          string       `json:"query"`
	ExpectContains []string     `json:"expect_contains"`
	ExpectEdges    []ExpectEdge `json:"expect_edges"`
}

// Memory is one text to remember; ReferenceTime (RFC 3339) is when it was written.
type Memory struct {
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

func loadCases(only []string) ([]Case, error) {
	entries, err := casesFS.ReadDir("cases")
	if err != nil {
		return nil, err
	}
	var cases []Case
	for _, e := range entries {
		data, err := casesFS.ReadFile("cases/" + e.Name())
		if err != nil {
			return nil, err
		}
		var c Case
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if c.Name == "" {
			c.Name = strings.TrimSuffix(e.Name(), ".json")
		}
		if len(only) == 0 || contains(only, c.Name) {
			cases = append(cases, c)
		}
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
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

// run executes one case against a fresh database. It returns the recall answer and a list of failed
// temporal-fact expectations.
func run(c Case, llm memory.LLM, verbose bool) (answer string, problems []string, err error) {
	dir, err := os.MkdirTemp("", "gligabao-eval-*")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(dir)

	database, err := db.Open(filepath.Join(dir, "eval.db"))
	if err != nil {
		return "", nil, err
	}
	defer database.Close()

	srv := httptest.NewServer(server.NewHandler(database, llm))
	defer srv.Close()

	for i, m := range c.Remember {
		body := map[string]any{"text": m.Text}
		if m.ReferenceTime != "" {
			body["reference_time"] = m.ReferenceTime
		}
		var rem struct {
			MemoryID int64 `json:"memory_id"`
		}
		if err := post(srv.URL+"/v1/gb/remember", body, &rem); err != nil {
			return "", nil, fmt.Errorf("remember #%d: %w", i+1, err)
		}
	}
	rows, err := database.Query(`
		SELECT e.id, s.name, t.name, e.relation, e.valid_at, e.invalid_at, e.expired_at
		FROM gb_edges e JOIN gb_entities s ON s.id = e.source_id JOIN gb_entities t ON t.id = e.target_id ORDER BY e.id`)
	if err != nil {
		return "", nil, err
	}
	type edge struct {
		id                       int64
		source, target, relation string
		validAt                  string
		invalidAt, expiredAt     sql.NullString
	}
	var edges []edge
	for rows.Next() {
		var e edge
		if err := rows.Scan(&e.id, &e.source, &e.target, &e.relation, &e.validAt, &e.invalidAt, &e.expiredAt); err != nil {
			rows.Close()
			return "", nil, err
		}
		edges = append(edges, e)
	}
	rows.Close()
	if verbose {
		fmt.Println("  stored edges:")
		for _, e := range edges {
			fmt.Printf("    [%d] %s --%s--> %s valid_at=%s invalid_at=%q expired_at=%q\n",
				e.id, e.source, e.relation, e.target, e.validAt, e.invalidAt.String, e.expiredAt.String)
		}
	}
	for _, want := range c.ExpectEdges {
		var found *edge
		for i, e := range edges {
			if strings.EqualFold(e.source, want.Source) && strings.EqualFold(e.target, want.Target) {
				found = &edges[i]
				break
			}
		}
		desc := want.Source + " -> " + want.Target
		switch {
		case found == nil:
			problems = append(problems, desc+": no edge stored")
		default:
			if want.ValidAt != "" && found.validAt != want.ValidAt {
				problems = append(problems, fmt.Sprintf("%s: valid_at = %s, want %s", desc, found.validAt, want.ValidAt))
			}
			if want.InvalidAt != "" && found.invalidAt.String != want.InvalidAt {
				problems = append(problems, fmt.Sprintf("%s: invalid_at = %q, want %s", desc, found.invalidAt.String, want.InvalidAt))
			}
			if want.Ended != found.invalidAt.Valid {
				problems = append(problems, fmt.Sprintf("%s: ended = %v, want %v", desc, found.invalidAt.Valid, want.Ended))
			}
			if want.Ended && !found.expiredAt.Valid {
				problems = append(problems, desc+": ended but expired_at is not set")
			}
		}
	}

	var rec struct {
		Answer string `json:"answer"`
	}
	if err := post(srv.URL+"/v1/gb/recall", map[string]any{"query": c.Query}, &rec); err != nil {
		return "", problems, fmt.Errorf("recall: %w", err)
	}
	return rec.Answer, problems, nil
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
	flag.Parse()

	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY must be set to run evals")
	}
	llm, err := ai.New(key)
	if err != nil {
		return err
	}
	cases, err := loadCases(flag.Args())
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
		answer, problems, err := run(c, l, *verbose)
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
