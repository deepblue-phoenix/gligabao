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

// TestNearbyEntityGeo checks the proximity query: radius, order, limit, exclusion, the dataset, the antimeridian and the poles,
// with the geo index and without it (the results are the same).
func TestNearbyEntityGeo(t *testing.T) { dbtest.Run(t, testNearbyEntityGeo) }

func testNearbyEntityGeo(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := fmt.Sprintf("near-%d", time.Now().UnixNano())
	dsID, err := relational.EnsureDataset(ctx, d, user, "proj")
	check(err)
	otherDS, err := relational.EnsureDataset(ctx, d, user, "other")
	check(err)
	now := relational.Now()

	ids := map[string]int64{}
	tx, err := graph.Begin(ctx, d)
	check(err)
	defer tx.Rollback()
	put := func(ds int64, name string, lat, lon float64) {
		id, err := tx.UpsertEntity(ctx, ds, graph.Entity{Name: name, Type: "place"}, name)
		check(err)
		check(tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{EntityID: id, Kind: "city", Latitude: lat, Longitude: lon, Provider: "stub", Now: now}))
		ids[name] = id
	}
	put(dsID, "Paris", 48.8566, 2.3522)
	put(dsID, "Versailles", 48.8049, 2.1204) // 17 km from Paris
	put(dsID, "Reims", 49.2583, 4.0317)      // 130 km
	put(dsID, "London", 51.5074, -0.1278)    // 344 km
	put(dsID, "Rome", 41.9028, 12.4964)      // 1100 km
	put(dsID, "Tokyo", 35.6762, 139.6503)
	put(dsID, "Suva", -18.1416, 178.4419) // Fiji, just west of the antimeridian
	put(dsID, "Taveuni", -16.85, -179.98) // just east of it, 150 km from Suva
	put(dsID, "NorthPole", 90, 0)
	put(dsID, "Alert", 82.5, -62.3)        // 800 km from the pole
	put(dsID, "PoleOtherSide", 89.9, 180)  // 11 km from the pole
	put(otherDS, "Elsewhere", 48.86, 2.36) // right by Paris, in another dataset
	check(tx.Commit())

	names := func(res []graph.NearbyGeo) []string {
		var out []string
		for _, r := range res {
			out = append(out, r.EntityName)
		}
		return out
	}
	eq := func(got []graph.NearbyGeo, want ...string) {
		t.Helper()
		g := names(got)
		if fmt.Sprint(g) != fmt.Sprint(want) {
			t.Errorf("got %v, want %v", g, want)
		}
	}
	run := func(t *testing.T) {
		near := func(name string, radius float64, limit int, exclude ...int64) []graph.NearbyGeo {
			t.Helper()
			var lat, lon float64
			g, err := graph.GetEntityGeo(ctx, d, ids[name])
			check(err)
			lat, lon = g.Latitude, g.Longitude
			res, err := graph.NearbyEntityGeo(ctx, d, dsID, lat, lon, radius, limit, exclude...)
			check(err)
			return res
		}
		eq(near("Paris", 20, 0), "Paris", "Versailles")
		eq(near("Paris", 20, 0, ids["Paris"]), "Versailles")
		eq(near("Paris", 200, 0, ids["Paris"]), "Versailles", "Reims")
		eq(near("Paris", 500, 2), "Paris", "Versailles")
		eq(near("Paris", 1, 0), "Paris")
		eq(near("Paris", 1500, 0), "Paris", "Versailles", "Reims", "London", "Rome")
		if res := near("Paris", 20, 0); res[1].DistanceKm < 16 || res[1].DistanceKm > 18 || res[1].Latitude != 48.8049 {
			t.Errorf("versailles: %+v", res[1])
		}
		eq(near("Suva", 300, 0, ids["Suva"]), "Taveuni")    // across the antimeridian
		eq(near("Taveuni", 300, 0, ids["Taveuni"]), "Suva") // and back
		eq(near("NorthPole", 100, 0, ids["NorthPole"]), "PoleOtherSide")
		eq(near("NorthPole", 1000, 0, ids["NorthPole"]), "PoleOtherSide", "Alert")
		// a radius as large as the Earth finds everything of the dataset
		if res := near("Paris", 20000, 0); len(res) != 11 {
			t.Errorf("whole Earth: %v", names(res))
		}
	}
	t.Run("without index", run)
	if ok, err := graph.EnableGeoIndex(ctx, d); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Skip("no geo index in this database (PostGIS not enabled)")
	}
	// The rows stored before the index have to be in it (PostgreSQL fills it in; SQLite does not backfill, so store them again).
	tx, err = graph.Begin(ctx, d)
	check(err)
	defer tx.Rollback()
	all, err := graph.ListEntityGeo(ctx, d, dsID)
	check(err)
	for _, g := range all {
		check(tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{EntityID: g.EntityID, Kind: g.Kind, Latitude: g.Latitude, Longitude: g.Longitude, Provider: "stub", Now: now}))
	}
	check(tx.Commit())
	t.Run("with index", run)

	for _, bad := range []struct{ lat, lon, r float64 }{{91, 0, 1}, {0, 181, 1}, {0, 0, 0}, {0, 0, -1}} {
		if _, err := graph.NearbyEntityGeo(ctx, d, dsID, bad.lat, bad.lon, bad.r, 0); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}
