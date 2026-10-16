package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
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
