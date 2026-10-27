package admin_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// TestGeoIndex checks the geo index probe: SQLite always has R*Tree (modernc.org/sqlite compiles it in) and the probe leaves no
// table behind; PostgreSQL reports whether PostGIS is enabled (either answer is valid, but it must be consistent).
func TestGeoIndex(t *testing.T) { dbtest.Run(t, testGeoIndex) }

func testGeoIndex(t *testing.T, d *sql.DB) {
	h := admin.GeoIndexOf([]admin.Named{{Name: "standard", DB: d}})
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/admin/api/geo-index", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	var st admin.GeoIndexStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", st)
	switch st.Backend {
	case config.DBBackendSQLite:
		if !st.Enabled || st.Index != "rtree" || st.Version == "" {
			t.Errorf("sqlite: %+v", st)
		}
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name LIKE 'gb_rtree_probe%'`).Scan(&n); err != nil || n != 0 {
			t.Errorf("probe left tables behind: %d, %v", n, err)
		}
	case config.DBBackendPostgres:
		if st.Index != "postgis" || (st.Enabled && st.Version == "") || (!st.Enabled && st.Detail == "") {
			t.Errorf("postgres: %+v", st)
		}
	default:
		t.Errorf("backend %q", st.Backend)
	}

	w = httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/admin/api/geo-index?db=nope", nil))
	if w.Code != 404 {
		t.Errorf("unknown db: status %d", w.Code)
	}
}
