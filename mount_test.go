package gligabao_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao"
)

// TestMount checks that Mount registers gligabao's handlers on the host's mux, next to the host's own routes, without listening.
func TestMount(t *testing.T) {
	cfg := gligabao.DefaultConfig()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("host root")) })
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("host health")) })

	wrapped := 0
	release, err := gligabao.Mount(t.Context(), cfg, gligabao.ServeOptions{DBPath: filepath.Join(t.TempDir(), "gb.db")}, gligabao.MountOptions{
		Mux: mux,
		Wrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wrapped++
				next.ServeHTTP(w, r)
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	// the host keeps its own routes
	if rec := get("/"); rec.Body.String() != "host root" {
		t.Fatalf("/: %q", rec.Body)
	}
	if rec := get("/health"); rec.Body.String() != "host health" {
		t.Fatalf("/health: %q", rec.Body)
	}
	if wrapped != 0 {
		t.Fatalf("wrap ran for host routes: %d", wrapped)
	}
	// gligabao's are under /v1/gb/ and go through Wrap
	if rec := get("/v1/gb/datasets?user_id=u1"); rec.Code != http.StatusOK {
		t.Fatalf("a gligabao route is not mounted: %d", rec.Code)
	}
	if rec := get("/v1/gb/nope"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "404") {
		t.Fatalf("unknown gligabao route: %d %q", rec.Code, rec.Body)
	}
	if wrapped != 2 {
		t.Fatalf("wrap ran %d times, want 2", wrapped)
	}

	release()
	release() // idempotent
	if rec := get("/v1/gb/datasets?user_id=u1"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after release: %d", rec.Code)
	}
}

// TestMountConflict checks that a host route under /v1/gb/ is an error, not a panic.
func TestMountConflict(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/gb/", func(w http.ResponseWriter, r *http.Request) {})
	_, err := gligabao.Mount(t.Context(), nil, gligabao.ServeOptions{DBPath: filepath.Join(t.TempDir(), "gb.db")}, gligabao.MountOptions{Mux: mux})
	if err == nil {
		t.Fatal("want an error")
	}
	if _, err := gligabao.Mount(t.Context(), nil, gligabao.ServeOptions{}, gligabao.MountOptions{}); err == nil {
		t.Fatal("want an error for a nil mux")
	}
}
