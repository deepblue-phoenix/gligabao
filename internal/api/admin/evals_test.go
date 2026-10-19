package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/evals/experiments"
)

// TestEvals checks the admin eval endpoints: experiment counts, run details and trajectory files.
func TestEvals(t *testing.T) { dbtest.Run(t, testEvals) }

func testEvals(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dir := t.TempDir()

	b := atif.NewBuilder(atif.Agent{Name: "gligabao-recall", Version: "0.1"})
	b.User("question")
	b.Tool("looking up", "search", map[string]any{"q": "x"}, "found")
	traj := b.Build()
	if _, err := atif.WriteFile(dir, traj); err != nil {
		t.Fatal(err)
	}

	tr, err := experiments.Start(ctx, d, experiments.Options{Name: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Record(ctx, experiments.CaseOutcome{Eval: "s", Name: "a", Trajectories: []*atif.Trajectory{traj}, TrajectoryDir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Finish(ctx); err != nil {
		t.Fatal(err)
	}

	get := func(h http.HandlerFunc, pattern, id string, out any) int {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("GET "+pattern, h)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x/"+id, nil))
		if out != nil && w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code
	}

	var ov struct {
		Totals      admin.EvalTotals `json:"totals"`
		Experiments []struct {
			Name        string `json:"name"`
			Runs, Cases int
			RunsPassed  int                 `json:"runs_passed"`
			LastRun     *struct{ ID int64 } `json:"last_run"`
		} `json:"experiments"`
	}
	if c := get(admin.EvalOverview(d), "/x/", "", &ov); c != 200 {
		t.Fatal(c)
	}
	if ov.Totals.Experiments != 1 || ov.Totals.Runs != 1 || ov.Totals.Cases != 1 || ov.Totals.Passed != 1 ||
		len(ov.Experiments) != 1 || ov.Experiments[0].RunsPassed != 1 || ov.Experiments[0].LastRun == nil {
		t.Fatalf("overview: %+v", ov)
	}

	var exp struct{ Runs []struct{ ID int64 } }
	if c := get(admin.EvalExperiment(d), "/x/{id}", strconv.FormatInt(tr.Experiment.ID, 10), &exp); c != 200 || len(exp.Runs) != 1 {
		t.Fatalf("experiment: %d %+v", c, exp)
	}
	if c := get(admin.EvalExperiment(d), "/x/{id}", "999", nil); c != http.StatusNotFound {
		t.Fatalf("missing experiment: %d", c)
	}
	if c := get(admin.EvalExperiment(d), "/x/{id}", "abc", nil); c != http.StatusBadRequest {
		t.Fatalf("bad id: %d", c)
	}

	var hist struct {
		Points []struct {
			RunID  int64  `json:"run_id"`
			Status string `json:"status"`
		}
	}
	if c := get(func(w http.ResponseWriter, r *http.Request) {
		r.URL.RawQuery = "eval=s&name=a"
		admin.EvalCaseHistory(d)(w, r)
	}, "/x/{id}", strconv.FormatInt(tr.Experiment.ID, 10), &hist); c != 200 || len(hist.Points) != 1 ||
		hist.Points[0].RunID != tr.Run.ID || hist.Points[0].Status != "passed" {
		t.Fatalf("history: %d %+v", c, hist)
	}
	if c := get(admin.EvalCaseHistory(d), "/x/{id}", strconv.FormatInt(tr.Experiment.ID, 10), nil); c != http.StatusBadRequest {
		t.Fatalf("history without case: %d", c)
	}

	var run struct {
		CaseResults []struct {
			Trajectories []struct {
				TrajectoryID string `json:"trajectory_id"`
			}
		} `json:"case_results"`
	}
	if c := get(admin.EvalRun(d), "/x/{id}", strconv.FormatInt(tr.Run.ID, 10), &run); c != 200 ||
		len(run.CaseResults) != 1 || len(run.CaseResults[0].Trajectories) != 1 {
		t.Fatalf("run: %d %+v", c, run)
	}

	var got atif.Trajectory
	if c := get(admin.EvalTrajectory(d, dir), "/x/{id}", traj.TrajectoryID, &got); c != 200 || got.TrajectoryID != traj.TrajectoryID || len(got.Steps) != 2 {
		t.Fatalf("trajectory: %d %+v", c, got)
	}
	// Only recorded trajectories are served, never arbitrary files.
	if c := get(admin.EvalTrajectory(d, dir), "/x/{id}", "..%2Fetc%2Fpasswd", nil); c != http.StatusNotFound {
		t.Fatalf("unrecorded trajectory: %d", c)
	}
}
