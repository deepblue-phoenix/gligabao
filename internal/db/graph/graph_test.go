package graph_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ivan/gligabao/internal/db/graph"
	"github.com/ivan/gligabao/internal/db/relational"
	"github.com/ivan/gligabao/internal/db/relational/dbtest"
)

// TestBackends runs the same scenario against every available backend (see dbtest).
func TestBackends(t *testing.T) { dbtest.Run(t, scenario) }

func scenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	user := fmt.Sprintf("u-%d", time.Now().UnixNano())
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	dsID, err := relational.EnsureDataset(ctx, d, user, "proj")
	check(err)
	other, err := relational.EnsureDataset(ctx, d, user, "main")
	check(err)

	// A memory: entities, edges, an invalidation.
	tx, err := graph.Begin(ctx, d)
	check(err)
	defer tx.Rollback()
	now := relational.Now()
	memID, err := tx.InsertMemory(ctx, dsID, "Alice works at Acme", now.Add(-time.Hour), now)
	check(err)
	alice, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Alice", Type: "person", Description: "an engineer"}, "alice")
	check(err)
	again, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Alice", Type: "person"}, "alice")
	check(err)
	if again != alice {
		t.Fatalf("upsert created a second entity: %d vs %d", again, alice)
	}
	acme, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Acme", Type: "org"}, "acme")
	check(err)
	if id, err := tx.FindEntityID(ctx, dsID, "acme"); err != nil || id != acme {
		t.Fatalf("FindEntityID = %d, %v", id, err)
	}
	if _, err := tx.FindEntityID(ctx, dsID, "zzz"); !errors.Is(err, graph.ErrNotFound) {
		t.Fatalf("FindEntityID missing: %v", err)
	}
	valid := now.Add(-time.Hour)
	e1, err := tx.InsertEdge(ctx, graph.NewEdge{DatasetID: dsID, SourceID: alice, TargetID: acme, Relation: "works_at",
		Description: "Alice works at Acme.", ValidAt: valid, CreatedAt: now, MemoryID: memID})
	check(err)
	check(tx.MergeDuplicateEdge(ctx, dsID, e1, nil))
	edge, err := tx.GetEdge(ctx, dsID, e1)
	check(err)
	if edge.Source != "Alice" || edge.Target != "Acme" || edge.ValidAt == nil || !edge.ValidAt.Equal(valid) || edge.InvalidAt != nil || edge.ExpiredAt != nil {
		t.Fatalf("edge: %+v", edge)
	}
	if _, err := tx.GetEdge(ctx, other, e1); !errors.Is(err, graph.ErrNotFound) {
		t.Fatalf("edge of another dataset: %v", err)
	}
	check(tx.Commit())

	cands, err := graph.LoadCandidateEdges(ctx, d, dsID, []string{"alice"}, 10)
	check(err)
	if len(cands) != 1 || cands[0].ID != e1 {
		t.Fatalf("candidates: %+v", cands)
	}

	tx, err = graph.Begin(ctx, d)
	check(err)
	defer tx.Rollback()
	check(tx.MergeDuplicateEdge(ctx, dsID, e1, &now))
	check(tx.InvalidateEdge(ctx, e1, now, now))
	check(tx.Commit())
	if cands, err = graph.LoadCandidateEdges(ctx, d, dsID, []string{"alice"}, 10); err != nil || len(cands) != 0 {
		t.Fatalf("expired edge still a candidate: %+v, %v", cands, err)
	}
	edges, err := graph.ListEdges(ctx, d, user, "proj")
	check(err)
	if len(edges) != 1 || edges[0].InvalidAt == nil || edges[0].ExpiredAt == nil {
		t.Fatalf("ListEdges: %+v", edges)
	}

	// Retrieval.
	seeds, err := graph.MatchEntities(ctx, d, dsID, "where does alice work?", []string{"alice", "work"}, 10)
	check(err)
	if len(seeds) != 1 || seeds[0] != alice {
		t.Fatalf("MatchEntities: %v", seeds)
	}
	if seeds, err = graph.MatchEntities(ctx, d, dsID, "nothing relevant", []string{"nothing"}, 10); err != nil || len(seeds) != 0 {
		t.Fatalf("MatchEntities none: %v, %v", seeds, err)
	}
	fb, err := graph.FallbackSeedEntities(ctx, d, dsID, 10)
	check(err)
	if len(fb) != 2 {
		t.Fatalf("FallbackSeedEntities: %v", fb)
	}
	around, err := graph.EdgesAround(ctx, d, dsID, []int64{alice}, 10)
	check(err)
	if len(around) != 1 || around[0].SourceID != alice || around[0].TargetID != acme || around[0].MemoryID == nil || *around[0].MemoryID != memID || around[0].ExpiredAt == nil {
		t.Fatalf("EdgesAround: %+v", around)
	}
	ents, err := graph.EntitiesByID(ctx, d, []int64{alice, acme})
	check(err)
	if len(ents) != 2 {
		t.Fatalf("EntitiesByID: %+v", ents)
	}

	// Inspection.
	if c, err := graph.DatasetCounts(ctx, d, dsID); err != nil || c != (graph.Counts{Memories: 1, Entities: 2, Edges: 1}) {
		t.Fatalf("DatasetCounts: %+v, %v", c, err)
	}
	if mems, err := graph.ListMemories(ctx, d, dsID); err != nil || len(mems) != 1 || mems[0].ID != memID || mems[0].Text != "Alice works at Acme" || !mems[0].ValidAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("ListMemories: %+v, %v", mems, err)
	}
	if ents, err := graph.ListEntities(ctx, d, dsID); err != nil || len(ents) != 2 || ents[0].Name != "Alice" || ents[0].Description != "an engineer" {
		t.Fatalf("ListEntities: %+v, %v", ents, err)
	}
	if n, err := graph.CountCrossDatasetEdges(ctx, d); err != nil || n != 0 {
		t.Fatalf("CountCrossDatasetEdges: %d, %v", n, err)
	}
}
