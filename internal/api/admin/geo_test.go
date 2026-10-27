package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
