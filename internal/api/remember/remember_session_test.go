package remember

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

func TestRememberSession(t *testing.T) { dbtest.Run(t, testRememberSession) }

func testRememberSession(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	llm := fakeLLM{`{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"}],
	"edges":[{"source":"Alice","target":"Acme","relation":"works_at"}]}`}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, llm))
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		return rec
	}

	// Session A creates the entities and the edge; session B repeats them, so nothing new is written for B except
	// its artifact; a call without a session adds no session link at all.
	var sessions []string
	for _, body := range []string{
		`{"user_id":"u1","text":"Alice works at Acme.","session_id":"sess-a"}`,
		`{"user_id":"u1","text":"Alice works at Acme.","session_id":" sess-b "}`, // trimmed
		`{"user_id":"u1","text":"Alice works at Acme."}`,
	} {
		rec := post(body)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", body, rec.Code, rec.Body)
		}
		var res memory_remember.Result
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, res.SessionID)
	}
	if sessions[0] != "sess-a" || sessions[1] != "sess-b" || sessions[2] != "" {
		t.Errorf("result session IDs: %q", sessions)
	}

	dsID, err := relational.DatasetID(ctx, d, "u1", relational.DefaultDataset)
	if err != nil {
		t.Fatal(err)
	}
	c, err := graph.DatasetCounts(ctx, d, dsID)
	if err != nil || c != (graph.Counts{Artifacts: 3, Entities: 2, Edges: 1}) {
		t.Fatalf("counts: %+v, %v", c, err)
	}
	arts, err := graph.ListArtifacts(ctx, d, dsID)
	if err != nil || len(arts) != 3 || arts[0].SessionID != "sess-a" || arts[1].SessionID != "sess-b" || arts[2].SessionID != "" {
		t.Fatalf("artifacts: %+v, %v", arts, err)
	}

	// Both sessions are recorded against the entities and the edge, although session B created none of them.
	ents, err := graph.ListEntities(ctx, d, dsID)
	if err != nil || len(ents) != 2 {
		t.Fatalf("entities: %+v, %v", ents, err)
	}
	var wantES []graph.EntitySession
	for _, e := range ents {
		wantES = append(wantES, graph.EntitySession{EntityID: e.ID, SessionID: "sess-a"}, graph.EntitySession{EntityID: e.ID, SessionID: "sess-b"})
	}
	es, err := graph.ListEntitySessions(ctx, d, dsID)
	if err != nil || len(es) != len(wantES) {
		t.Fatalf("entity sessions: %+v, %v", es, err)
	}
	for i := range wantES {
		if es[i] != wantES[i] {
			t.Errorf("entity session[%d] = %+v, want %+v", i, es[i], wantES[i])
		}
	}
	edges, err := graph.ListGraphEdges(ctx, d, dsID)
	if err != nil || len(edges) != 1 {
		t.Fatalf("edges: %+v, %v", edges, err)
	}
	gs, err := graph.ListEdgeSessions(ctx, d, dsID)
	if err != nil || len(gs) != 2 || gs[0] != (graph.EdgeSession{EdgeID: edges[0].ID, SessionID: "sess-a"}) || gs[1] != (graph.EdgeSession{EdgeID: edges[0].ID, SessionID: "sess-b"}) {
		t.Fatalf("edge sessions: %+v, %v", gs, err)
	}

	// An invalid session ID is refused before anything is stored.
	for _, body := range []string{
		`{"user_id":"u1","text":"x","session_id":"` + strings.Repeat("s", 129) + `"}`,
		`{"user_id":"u1","text":"x","session_id":"a\nb"}`,
	} {
		if rec := post(body); rec.Code != 400 {
			t.Errorf("invalid session status = %d, want 400: %s", rec.Code, rec.Body)
		}
	}
	if c, err := graph.DatasetCounts(ctx, d, dsID); err != nil || c.Artifacts != 3 {
		t.Errorf("an invalid session stored an artifact: %+v, %v", c, err)
	}
}
