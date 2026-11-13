package graph_test

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestInContextEvents(t *testing.T) { dbtest.Run(t, testInContextEvents) }

// CountInContext writes a row per entity and edge and call, with the pipeline, stage and run of the call and the time; a repeated
// ID gets one row per call, an unknown ID none, and the rows are listed by dataset, newest first.
func testInContextEvents(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	dsID, err := relational.EnsureDataset(ctx,
		d,
		"u1",
		"work")
	if err != nil {
		t.Fatal(err)
	}
	other, err := relational.EnsureDataset(ctx,
		d,
		"u1",
		"other")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := relational.Now()
	alice, _ := tx.UpsertEntity(ctx,
		dsID,
		graph.Entity{
			Name: "Alice",
			Type: "person",
		},
		"alice")
	acme, _ := tx.UpsertEntity(ctx,
		dsID,
		graph.Entity{
			Name: "Acme",
			Type: "org",
		},
		"acme")
	bob, _ := tx.UpsertEntity(ctx,
		other,
		graph.Entity{
			Name: "Bob",
			Type: "person",
		},
		"bob")
	art, err := tx.InsertArtifact(ctx,
		dsID,
		graph.ArtifactEpisode,
		"Alice works at Acme",
		"",
		now,
		now)
	if err != nil {
		t.Fatal(err)
	}
	edge, err := tx.InsertEdge(ctx, graph.NewEdge{
		DatasetID:  dsID,
		SourceID:   alice,
		TargetID:   acme,
		Relation:   "works_at",
		ValidAt:    now,
		CreatedAt:  now,
		ArtifactID: art,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	count := func(ev graph.InContextEvent,
		ents,
		edges []int64) {
		t.Helper()
		if err := graph.CountInContext(ctx,
			d,
			ev,
			ents,
			edges); err != nil {
			t.Fatal(err)
		}
	}
	before := time.Now().UTC().Add(-2 * time.Second)
	count(graph.InContextEvent{
		Pipeline: graph.PipelineQuery,
		Stage:    "query",
		RunID:    7,
	},
		[]int64{alice, acme, alice, 9999},
		[]int64{edge, edge})
	count(graph.InContextEvent{
		Pipeline: graph.PipelineQuery,
		Stage:    "evidence_only",
	},
		[]int64{alice},
		nil)
	count(graph.InContextEvent{
		Pipeline: graph.PipelineRemember,
		Stage:    "resolve_edges",
		RunID:    3,
	},
		nil,
		[]int64{edge})
	count(graph.InContextEvent{
		Pipeline: graph.PipelineRemember,
		Stage:    "detect_geo",
	},
		nil,
		nil)
	count(graph.InContextEvent{
		Pipeline: graph.PipelineQuery,
		Stage:    "query",
	},
		[]int64{bob},
		nil)
	if err := graph.CountInContext(ctx,
		d,
		graph.InContextEvent{
			Pipeline: "other",
			Stage:    "query",
		},
		[]int64{alice},
		nil); err == nil {
		t.Error("an unknown pipeline was accepted")
	}

	list, err := graph.ListInContextEvents(ctx,
		d,
		dsID)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		kind     string
		id       int64
		pipeline string
		stage    string
		run      int64
	}
	var got []row
	for i, e := range list {
		if e.DatasetID != dsID || e.CreatedAt.Before(before) || e.CreatedAt.After(time.Now().Add(2*time.Second)) || (i > 0 && e.ID >= list[i-1].ID) {
			t.Errorf("event %+v", e)
		}
		got = append(got, row{e.Kind, e.ItemID, e.Pipeline, e.Stage, e.RunID})
	}
	// Newest first: the remember call, then the evidence-only one, then the query call (two entities, one edge).
	want := []row{
		{graph.CounterEdge, edge, graph.PipelineRemember, "resolve_edges", 3},
		{graph.CounterEntity, alice, graph.PipelineQuery, "evidence_only", 0},
	}
	if len(got) != 5 {
		t.Fatalf("events = %+v, want 5", got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("event %d = %+v, want %+v",
				i,
				got[i],
				w)
		}
	}
	rest := map[row]int{}
	for _, r := range got[2:] {
		rest[r]++
	}
	for _, r := range []row{
		{graph.CounterEntity, alice, graph.PipelineQuery, "query", 7},
		{graph.CounterEntity, acme, graph.PipelineQuery, "query", 7},
		{graph.CounterEdge, edge, graph.PipelineQuery, "query", 7},
	} {
		if rest[r] != 1 {
			t.Errorf("rows %+v: %d, want 1; all: %+v",
				r,
				rest[r],
				got)
		}
	}
	if o, err := graph.ListInContextEvents(ctx,
		d,
		other); err != nil || len(o) != 1 || o[0].ItemID != bob {
		t.Errorf("events of the other dataset: %+v, %v",
			o,
			err)
	}

	// The counts are the rows per item and pipeline; items without rows are not listed, nor those of the other dataset.
	counts, err := graph.InContextCounts(ctx,
		d,
		dsID)
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := []graph.InContextCount{
		{
			Kind:     graph.CounterEdge,
			ItemID:   edge,
			Query:    1,
			Remember: 1,
		},
		{
			Kind:   graph.CounterEntity,
			ItemID: alice,
			Query:  2,
		},
		{
			Kind:   graph.CounterEntity,
			ItemID: acme,
			Query:  1,
		},
	}
	slices.SortFunc(counts, func(a,
		b graph.InContextCount) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.ItemID, b.ItemID))
	})
	slices.SortFunc(wantCounts, func(a,
		b graph.InContextCount) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.ItemID, b.ItemID))
	})
	if !slices.Equal(counts, wantCounts) {
		t.Errorf("counts = %+v, want %+v",
			counts,
			wantCounts)
	}
}
