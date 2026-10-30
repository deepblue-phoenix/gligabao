package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// TestEvals checks the admin eval endpoints: experiment counts, run details and trajectory files.
func TestEvals(t *testing.T) { dbtest.Run(t, testEvals) }

func testEvals(t *testing.T, d *sql.DB) {
	dir := t.TempDir()

	b := atif.NewBuilder(atif.Agent{Name: "gligabao-query", Version: "0.1"})
	b.User("question")
	b.Tool("looking up", "search", map[string]any{"q": "x"}, "found")
	traj := b.Build()
	if _, err := atif.WriteFile(dir, traj); err != nil {
		t.Fatal(err)
	}

	rec := recordCase(t, d, "base", "s", "a", 0, dir, traj)
	store := storeOf(t, d)

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
			Name         string `json:"name"`
			Runs, Cases  int
			RunsByStatus map[string]int      `json:"runs_by_status"`
			LastRun      *struct{ ID int64 } `json:"last_run"`
		} `json:"experiments"`
	}
	if c := get(admin.EvalOverview(d, store), "/x/", "", &ov); c != 200 {
		t.Fatal(c)
	}
	if ov.Totals.Experiments != 1 || ov.Totals.Runs != 1 || ov.Totals.Cases != 1 || ov.Totals.Counts.Passed != 1 ||
		len(ov.Experiments) != 1 || ov.Experiments[0].RunsByStatus["completed"] != 1 || ov.Experiments[0].LastRun == nil {
		t.Fatalf("overview: %+v", ov)
	}

	var exp struct{ Runs []struct{ ID int64 } }
	if c := get(admin.EvalExperiment(d, store), "/x/{id}", strconv.FormatInt(int64(rec.ExperimentID), 10), &exp); c != 200 || len(exp.Runs) != 1 {
		t.Fatalf("experiment: %d %+v", c, exp)
	}
	if c := get(admin.EvalExperiment(d, store), "/x/{id}", "999", nil); c != http.StatusNotFound {
		t.Fatalf("missing experiment: %d", c)
	}
	if c := get(admin.EvalExperiment(d, store), "/x/{id}", "abc", nil); c != http.StatusBadRequest {
		t.Fatalf("bad id: %d", c)
	}

	var hist struct {
		Points []struct {
			RunID   int64  `json:"run_id"`
			Status  string `json:"status"`
			Verdict string `json:"verdict"`
		}
	}
	if c := get(func(w http.ResponseWriter, r *http.Request) {
		r.URL.RawQuery = "eval=s&name=a"
		admin.EvalCaseHistory(d, store)(w, r)
	}, "/x/{id}", strconv.FormatInt(int64(rec.ExperimentID), 10), &hist); c != 200 || len(hist.Points) != 1 ||
		hist.Points[0].RunID != int64(rec.RunID) || hist.Points[0].Status != "completed" || hist.Points[0].Verdict != "pass" {
		t.Fatalf("history: %d %+v", c, hist)
	}
	if c := get(admin.EvalCaseHistory(d, store), "/x/{id}", strconv.FormatInt(int64(rec.ExperimentID), 10), nil); c != http.StatusBadRequest {
		t.Fatalf("history without case: %d", c)
	}

	var run struct {
		CaseRuns []struct {
			Trajectories []struct {
				TrajectoryID string `json:"trajectory_id"`
			}
		} `json:"case_runs"`
	}
	if c := get(admin.EvalRun(d, store), "/x/{id}", strconv.FormatInt(int64(rec.RunID), 10), &run); c != 200 ||
		len(run.CaseRuns) != 1 || len(run.CaseRuns[0].Trajectories) != 1 {
		t.Fatalf("run: %d %+v", c, run)
	}

	var got atif.Trajectory
	if c := get(admin.EvalTrajectory(d, store, dir), "/x/{id}", traj.TrajectoryID, &got); c != 200 || got.TrajectoryID != traj.TrajectoryID || len(got.Steps) != 2 {
		t.Fatalf("trajectory: %d %+v", c, got)
	}
	// Only recorded trajectories are served, never arbitrary files.
	if c := get(admin.EvalTrajectory(d, store, dir), "/x/{id}", "..%2Fetc%2Fpasswd", nil); c != http.StatusNotFound {
		t.Fatalf("unrecorded trajectory: %d", c)
	}
}

// TestEvalCaseRunTemporal checks the temporal endpoint of a case run: the events of all its remember calls together (oldest first),
// with the reference times; a query call is left out; a case without a dataset and unknown case runs are 404.
func TestEvalCaseRunTemporal(t *testing.T) { dbtest.Run(t, testEvalCaseRunTemporal) }

func testEvalCaseRunTemporal(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	fact := func(org string) string {
		return `{"entities":[{"name":"Alice","type":"person"},{"name":"` + org + `","type":"organization"}],
		"edges":[{"source":"Alice","target":"` + org + `","relation":"works_at","description":"Alice works at ` + org + `."}]}`
	}
	llm := &scriptedLLM{extractions: []string{fact("Acme"), fact("Globex")}}
	ref1, ref2 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	r1, err := memory_remember.Remember(ctx, d, llm, "u1", "main", "knowledge", "Alice works at Acme.", ref1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := memory_remember.Remember(ctx, d, llm, "u1", "main", "knowledge", "Alice started working at Globex.", ref2)
	if err != nil {
		t.Fatal(err)
	}
	dsID, err := relational.DatasetID(ctx, d, "u1", "main")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	traj := func(agent string) *atif.Trajectory {
		b := atif.NewBuilder(atif.Agent{Name: agent, Version: "0.1"})
		b.User("x")
		tr := b.Build()
		if _, err := atif.WriteFile(dir, tr); err != nil {
			t.Fatal(err)
		}
		return tr
	}
	// Two remember calls, then a query whose source artifact must not count as one.
	rec := recordCaseArtifacts(t, d, "temporal", "s", "a", dsID, dir, [][]int64{{r1.ArtifactID}, {r2.ArtifactID}, {r1.ArtifactID}},
		traj("gligabao-remember"), traj("gligabao-remember"), traj("gligabao-query"))
	none := recordCase(t, d, "temporal-none", "s", "b", 0, dir)
	store := storeOf(t, d)

	h := admin.EvalCaseRunTemporal(d, store)
	get := func(id string, out any) int {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /x/{id}", h)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x/"+id, nil))
		if out != nil && w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code
	}
	var got struct {
		CaseRunID   int64   `json:"case_run_id"`
		ArtifactIDs []int64 `json:"artifact_ids"`
		Artifacts   []struct {
			ID      int64     `json:"id"`
			ValidAt time.Time `json:"valid_at"`
		} `json:"artifacts"`
		Events []graph.EdgeEvent `json:"events"`
	}
	if c := get(strconv.FormatInt(int64(rec.CaseRunID), 10), &got); c != 200 || got.CaseRunID != int64(rec.CaseRunID) ||
		len(got.ArtifactIDs) != 2 || got.ArtifactIDs[0] != r1.ArtifactID || got.ArtifactIDs[1] != r2.ArtifactID ||
		len(got.Events) != 3 || len(got.Artifacts) != 2 {
		t.Fatalf("case run: %d %+v", c, got)
	}
	// Acme created, Globex created, Acme invalidated by it; oldest first.
	if got.Events[0].Event != graph.EdgeCreated || got.Events[0].Edge.Target != "Acme" || got.Events[1].Event != graph.EdgeCreated ||
		got.Events[1].Edge.Target != "Globex" || got.Events[2].Event != graph.EdgeInvalidated || got.Events[2].Edge.Target != "Acme" {
		t.Errorf("events: %+v", got.Events)
	}
	if got.Artifacts[0].ID != r1.ArtifactID || !got.Artifacts[0].ValidAt.Equal(ref1) || !got.Artifacts[1].ValidAt.Equal(ref2) {
		t.Errorf("artifacts: %+v", got.Artifacts)
	}

	for id, want := range map[string]int{strconv.FormatInt(int64(none.CaseRunID), 10): 404, "999999": 404, "abc": 400} {
		if c := get(id, nil); c != want {
			t.Errorf("%s: %d, want %d", id, c, want)
		}
	}
}
