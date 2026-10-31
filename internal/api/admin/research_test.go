package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

// TestResearchAdmin checks the research endpoints of the admin UI: the counts and lists, one agent with its sub-agents, the refusal to stop
// an agent that is not working, and the provenance (what the sessions of an agent and of its sub-agents stored).
func TestResearchAdmin(t *testing.T) { dbtest.Run(t, testResearchAdmin) }

func testResearchAdmin(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	ops := agent_research.SQLiteOps
	if strings.HasSuffix(t.Name(), "postgres") {
		ops = agent_research.PostgresOps
	}
	m := agent_research.NewManager(ctx, d, ops, nil)
	st := ops.Store
	mk := func(r *core.Run) *core.Run {
		t.Helper()
		if err := st.CreateRun(ctx, d, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	started := time.Now().UTC().Add(-26 * time.Hour)
	root := mk(&core.Run{UserID: "u1", Topic: "root topic", State: core.StateStarting, CreatedAt: started})
	kid := mk(&core.Run{UserID: "u1", Topic: "kid topic", ParentID: &root.ID, Name: "kid", Reason: "why", CreatedAt: started})
	failed := mk(&core.Run{UserID: "u2", Topic: "failing", CreatedAt: started})
	if _, err := st.FinishRun(ctx, d, failed.ID, core.StatusFailed, "", "the model said no"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishRun(ctx, d, kid.ID, core.StatusCompleted, "kid report", ""); err != nil {
		t.Fatal(err)
	}

	do := func(h http.HandlerFunc, method, url string, out any) int {
		t.Helper()
		req := httptest.NewRequest(method, url, nil)
		mux := http.NewServeMux()
		mux.HandleFunc("GET /x/{id}", h)
		mux.HandleFunc("GET /x/{id}/provenance", h)
		mux.HandleFunc("POST /x/{id}/stop", h)
		mux.HandleFunc("GET /x", h)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if out != nil && rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code
	}

	var list struct {
		Counts admin.ResearchCounts  `json:"counts"`
		Runs   []admin.ResearchAgent `json:"runs"`
		Total  int                   `json:"total"`
	}
	if c := do(admin.ResearchListOf(m), "GET", "/x", &list); c != 200 {
		t.Fatalf("list: %d", c)
	}
	if list.Counts != (admin.ResearchCounts{Running: 1, Completed: 1, Failed: 1, Total: 3}) || len(list.Runs) != 3 || list.Total != 3 {
		t.Fatalf("list: %+v", list)
	}
	byID := map[int64]admin.ResearchAgent{}
	for _, r := range list.Runs {
		byID[r.ID] = r
	}
	if r := byID[root.ID]; r.Children != 1 || r.AgeMS < 25*3600*1000 || r.DurationMS < 25*3600*1000 || r.FinishedAt != nil {
		t.Errorf("root: %+v", r)
	}
	if r := byID[kid.ID]; r.Children != 0 || !r.HasReport || r.Report != "" || r.DurationMS > r.AgeMS {
		t.Errorf("kid: %+v", r)
	}
	if r := byID[failed.ID]; r.Status != core.StatusFailed || r.Error != "the model said no" {
		t.Errorf("failed: %+v", r)
	}
	if c := do(admin.ResearchListOf(m), "GET", "/x?status=running", &list); c != 200 || len(list.Runs) != 1 || list.Total != 1 || list.Runs[0].ID != root.ID {
		t.Errorf("running: %d %+v", c, list)
	}
	if c := do(admin.ResearchListOf(m), "GET", "/x?status=finished", &list); c != 200 || len(list.Runs) != 2 || list.Total != 2 {
		t.Errorf("finished: %d %+v", c, list)
	}
	if c := do(admin.ResearchListOf(m), "GET", "/x?status=finished&roots_only=true", &list); c != 200 || len(list.Runs) != 1 || list.Runs[0].ID != failed.ID {
		t.Errorf("finished roots: %d %+v", c, list)
	}
	if c := do(admin.ResearchListOf(m), "GET", "/x?status=bogus", nil); c != 400 {
		t.Errorf("bogus status: %d", c)
	}
	if c := do(admin.ResearchListOf(m), "GET", "/x?limit=0", nil); c != 400 {
		t.Errorf("bad limit: %d", c)
	}

	var one struct {
		admin.ResearchAgent
		Parent    *admin.ResearchAgent  `json:"parent"`
		Subagents []admin.ResearchAgent `json:"subagents"`
	}
	if c := do(admin.ResearchGetOf(m), "GET", "/x/"+itoa(root.ID), &one); c != 200 || one.ID != root.ID || one.Parent != nil || len(one.Subagents) != 1 || one.Subagents[0].Name != "kid" {
		t.Fatalf("get root: %d %+v", c, one)
	}
	if c := do(admin.ResearchGetOf(m), "GET", "/x/"+itoa(kid.ID), &one); c != 200 || one.Parent == nil || one.Parent.ID != root.ID || one.Report != "kid report" || len(one.States) != 0 && one.States[0].RunID != kid.ID {
		t.Fatalf("get kid: %d %+v", c, one)
	}
	if c := do(admin.ResearchGetOf(m), "GET", "/x/999", nil); c != 404 {
		t.Errorf("get unknown: %d", c)
	}

	// Stopping: an agent that is not working (this manager runs none) cannot be stopped.
	if c := do(admin.ResearchStopOf(m), "POST", "/x/"+itoa(failed.ID)+"/stop", nil); c != 409 {
		t.Errorf("stop finished: %d", c)
	}
	if c := do(admin.ResearchStopOf(m), "POST", "/x/999/stop", nil); c != 404 {
		t.Errorf("stop unknown: %d", c)
	}

	// Provenance: root remembered in "main" of u1; its sub-agent in "side", and also mentioned an entity of root again.
	main, _ := relational.EnsureDataset(ctx, d, "u1", "main")
	side, _ := relational.EnsureDataset(ctx, d, "u1", "side")
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := relational.Now()
	rs, ks := agent_research.SessionID(root.ID), agent_research.SessionID(kid.ID)
	a1, err := tx.InsertArtifact(ctx, main, graph.ArtifactKnowledge, strings.Repeat("long text ", 200), rs, now, now)
	check(err)
	a2, err := tx.InsertArtifact(ctx, side, graph.ArtifactKnowledge, "kid found this", ks, now, now)
	check(err)
	_, err = tx.InsertArtifact(ctx, main, graph.ArtifactKnowledge, "by somebody else", "research-999", now, now)
	check(err)
	alice, err := tx.UpsertEntity(ctx, main, graph.Entity{Name: "Alice", Type: "person"}, "alice")
	check(err)
	acme, err := tx.UpsertEntity(ctx, main, graph.Entity{Name: "Acme", Type: "org"}, "acme")
	check(err)
	bob, err := tx.UpsertEntity(ctx, side, graph.Entity{Name: "Bob", Type: "person"}, "bob")
	check(err)
	e1, err := tx.InsertEdge(ctx, graph.NewEdge{DatasetID: main, SourceID: alice, TargetID: acme, Relation: "works_at", ValidAt: now, CreatedAt: now, ArtifactID: a1})
	check(err)
	e2, err := tx.InsertEdge(ctx, graph.NewEdge{DatasetID: side, SourceID: bob, TargetID: bob, Relation: "knows", ValidAt: now, CreatedAt: now, ArtifactID: a2})
	check(err)
	check(tx.LinkEntitySession(ctx, alice, rs))
	check(tx.LinkEntitySession(ctx, acme, rs))
	check(tx.LinkEntitySession(ctx, alice, ks))
	check(tx.LinkEntitySession(ctx, bob, ks))
	check(tx.LinkEdgeSession(ctx, e1, rs))
	check(tx.LinkEdgeSession(ctx, e1, ks)) // the kid restated the fact of its parent
	check(tx.LinkEdgeSession(ctx, e2, ks))
	check(tx.Commit())

	type prov struct {
		Agents    []admin.ProvenanceAgent    `json:"agents"`
		Datasets  []admin.ProvenanceDataset  `json:"datasets"`
		Artifacts []admin.ProvenanceArtifact `json:"artifacts"`
		Entities  []admin.ProvenanceEntity   `json:"entities"`
		Edges     []admin.ProvenanceEdge     `json:"edges"`
	}
	var p prov
	if c := do(admin.ResearchProvenanceOf(m), "GET", "/x/"+itoa(root.ID)+"/provenance", &p); c != 200 {
		t.Fatalf("provenance: %d", c)
	}
	if len(p.Agents) != 1 || len(p.Artifacts) != 1 || len(p.Entities) != 2 || len(p.Edges) != 1 || len(p.Datasets) != 1 || p.Datasets[0].Name != "main" ||
		p.Datasets[0].Artifacts != 1 || p.Datasets[0].Entities != 2 || p.Datasets[0].Edges != 1 {
		t.Fatalf("root only: %+v", p)
	}
	if a := p.Artifacts[0]; a.ID != a1 || a.RunID != root.ID || len([]rune(a.Text)) > 700 || !strings.HasSuffix(a.Text, "…") {
		t.Errorf("artifact: %+v", a)
	}
	if e := p.Edges[0]; !e.Created || e.Source != "Alice" || e.Target != "Acme" || len(e.RunIDs) != 1 || e.RunIDs[0] != root.ID {
		t.Errorf("edge: %+v", e)
	}

	p = prov{}
	if c := do(admin.ResearchProvenanceOf(m), "GET", "/x/"+itoa(root.ID)+"/provenance?include_subagents=true", &p); c != 200 {
		t.Fatalf("provenance with sub-agents: %d", c)
	}
	if len(p.Agents) != 2 || len(p.Artifacts) != 2 || len(p.Entities) != 3 || len(p.Edges) != 2 || len(p.Datasets) != 2 {
		t.Fatalf("with sub-agents: %+v", p)
	}
	for _, e := range p.Entities {
		if e.Name == "Alice" && len(e.RunIDs) != 2 {
			t.Errorf("alice was discovered by both: %+v", e)
		}
	}
	if c := do(admin.ResearchProvenanceOf(m), "GET", "/x/"+itoa(kid.ID)+"/provenance", &p); c != 200 || len(p.Artifacts) != 1 || p.Artifacts[0].DatasetID != side {
		t.Errorf("kid provenance: %d %+v", c, p)
	}
	if c := do(admin.ResearchProvenanceOf(m), "GET", "/x/999/provenance", nil); c != 404 {
		t.Errorf("provenance of unknown: %d", c)
	}
	if c := do(admin.ResearchProvenanceOf(m), "GET", "/x/"+itoa(failed.ID)+"/provenance", &p); c != 200 || len(p.Artifacts) != 0 || len(p.Datasets) != 0 {
		t.Errorf("agent without provenance: %d %+v", c, p)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestResearchStart checks the start endpoint of the admin UI: it refuses a bad body, and without an LLM (no Anthropic key) it answers 503.
func TestResearchStart(t *testing.T) {
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		ops := agent_research.SQLiteOps
		if strings.HasSuffix(t.Name(), "postgres") {
			ops = agent_research.PostgresOps
		}
		h := admin.ResearchStartOf(agent_research.NewManager(context.Background(), d, ops, nil))
		for body, want := range map[string]int{
			`{"topic": "x"`:                          http.StatusBadRequest,
			`{"topic": "x", "user_id": "a\u0001"}`:   http.StatusBadRequest,
			`{"topic": "what is the tallest tree?"}`: http.StatusServiceUnavailable,
		} {
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)))
			if rec.Code != want {
				t.Errorf("%s: %d, want %d: %s", body, rec.Code, want, rec.Body)
			}
		}
	})
}
