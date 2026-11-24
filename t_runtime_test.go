package gligabao_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/deepblue-phoenix/gligabao"
)

// TestRuntimeReachesTheHooks checks that OnStart runs before Mount returns, with the open databases, and that a per-request hook
// (Auth) gets the same Runtime.
func TestRuntimeReachesTheHooks(t *testing.T) {
	var onStart, inAuth *gligabao.Runtime
	var pingErr error
	mux := http.NewServeMux()
	release, err := gligabao.Mount(t.Context(),
		gligabao.DefaultConfig(),
		gligabao.ServeOptions{
			DBPath: filepath.Join(t.TempDir(), "gb.db"),
			Hooks: []gligabao.Hooks{{
				OnStart: func(ctx context.Context,
					rt *gligabao.Runtime) error {
					onStart = rt
					pingErr = rt.Relational.PingContext(ctx)
					return nil
				},
			}},
			Auth: func(_ context.Context,
				rt *gligabao.Runtime,
				_ string) (string, error) {
				inAuth = rt
				return "u1", nil
			},
		},
		gligabao.MountOptions{Mux: mux})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if onStart == nil || pingErr != nil || onStart.DBBackend != "sqlite" {
		t.Fatalf("OnStart runtime %+v, ping %v", onStart, pingErr)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/gb/datasets", nil)
	req.Header.Set("Authorization", "Bearer k")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || inAuth != onStart {
		t.Fatalf("status %d, auth runtime %p, want %p", rec.Code, inAuth, onStart)
	}
}

// TestOnStartErrorAbortsMount checks that a failing OnStart stops the start.
func TestOnStartErrorAbortsMount(t *testing.T) {
	boom := errors.New("boom")
	_, err := gligabao.Mount(t.Context(),
		gligabao.DefaultConfig(),
		gligabao.ServeOptions{
			DBPath: filepath.Join(t.TempDir(), "gb.db"),
			Hooks: []gligabao.Hooks{{
				OnStart: func(context.Context, *gligabao.Runtime) error { return boom },
			}},
		},
		gligabao.MountOptions{Mux: http.NewServeMux()})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
