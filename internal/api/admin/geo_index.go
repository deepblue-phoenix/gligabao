package admin

import (
	"context"
	"database/sql"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

// geoIndexTimeout bounds the checks of GeoIndex.
const geoIndexTimeout = 5 * time.Second

// GeoIndexStatus says whether a database can index coordinates, which is what proximity queries need: SQLite needs the R*Tree
// module ("rtree"), PostgreSQL the PostGIS extension ("postgis").
type GeoIndexStatus struct {
	// Backend is config.DBBackendSQLite or config.DBBackendPostgres ("" if the driver is not recognised).
	Backend string `json:"backend"`
	// Enabled is true when the geo index is usable: rtree compiled into SQLite, or PostGIS installed in the database.
	Enabled bool `json:"enabled"`
	// Index names the index technology of the backend: "rtree" or "postgis".
	Index string `json:"index,omitempty"`
	// Version is the PostGIS version, or the SQLite version for rtree.
	Version string `json:"version,omitempty"`
	// Available (postgres only) is true when PostGIS is installed on the server but not enabled in this database
	// (CREATE EXTENSION postgis).
	Available bool `json:"available,omitempty"`
	// Detail explains a disabled state in a sentence.
	Detail string `json:"detail,omitempty"`
}

// backendOf names the backend of d from its driver's package, "" if unknown.
func backendOf(d *sql.DB) string {
	t := reflect.TypeOf(d.Driver())
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return ""
	}
	switch p := t.PkgPath(); {
	case strings.Contains(p, "sqlite"):
		return config.DBBackendSQLite
	case strings.Contains(p, "pgx"), strings.Contains(p, "lib/pq"):
		return config.DBBackendPostgres
	}
	return ""
}

// CheckGeoIndex probes d: SQLite gets a throwaway R*Tree table (in the temp schema of one connection, never in the
// database file), PostgreSQL is asked for the postgis extension. It never returns an error: a probe that fails is
// reported as not enabled, with the reason in Detail.
func CheckGeoIndex(ctx context.Context, d *sql.DB) GeoIndexStatus {
	switch b := backendOf(d); b {
	case config.DBBackendSQLite:
		return checkRTree(ctx, d)
	case config.DBBackendPostgres:
		return checkPostGIS(ctx, d)
	default:
		return GeoIndexStatus{Backend: b, Detail: "unknown database driver"}
	}
}

func checkRTree(ctx context.Context, d *sql.DB) GeoIndexStatus {
	st := GeoIndexStatus{Backend: config.DBBackendSQLite, Index: "rtree"}
	conn, err := d.Conn(ctx)
	if err != nil {
		st.Detail = err.Error()
		return st
	}
	defer conn.Close()
	// temp tables live and die with this connection; the main database is not touched.
	if _, err := conn.ExecContext(ctx, `CREATE VIRTUAL TABLE temp.gb_rtree_probe USING rtree(id, min_lat, max_lat, min_lon, max_lon)`); err != nil {
		st.Detail = "this SQLite build has no R*Tree module: " + err.Error()
		return st
	}
	conn.ExecContext(ctx, `DROP TABLE temp.gb_rtree_probe`)
	st.Enabled = true
	conn.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&st.Version)
	return st
}

func checkPostGIS(ctx context.Context, d *sql.DB) GeoIndexStatus {
	st := GeoIndexStatus{Backend: config.DBBackendPostgres, Index: "postgis"}
	var version string
	err := d.QueryRowContext(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'postgis'`).Scan(&version)
	switch {
	case err == nil:
		st.Enabled, st.Version = true, version
	case err == sql.ErrNoRows:
		if d.QueryRowContext(ctx, `SELECT 1 FROM pg_available_extensions WHERE name = 'postgis'`).Scan(new(int)) == nil {
			st.Available = true
			st.Detail = "PostGIS is installed on the server but not enabled in this database: run CREATE EXTENSION postgis"
		} else {
			st.Detail = "PostGIS is not installed on the PostgreSQL server"
		}
	default:
		st.Detail = err.Error()
	}
	return st
}

// GeoIndexOf serves GET /admin/api/geo-index[?db=<name>] for one of dbs (the first without db): a GeoIndexStatus, always with
// status 200 (a failed probe is "enabled": false and a detail). It is a live check, not cached.
func GeoIndexOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		for _, n := range dbs {
			if n.Name == name {
				ctx, cancel := context.WithTimeout(r.Context(), geoIndexTimeout)
				defer cancel()
				httputil.WriteJSON(w, http.StatusOK, CheckGeoIndex(ctx, n.DB))
				return
			}
		}
		httputil.Error(w, http.StatusNotFound, "unknown database")
	}
}
