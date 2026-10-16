package admin

import (
	"context"
	"io"
	"net/http"
	"regexp"
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

// actionName is what a backend or action name in a URL may look like (they go into the sidecar's path).
var actionName = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// SidecarBackendProxy serves the routes of a backend that the sidecar answers itself, by passing them on to the sidecar
// and its answer back, unchanged and flushed as it arrives so that an SSE stream (the log of an action) is live. The path
// pattern has the wildcard {name} and, for an action's routes, {action}. The sidecar's path is /backends/{name}, then
// /actions/{action} if the pattern has {action}, then suffix ("", "/events", "/cancel", "/volumes"). open sends the request
// (sidecar.Client.Open); the query goes along.
func SidecarBackendProxy(open func(ctx context.Context, method, path, rawQuery string, body io.Reader) (*http.Response, error), suffix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, action := r.PathValue("name"), r.PathValue("action")
		if !actionName.MatchString(name) || (action != "" && !actionName.MatchString(action)) {
			httputil.Error(w, http.StatusBadRequest, "invalid backend or action name")
			return
		}
		if open == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "sidecar not configured")
			return
		}
		path := "/backends/" + name
		if action != "" {
			path += "/actions/" + action
		}
		path += suffix
		resp, err := open(r.Context(), r.Method, path, r.URL.RawQuery, nil)
		if err != nil {
			httputil.Error(w, http.StatusBadGateway, err.Error())
			return
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)
		rc := http.NewResponseController(w)
		buf := make([]byte, 8<<10)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				_ = rc.Flush()
			}
			if err != nil {
				return
			}
		}
	}
}
