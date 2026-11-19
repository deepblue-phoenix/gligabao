package remember

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil/httputiltest"
)

func TestRememberRunsOfUser(t *testing.T) { dbtest.Run(t, testRememberRunsOfUser) }

func testRememberRunsOfUser(t *testing.T,
	d *sql.DB) {
	llm := fakeLLM{`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t,
		d,
		llm))
	mux.HandleFunc("GET /v1/gb/remember/runs", RunsHandler(d))
	mux.HandleFunc("GET /v1/gb/remember/runs/{id}", RunHandler(d))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httputiltest.NewRequest("POST",
		"/v1/gb/remember",
		strings.NewReader(`{"user_id":"u1","text":"Alice."}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s",
			rec.Code,
			rec.Body)
	}

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httputiltest.NewRequest("GET",
		"/v1/gb/remember/runs?user_id=u1",
		nil))
	var out struct {
		Runs  []struct{ ID int64 } `json:"runs"`
		Total int                  `json:"total"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &out); err != nil || list.Code != 200 || out.Total != 1 || len(out.Runs) != 1 {
		t.Fatalf("list: %d %s",
			list.Code,
			list.Body)
	}

	other := httptest.NewRecorder()
	mux.ServeHTTP(other, httputiltest.NewRequest("GET",
		"/v1/gb/remember/runs?user_id=u2",
		nil))
	if err := json.Unmarshal(other.Body.Bytes(), &out); err != nil || out.Total != 0 {
		t.Errorf("another user sees runs: %s", other.Body)
	}

	one := httptest.NewRecorder()
	mux.ServeHTTP(one, httputiltest.NewRequest("GET",
		"/v1/gb/remember/runs/1?user_id=u2",
		nil))
	if one.Code != 404 {
		t.Errorf("run of another user: %d", one.Code)
	}
	mine := httptest.NewRecorder()
	mux.ServeHTTP(mine, httputiltest.NewRequest("GET",
		"/v1/gb/remember/runs/1?user_id=u1",
		nil))
	var run struct {
		Params map[string]any `json:"params"`
	}
	_ = json.Unmarshal(mine.Body.Bytes(), &run)
	if _, has := run.Params["user_id"]; has {
		t.Errorf("params have the user_id: %v", run.Params)
	}
	// every flag is recorded, the ones that were not set too
	for _, k := range []string{"web_search", "classify_graph", "graph_classes", "ontology_grounding", "ontology_grounding_mode", "ontologies",
		"geo", "geo_sync", "graph_index", "timeseries", "timeseries_fetch", "timeseries_observations", "chunked", "chunk_size",
		"chunk_overlap_percent", "models", "reasoning"} {
		if _, has := run.Params[k]; !has {
			t.Errorf("params lack the flag %q: %v", k, run.Params)
		}
	}
	if run.Params["geo"] != false || run.Params["ontology_grounding"] != false {
		t.Errorf("unset flags: %v", run.Params)
	}
	if mine.Code != 200 || !strings.Contains(mine.Body.String(), `"cost_usd"`) {
		t.Errorf("own run: %d %s",
			mine.Code,
			mine.Body)
	}
}
