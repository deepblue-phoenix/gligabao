package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// fakeRerunner answers a rerun with the case run it was given (the real one makes a new case run), or fails with err.
type fakeRerunner struct {
	reply core.CaseRunID
	err   error
	calls []string
}

func (f *fakeRerunner) Models() []ai.ModelInfo {
	return []ai.ModelInfo{{Name: "zeta-model", Kind: ai.KindLLM}, {Name: "alpha-model", Kind: ai.KindLLM}}
}

func (f *fakeRerunner) Rerun(_ context.Context, id core.CaseRunID, opts core.RerunOptions) (core.CaseRunID, error) {
	f.calls = append(f.calls, fmt.Sprintf("%d %q %q %q %q", id, opts.RememberModel, opts.QueryModel, opts.RememberReasoning, opts.QueryReasoning))
	return f.reply, f.err
}

// TestEvalCaseRunRerun checks the endpoint of the rerun of a case: only the models of the price table are taken, the answer is the
// new case run with its eval run and experiment run, and each way the rerun can be refused has its status; the debug view offers the
// models and says of a rerun what it repeated.
func TestEvalCaseRunRerun(t *testing.T) { dbtest.Run(t, testEvalCaseRunRerun) }

func testEvalCaseRunRerun(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	store := storeOf(t, d)
	orig := recordCase(t, d, "x", "s", "c1", 0, t.TempDir())
	again := recordCase(t, d, "x", "s", "c2", 0, t.TempDir()) // stands for the case run the rerun made
	// Make the second one look like a rerun: its eval run says so.
	cr, err := store.GetCaseRun(ctx, d, again.CaseRunID)
	if err != nil {
		t.Fatal(err)
	}
	er, err := store.GetEvalRun(ctx, d, cr.EvalRunID)
	if err != nil {
		t.Fatal(err)
	}
	er.Params = core.Params{core.ParamRerunOf: int64(orig.CaseRunID), core.ParamRememberModel: "alpha-model"}
	if err := store.UpdateEvalRun(ctx, d, er); err != nil {
		t.Fatal(err)
	}

	call := func(h http.HandlerFunc, method, id, body string, out any) (int, string) {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc(method+" /x/{id}", h)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/x/"+id, strings.NewReader(body)))
		if out != nil && w.Code/100 == 2 {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, w.Body.String()
	}
	id := func(r recorded) string { return strconv.FormatInt(int64(r.CaseRunID), 10) }

	rr := &fakeRerunner{reply: again.CaseRunID}
	post := func(rr admin.EvalRerunner) http.HandlerFunc { return admin.EvalCaseRunRerun(d, store, rr) }
	var started struct {
		CaseRunID       int64  `json:"case_run_id"`
		EvalRunID       int64  `json:"eval_run_id"`
		ExperimentRunID *int64 `json:"experiment_run_id"`
	}
	if c, body := call(post(rr), "POST", id(orig), `{"remember_model":"alpha-model","query_model":" zeta-model ","remember_reasoning":"low","query_reasoning":" High "}`, &started); c != 202 ||
		started.CaseRunID != int64(again.CaseRunID) || started.EvalRunID != int64(er.ID) || started.ExperimentRunID == nil || *started.ExperimentRunID != int64(again.RunID) {
		t.Fatalf("rerun: %d %s", c, body)
	}
	if len(rr.calls) != 1 || rr.calls[0] != fmt.Sprintf("%d %q %q %q %q", orig.CaseRunID, "alpha-model", "zeta-model", "low", "High") {
		t.Fatalf("calls: %v", rr.calls)
	}
	// No body: the models stay as they were.
	if c, body := call(post(rr), "POST", id(orig), "", nil); c != 202 || rr.calls[1] != fmt.Sprintf("%d %q %q %q %q", orig.CaseRunID, "", "", "", "") {
		t.Fatalf("rerun without a body: %d %s %v", c, body, rr.calls)
	}

	n := len(rr.calls)
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		id   string
		body string
		want int
	}{
		{"model not in the price table", post(rr), id(orig), `{"remember_model":"gpt-9"}`, 400},
		{"query model not in the price table", post(rr), id(orig), `{"query_model":"alpha"}`, 400},
		{"unknown remember level", post(rr), id(orig), `{"remember_reasoning":"extreme"}`, 400},
		{"unknown query level", post(rr), id(orig), `{"query_reasoning":"max"}`, 400},
		{"bad levels by the rerunner", post(&fakeRerunner{err: fmt.Errorf("%w: x", memory_core.ErrInvalidReasoning)}), id(orig), "", 400},
		{"bad body", post(rr), id(orig), "{", 400},
		{"bad id", post(rr), "abc", "", 400},
		{"missing", post(rr), "999999", "", 404},
		{"cannot rerun", post(&fakeRerunner{err: fmt.Errorf("%w: gone", core.ErrRerunRefused)}), id(orig), "", 409},
		{"busy", post(&fakeRerunner{err: core.ErrRerunBusy}), id(orig), "", 409},
		{"not found by the rerunner", post(&fakeRerunner{err: evalsdb.ErrNotFound}), id(orig), "", 404},
		{"bad models", post(&fakeRerunner{err: fmt.Errorf("%w: x", memory_core.ErrInvalidModels)}), id(orig), "", 400},
		{"failure", post(&fakeRerunner{err: fmt.Errorf("disk")}), id(orig), "", 500},
		{"no rerunner", post(nil), id(orig), "", 503},
	} {
		if c, body := call(tc.h, "POST", tc.id, tc.body, nil); c != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, c, body, tc.want)
		}
	}
	if len(rr.calls) != n {
		t.Errorf("a refused request reached the rerunner: %v", rr.calls[n:])
	}

	// The debug view offers the models (sorted) when the server can rerun, and says what a rerun repeated.
	type debug struct {
		Rerun *struct {
			Of            int64  `json:"of"`
			RememberModel string `json:"remember_model"`
			QueryModel    string `json:"query_model"`
		} `json:"rerun"`
		RerunOptions *struct {
			Models    []string `json:"models"`
			Reasoning []string `json:"reasoning"`
		} `json:"rerun_options"`
	}
	var dbg debug
	debugOf := func(rr admin.EvalRerunner) http.HandlerFunc { return admin.EvalCaseRunDebug(d, store, nil, rr) }
	if c, body := call(debugOf(rr), "GET", id(orig), "", &dbg); c != 200 || dbg.Rerun != nil || dbg.RerunOptions == nil || fmt.Sprint(dbg.RerunOptions.Models) != "[alpha-model zeta-model]" ||
		fmt.Sprint(dbg.RerunOptions.Reasoning) != "[none low medium high]" {
		t.Fatalf("debug of the original: %d %s", c, body)
	}
	dbg = debug{}
	if c, body := call(debugOf(rr), "GET", id(again), "", &dbg); c != 200 || dbg.Rerun == nil || dbg.Rerun.Of != int64(orig.CaseRunID) || dbg.Rerun.RememberModel != "alpha-model" || dbg.Rerun.QueryModel != "" {
		t.Fatalf("debug of the rerun: %d %s", c, body)
	}
	dbg = debug{}
	if c, body := call(debugOf(nil), "GET", id(orig), "", &dbg); c != 200 || dbg.RerunOptions != nil {
		t.Fatalf("debug without a rerunner: %d %s", c, body)
	}

	// The run view marks the rerun.
	var run struct {
		CaseRuns []struct {
			ID    int64 `json:"id"`
			Rerun *struct {
				Of int64 `json:"of"`
			} `json:"rerun"`
		} `json:"case_runs"`
	}
	runH := admin.EvalRun(d, store)
	if c, body := call(runH, "GET", strconv.FormatInt(int64(again.RunID), 10), "", &run); c != 200 {
		t.Fatalf("run: %d %s", c, body)
	}
	found := false
	for _, cr := range run.CaseRuns {
		if cr.ID == int64(again.CaseRunID) {
			found = true
			if cr.Rerun == nil || cr.Rerun.Of != int64(orig.CaseRunID) {
				t.Errorf("the rerun is not marked in its run: %+v", cr)
			}
		}
	}
	if !found {
		t.Errorf("the rerun's case run is not in the run: %+v", run.CaseRuns)
	}
}
