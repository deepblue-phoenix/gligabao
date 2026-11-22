package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/query"
	"github.com/deepblue-phoenix/gligabao/internal/api/remember"
	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

func TestEvalCalls(t *testing.T) { dbtest.Run(t, testEvalCalls) }

// The evals call remember.Remember and query.Run themselves, with the options of the API (RememberOptions, QueryOptions) and
// memory_core.WithEval: the call has an ATIF trajectory, which the routes of Register never have.
func testEvalCalls(t *testing.T,
	d *sql.DB) {
	llm := &costLLM{
		outs: []string{
			`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`,
			`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`,
			`{"answer":"Alice.","entities":[1],"edges":[]}`,
			`{"answer":"Alice.","entities":[1],"edges":[]}`,
		},
	}
	database := withVectors(t, d)
	mgr := mgrFor(t,
		database,
		llm)
	mux := http.NewServeMux()
	Register(mux,
		mgr,
		database,
		llm,
		fakeEmbedder{},
		nil,
		nil,
		nil)
	eval := []memory_core.Option{memory_core.WithEval()}
	rememberOpts := append(RememberOptions(database,
		fakeEmbedder{},
		nil,
		nil,
		nil), eval...)
	queryOpts := append(QueryOptions(database,
		fakeEmbedder{}), eval...)

	type resp struct {
		TrajectoryID string           `json:"trajectory_id"`
		Trajectory   *atif.Trajectory `json:"trajectory"`
	}
	plain := func(path,
		body string) resp {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, newReq("POST",
			path,
			strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s",
				rec.Code,
				rec.Body)
		}
		var r resp
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}

	// The routes of Register generate no trajectory; a typed call with WithEval does.
	noTrajectory := func(path,
		body string) {
		t.Helper()
		if r := plain(path, body); r.TrajectoryID != "" || r.Trajectory != nil {
			t.Fatalf("%s: %+v",
				path,
				r)
		}
	}
	noTrajectory("/v1/gb/remember", `{"user_id":"u1","text":"Alice","classify_graph":false}`)
	rem, err := remember.Remember(t.Context(),
		mgr,
		database.Relational,
		"u1",
		remember.Request{Text: "Alice"},
		rememberOpts)
	if err != nil {
		t.Fatal(err)
	}
	if rem.Trajectory == nil || rem.TrajectoryID != rem.Trajectory.TrajectoryID || len(rem.Trajectory.Steps) == 0 {
		t.Fatalf("remember: %+v", rem)
	}
	noTrajectory("/v1/gb/query", `{"user_id":"u1","query":"Alice?"}`)
	ans, err := query.Run(t.Context(),
		database.Relational,
		llm,
		"u1",
		query.Request{Query: "Alice?"},
		queryOpts)
	if err != nil {
		t.Fatal(err)
	}
	if ans.Trajectory == nil || ans.TrajectoryID != ans.Trajectory.TrajectoryID || len(ans.Trajectory.Steps) == 0 {
		t.Fatalf("query: %+v", ans)
	}
}
