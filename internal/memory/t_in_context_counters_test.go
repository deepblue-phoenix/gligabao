package memory_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// inContextEvents returns the in-context events of the dataset, oldest first.
func inContextEvents(t *testing.T,
	d *sql.DB,
	user,
	dataset string) []graph.InContextEvent {
	t.Helper()
	ctx := context.Background()
	dsID, err := relational.DatasetID(ctx,
		d,
		user,
		dataset)
	if err != nil {
		t.Fatal(err)
	}
	list, err := graph.ListInContextEvents(ctx,
		d,
		dsID)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list
}

func TestInContextEventsPipelines(t *testing.T) { dbtest.Run(t, testInContextEventsPipelines) }

// The entities and edges that go into an LLM context are logged with the pipeline, stage and run of the call: Remember logs the
// stored facts it compares the new ones with (resolve_edges), Query logs what it gives the LLM that answers, and a retrieval-only query,
// which calls no LLM, logs what it returns.
func testInContextEventsPipelines(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	llm := &personLLM{}
	vopt := withVectors(t) // a query embeds the question
	remember := func(text string) *memory_remember.Result {
		t.Helper()
		res, err := memory_remember.Remember(ctx,
			d,
			llm,
			"u1",
			"work",
			"episode",
			text,
			relational.Now(),
			vopt)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	remember("Person1 works at Acme.")
	if got := inContextEvents(t,
		d,
		"u1",
		"work"); len(got) != 0 {
		t.Errorf("the first text has nothing stored to compare with, yet events = %+v", got)
	}

	second := remember("Person2 works at Acme.") // its candidates: the stored fact Person1 -> Acme
	got := inContextEvents(t,
		d,
		"u1",
		"work")
	if len(got) != 1 || got[0].Kind != graph.CounterEdge || got[0].Pipeline != graph.PipelineRemember || got[0].Stage != memory_core.StageResolveEdges ||
		got[0].RunID != second.RememberRunID || second.RememberRunID == 0 || got[0].CreatedAt.IsZero() {
		t.Errorf("remember events = %+v, want the fact of the second run (%d)",
			got,
			second.RememberRunID)
	}

	question := "Where does Person1 work?"
	// A retrieval-only query sends nothing to an LLM; what it returns is logged all the same, with the stage that says so.
	ev, err := memory_query.Query(ctx,
		d,
		answerLLM{},
		"u1",
		"work",
		question,
		vopt,
		memory_core.WithEvidenceOnly(true))
	if err != nil {
		t.Fatal(err)
	}
	got = inContextEvents(t,
		d,
		"u1",
		"work")[1:]
	if len(got) != len(ev.Evidence.Entities)+len(ev.Evidence.Edges) || len(got) == 0 {
		t.Fatalf("evidence-only events = %+v, evidence = %+v",
			got,
			ev.Evidence)
	}
	for _, e := range got {
		if e.Pipeline != graph.PipelineQuery || e.Stage != memory_core.InContextEvidenceOnly || e.RunID != ev.QueryRunID || ev.QueryRunID == 0 {
			t.Errorf("evidence-only event %+v, want run %d",
				e,
				ev.QueryRunID)
		}
	}

	ans, err := memory_query.Query(ctx,
		d,
		answerLLM{},
		"u1",
		"work",
		question,
		vopt)
	if err != nil {
		t.Fatal(err)
	}
	answered := inContextEvents(t,
		d,
		"u1",
		"work")[1+len(got):]
	// Everything in the graph context is logged once: as many entities and edges as the evidence-only call returned.
	if len(answered) != len(got) {
		t.Errorf("answering query events = %d, want %d: %+v",
			len(answered),
			len(got),
			answered)
	}
	for _, e := range answered {
		if e.Pipeline != graph.PipelineQuery || e.Stage != memory_core.StageQuery || e.RunID != ans.QueryRunID || ans.QueryRunID == 0 {
			t.Errorf("query event %+v, want run %d",
				e,
				ans.QueryRunID)
		}
	}
	for _, e := range inContextEvents(t,
		d,
		"u1",
		"work") {
		if e.Pipeline == graph.PipelineRemember && e.Kind == graph.CounterEntity {
			t.Errorf("remember logged an entity without an LLM prompt that lists it: %+v", e)
		}
	}
}
