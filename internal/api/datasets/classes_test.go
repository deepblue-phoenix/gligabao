package datasets_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/datasets"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil/httputiltest"
)

// TestClasses checks GET /v1/gb/datasets/{name}/classes: the classes are counted per artifact over the whole dataset, most frequent first.
func TestClasses(t *testing.T) { dbtest.Run(t, testClasses) }

func testClasses(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ds, err := relational.CreateDataset(ctx, d, userID, "classesds", "", nil)
	must(err)
	other, err := relational.CreateDataset(ctx, d, userID, "otherds", "", nil)
	must(err)
	now := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)
	add := func(dsID int64, classes ...string) {
		tx, err := graph.Begin(ctx, d)
		must(err)
		defer tx.Rollback()
		a, err := tx.InsertArtifact(ctx, dsID, graph.ArtifactEpisode, "text", "", now, now)
		must(err)
		must(tx.Commit())
		_, err = graph.InsertArtifactClasses(ctx, d, dsID, a, 0, classes, now)
		must(err)
	}
	add(ds.ID, "sports", "politics")
	add(ds.ID, "sports")
	add(ds.ID, "sports", "finance")
	add(ds.ID, "finance")
	add(other.ID, "other")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/gb/datasets/{name}/classes", datasets.Classes(d))
	get := func(path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httputiltest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Fatalf("GET %s: status %d, want %d: %s", path, rec.Code, want, rec.Body)
		}
		return rec
	}
	var got struct {
		Dataset string
		Classes []struct {
			Class string
			Count int
		}
	}
	must(json.Unmarshal(get("/v1/gb/datasets/classesds/classes?n=2&user_id="+userID, 200).Body.Bytes(), &got))
	if got.Dataset != "classesds" || len(got.Classes) != 2 ||
		got.Classes[0].Class != "sports" || got.Classes[0].Count != 3 ||
		got.Classes[1].Class != "finance" || got.Classes[1].Count != 2 {
		t.Errorf("top 2 = %+v", got)
	}
	must(json.Unmarshal(get("/v1/gb/datasets/classesds/classes?user_id="+userID, 200).Body.Bytes(), &got))
	if len(got.Classes) != 3 {
		t.Errorf("default n: %+v", got)
	}
	get("/v1/gb/datasets/classesds/classes?n=0&user_id="+userID, 400)
	get("/v1/gb/datasets/classesds/classes?n=x&user_id="+userID, 400)
	get("/v1/gb/datasets/nope/classes?user_id="+userID, 404)
}
