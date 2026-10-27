package graph_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

// TestGeoIndex checks that a geo entity is in the geo index of the database (the R*Tree of SQLite, the PostGIS column of
// PostgreSQL, when the extension is enabled) once it is stored, that moving it moves it in the index, and that deleting its
// entity removes it.
func TestGeoIndex(t *testing.T) { dbtest.Run(t, testGeoIndex) }

func testGeoIndex(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	postgres := strings.HasSuffix(t.Name(), "/postgres")
	hasIndex := func() bool { // the R*Tree table (SQLite) or the geo column (PostgreSQL) exists
		var n int
		if postgres {
			check(d.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema()
				AND table_name = 'gb_entity_geo' AND column_name = 'geo'`).Scan(&n))
		} else {
			check(d.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = 'gb_entity_geo_rtree'`).Scan(&n))
		}
		return n > 0
	}
	if postgres {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM pg_extension WHERE extname = 'postgis'`).Scan(&n); err != nil || n == 0 {
			t.Skip("PostGIS is not enabled in the test database")
		}
	}
	// Geo off (Open alone): no geo index, and the places are stored all the same.
	if hasIndex() {
		t.Fatal("the geo index exists although geo was not turned on")
	}
	user := fmt.Sprintf("geoidx-%d", time.Now().UnixNano())
	dsID, err := relational.EnsureDataset(ctx, d, user, "proj")
	check(err)
	now := relational.Now()
	{
		tx, err := graph.Begin(ctx, d)
		check(err)
		off, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Before", Type: "place"}, "before")
		check(err)
		check(tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{EntityID: off, Kind: "city", Latitude: 1, Longitude: 2, Provider: "stub", Now: now}))
		check(tx.Commit())
	}
	ok, err := graph.EnableGeoIndex(ctx, d)
	check(err)
	if !ok || !hasIndex() {
		t.Fatalf("EnableGeoIndex: ok=%v, index=%v", ok, hasIndex())
	}
	if ok, err := graph.EnableGeoIndex(ctx, d); err != nil || !ok { // idempotent
		t.Fatalf("EnableGeoIndex again: %v, %v", ok, err)
	}
	if !postgres { // no backfill on SQLite: the place stored while geo was off is not indexed
		var n int
		check(d.QueryRowContext(ctx, `SELECT count(*) FROM gb_entity_geo_rtree`).Scan(&n))
		if n != 0 {
			t.Fatalf("rtree rows after enabling = %d, want 0 (no backfill)", n)
		}
	}
	// inBox counts the indexed entities within the box (lat/lon, in degrees) around a point.
	inBox := func(id int64, lat, lon, deg float64) int {
		t.Helper()
		var n int
		if postgres {
			check(d.QueryRowContext(ctx, `SELECT count(*) FROM gb_entity_geo WHERE entity_id = $1
				AND ST_DWithin(geo, ST_SetSRID(ST_MakePoint($3, $2), 4326)::geography, $4)`, id, lat, lon, deg*111000).Scan(&n))
		} else {
			check(d.QueryRowContext(ctx, `SELECT count(*) FROM gb_entity_geo_rtree WHERE entity_id = ?
				AND min_lat >= ? AND max_lat <= ? AND min_lon >= ? AND max_lon <= ?`, id, lat-deg, lat+deg, lon-deg, lon+deg).Scan(&n))
		}
		return n
	}

	var id int64
	put := func(lat, lon float64) {
		t.Helper()
		tx, err := graph.Begin(ctx, d)
		check(err)
		defer tx.Rollback()
		if id == 0 {
			id, err = tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Paris", Type: "place"}, "paris")
			check(err)
		}
		check(tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{EntityID: id, Kind: "city", Latitude: lat, Longitude: lon, Provider: "stub", Now: now}))
		check(tx.Commit())
	}
	put(48.85, 2.35)
	if inBox(id, 48.85, 2.35, 0.1) != 1 || inBox(id, 41.9, 12.5, 0.1) != 0 {
		t.Fatal("a stored place is not in the geo index")
	}
	put(41.9, 12.5) // the upsert of the same entity moves it
	if inBox(id, 48.85, 2.35, 0.1) != 0 || inBox(id, 41.9, 12.5, 0.1) != 1 {
		t.Fatal("a moved place is not moved in the geo index")
	}

	if !postgres {
		var n int
		check(d.QueryRowContext(ctx, `SELECT count(*) FROM gb_entity_geo_rtree`).Scan(&n))
		if n != 1 { // only the place stored after geo was turned on
			t.Fatalf("rtree rows = %d, want 1", n)
		}
		_, err = d.ExecContext(ctx, `DELETE FROM gb_entities WHERE id = ?`, id) // cascades to gb_entity_geo
		check(err)
		check(d.QueryRowContext(ctx, `SELECT count(*) FROM gb_entity_geo_rtree`).Scan(&n))
		if n != 0 {
			t.Fatalf("rtree rows after the entity was deleted = %d, want 0", n)
		}
	}
}
