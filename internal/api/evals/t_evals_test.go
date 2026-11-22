package evals_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/evals"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runmanager"
	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

// fakeLLM answers by what the prompt asks for (the query prompt gets the answer, anything else an extraction); it also serves the
// commentaries.
type fakeLLM struct {
	mu   sync.Mutex
	cost float64
}

func (l *fakeLLM) Complete(_ context.Context,
	req llm.Request) (*llm.Response, error) {
	out := `{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`
	if len(req.Messages) > 0 && strings.Contains(req.Messages[len(req.Messages)-1].Content, "<graph>") {
		out = `{"answer":"Alice.","entities":[1],"edges":[]}`
	}
	return &llm.Response{
		Text:         out,
		StopReason:   "end_turn",
		Provider:     llm.ProviderAnthropic,
		Model:        "m",
		InputTokens:  10,
		OutputTokens: 5,
		CostUSD:      l.cost,
	}, nil
}

type fakeEmbedder struct{ dims int }

func (f fakeEmbedder) EmbedBatchUsage(_ context.Context,
	texts []string) ([][]float32, *llm.EmbedUsage, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, f.dims)
		out[i][0] = 1
	}
	return out, &llm.EmbedUsage{
		Provider: llm.ProviderOpenAI,
		Model:    "test-embed",
	}, nil
}

// api is the routes of the evals over a real run manager (with a fake LLM) on a fresh database of the backend.
type api struct {
	t   *testing.T
	d   *sql.DB
	m   *runmanager.Manager
	svc *evals.Service
	mux *http.ServeMux
}

func newAPI(t *testing.T,
	d *sql.DB,
	withLLM bool) *api {
	t.Helper()
	backend := config.DBBackendSQLite
	if strings.Contains(t.Name(), "postgres") {
		backend = config.DBBackendPostgres
	}
	vs, err := vector.NewChromem(":memory:", 2)
	if err != nil {
		t.Fatal(err)
	}
	database := &db.DB{
		Relational: d,
		Graph:      d,
		Vector:     vs,
		Backend:    backend,
	}
	ctx, stop := context.WithCancel(context.Background())
	cfg := runmanager.Config{
		DB:            database,
		Files:         userfiles.FS{Dir: t.TempDir()},
		Prices:        llm.NewPriceTable(nil),
		TrajectoryDir: t.TempDir(),
	}
	var lm *fakeLLM
	if withLLM {
		lm = &fakeLLM{cost: 0.01}
		cfg.LLM = lm
		cfg.Embedder = fakeEmbedder{dims: 2}
	}
	m, err := runmanager.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop()
		m.Wait()
	})
	a := &api{
		t: t,
		d: d,
		m: m,
		svc: &evals.Service{
			DB:            d,
			Store:         m.Store(),
			Mgr:           m,
			TrajectoryDir: cfg.TrajectoryDir,
		},
		mux: http.NewServeMux(),
	}
	if lm != nil {
		a.svc.LLM = lm
	}
	evals.Register(a.mux,
		func(h http.HandlerFunc) http.Handler { return httputil.Authenticate(nil, h) },
		a.svc)
	return a
}

// do sends a request as the user ("" sends no user) and returns the status and the body.
func (a *api) do(method,
	path,
	user string,
	body any) (int, []byte) {
	a.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	case []byte:
		rd = bytes.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			a.t.Fatal(err)
		}
		rd = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method,
		"/v1/gb/evals"+path,
		rd)
	if user != "" {
		req.Header.Set(httputil.UserIDHeader, user)
	}
	if strings.Contains(path, "/files/") {
		req.Header.Set("Content-Type", "text/plain")
	}
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

// expect sends a request and fails unless the status is want; it decodes the body into out (if not nil).
func (a *api) expect(want int,
	method,
	path,
	user string,
	body any,
	out any) {
	a.t.Helper()
	code, data := a.do(method,
		path,
		user,
		body)
	if code != want {
		a.t.Fatalf("%s %s as %q: status %d, want %d: %s",
			method,
			path,
			user,
			code,
			want,
			data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			a.t.Fatalf("%s %s: %v: %s",
				method,
				path,
				err,
				data)
		}
	}
}

// eval is the JSON of an eval of the cases (name and the text the query is expected to contain).
func eval(name string,
	expected ...string) string {
	cases := make([]string, len(expected))
	for i, e := range expected {
		cases[i] = `{"name":"c` + strconv.Itoa(i+1) + `","remember":[{"text":"Alice is here."}],"query":"Who?","expect_contains":["` + e + `"]}`
	}
	return `{"name":"` + name + `","cases":[` + strings.Join(cases, ",") + `]}`
}

// run starts a run of the eval of the user and waits for it to end.
func (a *api) run(user,
	name string) int64 {
	a.t.Helper()
	var started struct {
		RunID  int64  `json:"run_id"`
		Status string `json:"status"`
	}
	a.expect(http.StatusAccepted,
		"POST",
		"/"+name+"/runs",
		user,
		"",
		&started)
	if started.Status != "pending" || started.RunID == 0 {
		a.t.Fatalf("started: %+v", started)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		var run evals.RunView
		a.expect(http.StatusOK,
			"GET",
			"/runs/"+strconv.FormatInt(started.RunID, 10),
			user,
			nil,
			&run)
		if run.Status.Finished() {
			return started.RunID
		}
	}
	a.t.Fatalf("run %d did not end", started.RunID)
	return 0
}

func path(parts ...any) string {
	var b strings.Builder
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			b.WriteString(v)
		case int64:
			b.WriteString(strconv.FormatInt(v, 10))
		}
	}
	return b.String()
}

func TestEvalsAPI(t *testing.T) { dbtest.Run(t, testEvalsAPI) }

func testEvalsAPI(t *testing.T,
	d *sql.DB) {
	a := newAPI(t, d, true)
	ctx := context.Background()

	// The user is required, and the owner of the training evals is refused.
	a.expect(http.StatusBadRequest, "GET", "", "", nil, nil)
	a.expect(http.StatusBadRequest, "GET", "", "system", nil, nil)
	a.expect(http.StatusBadRequest, "PUT", "/e", "system", eval("e", "alice"), nil)

	// Empty list.
	var list struct {
		Evals  []evals.EvalView `json:"evals"`
		Totals evals.Totals     `json:"totals"`
	}
	a.expect(http.StatusOK, "GET", "", "alice", nil, &list)
	if len(list.Evals) != 0 || list.Totals.Evals != 0 {
		t.Fatalf("list: %+v", list)
	}

	// PUT: not valid (the problems are listed), reserved name, name that differs from the path, too large, then new (201) and replaced (200).
	var bad struct {
		Error    string   `json:"error"`
		Problems []string `json:"problems"`
	}
	a.expect(http.StatusBadRequest, "PUT", "/e", "alice", `{"name":"e","cases":[{"name":"c1"}]}`, &bad)
	if bad.Error != "invalid eval" || len(bad.Problems) == 0 {
		t.Fatalf("problems: %+v", bad)
	}
	a.expect(http.StatusBadRequest, "PUT", "/runs", "alice", eval("runs", "alice"), &bad)
	if len(bad.Problems) == 0 {
		t.Errorf("reserved name: %+v", bad)
	}
	a.expect(http.StatusBadRequest, "PUT", "/e", "alice", eval("other", "alice"), nil)
	a.expect(http.StatusRequestEntityTooLarge, "PUT", "/e", "alice", strings.Repeat("x", 2<<20), nil)
	var put struct {
		Name      string    `json:"name"`
		Cases     int       `json:"cases"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	a.expect(http.StatusCreated, "PUT", "/e", "alice", eval("e", "alice", "alice"), &put)
	if put.Name != "e" || put.Cases != 2 || put.UpdatedAt.IsZero() {
		t.Fatalf("put: %+v", put)
	}
	a.expect(http.StatusOK, "PUT", "/e", "alice", eval("e", "alice", "alice"), &put)

	// GET: the eval with its files and when it was saved; another user does not have it.
	var got map[string]any
	a.expect(http.StatusOK, "GET", "/e", "alice", nil, &got)
	if got["name"] != "e" || got["files"] == nil || got["updated_at"] == nil {
		t.Fatalf("get: %v", got)
	}
	a.expect(http.StatusNotFound, "GET", "/e", "bob", nil, nil)
	a.expect(http.StatusNotFound, "GET", "/missing", "alice", nil, nil)

	// Files: a text file used by an eval, uploaded and deleted.
	a.expect(http.StatusNotFound, "PUT", "/missing/files/corpus/x.txt", "alice", "text", nil)
	a.expect(http.StatusNotFound, "PUT", "/e/files/corpus/x.txt", "bob", "text", nil)
	a.expect(http.StatusBadRequest, "PUT", "/e/files/a%20b.txt", "alice", "text", nil)
	a.expect(http.StatusRequestEntityTooLarge, "PUT", "/e/files/corpus/big.txt", "alice", strings.Repeat("x", 300<<10), nil)
	a.expect(http.StatusNoContent, "PUT", "/e/files/corpus/x.txt", "alice", "Bob is here.", nil)
	a.expect(http.StatusNoContent, "DELETE", "/e/files/corpus/x.txt", "alice", nil, nil)
	a.expect(http.StatusNotFound, "DELETE", "/e/files/corpus/x.txt", "alice", nil, nil)

	// The list now has the eval.
	a.expect(http.StatusOK, "GET", "", "alice", nil, &list)
	if len(list.Evals) != 1 || list.Evals[0].Name != "e" || list.Evals[0].Cases != 2 || list.Totals.Cases != 2 {
		t.Fatalf("list: %+v", list)
	}
	a.expect(http.StatusOK, "GET", "", "bob", nil, &list)
	if len(list.Evals) != 0 {
		t.Fatalf("bob's list: %+v", list)
	}

	// Runs: unknown eval, bad options, then a run to its end.
	a.expect(http.StatusNotFound, "POST", "/missing/runs", "alice", "", nil)
	a.expect(http.StatusNotFound, "POST", "/e/runs", "bob", "", nil)
	a.expect(http.StatusBadRequest, "POST", "/e/runs", "alice", `{"models":{"extract":"not-a-model"}}`, nil)
	a.expect(http.StatusBadRequest, "POST", "/e/runs", "alice", `{"nope":1}`, nil)
	a.expect(http.StatusBadRequest, "POST", "/e/runs", "alice", `{"cases":["nothing"]}`, nil)
	runID := a.run("alice", "e")

	var runs struct {
		Runs []evals.RunView `json:"runs"`
	}
	a.expect(http.StatusOK, "GET", "/e/runs?limit=5", "alice", nil, &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].ID != runID || runs.Runs[0].Eval != "e" || runs.Runs[0].Counts.Passed != 2 {
		t.Fatalf("runs: %+v", runs)
	}
	a.expect(http.StatusBadRequest, "GET", "/e/runs?limit=0", "alice", nil, nil)
	a.expect(http.StatusNotFound, "GET", "/e/runs", "bob", nil, nil)

	var run evals.RunView
	a.expect(http.StatusOK, "GET", path("/runs/", runID), "alice", nil, &run)
	if run.Status != core.StatusCompleted || len(run.Cases) != 2 || len(run.Cases[0].Trajectories) == 0 {
		t.Fatalf("run: %+v", run)
	}
	cr := run.Cases[0]
	a.expect(http.StatusNotFound, "GET", path("/runs/", runID), "bob", nil, nil)
	a.expect(http.StatusNotFound, "GET", "/runs/99999", "alice", nil, nil)
	a.expect(http.StatusBadRequest, "GET", "/runs/abc", "alice", nil, nil)

	// The case run, its trajectory, graph, temporal changes and debug view.
	var one evals.CaseRunView
	a.expect(http.StatusOK, "GET", path("/case_runs/", cr.ID), "alice", nil, &one)
	if one.Eval != "e" || one.Name != "c1" || one.Verdict != core.VerdictPass {
		t.Fatalf("case run: %+v", one)
	}
	a.expect(http.StatusOK, "GET", path("/trajectories/", cr.Trajectories[0].TrajectoryID), "alice", nil, nil)
	a.expect(http.StatusNotFound, "GET", path("/trajectories/", cr.Trajectories[0].TrajectoryID), "bob", nil, nil)
	a.expect(http.StatusNotFound, "GET", "/trajectories/nope", "alice", nil, nil)
	var g struct {
		Dataset  string            `json:"dataset"`
		Entities []json.RawMessage `json:"entities"`
	}
	a.expect(http.StatusOK, "GET", path("/case_runs/", cr.ID, "/graph"), "alice", nil, &g)
	if !strings.HasPrefix(g.Dataset, "r"+strconv.FormatInt(runID, 10)+"-") || len(g.Entities) == 0 {
		t.Fatalf("graph: %+v", g)
	}
	a.expect(http.StatusOK, "GET", path("/case_runs/", cr.ID, "/temporal"), "alice", nil, nil)
	var dbg evals.DebugResponse
	a.expect(http.StatusOK, "GET", path("/case_runs/", cr.ID, "/debug"), "alice", nil, &dbg)
	if dbg.Eval != "e" || dbg.Name != "c1" || dbg.Commentable || !dbg.CommentaryEnabled || dbg.RerunOptions == nil || len(dbg.RerunOptions.Models) == 0 {
		t.Fatalf("debug: %+v", dbg)
	}
	for _, p := range []string{"", "/graph", "/temporal", "/debug"} {
		a.expect(http.StatusNotFound, "GET", path("/case_runs/", cr.ID, p), "bob", nil, nil)
		a.expect(http.StatusNotFound, "GET", path("/case_runs/", int64(99999), p), "alice", nil, nil)
	}

	// The history of a case over the runs of the eval.
	var hist evals.CaseHistory
	a.expect(http.StatusOK, "GET", "/e/history?case=c1", "alice", nil, &hist)
	if hist.Eval != "e" || len(hist.Points) != 1 || hist.Points[0].RunID != runID {
		t.Fatalf("history: %+v", hist)
	}
	a.expect(http.StatusBadRequest, "GET", "/e/history", "alice", nil, nil)
	a.expect(http.StatusNotFound, "GET", "/e/history?case=c1", "bob", nil, nil)
	a.expect(http.StatusNotFound, "GET", "/nope/unknown", "alice", nil, nil)

	// A passed case has nothing to comment on; a missing case run is 404 for the other user.
	a.expect(http.StatusBadRequest, "POST", path("/case_runs/", cr.ID, "/commentary"), "alice", "", nil)
	a.expect(http.StatusNotFound, "POST", path("/case_runs/", cr.ID, "/commentary"), "bob", "", nil)

	// Cancelling a run that has ended is 409; another user's run is 404.
	a.expect(http.StatusConflict, "POST", path("/runs/", runID, "/cancel"), "alice", nil, nil)
	a.expect(http.StatusNotFound, "POST", path("/runs/", runID, "/cancel"), "bob", nil, nil)

	// Rerun: a bad model, another user's case run, then a rerun of the case with the same model.
	a.expect(http.StatusBadRequest, "POST", path("/case_runs/", cr.ID, "/rerun"), "alice", `{"remember_model":"nope"}`, nil)
	a.expect(http.StatusBadRequest, "POST", path("/case_runs/", cr.ID, "/rerun"), "alice", `{"query_reasoning":"nope"}`, nil)
	a.expect(http.StatusNotFound, "POST", path("/case_runs/", cr.ID, "/rerun"), "bob", "", nil)
	var rr evals.RerunStarted
	a.expect(http.StatusAccepted, "POST", path("/case_runs/", cr.ID, "/rerun"), "alice", "", &rr)
	if rr.CaseRunID == 0 || rr.EvalRunID == 0 || rr.EvalRunID == runID {
		t.Fatalf("rerun: %+v", rr)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		var run evals.RunView
		a.expect(http.StatusOK, "GET", path("/runs/", rr.EvalRunID), "alice", nil, &run)
		if run.Status.Finished() {
			break
		}
	}
	a.expect(http.StatusOK, "GET", path("/case_runs/", cr.ID, "/debug"), "alice", nil, &dbg)
	if len(dbg.Reruns) != 1 || dbg.Reruns[0].CaseRunID != rr.CaseRunID {
		t.Fatalf("reruns: %+v", dbg.Reruns)
	}
	a.expect(http.StatusOK, "GET", path("/case_runs/", rr.CaseRunID, "/debug"), "alice", nil, &dbg)
	if dbg.Rerun == nil || dbg.Rerun.Of != cr.ID {
		t.Fatalf("rerun view: %+v", dbg.Rerun)
	}

	// Once the eval datasets of a run are deleted (the retention does it), its graph and temporal changes are 410.
	if err := a.m.DeleteDatasets(ctx, "alice", core.EvalRunID(rr.EvalRunID)); err != nil {
		t.Fatal(err)
	}
	var gone struct {
		Error string `json:"error"`
	}
	a.expect(http.StatusGone, "GET", path("/case_runs/", rr.CaseRunID, "/graph"), "alice", nil, &gone)
	if !strings.Contains(gone.Error, "retention") {
		t.Errorf("gone: %+v", gone)
	}
	a.expect(http.StatusGone, "GET", path("/case_runs/", rr.CaseRunID, "/temporal"), "alice", nil, nil)
	a.expect(http.StatusOK, "GET", path("/case_runs/", rr.CaseRunID), "alice", nil, nil)

	// A case that fails can be commented on: the commentary is stored and listed with the debug view.
	a.expect(http.StatusCreated, "PUT", "/f", "alice", eval("f", "zzz"), nil)
	failRun := a.run("alice", "f")
	var fr evals.RunView
	a.expect(http.StatusOK, "GET", path("/runs/", failRun), "alice", nil, &fr)
	if fr.Counts.Failed != 1 {
		t.Fatalf("failing run: %+v", fr)
	}
	failCase := fr.Cases[0].ID
	var com core.CaseCommentary
	a.expect(http.StatusOK, "POST", path("/case_runs/", failCase, "/commentary"), "alice", `{"model":"`+"claude-haiku-4-5"+`"}`, &com)
	if com.Text == "" || com.CaseRunID != core.CaseRunID(failCase) {
		t.Fatalf("commentary: %+v", com)
	}
	a.expect(http.StatusOK, "GET", path("/case_runs/", failCase, "/debug"), "alice", nil, &dbg)
	if !dbg.Commentable || len(dbg.Commentaries) != 1 {
		t.Fatalf("debug of a failure: %+v", dbg)
	}

	// Bookmarks at any level: added once per object (again updates the note), listed with the names, per user, deleted.
	var bm, fact core.EvalBookmark
	factBody := path(`{"kind":"fact","target_id":`, failCase, `,"edge_id":7,"fact":{"source":"Alice","relation":"works_at","target":"Acme"},"note":"wrong"}`)
	a.expect(http.StatusCreated, "POST", "/bookmarks", "alice", factBody, &fact)
	if fact.EvalRunID != core.EvalRunID(failRun) || fact.CaseRunID != core.CaseRunID(failCase) || fact.CaseDefID == 0 {
		t.Errorf("fact levels: %+v", fact)
	}
	a.expect(http.StatusBadRequest, "POST", "/bookmarks", "alice", path(`{"kind":"fact","target_id":`, failCase, `}`), nil)
	a.expect(http.StatusBadRequest, "POST", "/bookmarks", "alice", `{"kind":"nope","target_id":1}`, nil)
	a.expect(http.StatusNotFound, "POST", "/bookmarks", "bob", factBody, nil)
	var again core.EvalBookmark
	a.expect(http.StatusCreated, "POST", "/bookmarks", "alice", strings.Replace(factBody, "wrong", "check", 1), &again)
	if again.ID != fact.ID {
		t.Errorf("bookmarked twice: %d %d", fact.ID, again.ID)
	}
	a.expect(http.StatusCreated, "POST", "/bookmarks", "alice", path(`{"kind":"case_run","target_id":`, failCase, `}`), &bm)
	a.expect(http.StatusCreated, "POST", "/bookmarks", "alice", path(`{"kind":"eval_run","target_id":`, failRun, `}`), nil)
	a.expect(http.StatusCreated, "POST", "/bookmarks", "alice", path(`{"kind":"case_def","target_id":`, int64(fact.CaseDefID), `}`), nil)
	var bms struct {
		Bookmarks []core.EvalBookmark `json:"bookmarks"`
	}
	a.expect(http.StatusOK, "GET", "/bookmarks", "alice", nil, &bms)
	if len(bms.Bookmarks) != 4 || bms.Bookmarks[3].Kind != core.BookmarkFact || bms.Bookmarks[3].CaseName == "" || bms.Bookmarks[3].EvalName != "f" || bms.Bookmarks[3].Note != "check" || bms.Bookmarks[3].Fact == nil {
		t.Fatalf("bookmarks: %+v", bms.Bookmarks)
	}
	a.expect(http.StatusOK, "GET", "/bookmarks", "bob", nil, &bms)
	if len(bms.Bookmarks) != 0 {
		t.Errorf("bob sees bookmarks: %+v", bms.Bookmarks)
	}
	a.expect(http.StatusNotFound, "DELETE", path("/bookmarks/", bm.ID), "bob", nil, nil)
	a.expect(http.StatusNoContent, "DELETE", path("/bookmarks/", bm.ID), "alice", nil, nil)
	a.expect(http.StatusNotFound, "DELETE", path("/bookmarks/", bm.ID), "alice", nil, nil)

	// Delete a run: its datasets go too; the run is then 404.
	live, err := relational.ListEvalDatasets(ctx, d, "alice", runID)
	if err != nil || len(live) != 2 {
		t.Fatalf("datasets before: %v %v", live, err)
	}
	a.expect(http.StatusNotFound, "DELETE", path("/runs/", runID), "bob", nil, nil)
	a.expect(http.StatusNoContent, "DELETE", path("/runs/", runID), "alice", nil, nil)
	a.expect(http.StatusNotFound, "GET", path("/runs/", runID), "alice", nil, nil)
	a.expect(http.StatusNotFound, "DELETE", path("/runs/", runID), "alice", nil, nil)
	if live, _ = relational.ListEvalDatasets(ctx, d, "alice", runID); len(live) != 0 {
		t.Errorf("datasets after: %v", live)
	}

	// Delete an eval: its runs and datasets go; the file is gone; the corpus files stay.
	a.expect(http.StatusNotFound, "DELETE", "/f", "bob", nil, nil)
	a.expect(http.StatusNoContent, "DELETE", "/f", "alice", nil, nil)
	a.expect(http.StatusNotFound, "GET", "/f", "alice", nil, nil)
	a.expect(http.StatusNotFound, "GET", path("/runs/", failRun), "alice", nil, nil)
	a.expect(http.StatusNotFound, "DELETE", "/f", "alice", nil, nil)
	a.expect(http.StatusOK, "GET", "", "alice", nil, &list)
	if len(list.Evals) != 1 || list.Evals[0].Name != "e" {
		t.Fatalf("list at the end: %+v", list)
	}
}

// TestEvalsAPIActiveRun checks that an eval and a run that is going cannot be deleted (409), and that a run can be cancelled.
func TestEvalsAPIActiveRun(t *testing.T) { dbtest.Run(t, testEvalsAPIActiveRun) }

func testEvalsAPIActiveRun(t *testing.T,
	d *sql.DB) {
	a := newAPI(t, d, true)
	ctx := context.Background()
	a.expect(http.StatusCreated, "PUT", "/e", "alice", eval("e", "alice"), nil)
	// A run recorded as pending, as a manager that has not got to it yet would have it.
	def, err := a.m.Store().GetEvalDefByName(ctx, d, "alice", "e")
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.m.Runner().CreateRun(ctx,
		def,
		"other:1:1",
		runnerOptions())
	if err != nil {
		t.Fatal(err)
	}
	a.expect(http.StatusConflict, "DELETE", "/e", "alice", nil, nil)
	a.expect(http.StatusConflict, "DELETE", path("/runs/", int64(run.ID)), "alice", nil, nil)
	// Two runs are the most a user may have in progress: a third does not start (429), another user's still do.
	if _, err := a.m.Runner().CreateRun(ctx, def, "other:1:1", runnerOptions()); err != nil {
		t.Fatal(err)
	}
	var tooMany struct {
		Error string `json:"error"`
	}
	a.expect(http.StatusTooManyRequests, "POST", "/e/runs", "alice", "", &tooMany)
	if tooMany.Error != "too many runs in progress" {
		t.Errorf("429: %+v", tooMany)
	}
	var c struct {
		RunID  int64  `json:"run_id"`
		Status string `json:"status"`
	}
	a.expect(http.StatusAccepted, "POST", path("/runs/", int64(run.ID), "/cancel"), "alice", nil, &c)
	if c.RunID != int64(run.ID) {
		t.Fatalf("cancel: %+v", c)
	}
	got, err := a.m.Store().GetEvalRun(ctx, d, "alice", run.ID)
	if err != nil || !got.CancelRequested {
		t.Fatalf("cancel requested: %+v %v", got, err)
	}
}

// TestEvalsAPINoLLM checks the answers of a server without an LLM: no run starts, no commentary, no rerun (503).
func TestEvalsAPINoLLM(t *testing.T) { dbtest.Run(t, testEvalsAPINoLLM) }

func testEvalsAPINoLLM(t *testing.T,
	d *sql.DB) {
	a := newAPI(t, d, false)
	a.expect(http.StatusCreated, "PUT", "/e", "alice", eval("e", "alice"), nil)
	a.expect(http.StatusServiceUnavailable, "POST", "/e/runs", "alice", "", nil)
	a.expect(http.StatusServiceUnavailable, "POST", "/case_runs/1/commentary", "alice", "", nil)
	a.expect(http.StatusServiceUnavailable, "POST", "/case_runs/1/rerun", "alice", "", nil)
}
