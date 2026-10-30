package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

// researchManager makes a research manager over d whose LLM always answers with a report.
func researchManager(t *testing.T, d *sql.DB) *agent_research.Manager {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"the report"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	client, err := ai.New("k", ai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	m := agent_research.NewManager(t.Context(), d, agent_research.SQLiteOps, client)
	t.Cleanup(m.Wait)
	return m
}

func TestResearchRoutes(t *testing.T) { dbtest.Run(t, testResearchRoutes) }

func testResearchRoutes(t *testing.T, d *sql.DB) {
	if !strings.HasSuffix(t.Name(), "sqlite") {
		t.Skip("the research manager of this test is the sqlite one")
	}
	database := &db.DB{Relational: d, Graph: d}
	m := researchManager(t, d)
	keys := map[string]string{"key-alice": "alice", "key-bob": "bob"}
	auth := func(_ context.Context, key string) (string, error) {
		if u, ok := keys[key]; ok {
			return u, nil
		}
		return "", errors.New("unknown key")
	}
	mux := http.NewServeMux()
	Register(mux, mgrFor(t, database, nil), database, nil, nil, nil, nil, nil, WithAuth(auth), WithResearch(m))
	do := func(key, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := do("", "POST", "/v1/gb/research", `{"topic":"t"}`); rec.Code != 401 {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := do("key-alice", "POST", "/v1/gb/research", `{"topic":"  "}`); rec.Code != 400 {
		t.Fatalf("no topic: %d %s", rec.Code, rec.Body)
	}
	if rec := do("key-alice", "POST", "/v1/gb/research", `{`); rec.Code != 400 {
		t.Fatalf("bad json: %d", rec.Code)
	}

	// The user is the owner of the key, whatever user_id the caller sends.
	rec := do("key-alice", "POST", "/v1/gb/research", `{"user_id":"mallory","topic":"batteries","max_steps":3}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var started struct {
		ID       int64  `json:"id"`
		UserID   string `json:"user_id"`
		Topic    string `json:"topic"`
		Status   string `json:"status"`
		State    string `json:"state"`
		MaxSteps int    `json:"max_steps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.ID == 0 || started.UserID != "alice" || started.Status != "running" ||
		started.State != "starting" || started.Topic != "batteries" || started.MaxSteps != 3 {
		t.Fatalf("started: %+v (%v) %s", started, err, rec.Body)
	}
	m.Wait()

	type run struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		State  string `json:"state"`
		Report string `json:"report"`
		States []struct {
			Seq   int    `json:"seq"`
			State string `json:"state"`
		} `json:"states"`
	}
	path := "/v1/gb/research/" + strconv.FormatInt(started.ID, 10)
	rec = do("key-alice", "GET", path, "")
	var got run
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Status != "completed" || got.Report != "the report" || got.State != "completed" {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	var names []string
	for _, s := range got.States {
		names = append(names, s.State)
	}
	// The model answers at its first call, which is not the last step (max_steps is 3): a thinking step.
	if strings.Join(names, ",") != "starting,thinking,completed" {
		t.Fatalf("states: %v", names)
	}

	if rec := do("key-bob", "GET", path, ""); rec.Code != 404 {
		t.Fatalf("another user's run: %d", rec.Code)
	}
	if rec := do("key-alice", "GET", "/v1/gb/research/0", ""); rec.Code != 400 {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := do("key-alice", "GET", "/v1/gb/research/999", ""); rec.Code != 404 {
		t.Fatalf("unknown id: %d", rec.Code)
	}

	var list struct {
		Runs []run `json:"runs"`
	}
	rec = do("key-alice", "GET", "/v1/gb/research", "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Runs) != 1 || list.Runs[0].ID != started.ID || list.Runs[0].States != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	for q, want := range map[string]int{"active=true": 0, "active=false": 1, "status=completed": 1, "status=failed": 0, "limit=1": 1} {
		rec = do("key-alice", "GET", "/v1/gb/research?"+q, "")
		list.Runs = nil
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Runs) != want {
			t.Fatalf("list ?%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	if rec = do("key-bob", "GET", "/v1/gb/research", ""); !strings.Contains(rec.Body.String(), `"runs":[]`) {
		t.Fatalf("bob's list: %s", rec.Body)
	}
	for _, q := range []string{"active=maybe", "limit=0", "limit=x", "status=bogus", "active=true&status=failed"} {
		if rec = do("key-alice", "GET", "/v1/gb/research?"+q, ""); rec.Code != 400 {
			t.Fatalf("list ?%s: %d", q, rec.Code)
		}
	}
}

func TestResearchRoutesNeedTheManager(t *testing.T) {
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		database := &db.DB{Relational: d, Graph: d}
		mux := http.NewServeMux()
		Register(mux, mgrFor(t, database, nil), database, nil, nil, nil, nil, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/gb/research?user_id=u", nil))
		if rec.Code != 404 {
			t.Fatalf("status %d", rec.Code)
		}
	})
}

func TestResearchNeedsAUserWithoutAuth(t *testing.T) {
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		if !strings.HasSuffix(t.Name(), "sqlite") {
			t.Skip("sqlite manager")
		}
		database := &db.DB{Relational: d, Graph: d}
		mux := http.NewServeMux()
		Register(mux, mgrFor(t, database, nil), database, nil, nil, nil, nil, nil, WithResearch(researchManager(t, d)))
		for method, path := range map[string]string{"GET": "/v1/gb/research", "POST": "/v1/gb/research"} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{"topic":"t"}`)))
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), "user_id is required") {
				t.Fatalf("%s: %d %s", method, rec.Code, rec.Body)
			}
		}
	})
}

func TestMCPResearchTools(t *testing.T) {
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		if !strings.HasSuffix(t.Name(), "sqlite") {
			t.Skip("sqlite manager")
		}
		database := &db.DB{Relational: d, Graph: d}
		m := researchManager(t, d)
		mux := http.NewServeMux()
		RegisterMCP(mux, mgrFor(t, database, nil), database, nil, nil, nil, nil, nil, "test", WithResearch(m))
		srv := httptest.NewServer(mux)
		defer srv.Close()
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(),
			&sdk.StreamableClientTransport{Endpoint: srv.URL + "/v1/gb/mcp", DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		call := func(name string, args map[string]any) (map[string]any, string) {
			t.Helper()
			res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			txt := res.Content[0].(*sdk.TextContent).Text
			if res.IsError {
				return nil, txt
			}
			out, _ := res.StructuredContent.(map[string]any)
			return out, txt
		}

		out, txt := call("research_start", map[string]any{"user_id": "u1", "topic": "batteries", "max_steps": 2})
		if out == nil || out["status"] != "running" || out["topic"] != "batteries" || out["user_id"] != "u1" {
			t.Fatalf("start: %v %s", out, txt)
		}
		id := out["id"]
		m.Wait()

		out, txt = call("research_get", map[string]any{"user_id": "u1", "id": id})
		if out == nil || out["status"] != "completed" || out["report"] != "the report" || len(out["states"].([]any)) < 3 {
			t.Fatalf("get: %v %s", out, txt)
		}
		if _, txt = call("research_get", map[string]any{"user_id": "u2", "id": id}); !strings.Contains(txt, "not found") {
			t.Fatalf("get of another user's: %s", txt)
		}
		out, _ = call("research_list", map[string]any{"user_id": "u1"})
		if runs, _ := out["runs"].([]any); len(runs) != 1 {
			t.Fatalf("list: %v", out)
		}
		out, _ = call("research_list", map[string]any{"user_id": "u1", "active": true})
		if runs, _ := out["runs"].([]any); len(runs) != 0 {
			t.Fatalf("active list: %v", out)
		}
		if _, txt = call("research_start", map[string]any{"user_id": "u1", "topic": "  "}); !strings.Contains(txt, "topic is required") {
			t.Fatalf("empty topic: %s", txt)
		}
	})
}

func TestMCPHasNoResearchToolsWithoutTheManager(t *testing.T) {
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		database := &db.DB{Relational: d, Graph: d}
		mux := http.NewServeMux()
		RegisterMCP(mux, mgrFor(t, database, nil), database, nil, nil, nil, nil, nil, "test")
		srv := httptest.NewServer(mux)
		defer srv.Close()
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(),
			&sdk.StreamableClientTransport{Endpoint: srv.URL + "/v1/gb/mcp", DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		res, err := cs.ListTools(t.Context(), nil)
		if err != nil || len(res.Tools) != 2 {
			t.Fatalf("tools: %+v %v", res, err)
		}
	})
}
