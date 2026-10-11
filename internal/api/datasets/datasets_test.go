package datasets_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/philippgille/chromem-go"

	"github.com/deepblue-phoenix/gligabao/internal/api/datasets"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
)

const (
	userID  = "lifecycle-user"
	dsName  = "lifecycle"
	vecDims = 3
)

// TestDatasetLifecycle walks one dataset through its whole life against every backend: create it, check it is in the
// relational DB, put graph and vector data into it, check that data is there, delete it, and check that the
// relational DB, the graph DB and the vector DB all reflect the deletion.
func TestDatasetLifecycle(t *testing.T) { dbtest.Run(t, testDatasetLifecycle) }

func testDatasetLifecycle(t *testing.T, d *sql.DB) {
	ctx := context.Background()

	// A persistent vector store, so that the collections can be listed independently of the store under test by
	// opening the same directory with chromem itself.
	vecDir := t.TempDir()
	vectors, err := vector.NewChromem(vecDir, vecDims)
	if err != nil {
		t.Fatal(err)
	}
	collections := func() map[string]*chromem.Collection {
		t.Helper()
		cdb, err := chromem.NewPersistentDB(vecDir, false)
		if err != nil {
			t.Fatal(err)
		}
		return cdb.ListCollections()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/datasets", datasets.Create(d))
	mux.HandleFunc("GET /v1/gb/datasets", datasets.List(d))
	mux.HandleFunc("DELETE /v1/gb/datasets/{name}", datasets.Delete(d, vectors))
	mux.HandleFunc("GET /v1/gb/datasets/{name}/graph", datasets.Graph(d))

	do := func(method, path, body string, wantCode int) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rec.Code != wantCode {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, wantCode, rec.Body)
		}
		return rec
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// find returns the user's dataset called dsName (live, or also deleted ones), or nil.
	find := func(includeDeleted bool) *relational.Dataset {
		t.Helper()
		list, err := relational.ListDatasets(ctx, d, userID, includeDeleted)
		must(err)
		for i := range list {
			if list[i].Name == dsName {
				return &list[i]
			}
		}
		return nil
	}
	// exportGraph reads the graph through the API.
	type graphOut struct {
		Entities []struct{ Name, Type string }
		Edges    []struct{ Source, Target, Relation string }
	}
	exportGraph := func() graphOut {
		t.Helper()
		var g graphOut
		must(json.Unmarshal(do("GET", "/v1/gb/datasets/"+dsName+"/graph?user_id="+userID, "", 200).Body.Bytes(), &g))
		return g
	}

	//-----------------------------------------------------------------------------------------
	// 1. Create the dataset; it must show up in the relational DB (and the list endpoint).

	if find(true) != nil {
		t.Fatal("dataset exists before it was created")
	}
	do("POST", "/v1/gb/datasets", `{"user_id":"`+userID+`","name":"`+dsName+`","description":"lifecycle test","tags":["t1","t2"]}`, 201)

	ds := find(false)
	if ds == nil {
		t.Fatal("created dataset is not in the relational DB")
	}
	if ds.ID == 0 || ds.UserID != userID || ds.Description != "lifecycle test" || strings.Join(ds.Tags, ",") != "t1,t2" ||
		ds.Deleted || ds.DeletedAt != nil || ds.CreatedAt.IsZero() {
		t.Fatalf("stored dataset = %+v", *ds)
	}
	id, err := relational.DatasetID(ctx, d, userID, dsName)
	must(err)
	if id != ds.ID {
		t.Errorf("DatasetID = %d, want %d", id, ds.ID)
	}
	var listed struct {
		Datasets []relational.Dataset `json:"datasets"`
	}
	must(json.Unmarshal(do("GET", "/v1/gb/datasets?user_id="+userID, "", 200).Body.Bytes(), &listed))
	if len(listed.Datasets) != 1 || listed.Datasets[0].ID != id {
		t.Errorf("list endpoint = %+v", listed.Datasets)
	}

	// Nothing in the graph or vector DB yet.
	if c, err := graph.DatasetCounts(ctx, d, id); err != nil || c != (graph.Counts{}) {
		t.Errorf("new dataset graph counts = %+v (err %v), want empty", c, err)
	}
	if g := exportGraph(); len(g.Entities) != 0 || len(g.Edges) != 0 {
		t.Errorf("new dataset graph = %+v, want empty", g)
	}
	ns := memory.VectorNamespace(id)
	if _, ok := collections()[ns]; ok {
		t.Fatalf("vector collection %q exists before any vector was stored", ns)
	}

	//-----------------------------------------------------------------------------------------
	// 2. Insert graph data (1 memory, 2 entities, 1 edge) and vectors; both must be reflected.

	now := time.Now().UTC().Truncate(time.Second)
	tx, err := graph.Begin(ctx, d)
	must(err)
	defer tx.Rollback()
	artID, err := tx.InsertArtifact(ctx, id, graph.ArtifactEpisode, "Alice works at Acme.", now, now)
	must(err)
	alice, err := tx.UpsertEntity(ctx, id, graph.Entity{Name: "Alice", Type: "person"}, "alice")
	must(err)
	acme, err := tx.UpsertEntity(ctx, id, graph.Entity{Name: "Acme", Type: "organization"}, "acme")
	must(err)
	_, err = tx.InsertEdge(ctx, graph.NewEdge{
		DatasetID: id, SourceID: alice, TargetID: acme, Relation: "works_at", Description: "Alice works at Acme.",
		ValidAt: now, CreatedAt: now, ArtifactID: artID,
	})
	must(err)
	must(tx.Commit())

	if c, err := graph.DatasetCounts(ctx, d, id); err != nil || c != (graph.Counts{Artifacts: 1, Entities: 2, Edges: 1}) {
		t.Errorf("graph counts = %+v (err %v), want 1 memory, 2 entities, 1 edge", c, err)
	}
	g := exportGraph()
	if len(g.Entities) != 2 || len(g.Edges) != 1 || g.Edges[0].Source != "Alice" || g.Edges[0].Target != "Acme" || g.Edges[0].Relation != "works_at" {
		t.Errorf("exported graph = %+v", g)
	}

	must(vectors.Upsert(ctx, ns, []vector.Vector{
		{ID: "alice", Values: []float32{1, 0, 0}, Metadata: map[string]any{"kind": "entity"}},
		{ID: "acme", Values: []float32{0, 1, 0}, Metadata: map[string]any{"kind": "entity"}},
	}))
	got, err := vectors.Query(ctx, ns, []float32{1, 0.1, 0}, vector.QueryOptions{})
	must(err)
	if len(got) != 2 || got[0].ID != "alice" || got[1].ID != "acme" || got[0].Metadata["kind"] != "entity" {
		t.Errorf("vector query = %+v, want alice then acme", got)
	}
	col, ok := collections()[ns]
	if !ok {
		t.Fatalf("vector collection %q is missing from the vector DB", ns)
	}
	if col.Count() != 2 {
		t.Errorf("vector collection has %d vectors, want 2", col.Count())
	}

	//-----------------------------------------------------------------------------------------
	// 3. Delete the dataset through the API; check every database.

	do("DELETE", "/v1/gb/datasets/"+dsName+"?user_id="+userID, "", 204)

	// Relational: no live dataset any more, but the row is kept, marked deleted.
	if find(false) != nil {
		t.Error("deleted dataset is still listed as live in the relational DB")
	}
	if _, err := relational.DatasetID(ctx, d, userID, dsName); err == nil {
		t.Error("DatasetID still resolves the deleted dataset")
	}
	if gone := find(true); gone == nil || gone.ID != id || !gone.Deleted || gone.DeletedAt == nil {
		t.Errorf("deleted dataset row = %+v, want kept with deleted = true and deleted_at set", gone)
	}
	must(json.Unmarshal(do("GET", "/v1/gb/datasets?user_id="+userID, "", 200).Body.Bytes(), &listed))
	if len(listed.Datasets) != 0 {
		t.Errorf("list endpoint still returns %+v", listed.Datasets)
	}
	do("DELETE", "/v1/gb/datasets/"+dsName+"?user_id="+userID, "", 404) // already deleted

	// Graph: deletion is soft, so the rows stay in storage, but the dataset can no longer be read through the API.
	if c, err := graph.DatasetCounts(ctx, d, id); err != nil || c != (graph.Counts{Artifacts: 1, Entities: 2, Edges: 1}) {
		t.Errorf("graph counts after delete = %+v (err %v), want the rows kept", c, err)
	}
	do("GET", "/v1/gb/datasets/"+dsName+"/graph?user_id="+userID, "", 404)

	// Vector: the collection is gone, both from the store and from the files on disk.
	if _, ok := collections()[ns]; ok {
		t.Errorf("vector collection %q still exists after delete", ns)
	}
	if got, err := vectors.Query(ctx, ns, []float32{1, 0, 0}, vector.QueryOptions{}); err != nil || len(got) != 0 {
		t.Errorf("query of deleted namespace = %+v (err %v), want nothing", got, err)
	}
	if n := len(collections()); n != 0 {
		t.Errorf("vector DB has %d collections, want 0", n)
	}

	//-----------------------------------------------------------------------------------------
	// 4. The name is free again: a new dataset gets a new ID, an empty graph and a fresh, separate namespace.

	do("POST", "/v1/gb/datasets", `{"user_id":"`+userID+`","name":"`+dsName+`"}`, 201)
	again := find(false)
	if again == nil || again.ID == id {
		t.Fatalf("recreated dataset = %+v, want a new ID (old %d)", again, id)
	}
	if c, err := graph.DatasetCounts(ctx, d, again.ID); err != nil || c != (graph.Counts{}) {
		t.Errorf("recreated dataset graph counts = %+v (err %v), want empty", c, err)
	}
	if g := exportGraph(); len(g.Entities) != 0 || len(g.Edges) != 0 {
		t.Errorf("recreated dataset graph = %+v, want empty", g)
	}
	if _, ok := collections()[memory.VectorNamespace(again.ID)]; ok {
		t.Error("recreated dataset already has a vector collection")
	}
}
