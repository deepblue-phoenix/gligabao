package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
)

// TestOverview checks that the admin overview lists the datasets of every user and counts the whole system.
func TestOverview(t *testing.T) { dbtest.Run(t, testOverview) }

func testOverview(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	for _, c := range []struct{ user, name string }{{"alice", "a"}, {"bob", "b"}} {
		if _, err := memory.CreateDataset(ctx, d, c.user, c.name, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := memory.DeleteDataset(ctx, d, nil, "bob", "b"); err != nil {
		t.Fatal(err)
	}

	get := func(url string) (out struct {
		Datasets []struct {
			UserID, Name string `json:"-"`
			User         string `json:"user_id"`
			DatasetName  string `json:"name"`
			Artifacts    *int   `json:"artifacts"`
		} `json:"datasets"`
		Totals struct{ Artifacts, Entities, Edges *int } `json:"totals"`
	}) {
		t.Helper()
		w := httptest.NewRecorder()
		admin.Overview(d)(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", url, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	live := get("/admin/api/overview")
	if len(live.Datasets) != 1 || live.Datasets[0].User != "alice" || live.Datasets[0].Artifacts == nil {
		t.Fatalf("live: %+v", live.Datasets)
	}
	if live.Totals.Artifacts == nil || live.Totals.Entities == nil || live.Totals.Edges == nil {
		t.Fatalf("totals missing: %+v", live.Totals)
	}
	if all := get("/admin/api/overview?include_deleted=true"); len(all.Datasets) != 2 {
		t.Fatalf("include_deleted: %+v", all.Datasets)
	}
}

// TestGraph checks that the admin graph endpoint returns the graph of a live dataset and 404s otherwise.
func TestGraph(t *testing.T) { dbtest.Run(t, testGraph) }

func testGraph(t *testing.T, d *sql.DB) {
	if _, err := memory.CreateDataset(context.Background(), d, "alice", "a", "", nil); err != nil {
		t.Fatal(err)
	}
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		admin.Graph(d)(w, httptest.NewRequest(http.MethodGet, url, nil))
		return w
	}
	w := get("/admin/api/graph?user_id=alice&dataset=a")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var g struct {
		Entities, Edges []any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil || g.Entities == nil || g.Edges == nil {
		t.Fatalf("bad graph: %v %s", err, w.Body)
	}
	if w := get("/admin/api/graph?user_id=alice&dataset=nope"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown dataset: %d", w.Code)
	}
}

// TestDeleteDataset checks that the admin delete endpoint soft-deletes a dataset and 404s on unknown ones.
func TestDeleteDataset(t *testing.T) { dbtest.Run(t, testDeleteDataset) }

func testDeleteDataset(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	if _, err := memory.CreateDataset(ctx, d, "alice", "a", "", nil); err != nil {
		t.Fatal(err)
	}
	del := func(url string) int {
		w := httptest.NewRecorder()
		admin.DeleteDatasetOf([]admin.Named{{Name: "standard", DB: d}})(w, httptest.NewRequest(http.MethodDelete, url, nil))
		return w.Code
	}
	if c := del("/admin/api/datasets?user_id=alice&dataset=a"); c != http.StatusNoContent {
		t.Fatalf("delete: %d", c)
	}
	if c := del("/admin/api/datasets?user_id=alice&dataset=a"); c != http.StatusNotFound {
		t.Fatalf("delete again: %d", c)
	}
	if c := del("/admin/api/datasets?db=nope&user_id=alice&dataset=a"); c != http.StatusNotFound {
		t.Fatalf("unknown db: %d", c)
	}
	live, err := memory.ListDatasets(ctx, d, "alice", false)
	if err != nil || len(live) != 0 {
		t.Fatalf("live: %v %v", live, err)
	}
	all, err := memory.ListDatasets(ctx, d, "alice", true)
	if err != nil || len(all) != 1 || !all[0].Deleted {
		t.Fatalf("all: %v %v", all, err)
	}
}

// TestVectors checks that the admin vectors endpoint reports every index of a live dataset, and 404s otherwise.
func TestVectors(t *testing.T) { dbtest.Run(t, testVectors) }

func testVectors(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	ds, err := memory.CreateDataset(ctx, d, "alice", "a", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := vector.NewChromem(":memory:", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, memory.EntityDataset(ds.ID), []vector.Vector{{ID: "entity-1", Values: []float32{1, 0}}}); err != nil {
		t.Fatal(err)
	}
	h := admin.VectorsOf([]admin.Named{{Name: "standard", DB: d, Vectors: store}})
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, url, nil))
		return w
	}
	w := get("/admin/api/vectors?user_id=alice&dataset=a")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var out struct {
		Backend string `json:"backend"`
		Indexes []struct {
			Index string `json:"index"`
			Count int    `json:"count"`
			Dims  int    `json:"dimensions"`
		} `json:"indexes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Backend != "chromem" || len(out.Indexes) != len(memory.VectorIndexes) || out.Indexes[0].Index != "entities" || out.Indexes[0].Count != 1 || out.Indexes[1].Count != 0 || out.Indexes[0].Dims != 2 {
		t.Fatalf("%s", w.Body)
	}
	if w := get("/admin/api/vectors?user_id=alice&dataset=nope"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown dataset: %d", w.Code)
	}
	if w := get("/admin/api/vectors?db=nope&user_id=alice&dataset=a"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown db: %d", w.Code)
	}
}
