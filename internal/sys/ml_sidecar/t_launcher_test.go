package ml_sidecar

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLocalHostPort(t *testing.T) {
	cases := map[string]struct {
		host, port string
		err        error
	}{
		"":                         {"127.0.0.1", "8787", nil},
		"http://127.0.0.1:9000":    {"127.0.0.1", "9000", nil},
		"http://localhost":         {"localhost", "8787", nil},
		"http://[::1]:8000/":       {"::1", "8000", nil},
		"http://ml-sidecar:8787":   {"", "", ErrNotLocal},
		"https://ml.example.com":   {"", "", ErrNotLocal},
		"http://10.0.0.5:8787/api": {"", "", ErrNotLocal},
	}
	for in, want := range cases {
		host, port, err := localHostPort(in)
		if !errors.Is(err, want.err) || host != want.host || port != want.port {
			t.Errorf("%q: got %q %q %v, want %q %q %v", in, host, port, err, want.host, want.port, want.err)
		}
	}
}

func TestResolveDir(t *testing.T) {
	ok := func(d string) bool { return d == "b" || d == "given" }
	if d, err := resolveDir("", []string{"a", "b", "c"}, ok); err != nil || d != "b" {
		t.Errorf("first candidate that has the script: %q %v", d, err)
	}
	if d, err := resolveDir("given", []string{"b"}, ok); err != nil || d != "given" {
		t.Errorf("given dir: %q %v", d, err)
	}
	if _, err := resolveDir("nope", []string{"b"}, ok); !errors.Is(err, ErrDirNotFound) {
		t.Errorf("a given dir without the script: %v", err)
	}
	if _, err := resolveDir("", []string{"a"}, ok); !errors.Is(err, ErrDirNotFound) {
		t.Errorf("no candidate: %v", err)
	}
}

func TestPythonFor(t *testing.T) {
	venv := filepath.Join("d", ".venv", "bin", "python")
	winVenv := filepath.Join("d", ".venv", "Scripts", "python.exe")
	has := func(p string) func(string) bool { return func(x string) bool { return x == p } }
	none := func(string) bool { return false }
	if got := pythonFor("d", "linux", has(venv)); got != venv {
		t.Errorf("venv: %q", got)
	}
	if got := pythonFor("d", "linux", none); got != "python3" {
		t.Errorf("system: %q", got)
	}
	if got := pythonFor("d", "windows", has(winVenv)); got != winVenv {
		t.Errorf("windows venv: %q", got)
	}
	if got := pythonFor("d", "windows", none); got != "python" {
		t.Errorf("windows system: %q", got)
	}
}

// fakeScript is a stand-in for main_ml_sidecar.py: it serves GET /health on ML_SIDECAR_PORT.
const fakeScript = `
import os, http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
        self.wfile.write(b'{"status": "ok"}')
    def log_message(self, *a): pass
http.server.HTTPServer((os.environ["ML_SIDECAR_HOST"], int(os.environ["ML_SIDECAR_PORT"])), H).serve_forever()
`

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestLaunchStartsAndStops(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, mainScript), []byte(fakeScript), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := Launch(ctx, LaunchOptions{
		URL:          "http://127.0.0.1:" + strconv.Itoa(freePort(t)),
		Dir:          dir,
		ReadyTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if _, err := p.Client.Health(ctx); err != nil {
		t.Fatalf("health after launch: %v", err)
	}
	cancel()
	done := make(chan struct{})
	go func() { _ = p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the process did not stop with the context")
	}
}

func TestLaunchReportsAnEarlyExit(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, mainScript), []byte("raise SystemExit(3)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Launch(context.Background(), LaunchOptions{
		URL:          "http://127.0.0.1:" + strconv.Itoa(freePort(t)),
		Dir:          dir,
		ReadyTimeout: 20 * time.Second,
	})
	if err == nil {
		t.Fatal("want an error for a script that exits")
	}
}

func TestLaunchRefusesARemoteURL(t *testing.T) {
	if _, err := Launch(context.Background(), LaunchOptions{URL: "http://ml.example.com:8787"}); !errors.Is(err, ErrNotLocal) {
		t.Fatalf("got %v", err)
	}
}
