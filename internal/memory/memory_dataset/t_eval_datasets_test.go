package memory_dataset_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/datasets"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_dataset"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil/httputiltest"
)

// TestEvalDatasets: an eval dataset (kind 'eval') is invisible to every regular dataset function and route, the same name can exist as a
// regular dataset next to it, the eval context resolves only the eval dataset of its run, and the cleanup deletes it.
func TestEvalDatasets(t *testing.T) { dbtest.Run(t, testEvalDatasets) }

func testEvalDatasets(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	const user = "eval-hide-user"
	const name = "r7-case"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ds, err := memory_dataset.EnsureEvalDataset(ctx,
		d,
		user,
		name,
		7)
	must(err)
	if ds.Kind != relational.KindEval || ds.EvalRunID == nil || *ds.EvalRunID != 7 {
		t.Fatalf("eval dataset: %+v", ds)
	}
	again, err := memory_dataset.EnsureEvalDataset(ctx,
		d,
		user,
		name,
		7)
	must(err)
	if again.ID != ds.ID {
		t.Fatalf("ensure twice: %d, %d", ds.ID, again.ID)
	}

	// Regular functions never see it.
	list, err := memory_dataset.ListDatasets(ctx,
		d,
		user,
		true)
	must(err)
	if len(list) != 0 {
		t.Fatalf("regular list: %+v", list)
	}
	if _, err := relational.DatasetID(ctx,
		d,
		user,
		name); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("regular DatasetID: %v", err)
	}
	if err := memory_dataset.DeleteDataset(ctx,
		d,
		nil,
		user,
		name); !errors.Is(err, memory_core.ErrDatasetNotFound) {
		t.Fatalf("regular delete: %v", err)
	}
	all, err := relational.ListAllDatasets(ctx,
		d,
		true)
	must(err)
	for _, o := range all {
		if o.ID == ds.ID {
			t.Fatal("ListAllDatasets lists an eval dataset")
		}
	}
	anyKind, err := relational.ListAllDatasetsAnyKind(ctx,
		d,
		false)
	must(err)
	found := false
	for _, o := range anyKind {
		found = found || o.ID == ds.ID
	}
	if !found {
		t.Fatal("ListAllDatasetsAnyKind misses the eval dataset")
	}

	// The API: not listed, delete is 404.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/gb/datasets", datasets.List(d))
	mux.HandleFunc("DELETE /v1/gb/datasets/{name}", datasets.Delete(d, nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httputiltest.NewRequest("GET",
		"/v1/gb/datasets?user_id="+user,
		nil))
	var out struct {
		Datasets []relational.Dataset `json:"datasets"`
	}
	must(json.Unmarshal(rec.Body.Bytes(), &out))
	if rec.Code != 200 || len(out.Datasets) != 0 {
		t.Fatalf("GET /v1/gb/datasets: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httputiltest.NewRequest("DELETE",
		"/v1/gb/datasets/"+name+"?user_id="+user,
		nil))
	if rec.Code != 404 {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body)
	}

	// A regular remember into that name makes a new 'data' dataset.
	regularID, err := memory_core.EnsureDataset(ctx,
		d,
		user,
		name)
	must(err)
	if regularID == ds.ID {
		t.Fatal("the regular dataset is the eval one")
	}

	// The eval context resolves the eval dataset of its run only.
	evalID, err := memory_core.DatasetID(memory_dataset.WithEval(ctx, 7),
		d,
		user,
		name)
	must(err)
	if evalID != ds.ID {
		t.Fatalf("eval context: %d, want %d", evalID, ds.ID)
	}
	if _, err := memory_core.DatasetID(memory_dataset.WithEval(ctx, 8),
		d,
		user,
		name); !errors.Is(err, memory_core.ErrDatasetNotFound) {
		t.Fatalf("other run: %v", err)
	}
	if id, err := memory_core.DatasetID(ctx,
		d,
		user,
		name); err != nil || id != regularID {
		t.Fatalf("regular context: %d %v", id, err)
	}
	if id, err := memory_core.EnsureDataset(memory_dataset.WithEval(ctx, 9),
		d,
		user,
		"r9-x"); err != nil || id == 0 {
		t.Fatalf("ensure in an eval context: %d %v", id, err)
	}
	if _, err := relational.DatasetID(ctx,
		d,
		user,
		"r9-x"); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("an eval-context ensure made a regular dataset: %v", err)
	}

	// Cleanup.
	n, err := memory_dataset.DeleteEvalDatasets(ctx,
		d,
		nil,
		user,
		7)
	must(err)
	if n != 1 {
		t.Fatalf("deleted %d, want 1", n)
	}
	if _, err := memory_core.DatasetID(memory_dataset.WithEval(ctx, 7),
		d,
		user,
		name); !errors.Is(err, memory_core.ErrDatasetNotFound) {
		t.Fatalf("after cleanup: %v", err)
	}
	if id, err := relational.DatasetID(ctx,
		d,
		user,
		name); err != nil || id != regularID {
		t.Fatalf("cleanup touched the regular dataset: %d %v", id, err)
	}
}
