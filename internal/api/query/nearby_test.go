package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
)

// TestNearbyHandler checks GET /v1/gb/geo/nearby: the geo entities around a geo entity or a point, within the radius.
func TestNearbyHandler(t *testing.T) { dbtest.Run(t, testNearbyHandler) }

func testNearbyHandler(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dsID, err := relational.EnsureDataset(ctx, d, "u3", "main")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := relational.Now()
	ent := func(name, typ string, lat, lon float64) int64 {
		id, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: name, Type: typ}, name)
		if err != nil {
			t.Fatal(err)
		}
		if typ == "place" {
			if err := tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{EntityID: id, Kind: "city", Latitude: lat, Longitude: lon, Provider: "stub", Now: now}); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	paris := ent("Paris", "place", 48.8566, 2.3522)
	ent("Versailles", "place", 48.8049, 2.1204) // 17 km
	ent("Reims", "place", 49.2583, 4.0317)      // 130 km
	bob := ent("Bob", "person", 0, 0)           // no coordinates
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	h := NearbyHandler(d)
	get := func(params string) (int, memory_query.GeoContext) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/gb/geo/nearby?user_id=u3&"+params, nil))
		var res memory_query.GeoContext
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, res
	}
	id := strconv.FormatInt(paris, 10)

	code, res := get("entity_id=" + id)
	if code != 200 || res.RadiusKm != memory_core.DefaultGeoRadiusKm || len(res.Anchors) != 1 || res.Anchors[0].Name != "Paris" ||
		len(res.Nearby) != 1 || res.Nearby[0].Name != "Versailles" || res.Nearby[0].Anchor != "Paris" {
		t.Fatalf("default radius: %d %+v", code, res)
	}
	if code, res := get("entity_id=" + id + "&radius_km=200"); code != 200 || len(res.Nearby) != 2 || res.Nearby[1].Name != "Reims" {
		t.Fatalf("radius 200: %d %+v", code, res)
	}
	if code, res := get("latitude=48.8566&longitude=2.3522&radius_km=20"); code != 200 || len(res.Nearby) != 2 || res.Nearby[0].Name != "Paris" {
		t.Fatalf("point (Paris itself is listed, it is not an anchor entity): %d %+v", code, res)
	}
	if code, _ := get("entity_id=" + strconv.FormatInt(bob, 10)); code != 404 {
		t.Fatalf("entity without coordinates: %d", code)
	}
	if code, _ := get("entity_id=99999"); code != 404 {
		t.Fatalf("unknown entity: %d", code)
	}
	if code, _ := get("dataset=nope&entity_id=" + id); code != 404 {
		t.Fatalf("unknown dataset: %d", code)
	}
	for _, bad := range []string{"", "entity_id=x", "entity_id=" + id + "&radius_km=0", "entity_id=" + id + "&radius_km=abc",
		"entity_id=" + id + "&radius_km=30000", "latitude=95&longitude=0", "latitude=1"} {
		if code, _ := get(bad); code != 400 {
			t.Errorf("%q: %d, want 400", bad, code)
		}
	}
	// the server's default radius
	rec := httptest.NewRecorder()
	NearbyHandler(d, memory_core.WithGeoRadius(5)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x?user_id=u3&entity_id="+id, nil))
	var small memory_query.GeoContext
	if err := json.Unmarshal(rec.Body.Bytes(), &small); err != nil || small.RadiusKm != 5 || len(small.Nearby) != 0 || small.Nearby == nil {
		t.Fatalf("server radius: %s", rec.Body)
	}
}
