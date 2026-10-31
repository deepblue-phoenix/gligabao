package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
)

// TestEvalCaseRunDebugReruns checks that the debug view of a case run lists the reruns of it (and of those reruns) with their models
// and how they ended, and only those: a rerun of another case run in the same experiment run is left out.
func TestEvalCaseRunDebugReruns(t *testing.T) { dbtest.Run(t, testEvalCaseRunDebugReruns) }

func testEvalCaseRunDebugReruns(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	store := storeOf(t, d)
	r := runner.New(d, store)
	mk := func(name string) *core.EvalDef {
		def, err := r.SyncEval(ctx, runner.EvalSpec{Name: name, Type: core.TypeKnowledge, Cases: []runner.CaseSpec{{Name: "c", Meta: &core.KnowledgeCaseDefMeta{}}}})
		if err != nil {
			t.Fatal(err)
		}
		return def
	}
	defA, defB := mk("a"), mk("b")
	xd, err := r.EnsureExperiment(ctx, "x", "", []core.EvalDefID{defA.ID, defB.ID})
	if err != nil {
		t.Fatal(err)
	}
	xr, err := r.StartExperimentRun(ctx, xd, nil)
	if err != nil {
		t.Fatal(err)
	}
	xid := xr.Run().ID
	// record runs the one case of def in the experiment run with the params, as passed or failed, and returns its case run.
	record := func(def *core.EvalDef, params core.Params, verdict core.Verdict) core.CaseRunID {
		var id core.CaseRunID
		ex := caseExecutor{func(ctx context.Context, rec *runner.EvalRecorder, def *core.EvalDef) error {
			cr, err := rec.StartCase(ctx, def.Cases[0])
			if err != nil {
				return err
			}
			cr.Status, cr.Verdict = core.StatusCompleted, verdict
			id = cr.ID
			return rec.FinishCase(ctx, cr, &core.KnowledgeCaseRunMeta{})
		}}
		run, err := r.RunEval(ctx, def, ex, runner.EvalOptions{ExperimentRunID: &xid, Params: params, Meta: &core.KnowledgeEvalRunMeta{}})
		if err != nil {
			t.Fatal(err)
		}
		if err := xr.Add(ctx, run); err != nil {
			t.Fatal(err)
		}
		return id
	}
	rerun := func(of core.CaseRunID, remember, query string) core.Params {
		p := core.Params{core.ParamRerunOf: int64(of)}
		if remember != "" {
			p[core.ParamRememberModel] = remember
		}
		if query != "" {
			p[core.ParamQueryModel] = query
		}
		return p
	}
	orig := record(defA, nil, core.VerdictFail)
	other := record(defB, nil, core.VerdictPass)
	first := record(defA, rerun(orig, "big", ""), core.VerdictFail)
	second := record(defA, rerun(first, "", "huge"), core.VerdictPass)
	record(defB, rerun(other, "big", ""), core.VerdictPass)

	type item struct {
		CaseRunID     int64  `json:"case_run_id"`
		Of            int64  `json:"of"`
		RememberModel string `json:"remember_model"`
		QueryModel    string `json:"query_model"`
		Status        string `json:"status"`
		Verdict       string `json:"verdict"`
	}
	reruns := func(id core.CaseRunID) []item {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /x/{id}", admin.EvalCaseRunDebug(d, store, nil, nil))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/x/%d", id), nil))
		var out struct {
			Reruns []item `json:"reruns"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Reruns == nil {
			t.Fatalf("debug of %d: %d %s", id, w.Code, w.Body.String())
		}
		return out.Reruns
	}

	got := reruns(orig)
	want := []item{
		{int64(first), int64(orig), "big", "", "completed", "fail"},
		{int64(second), int64(first), "", "huge", "completed", "pass"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reruns of the original: %+v, want %+v", got, want)
	}
	if got := reruns(first); fmt.Sprint(got) != fmt.Sprint(want[1:]) {
		t.Errorf("reruns of the first rerun: %+v, want %+v", got, want[1:])
	}
	if got := reruns(second); len(got) != 0 {
		t.Errorf("reruns of the last rerun: %+v, want none", got)
	}
	if got := reruns(other); len(got) != 1 || got[0].RememberModel != "big" {
		t.Errorf("reruns of the other case: %+v", got)
	}
}
