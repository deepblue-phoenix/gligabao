package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestEvalEndpoints(t *testing.T) { dbtest.Run(t, testEvalEndpoints) }

func testEvalEndpoints(t *testing.T, d *sql.DB) {
	llm := &costLLM{outs: []string{
		`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`,
		`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`,
		`{"answer":"Alice.","entities":[1],"edges":[]}`,
		`{"answer":"Alice.","entities":[1],"edges":[]}`,
	}}
	database := &db.DB{Relational: d, Graph: d}
	plain, eval := http.NewServeMux(), http.NewServeMux()
	Register(plain, mgrFor(t, database, llm), database, llm, nil, nil, nil, nil)
	Register(eval, mgrFor(t, database, llm), database, llm, nil, nil, nil, nil)
	RegisterEval(eval, mgrFor(t, database, llm), database, llm, nil, nil, nil, nil)

	do := func(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	type resp struct {
		TrajectoryID string           `json:"trajectory_id"`
		Trajectory   *atif.Trajectory `json:"trajectory"`
	}
	decode := func(rec *httptest.ResponseRecorder) resp {
		t.Helper()
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var r resp
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}

	// Not registered by default.
	for _, p := range []string{"/v1/gb/eval/remember", "/v1/gb/eval/recall"} {
		if rec := do(plain, p, `{}`); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s without eval mode: %d", p, rec.Code)
		}
	}

	for _, c := range []struct{ plain, eval, body string }{
		{"/v1/gb/remember", "/v1/gb/eval/remember", `{"user_id":"u1","text":"Alice"}`},
		{"/v1/gb/recall", "/v1/gb/eval/recall", `{"user_id":"u1","query":"Alice?"}`},
	} {
		// Only the eval endpoint generates a trajectory.
		if r := decode(do(eval, c.plain, c.body)); r.TrajectoryID != "" || r.Trajectory != nil {
			t.Fatalf("%s: %+v", c.plain, r)
		}
		r := decode(do(eval, c.eval, c.body))
		if r.Trajectory == nil || r.TrajectoryID != r.Trajectory.TrajectoryID || len(r.Trajectory.Steps) == 0 {
			t.Fatalf("%s: %+v", c.eval, r)
		}
	}
}
