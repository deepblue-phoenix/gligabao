package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// commentLLM answers every request with a fixed commentary and records the requests (or fails with err).
type commentLLM struct {
	reqs []ai.Request
	err  error
}

func (l *commentLLM) Complete(_ context.Context,
	req ai.Request) (*ai.Response, error) {
	l.reqs = append(l.reqs, req)
	if l.err != nil {
		return nil, l.err
	}
	return &ai.Response{
		Text:         "Cause: the extractor missed it.\nFinal advice: change the prompt.",
		Model:        req.Model,
		InputTokens:  1000,
		OutputTokens: 200,
		CostUSD:      0.002,
	}, nil
}

// TestEvalCaseRunCommentary checks the debug view and the commentary of a case run: only a case that failed can be commented on,
// the commentary (made of the checks and the trajectory) is stored and listed newest first, a failing LLM stores nothing, and a server
// without an LLM says so.
func TestEvalCaseRunCommentary(t *testing.T) { dbtest.Run(t, testEvalCaseRunCommentary) }

func testEvalCaseRunCommentary(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	dir := t.TempDir()
	b := atif.NewBuilder(atif.Agent{
		Name:    "gligabao-query",
		Version: "0.1",
	})
	b.User("what is the marker in the trajectory?")
	b.SetExtra("warnings", []string{"model m does not support the thinking level \"high\""})
	traj := b.Build()
	if _, err := atif.WriteFile(dir, traj); err != nil {
		t.Fatal(err)
	}
	store := storeOf(t, d)

	// A case that failed (the recorder made it pass: change its verdict and say which check did not hold), one that passed.
	failed := recordCase(t,
		d,
		"x-failed",
		"s",
		"bad",
		0,
		dir,
		traj)
	cr, err := store.GetCaseRun(ctx,
		d,
		failed.CaseRunID)
	if err != nil {
		t.Fatal(err)
	}
	cr.Verdict = core.VerdictFail
	if err := store.UpdateCaseRun(ctx,
		d,
		cr); err != nil {
		t.Fatal(err)
	}
	passed := recordCase(t,
		d,
		"x-passed",
		"s",
		"good",
		0,
		dir)

	call := func(h http.HandlerFunc,
		method,
		id,
		body string,
		out any) int {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc(method+" /x/{id}", h)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method,
			"/x/"+id,
			strings.NewReader(body)))
		if out != nil && w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code
	}
	type debug struct {
		Eval, Name        string
		Verdict           string
		Problems          []string
		PassedChecks      []string `json:"passed_checks"`
		Commentable       bool
		CommentaryEnabled bool `json:"commentary_enabled"`
		Commentaries      []core.CaseCommentary
		Warnings          []string
	}
	id := func(r recorded) string { return strconv.FormatInt(int64(r.CaseRunID), 10) }
	llm := &commentLLM{}
	debugOf := func(llm memory_core.LLM) http.HandlerFunc {
		return admin.EvalCaseRunDebug(d,
			store,
			dir,
			llm,
			nil)
	}

	var dbg debug
	if c := call(debugOf(llm),
		"GET",
		id(failed),
		"",
		&dbg); c != 200 || dbg.Eval != "s" || dbg.Name != "bad" || dbg.Verdict != "fail" || !dbg.Commentable ||
		!dbg.CommentaryEnabled || len(dbg.PassedChecks) != 1 || dbg.Problems == nil || len(dbg.Commentaries) != 0 {
		t.Fatalf("debug before: %d %+v",
			c,
			dbg)
	}
	if len(dbg.Warnings) != 1 || !strings.Contains(dbg.Warnings[0], "does not support the thinking level") {
		t.Fatalf("warnings of the trajectories: %q", dbg.Warnings)
	}
	if c := call(debugOf(nil),
		"GET",
		id(failed),
		"",
		&dbg); c != 200 || dbg.CommentaryEnabled {
		t.Fatalf("debug without an LLM: %d %+v",
			c,
			dbg)
	}
	if c := call(debugOf(llm),
		"GET",
		"999999",
		"",
		nil); c != 404 {
		t.Fatalf("debug of a missing case run: %d", c)
	}

	post := func(llm memory_core.LLM) http.HandlerFunc {
		return admin.EvalCaseRunCommentary(d,
			store,
			dir,
			llm)
	}
	var got core.CaseCommentary
	if c := call(post(llm),
		"POST",
		id(failed),
		`{"model":"my-model"}`,
		&got); c != 200 || got.ID == 0 || got.Model != "my-model" || !strings.Contains(got.Text, "Final advice") ||
		got.InputTokens != 1000 || got.CostUSD != 0.002 || int64(got.CaseRunID) != int64(failed.CaseRunID) {
		t.Fatalf("commentary: %d %+v",
			c,
			got)
	}
	if len(llm.reqs) != 1 || llm.reqs[0].Model != "my-model" || !strings.Contains(llm.reqs[0].Messages[0].Content, "s/bad") ||
		!strings.Contains(llm.reqs[0].Messages[0].Content, "what is the marker in the trajectory?") {
		t.Fatalf("the request: %+v", llm.reqs)
	}
	// Again, without a body: the default model, and the newest comes first.
	if c := call(post(llm),
		"POST",
		id(failed),
		"",
		&got); c != 200 || llm.reqs[1].Model == "" || llm.reqs[1].Model == "my-model" {
		t.Fatalf("second commentary: %d %q",
			c,
			llm.reqs[1].Model)
	}
	if c := call(debugOf(llm),
		"GET",
		id(failed),
		"",
		&dbg); c != 200 || len(dbg.Commentaries) != 2 || dbg.Commentaries[0].ID != got.ID || dbg.Commentaries[1].Model != "my-model" {
		t.Fatalf("debug after: %d %+v",
			c,
			dbg.Commentaries)
	}

	// Not for a case that passed, nor a missing one, nor without an LLM, nor with a bad body; a failed LLM call stores nothing.
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		id   string
		body string
		want int
	}{
		{"passed", post(llm), id(passed), "", 400},
		{"missing", post(llm), "999999", "", 404},
		{"bad id", post(llm), "abc", "", 400},
		{"no LLM", post(nil), id(failed), "", 503},
		{"bad body", post(llm), id(failed), "{", 400},
		{"LLM error", post(&commentLLM{
			err: errors.New("boom"),
		}), id(failed), "", 502},
	} {
		if c := call(tc.h,
			"POST",
			tc.id,
			tc.body,
			nil); c != tc.want {
			t.Errorf("%s: %d, want %d",
				tc.name,
				c,
				tc.want)
		}
	}
	if c := call(debugOf(llm),
		"GET",
		id(failed),
		"",
		&dbg); c != 200 || len(dbg.Commentaries) != 2 {
		t.Fatalf("commentaries after the refused ones: %d %d",
			c,
			len(dbg.Commentaries))
	}
	if c := call(debugOf(llm),
		"GET",
		id(passed),
		"",
		&dbg); c != 200 || dbg.Commentable {
		t.Fatalf("a passed case is commentable: %d %+v",
			c,
			dbg)
	}
}
