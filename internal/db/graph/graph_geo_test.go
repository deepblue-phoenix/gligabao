package graph_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

// TestGeoStatistics checks the count of geo entities and the ranking by live edges (as source or target, an expired edge and a
// deleted dataset do not count).
func TestGeoStatistics(t *testing.T) { dbtest.Run(t, testGeoStatistics) }

func testGeoStatistics(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := graph.GeoStatistics(ctx, d, 100)
	check(err)

	user := fmt.Sprintf("geo-%d", time.Now().UnixNano())
	dsID, err := relational.EnsureDataset(ctx, d, user, "proj")
	check(err)
	tx, err := graph.Begin(ctx, d)
	check(err)
	defer tx.Rollback()
	now := relational.Now()
	ent := func(name, typ string) int64 {
		id, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: name, Type: typ}, name)
		check(err)
		return id
	}
	artID, err := tx.InsertArtifact(ctx, dsID, graph.ArtifactEpisode, "text", "", now, now)
	check(err)
	paris, rome, alice := ent("Paris", "place"), ent("Rome", "place"), ent("Alice", "person")
	for _, g := range []struct {
		id       int64
		lat, lon float64
	}{{paris, 48.85, 2.35}, {rome, 41.9, 12.5}} {
		check(tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{EntityID: g.id, Kind: "city", Latitude: g.lat, Longitude: g.lon, Provider: "stub", Now: now}))
	}
	edge := func(s, tg int64) int64 {
		id, err := tx.InsertEdge(ctx, graph.NewEdge{DatasetID: dsID, SourceID: s, TargetID: tg, Relation: "r", ValidAt: now, CreatedAt: now, ArtifactID: artID})
		check(err)
		return id
	}
	edge(alice, paris)        // paris: target
	edge(paris, rome)         // paris: source, rome: target
	edge(paris, paris)        // a self-loop counts once
	edge(alice, alice)        // not geo
	gone := edge(rome, alice) // will expire
	check(tx.Commit())
	_, err = d.ExecContext(ctx, `UPDATE gb_edges SET expired_at = valid_at WHERE id = `+fmt.Sprint(gone))
	check(err)

	st, err := graph.GeoStatistics(ctx, d, 100)
	check(err)
	if st.Entities != before.Entities+2 || st.AllEntities != before.AllEntities+3 {
		t.Errorf("counts: %d/%d, before %d/%d", st.Entities, st.AllEntities, before.Entities, before.AllEntities)
	}
	conns := map[int64]int{}
	pos := map[int64]int{}
	for i, c := range st.Top {
		conns[c.EntityID], pos[c.EntityID] = c.Connections, i
		if c.EntityID == paris && (c.EntityName != "Paris" || c.UserID != user || c.Dataset != "proj" || c.Kind != "city" || c.Latitude != 48.85) {
			t.Errorf("paris: %+v", c)
		}
	}
	if conns[paris] != 3 || conns[rome] != 1 {
		t.Errorf("connections: paris %d (want 3), rome %d (want 1)", conns[paris], conns[rome])
	}
	if pos[paris] > pos[rome] {
		t.Errorf("paris must rank above rome: %v", pos)
	}
	for i := 1; i < len(st.Top); i++ {
		if st.Top[i].Connections > st.Top[i-1].Connections {
			t.Fatalf("not sorted at %d", i)
		}
	}
	if lim, err := graph.GeoStatistics(ctx, d, 1); err != nil || len(lim.Top) != 1 {
		t.Errorf("limit: %v, %v", lim, err)
	}

	// A deleted dataset is left out.
	_, err = d.ExecContext(ctx, `UPDATE gb_datasets SET deleted = TRUE WHERE id = `+fmt.Sprint(dsID))
	check(err)
	st, err = graph.GeoStatistics(ctx, d, 100)
	check(err)
	if st.Entities != before.Entities || st.AllEntities != before.AllEntities {
		t.Errorf("after delete: %d/%d, before %d/%d", st.Entities, st.AllEntities, before.Entities, before.AllEntities)
	}
}
