package memory_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// answerLLM answers every question the same way, or fails.
type answerLLM struct{ fail bool }

func (l answerLLM) Complete(_ context.Context, _ ai.Request) (*ai.Response, error) {
	if l.fail {
		return nil, errors.New("llm down")
	}
	return &ai.Response{StopReason: "end_turn", Model: "answer-model", Text: `{"answer":"Acme.","entities":[],"edges":[],"passages":[]}`}, nil
}

// queryStages returns the names of the stages of the run log with that status, in pipeline order.
func queryStages(run *relational.QueryRun, status string) []string {
	var out []string
	for _, s := range relational.QueryStages {
		if run.Stages[s.Name].Status == status {
			out = append(out, s.Name)
		}
	}
	return out
}

func lastQueryRun(t *testing.T, d *sql.DB) *relational.QueryRun {
	t.Helper()
	runs, _, err := relational.ListQueryRuns(context.Background(), d, relational.QueryRunFilter{}, 1, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("query runs: %d, %v", len(runs), err)
	}
	return &runs[0]
}

// withVectors is the option that turns vectors on: a query embeds the question, so it needs it.
func withVectors(t *testing.T) memory_core.Option {
	t.Helper()
	vs, err := vector.NewChromem(":memory:", 2)
	if err != nil {
		t.Fatal(err)
	}
	return memory.WithVectors(vs, &fakeEmbedder{})
}

func TestQueryRunLog(t *testing.T) { dbtest.Run(t, testQueryRunLog) }

// An answered question, with vectors: the stages it goes through succeeded with their times and inferences, and the ones it does
// not use (geo, with no resolver) are skipped.
func testQueryRunLog(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	vopt := withVectors(t)
	if _, err := memory_remember.Remember(ctx, d, &personLLM{}, "u1", "work", "episode", "Person1 works at Acme.", relational.Now(), vopt); err != nil {
		t.Fatal(err)
	}
	res, err := memory_query.Query(ctx, d, answerLLM{}, "u1", "work", "Where does Person1 work?", vopt,
		memory_core.WithSessionID("s7"), memory_core.WithModels(memory_core.Models{memory_core.StageQuery: "m-q"}))
	if err != nil {
		t.Fatal(err)
	}
	run := lastQueryRun(t, d)
	if res.QueryRunID != run.ID || run.UserID != "u1" || run.Dataset != "work" || run.Question != "Where does Person1 work?" || run.SessionID != "s7" ||
		run.Status != relational.RememberSucceeded || run.Error != nil || run.FinishedAt == nil || run.Params["models"] == nil {
		t.Fatalf("run = %+v, result run id %d", run, res.QueryRunID)
	}
	if got, want := strings.Join(queryStages(run, relational.RememberSucceeded), ","), "retrieve_graph,query"; got != want {
		t.Errorf("succeeded stages = %s, want %s", got, want)
	}
	if got, want := strings.Join(queryStages(run, relational.RememberSkipped), ","), "detect_geo,nearby_geo"; got != want {
		t.Errorf("skipped stages = %s, want %s", got, want)
	}
	for _, name := range []string{"retrieve_graph", "query"} {
		st := run.Stages[name]
		if st.Error != nil || st.StartedAt == nil || st.FinishedAt == nil || st.FinishedAt.Before(*st.StartedAt) || st.StartedAt.Before(run.StartedAt) {
			t.Errorf("stage %s = %+v", name, st)
		}
	}
	// Every inference of the query is in the stage that made it: the embedding of the question in retrieve_graph, the answer in query.
	all, _, err := relational.ListInferences(ctx, d, relational.InferenceFilter{Type: memory_query.CallQuery}, 100, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("query inferences: %d, %v", len(all), err)
	}
	for name, wantStage := range map[string]string{"retrieve_graph": memory_query.StageEmbed, "query": memory_core.StageQuery} {
		ids := run.Stages[name].InferenceIDs
		if len(ids) != 1 {
			t.Fatalf("%s inferences = %v", name, ids)
		}
		if inf, err := relational.GetInference(ctx, d, ids[0]); err != nil || inf.Stage != wantStage || inf.Type != memory_query.CallQuery {
			t.Errorf("%s inference = %+v, %v", name, inf, err)
		}
	}
	if len(run.Stages["nearby_geo"].InferenceIDs) != 0 || len(run.Stages["detect_geo"].InferenceIDs) != 0 {
		t.Errorf("a stage that did not run has inferences: %+v", run.Stages)
	}
}

func TestQueryRunLogFailure(t *testing.T) { dbtest.Run(t, testQueryRunLogFailure) }

// A failed answer fails the run and its query stage; the stage before it succeeded and the ones that are not used are skipped.
func testQueryRunLogFailure(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	vopt := withVectors(t)
	if _, err := memory_remember.Remember(ctx, d, &personLLM{}, "u1", "work", "episode", "Person1 works at Acme.", relational.Now(), vopt); err != nil {
		t.Fatal(err)
	}
	if _, err := memory_query.Query(ctx, d, answerLLM{fail: true}, "u1", "work", "Where does Person1 work?", vopt); err == nil {
		t.Fatal("no error")
	}
	run := lastQueryRun(t, d)
	if run.Status != relational.RememberFailed || run.Error == nil || !strings.Contains(*run.Error, "llm down") || run.FinishedAt == nil {
		t.Fatalf("run = %+v", run)
	}
	q := run.Stages["query"]
	if q.Status != relational.RememberFailed || q.Error == nil || !strings.Contains(*q.Error, "llm down") {
		t.Errorf("query stage = %+v", q)
	}
	if run.Stages["retrieve_graph"].Status != relational.RememberSucceeded {
		t.Errorf("retrieve_graph = %+v", run.Stages["retrieve_graph"])
	}
}

func TestQueryRunLogNothingToAnswerFrom(t *testing.T) {
	dbtest.Run(t, testQueryRunLogNothingToAnswerFrom)
}

// A dataset with nothing in it ends the pipeline at the first stage, with no LLM call: the run succeeded, and the stages after the
// first are skipped, not left as if they were still to come.
func testQueryRunLogNothingToAnswerFrom(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	if _, err := relational.EnsureDataset(ctx, d, "u1", "empty"); err != nil {
		t.Fatal(err)
	}
	res, err := memory_query.Query(ctx, d, answerLLM{fail: true}, "u1", "empty", "Anything?", withVectors(t))
	if err != nil {
		t.Fatal(err)
	}
	run := lastQueryRun(t, d)
	if res.QueryRunID != run.ID || run.Status != relational.RememberSucceeded {
		t.Fatalf("run = %+v", run)
	}
	if got, want := strings.Join(queryStages(run, relational.RememberSucceeded), ","), "retrieve_graph"; got != want {
		t.Errorf("succeeded stages = %s, want %s", got, want)
	}
	if got, want := strings.Join(queryStages(run, relational.RememberSkipped), ","), "detect_geo,nearby_geo,query"; got != want {
		t.Errorf("skipped stages = %s, want %s", got, want)
	}
}

func TestQueryOfAMissingDatasetHasNoRun(t *testing.T) {
	dbtest.Run(t, testQueryOfAMissingDatasetHasNoRun)
}

// A request that is rejected before the pipeline starts has no row, as for remember.
func testQueryOfAMissingDatasetHasNoRun(t *testing.T, d *sql.DB) {
	if _, err := memory_query.Query(context.Background(), d, answerLLM{}, "u1", "nope", "Anything?", withVectors(t)); !errors.Is(err, memory_core.ErrDatasetNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, total, err := relational.ListQueryRuns(context.Background(), d, relational.QueryRunFilter{}, 1, 0); err != nil || total != 0 {
		t.Fatalf("query runs: %d, %v", total, err)
	}
}
