package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/evals/experiments"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// webLLM extracts Alice, Acme and Globex from the text; the briefing of a web search ("BRIEFING about X") yields X and a
// new entity, Initech.
type webLLM struct{}

var briefingAbout = regexp.MustCompile(`BRIEFING about (\w+)`)

func (webLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	out := `{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"},{"name":"Globex","type":"organization"}],
		"edges":[{"source":"Alice","target":"Acme","relation":"works_at"},{"source":"Acme","target":"Globex","relation":"acquired"}]}`
	switch msg := req.Messages[0].Content; {
	case strings.HasPrefix(req.System, "You compare NEW facts"):
		out = `{"duplicates":[],"contradictions":[]}`
	case briefingAbout.MatchString(msg):
		x := briefingAbout.FindStringSubmatch(msg)[1]
		out = `{"entities":[{"name":"Initech","type":"organization"},{"name":"` + x + `","type":"organization"}],
			"edges":[{"source":"Initech","target":"` + x + `","relation":"partnered_with"}]}`
	}
	return &ai.Response{Text: out, StopReason: "end_turn", Model: "fake-model"}, nil
}

// fakeSearcher finds a briefing for every entity but Alice, for whom nothing is found.
type fakeSearcher struct{}

func (fakeSearcher) WebSearch(_ context.Context, req ai.WebSearchRequest) (*ai.WebSearchResponse, error) {
	name := regexp.MustCompile(`name: (\w+)`).FindStringSubmatch(req.Prompt)[1]
	text := "BRIEFING about " + name + " in 2026."
	if name == "Alice" {
		text = "NO_RESULTS"
	}
	return &ai.WebSearchResponse{Text: text, Queries: []string{name + " news", name + " deal"}, Provider: ai.ProviderAnthropic, Model: "fake-search-model",
		Sources:  []ai.WebSource{{URL: "https://news.example/" + name, Title: "News of " + name, PageAge: "May 1, 2026"}},
		Searches: 2, InputTokens: 10, OutputTokens: 5, CostUSD: 0.02}, nil
}

// TestWebSearches checks the web search endpoints of the admin UI: what a remember call with web_search left in the
// database (stats, per dataset) and what its trajectory recorded (per result, and the totals).
func TestWebSearches(t *testing.T) { dbtest.Run(t, testWebSearches) }

func testWebSearches(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dir := t.TempDir()
	res, err := memory_remember.Remember(ctx, d, webLLM{}, "u1", "main", "knowledge", "Alice works at Acme, which acquired Globex.", time.Time{},
		memory_core.WithEval(), memory_core.WithTrajectoryDir(dir), memory_core.WithWebSearcher(fakeSearcher{}), memory_core.WithWebSearch(true))
	if err != nil || len(res.WebSearches) != 3 || res.Trajectory == nil {
		t.Fatalf("remember: %v %+v", err, res)
	}
	// A call without web search leaves no web search behind.
	plain, err := memory_remember.Remember(ctx, d, webLLM{}, "u2", "main", "knowledge", "Alice works at Acme.", time.Time{},
		memory_core.WithEval(), memory_core.WithTrajectoryDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	dsID, err := relational.DatasetID(ctx, d, "u1", "main")
	if err != nil {
		t.Fatal(err)
	}

	get := func(h http.HandlerFunc, url string, out any) int {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /x/{id}", h)
		mux.HandleFunc("GET /x", h)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		if out != nil && w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatalf("%s: %v", w.Body, err)
			}
		}
		return w.Code
	}

	// The case that holds both calls.
	tr, err := experiments.Start(ctx, d, experiments.Options{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Record(ctx, experiments.CaseOutcome{Eval: "s", Name: "a", DatasetID: dsID, Trajectories: append([]*atif.Trajectory{res.Trajectory}, plain.Trajectory), TrajectoryDir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Finish(ctx); err != nil {
		t.Fatal(err)
	}

	// Per result (case), from the trajectory.
	var perResult struct {
		CaseRuns []struct {
			Totals   admin.WebSearchTotals `json:"totals"`
			Searches []admin.WebSearch     `json:"searches"`
		} `json:"case_runs"`
	}
	if c := get(admin.EvalRunWebSearches(d, dir), "/x/"+strconv.FormatInt(tr.Run.ID, 10), &perResult); c != 200 || len(perResult.CaseRuns) != 1 {
		t.Fatalf("per result: %d %+v", c, perResult)
	}
	e := perResult.CaseRuns[0]
	if e.Totals.EntitySearches != 3 || e.Totals.Found != 2 || e.Totals.Empty != 1 || e.Totals.EngineSearches != 6 || e.Totals.Latency.Count != 3 || e.Totals.Sources != 3 {
		t.Errorf("totals = %+v", e.Totals)
	}
	var acme *admin.WebSearch
	for i, s := range e.Searches {
		if s.Entity == "Acme" {
			acme = &e.Searches[i]
		}
		if s.Entity == "Alice" && (s.Found || s.ArtifactID != 0 || s.Briefing != "") {
			t.Errorf("Alice found something: %+v", s)
		}
	}
	if acme == nil || !acme.Found || acme.ArtifactID == 0 || acme.ParentArtifactID == 0 || acme.LatencyMS == nil || acme.Model != "fake-search-model" ||
		acme.Query != "latest world events and news about Acme (organization)" || len(acme.Searches) != 2 || len(acme.Sources) != 1 ||
		!strings.Contains(acme.Briefing, "BRIEFING about Acme") || len(acme.Entities) != 2 || len(acme.Edges) != 1 || acme.CostUSD != 0.02 {
		t.Fatalf("Acme = %+v", acme)
	}

	// The totals of the eval database: the same, from all the recorded trajectories.
	named := []admin.Named{{Name: "eval", DB: d, TrajectoryDir: dir}}
	var stats struct {
		Database struct {
			Datasets int `json:"datasets"`
			admin.DatasetWebSummary
		} `json:"database"`
		Datasets []struct {
			ID int64 `json:"id"`
		} `json:"datasets"`
		Trajectories *struct {
			Calls int `json:"calls"`
			admin.WebSearchTotals
		} `json:"trajectories"`
	}
	if c := get(admin.WebSearchStatsOf(named), "/x?db=eval", &stats); c != 200 {
		t.Fatalf("stats: %d", c)
	}
	db := stats.Database
	// Two results (Acme, Globex) in one dataset; Initech and the two searched entities are linked to them, and Initech is
	// the only entity that no text of the user mentions.
	if db.Datasets != 1 || db.Artifacts != 2 || db.Parents != 1 || db.Sources != 2 || db.Entities != 3 || db.OnlyWeb != 1 || db.Edges != 2 ||
		len(stats.Datasets) != 1 || stats.Datasets[0].ID != dsID {
		t.Errorf("database stats = %+v / %+v", db, stats.Datasets)
	}
	if stats.Trajectories == nil || stats.Trajectories.Calls != 1 || stats.Trajectories.EntitySearches != 3 || stats.Trajectories.EngineSearches != 6 {
		t.Errorf("trajectory stats = %+v", stats.Trajectories)
	}
	if c := get(admin.WebSearchStatsOf(named), "/x?db=nope", nil); c != http.StatusNotFound {
		t.Errorf("unknown db: %d", c)
	}
	// A database without trajectories reports none.
	stats.Trajectories = nil
	if c := get(admin.WebSearchStatsOf([]admin.Named{{Name: "standard", DB: d}}), "/x", &stats); c != 200 || stats.Trajectories != nil {
		t.Errorf("standard: %d %+v", c, stats.Trajectories)
	}

	// Per dataset, from the database.
	var ds struct {
		Summary  admin.DatasetWebSummary `json:"summary"`
		Searches []admin.WebSearch       `json:"searches"`
	}
	if c := get(admin.WebSearchDatasetOf(named), "/x?dataset_id="+strconv.FormatInt(dsID, 10), &ds); c != 200 || len(ds.Searches) != 2 {
		t.Fatalf("dataset: %d %+v", c, ds)
	}
	s := ds.Searches[0]
	if s.Entity == "" || s.EntityType != "organization" || s.ParentArtifactID == 0 || !strings.Contains(s.ParentText, "Alice works at Acme") || !strings.Contains(s.Briefing, "BRIEFING about "+s.Entity) ||
		len(s.Sources) != 1 || s.Sources[0].URL != "https://news.example/"+s.Entity || s.Sources[0].Title != "News of "+s.Entity || s.Sources[0].PageAge != "May 1, 2026" ||
		len(s.Entities) != 2 || len(s.Edges) != 1 || !s.Edges[0].Created || s.LatencyMS != nil {
		t.Errorf("dataset search = %+v", s)
	}
	if c := get(admin.WebSearchDatasetOf(named), "/x?dataset_id=999", nil); c != http.StatusNotFound {
		t.Errorf("unknown dataset: %d", c)
	}
	if c := get(admin.WebSearchDatasetOf(named), "/x?dataset_id=abc", nil); c != http.StatusBadRequest {
		t.Errorf("bad dataset id: %d", c)
	}
}
