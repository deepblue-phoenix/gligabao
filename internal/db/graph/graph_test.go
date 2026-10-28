package graph_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
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
	artID, err := tx.InsertArtifact(ctx, dsID, graph.ArtifactEpisode, "Alice works at Acme", "sess-1", now.Add(-time.Hour), now)
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
	// Entity provenance: Alice is mentioned by two artifacts, Acme only by the first. Linking twice is a no-op.
	art2, err := tx.InsertArtifact(ctx, dsID, graph.ArtifactKnowledge, "Alice is an engineer", "", now, now)
	check(err)
	check(tx.LinkEntity(ctx, alice, artID))
	check(tx.LinkEntity(ctx, alice, artID))
	check(tx.LinkEntity(ctx, alice, art2))
	check(tx.LinkEntity(ctx, acme, artID))
	valid := now.Add(-time.Hour)
	e1, err := tx.InsertEdge(ctx, graph.NewEdge{DatasetID: dsID, SourceID: alice, TargetID: acme, Relation: "works_at",
		Description: "Alice works at Acme.", ValidAt: valid, CreatedAt: now, ArtifactID: artID})
	check(err)
	// Edge provenance: the creator, then a later artifact that stated the same fact (a merged duplicate). Linking
	// twice is a no-op.
	check(tx.LinkEdge(ctx, e1, artID))
	check(tx.LinkEdge(ctx, e1, art2))
	check(tx.LinkEdge(ctx, e1, art2))
	// Session provenance: Alice was discovered in two sessions, Acme in one; the edge by the creator's session and by a
	// later session that restated it. Linking twice is a no-op.
	check(tx.LinkEntitySession(ctx, alice, "sess-1"))
	check(tx.LinkEntitySession(ctx, alice, "sess-1"))
	check(tx.LinkEntitySession(ctx, alice, "sess-2"))
	check(tx.LinkEntitySession(ctx, acme, "sess-1"))
	check(tx.LinkEdgeSession(ctx, e1, "sess-1"))
	check(tx.LinkEdgeSession(ctx, e1, "sess-2"))
	check(tx.LinkEdgeSession(ctx, e1, "sess-2"))
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
	if len(around) != 0 { // e1 was expired above: retrieval returns live facts only
		t.Fatalf("EdgesAround returned an expired edge: %+v", around)
	}
	if byID, err := graph.EdgesByID(ctx, d, dsID, []int64{e1}); err != nil || len(byID) != 0 {
		t.Fatalf("EdgesByID returned an expired edge: %+v, %v", byID, err)
	}
	ents, err := graph.EntitiesByID(ctx, d, []int64{alice, acme})
	check(err)
	if len(ents) != 2 {
		t.Fatalf("EntitiesByID: %+v", ents)
	}

	// Inspection.
	if c, err := graph.DatasetCounts(ctx, d, dsID); err != nil || c != (graph.Counts{Artifacts: 2, Entities: 2, Edges: 1}) {
		t.Fatalf("DatasetCounts: %+v, %v", c, err)
	}
	links, err := graph.ListEntityArtifacts(ctx, d, dsID)
	want := []graph.EntityArtifact{{EntityID: alice, ArtifactID: artID}, {EntityID: alice, ArtifactID: art2}, {EntityID: acme, ArtifactID: artID}}
	if err != nil || len(links) != len(want) {
		t.Fatalf("ListEntityArtifacts: %+v, %v", links, err)
	}
	for i := range want {
		if links[i] != want[i] {
			t.Errorf("ListEntityArtifacts[%d] = %+v, want %+v", i, links[i], want[i])
		}
	}
	elinks, err := graph.ListEdgeArtifacts(ctx, d, dsID)
	if err != nil || len(elinks) != 2 || elinks[0] != (graph.EdgeArtifact{EdgeID: e1, ArtifactID: artID}) || elinks[1] != (graph.EdgeArtifact{EdgeID: e1, ArtifactID: art2}) {
		t.Fatalf("ListEdgeArtifacts: %+v, %v", elinks, err)
	}
	esl, err := graph.ListEntitySessions(ctx, d, dsID)
	wantES := []graph.EntitySession{{EntityID: alice, SessionID: "sess-1"}, {EntityID: alice, SessionID: "sess-2"}, {EntityID: acme, SessionID: "sess-1"}}
	if err != nil || len(esl) != len(wantES) {
		t.Fatalf("ListEntitySessions: %+v, %v", esl, err)
	}
	for i := range wantES {
		if esl[i] != wantES[i] {
			t.Errorf("ListEntitySessions[%d] = %+v, want %+v", i, esl[i], wantES[i])
		}
	}
	if l, err := graph.ListEdgeSessions(ctx, d, dsID); err != nil || len(l) != 2 || l[0] != (graph.EdgeSession{EdgeID: e1, SessionID: "sess-1"}) || l[1] != (graph.EdgeSession{EdgeID: e1, SessionID: "sess-2"}) {
		t.Fatalf("ListEdgeSessions: %+v, %v", l, err)
	}
	if l, err := graph.ListEntitySessions(ctx, d, other); err != nil || len(l) != 0 {
		t.Fatalf("sessions of another dataset: %+v, %v", l, err)
	}
	if mems, err := graph.ListArtifacts(ctx, d, dsID); err != nil || len(mems) != 2 || mems[0].SessionID != "sess-1" || mems[1].SessionID != "" || mems[0].ID != artID || mems[0].Kind != graph.ArtifactEpisode || mems[1].Kind != graph.ArtifactKnowledge || mems[0].Text != "Alice works at Acme" || !mems[0].ValidAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("ListArtifacts: %+v, %v", mems, err)
	}
	if ents, err := graph.ListEntities(ctx, d, dsID); err != nil || len(ents) != 2 || ents[0].Name != "Alice" || ents[0].Description != "an engineer" {
		t.Fatalf("ListEntities: %+v, %v", ents, err)
	}
	if n, err := graph.CountCrossDatasetEdges(ctx, d); err != nil || n != 0 {
		t.Fatalf("CountCrossDatasetEdges: %d, %v", n, err)
	}
}

// TestModelID checks that entities and edges keep the model that inferred them.
func TestModelID(t *testing.T) { dbtest.Run(t, modelIDScenario) }

func modelIDScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	dsID, err := relational.EnsureDataset(ctx, d, fmt.Sprintf("u-%d", time.Now().UnixNano()), "models")
	check(err)
	// The models are referenced by ID, so they must be stored first; an unknown one is refused.
	ma, err := relational.EnsureModel(ctx, d, relational.Model{Name: "model-a"})
	check(err)
	mb, err := relational.EnsureModel(ctx, d, relational.Model{Name: "model-b"})
	check(err)
	if ma != relational.ModelID("model-a") || mb != relational.ModelID("model-b") || ma == mb {
		t.Fatalf("model IDs: %q, %q", ma, mb)
	}
	tx, err := graph.Begin(ctx, d)
	check(err)
	defer tx.Rollback()
	if _, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Ghost", Type: "person", ModelID: "no-such-model"}, "ghost"); err == nil {
		t.Fatal("an entity referencing an unknown model was stored")
	}
	check(tx.Rollback())
	tx, err = graph.Begin(ctx, d)
	check(err)
	defer tx.Rollback()
	now := relational.Now()
	artID, err := tx.InsertArtifact(ctx, dsID, graph.ArtifactKnowledge, "Alice works at Acme", "", now, now)
	check(err)

	alice, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Alice", Type: "person", Description: "an engineer", ModelID: ma}, "alice")
	check(err)
	// A re-mention without a description leaves the stored state, and so its model, alone.
	_, err = tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Alice", Type: "person", ModelID: mb}, "alice")
	check(err)
	if e, err := tx.GetEntity(ctx, dsID, alice); err != nil || e.ModelID != ma || e.Description != "an engineer" {
		t.Fatalf("after bare re-mention: %+v, %v", e, err)
	}
	// A new description replaces the old one and the model that wrote it.
	_, err = tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Alice", Type: "person", Description: "a staff engineer", ModelID: mb}, "alice")
	check(err)
	if e, err := tx.GetEntity(ctx, dsID, alice); err != nil || e.ModelID != mb || e.Description != "a staff engineer" {
		t.Fatalf("after new description: %+v, %v", e, err)
	}
	acme, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Acme", Type: "org", ModelID: ma}, "acme")
	check(err)
	eid, err := tx.InsertEdge(ctx, graph.NewEdge{DatasetID: dsID, SourceID: alice, TargetID: acme, Relation: "works_at",
		Description: "Alice works at Acme.", ValidAt: now, CreatedAt: now, ArtifactID: artID, ModelID: ma})
	check(err)
	if e, err := tx.GetEdge(ctx, dsID, eid); err != nil || e.ModelID != ma {
		t.Fatalf("edge: %+v, %v", e, err)
	}
	check(tx.Commit())

	ents, err := graph.ListEntities(ctx, d, dsID)
	check(err)
	got := map[string]string{}
	for _, e := range ents {
		got[e.Name] = e.ModelID
	}
	if got["Alice"] != mb || got["Acme"] != ma {
		t.Fatalf("listed entity models: %v", got)
	}
	edges, err := graph.ListGraphEdges(ctx, d, dsID)
	check(err)
	if len(edges) != 1 || edges[0].ModelID != ma {
		t.Fatalf("listed edges: %+v", edges)
	}
	around, err := graph.EdgesAround(ctx, d, dsID, []int64{alice}, 10)
	check(err)
	if len(around) != 1 || around[0].ModelID != ma {
		t.Fatalf("edges around: %+v", around)
	}
	byID, err := graph.EntitiesByID(ctx, d, []int64{alice})
	check(err)
	if len(byID) != 1 || byID[0].ModelID != mb {
		t.Fatalf("entities by id: %+v", byID)
	}
}
