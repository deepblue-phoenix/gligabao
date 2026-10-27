package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

// TestGeo checks GET /admin/api/geo: the count of geo entities and the most connected ones.
func TestGeo(t *testing.T) { dbtest.Run(t, testGeo) }

func testGeo(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	h := admin.GeoOf([]admin.Named{{Name: "standard", DB: d}})
	get := func(url string) (int, admin.GeoView) {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, url, nil))
		var v admin.GeoView
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, v
	}
	if code, v := get("/admin/api/geo"); code != 200 || v.Entities != 0 || v.Top == nil || len(v.Top) != 0 {
		t.Fatalf("empty database: %d %+v", code, v)
	}

	dsID, err := relational.EnsureDataset(ctx, d, "u1", "main")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := relational.Now()
	art, _ := tx.InsertArtifact(ctx, dsID, graph.ArtifactEpisode, "x", "", now, now)
	paris, _ := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Paris", Type: "place"}, "paris")
	bob, _ := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Bob", Type: "person"}, "bob")
	if err := tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{EntityID: paris, Kind: "city", Latitude: 48.85, Longitude: 2.35,
		Country: "France", BBox: &[4]float64{48.8, 2.2, 48.9, 2.5}, Provider: "stub", Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.InsertEdge(ctx, graph.NewEdge{DatasetID: dsID, SourceID: bob, TargetID: paris, Relation: "visited", ValidAt: now, CreatedAt: now, ArtifactID: art}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	code, v := get("/admin/api/geo")
	if code != 200 || v.Entities != 1 || v.AllEntities != 2 || len(v.Top) != 1 {
		t.Fatalf("%d %+v", code, v)
	}
	if g := v.Top[0]; g.Name != "Paris" || g.Connections != 1 || g.UserID != "u1" || g.Dataset != "main" || g.Latitude != 48.85 ||
		g.Country != "France" || g.BBox == nil || g.EntityID != paris {
		t.Errorf("top: %+v", g)
	}
	if code, _ := get("/admin/api/geo?db=nope"); code != 404 {
		t.Errorf("unknown db: %d", code)
	}
}

// TestGeoNearby checks GET /admin/api/geo/nearby: the same selection as query, on the database named by db.
func TestGeoNearby(t *testing.T) { dbtest.Run(t, testGeoNearby) }

func testGeoNearby(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dsID, err := relational.EnsureDataset(ctx, d, "u1", "main")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := relational.Now()
	var ids []int64
	for _, p := range []struct {
		name     string
		lat, lon float64
	}{{"Paris", 48.8566, 2.3522}, {"Versailles", 48.8049, 2.1204}} {
		id, _ := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: p.name, Type: "place"}, p.name)
		if err := tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{EntityID: id, Kind: "city", Latitude: p.lat, Longitude: p.lon, Provider: "stub", Now: now}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	h := admin.GeoNearbyOf([]admin.Named{{Name: "standard", DB: d}}, 0)
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, url, nil))
		return w
	}
	w := get("/admin/api/geo/nearby?db=standard&user_id=u1&dataset=main&entity_id=" + strconv.FormatInt(ids[0], 10))
	var res struct {
		RadiusKm float64 `json:"radius_km"`
		Nearby   []struct {
			Name       string  `json:"name"`
			DistanceKm float64 `json:"distance_km"`
		} `json:"nearby"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != 200 || res.RadiusKm != 50 || len(res.Nearby) != 1 || res.Nearby[0].Name != "Versailles" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := get("/admin/api/geo/nearby?db=nope&user_id=u1&entity_id=1"); w.Code != 404 {
		t.Fatalf("unknown database: %d", w.Code)
	}
}
