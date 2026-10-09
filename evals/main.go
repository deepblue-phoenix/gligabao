// Command evals runs end-to-end evals against the real LLM.
//
// Each case in evals/cases/*.json gets a fresh temporary database and an in-process
// HTTP server. The case is run through the public API: remember, then recall.
//
// Usage: go run ./evals [case-name ...]   (requires ANTHROPIC_API_KEY)
package main

import (
	"bytes"
	"embed"
	"encoding/json"
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
	"github.com/ivan/gligabao/internal/server"
)

//go:embed cases/*.json
var casesFS embed.FS

//---------------------------------------------------------------------------------------------

// Case is one eval: remember some text, then ask a question about it.
type Case struct {
	Name           string   `json:"name"`
	Remember       string   `json:"remember"`
	Query          string   `json:"query"`
	ExpectContains []string `json:"expect_contains"`
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

// run executes one case against a fresh database and returns the recall answer.
func run(c Case, llm *ai.Client) (answer string, err error) {
	dir, err := os.MkdirTemp("", "gligabao-eval-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	database, err := db.Open(filepath.Join(dir, "eval.db"))
	if err != nil {
		return "", err
	}
	defer database.Close()

	srv := httptest.NewServer(server.NewHandler(database, llm))
	defer srv.Close()

	var rem struct {
		MemoryID int64 `json:"memory_id"`
	}
	if err := post(srv.URL+"/v1/gb/remember", map[string]any{"text": c.Remember}, &rem); err != nil {
		return "", fmt.Errorf("remember: %w", err)
	}
	var rec struct {
		Answer string `json:"answer"`
	}
	if err := post(srv.URL+"/v1/gb/recall", map[string]any{"query": c.Query}, &rec); err != nil {
		return "", fmt.Errorf("recall: %w", err)
	}
	return rec.Answer, nil
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
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY must be set to run evals")
	}
	llm, err := ai.New(key)
	if err != nil {
		return err
	}
	cases, err := loadCases(os.Args[1:])
	if err != nil {
		return err
	}

	failed := 0
	for _, c := range cases {
		answer, err := run(c, llm)
		if err != nil {
			fmt.Printf("FAIL %s: %v\n", c.Name, err)
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
