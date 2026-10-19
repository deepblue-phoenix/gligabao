package admin_test

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
)

// TestSidecarBackendProxy checks that a backend's action and volumes routes reach the sidecar's /backends/{name}/actions/{action}[/events]
// (method, query and status kept), that an SSE answer arrives while the sidecar's stream is still open, and that bad names
// are rejected before anything is sent.
func TestSidecarBackendProxy(t *testing.T) {
	release := make(chan struct{})
	var gotMethod, gotURI string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotURI = r.Method, r.URL.RequestURI()
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("event: log\ndata: {}\n\n"))
			w.(http.Flusher).Flush()
			<-release // the stream stays open
			w.Write([]byte("event: state\ndata: {}\n\n"))
			return
		}
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"busy"}`))
	}))
	defer fake.Close()
	c := sidecar.New(fake.URL)

	mux := http.NewServeMux()
	const p = "/backends/{name}/actions/{action}"
	mux.HandleFunc("POST "+p, admin.SidecarBackendProxy(c.Open, ""))
	mux.HandleFunc("POST "+p+"/cancel", admin.SidecarBackendProxy(c.Open, "/cancel"))
	mux.HandleFunc("GET /backends/{name}/volumes", admin.SidecarBackendProxy(c.Open, "/volumes"))
	mux.HandleFunc("GET /backends/{name}/apps", admin.SidecarBackendProxy(c.Open, "/apps"))
	mux.HandleFunc("POST /backends/{name}/apps/{app}/actions/{action}", admin.SidecarBackendProxy(c.Open, ""))
	mux.HandleFunc("GET "+p+"/events", admin.SidecarBackendProxy(c.Open, "/events"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/backends/modal/actions/deploy?x=1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || gotMethod != "POST" || gotURI != "/backends/modal/actions/deploy?x=1" {
		t.Errorf("start: status %d, sidecar saw %s %s", resp.StatusCode, gotMethod, gotURI)
	}

	resp, err = http.Post(srv.URL+"/backends/modal/actions/deploy/cancel", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotMethod != "POST" || gotURI != "/backends/modal/actions/deploy/cancel" {
		t.Errorf("cancel: sidecar saw %s %s", gotMethod, gotURI)
	}

	resp, err = http.Get(srv.URL + "/backends/modal/volumes")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotMethod != "GET" || gotURI != "/backends/modal/volumes" {
		t.Errorf("volumes: sidecar saw %s %s", gotMethod, gotURI)
	}

	resp, err = http.Get(srv.URL + "/backends/modal/apps")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotMethod != "GET" || gotURI != "/backends/modal/apps" {
		t.Errorf("apps: sidecar saw %s %s", gotMethod, gotURI)
	}

	resp, err = http.Post(srv.URL+"/backends/modal/apps/gligabao-train-dev.1/actions/deploy", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotMethod != "POST" || gotURI != "/backends/modal/apps/gligabao-train-dev.1/actions/deploy" {
		t.Errorf("app action: sidecar saw %s %s", gotMethod, gotURI)
	}

	resp, err = http.Get(srv.URL + "/backends/modal/actions/deploy/events?from=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" || gotURI != "/backends/modal/actions/deploy/events?from=3" {
		t.Errorf("events: content type %q, sidecar saw %s", ct, gotURI)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Scan() // the first event arrives although the sidecar's stream is not finished
	if got := sc.Text(); got != "event: log" {
		t.Errorf("first line %q, want the log event, live", got)
	}
	close(release)

	resp, err = http.Post(srv.URL+"/backends/mo.dal/actions/deploy", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad name: status %d, want 400", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/backends/modal/apps/-x/actions/deploy", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad app name: status %d, want 400", resp.StatusCode)
	}
}
