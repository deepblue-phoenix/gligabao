package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/api/evals"
	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/usereval"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
)

// noManager is a run manager that cannot do anything (the admin views only read).
type noManager struct{}

func (noManager) Start(context.Context,
	string,
	string,
	core.RunOptions) (core.EvalRunID, error) {
	return 0, core.ErrNoLLM
}

func (noManager) Cancel(context.Context,
	string,
	core.EvalRunID) error {
	return core.ErrNoLLM
}

func (noManager) Rerun(context.Context,
	string,
	core.CaseRunID,
	core.RerunOptions) (core.CaseRunID, error) {
	return 0, core.ErrNoLLM
}
func (noManager) CanRerun() bool          { return false }
func (noManager) Models() []llm.ModelInfo { return nil }
func (noManager) DeleteDatasets(context.Context,
	string,
	core.EvalRunID) error {
	return nil
}
func (noManager) CheckBudget(context.Context,
	string) error {
	return nil
}
func (noManager) Env() usereval.Env { return usereval.Env{} }

// TestEvals checks the admin eval endpoints over the evals of two users: the list with its user filter, an eval with its runs, a run
// with its cases, the history of a case, the debug view and the trajectory files; each object is read as its owner, and an unknown one
// is 404.
func TestEvals(t *testing.T) { dbtest.Run(t, testEvals) }

func testEvals(t *testing.T,
	d *sql.DB) {
	dir := t.TempDir()
	b := atif.NewBuilder(atif.Agent{
		Name:    "gligabao-query",
		Version: "0.1",
	})
	b.User("question")
	b.Tool("looking up",
		"search",
		map[string]any{"q": "x"},
		"found")
	traj := b.Build()
	if _, err := atif.WriteFile(dir, traj); err != nil {
		t.Fatal(err)
	}
	alice := recordCase(t,
		d,
		"alice",
		"s",
		"a",
		0,
		dir,
		traj)
	bob := recordCase(t,
		d,
		"bob",
		"s",
		"a",
		0,
		dir)
	svc := &evals.Service{
		DB:            d,
		Store:         storeOf(t, d),
		Mgr:           noManager{},
		TrajectoryDir: dir,
	}

	get := func(h http.HandlerFunc,
		pattern,
		url string,
		out any) int {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("GET "+pattern, h)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			url,
			nil))
		if out != nil && w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatalf("%s: %v",
					w.Body,
					err)
			}
		}
		return w.Code
	}

	// The list: every user's evals, each with its owner; the filter keeps one user's.
	var ov struct {
		Totals evals.Totals `json:"totals"`
		Evals  []struct {
			ID      int64  `json:"id"`
			UserID  string `json:"user_id"`
			Name    string `json:"name"`
			Runs    int    `json:"runs"`
			Cases   int    `json:"cases"`
			LastRun *struct {
				ID int64 `json:"id"`
			} `json:"last_run"`
		} `json:"evals"`
	}
	if c := get(admin.EvalOverview(svc),
		"/x",
		"/x",
		&ov); c != 200 {
		t.Fatal(c)
	}
	if ov.Totals.Evals != 2 || ov.Totals.Runs != 2 || ov.Totals.Cases != 2 || ov.Totals.Counts.Passed != 2 || len(ov.Evals) != 2 ||
		ov.Evals[0].UserID != "alice" || ov.Evals[1].UserID != "bob" || ov.Evals[0].Name != "s" || ov.Evals[0].Runs != 1 || ov.Evals[0].LastRun == nil {
		t.Fatalf("overview: %+v", ov)
	}
	if c := get(admin.EvalOverview(svc),
		"/x",
		"/x?user_id=bob",
		&ov); c != 200 || len(ov.Evals) != 1 || ov.Evals[0].UserID != "bob" || ov.Totals.Evals != 1 {
		t.Fatalf("filtered overview: %d %+v", c, ov)
	}

	// An eval with its cases and runs.
	var def struct {
		UserID string   `json:"user_id"`
		Cases  []string `json:"cases"`
		Runs   []struct {
			ID int64 `json:"id"`
		} `json:"runs"`
	}
	if c := get(admin.EvalDef(svc),
		"/x/{id}",
		"/x/"+strconv.FormatInt(int64(alice.DefID), 10),
		&def); c != 200 || def.UserID != "alice" || len(def.Cases) != 1 || def.Cases[0] != "a" || len(def.Runs) != 1 || def.Runs[0].ID != int64(alice.RunID) {
		t.Fatalf("eval: %d %+v", c, def)
	}
	for url, want := range map[string]int{"/x/999": http.StatusNotFound, "/x/abc": http.StatusBadRequest} {
		if c := get(admin.EvalDef(svc),
			"/x/{id}",
			url,
			nil); c != want {
			t.Errorf("%s: %d", url, c)
		}
	}

	// The history of a case.
	var hist struct {
		Points []struct {
			RunID   int64  `json:"run_id"`
			Verdict string `json:"verdict"`
		}
	}
	if c := get(admin.EvalCaseHistory(svc),
		"/x/{id}",
		"/x/"+strconv.FormatInt(int64(bob.DefID), 10)+"?case=a",
		&hist); c != 200 || len(hist.Points) != 1 || hist.Points[0].RunID != int64(bob.RunID) || hist.Points[0].Verdict != "pass" {
		t.Fatalf("history: %d %+v", c, hist)
	}
	if c := get(admin.EvalCaseHistory(svc),
		"/x/{id}",
		"/x/"+strconv.FormatInt(int64(bob.DefID), 10),
		nil); c != http.StatusBadRequest {
		t.Errorf("history without a case: %d", c)
	}

	// A run of either user, with its case runs and their trajectories.
	for _, rec := range []recorded{alice, bob} {
		var run struct {
			Status string `json:"status"`
			Cases  []struct {
				ID           int64 `json:"id"`
				Trajectories []struct {
					TrajectoryID string `json:"trajectory_id"`
				} `json:"trajectories"`
			} `json:"cases"`
		}
		if c := get(admin.EvalRun(svc),
			"/x/{id}",
			"/x/"+strconv.FormatInt(int64(rec.RunID), 10),
			&run); c != 200 || run.Status != "completed" || len(run.Cases) != 1 || run.Cases[0].ID != int64(rec.CaseRunID) {
			t.Fatalf("run: %d %+v", c, run)
		}
	}
	if c := get(admin.EvalRun(svc),
		"/x/{id}",
		"/x/999",
		nil); c != http.StatusNotFound {
		t.Errorf("missing run: %d", c)
	}

	// The debug view of a case run, read as its owner.
	var dbg evals.DebugResponse
	if c := get(admin.EvalCaseRunDebug(svc),
		"/x/{id}",
		"/x/"+strconv.FormatInt(int64(alice.CaseRunID), 10),
		&dbg); c != 200 || dbg.Eval != "s" || dbg.Name != "a" || dbg.Commentable || dbg.RerunOptions != nil || len(dbg.Reruns) != 0 {
		t.Fatalf("debug: %d %+v", c, dbg)
	}
	if c := get(admin.EvalCaseRunDebug(svc),
		"/x/{id}",
		"/x/999",
		nil); c != http.StatusNotFound {
		t.Errorf("missing case run: %d", c)
	}

	// The trajectory file; an unknown one is 404.
	if c := get(admin.EvalTrajectory(svc),
		"/x/{id}",
		"/x/"+traj.TrajectoryID,
		nil); c != 200 {
		t.Fatalf("trajectory: %d", c)
	}
	if c := get(admin.EvalTrajectory(svc),
		"/x/{id}",
		"/x/nope",
		nil); c != http.StatusNotFound {
		t.Errorf("unknown trajectory: %d", c)
	}
}
