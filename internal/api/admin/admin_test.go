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
