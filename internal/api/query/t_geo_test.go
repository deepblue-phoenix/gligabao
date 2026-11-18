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
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil/httputiltest"
)

// TestGeoHandler checks GET /v1/gb/geo: the resolved places of a dataset, and how many are still pending.
func TestGeoHandler(t *testing.T) { dbtest.Run(t, testGeoHandler) }

func testGeoHandler(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	dsID, err := relational.EnsureDataset(ctx,
		d,
		"u4",
		"main")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := relational.Now()
	ent := func(name string) int64 {
		id, err := tx.UpsertEntity(ctx,
			dsID,
			graph.Entity{
				Name: name,
				Type: "place",
			},
			name)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	paris, lyon := ent("Paris"), ent("Lyon")
	if err := tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{
		EntityID:  paris,
		Kind:      "city",
		Latitude:  48.8566,
		Longitude: 2.3522,
		Provider:  "stub",
		Now:       now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.UpsertGeoPending(ctx, graph.NewGeoPending{
		EntityID: lyon,
		Kind:     "city",
		Query:    "Lyon, France",
		Now:      now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	get := func(params string) (int, geoResponse) {
		t.Helper()
		rec := httptest.NewRecorder()
		GeoHandler(d).ServeHTTP(rec, httputiltest.NewRequest(http.MethodGet,
			"/v1/gb/geo?user_id=u4&"+params,
			nil))
		var res geoResponse
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, res
	}
	if code, res := get(""); code != 200 || res.Dataset != "main" || res.Pending != 1 || len(res.Entities) != 1 ||
		res.Entities[0].Name != "Paris" || res.Entities[0].Latitude != 48.8566 || res.Entities[0].Provider != "stub" {
		t.Fatalf("all: %d %+v", code, res)
	}
	if code, res := get("entity_id=" + strconv.FormatInt(paris, 10)); code != 200 || len(res.Entities) != 1 {
		t.Fatalf("one: %d %+v", code, res)
	}
	if code, _ := get("entity_id=" + strconv.FormatInt(lyon, 10)); code != 404 {
		t.Fatalf("an entity that is still pending: %d", code)
	}
	if code, _ := get("dataset=nope"); code != 404 {
		t.Fatalf("unknown dataset: %d", code)
	}
	if code, _ := get("entity_id=x"); code != 400 {
		t.Fatalf("bad entity_id: %d", code)
	}
}
