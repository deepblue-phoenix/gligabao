package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
)

// sidecarPingTimeout bounds the live health check of SidecarStatusOf.
const sidecarPingTimeout = 3 * time.Second

// SidecarStatusOf serves GET /admin/api/sidecar/status: {"sidecar_up": bool, "sidecar_active": bool}. sidecar_up is a live
// health check (ping, nil = always down); sidecar_active is whether the server uses the sidecar (active is false if it was
// not active on startup, which only a restart changes). The admin UI's health indicator and the training view use them.
func SidecarStatusOf(active bool, ping func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		up := false
		if ping != nil {
			ctx, cancel := context.WithTimeout(r.Context(), sidecarPingTimeout)
			defer cancel()
			up = ping(ctx) == nil
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"sidecar_up": up, "sidecar_active": active})
	}
}

// SidecarBackendsOf serves GET /admin/api/sidecar/backends: {"backends": [...]} with the training backends the sidecar
// reports and their status (name, configured, healthy, and backend-specific fields such as the Modal app's "deployed").
// list nil or failing (sidecar down or too old) gives an empty list and an "error" text, still with status 200.
func SidecarBackendsOf(list func(context.Context) ([]sidecar.BackendStatus, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"backends": []sidecar.BackendStatus{}}
		if list == nil {
			out["error"] = "sidecar not configured"
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 2*sidecarPingTimeout+5*time.Second)
			defer cancel()
			if bs, err := list(ctx); err != nil {
				out["error"] = err.Error()
			} else if bs != nil {
				out["backends"] = bs
			}
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}
