package sargo

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sargoembed "github.com/deepblue-phoenix/gligabao/rust/sargo"
)

// fakeEnv makes the test binary act as the sargo binary: it serves /health on the address of the config given as its first argument.
const fakeEnv = "GLIGABAO_FAKE_SARGO"

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "" {
		os.Exit(m.Run())
	}
	if os.Getenv(fakeEnv) == "exit" {
		os.Exit(3)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		os.Exit(2)
	}
	addr, err := configAddress(string(data))
	if err != nil {
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health",
		func(w http.ResponseWriter,
			r *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})
	os.Exit(func() int {
		if err := http.ListenAndServe(addr, mux); err != nil {
			return 2
		}
		return 0
	}())
}

// selfBinary is the test binary, to be "extracted" as the fake sargo.
func selfBinary(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func configFor(addr string) string {
	return "[server]\naddress = \"" + addr + "\"\n"
}

func TestConfigAddress(t *testing.T) {
	for text, want := range map[string]string{
		"":                           defaultAddress,
		"[gcnii]\nweights = \"x\"\n": defaultAddress,
		configFor("0.0.0.0:9000"):    "0.0.0.0:9000",
		sargoembed.Config:            defaultAddress,
	} {
		got, err := configAddress(text)
		if err != nil || got != want {
			t.Errorf("configAddress(%q) = %q, %v; want %q", text, got, err, want)
		}
	}
	if _, err := configAddress("[server"); err == nil {
		t.Error("a malformed config must be an error")
	}
}

func TestBaseURL(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:8431": "http://127.0.0.1:8431",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		":9000":          "http://127.0.0.1:9000",
		"[::]:9000":      "http://127.0.0.1:9000",
		"localhost:80":   "http://localhost:80",
	} {
		if got := baseURL(addr); got != want {
			t.Errorf("baseURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestEmbeddedDefaultConfigIsTheRepoFile(t *testing.T) {
	data, err := os.ReadFile("../../rust/sargo/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if sargoembed.Config != string(data) {
		t.Error("the embedded default config differs from rust/sargo/config.toml")
	}
}

func TestStartServerWithDefaultConfig(t *testing.T) {
	t.Setenv(fakeEnv, "serve")
	s, err := startServer(context.Background(),
		ServerOptions{},
		selfBinary(t),
		configFor("127.0.0.1:18431"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Addr != "127.0.0.1:18431" {
		t.Errorf("Addr = %q", s.Addr)
	}
	if got, err := os.ReadFile(s.ConfigPath); err != nil || string(got) != configFor("127.0.0.1:18431") {
		t.Errorf("the default config was not extracted: %q, %v", got, err)
	}
	if err := s.Client.Health(context.Background()); err != nil {
		t.Errorf("Health: %v", err)
	}
	dir := filepath.Dir(s.ConfigPath)
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the extracted files were not removed: %v", err)
	}
	if err := s.Client.Health(context.Background()); err == nil {
		t.Error("the server still answers after Stop")
	}
	if err := s.Stop(); err != nil { // twice is fine
		t.Error(err)
	}
}

func TestStartServerUsesTheExternalConfig(t *testing.T) {
	t.Setenv(fakeEnv, "serve")
	ext := filepath.Join(t.TempDir(), "mine.toml")
	if err := os.WriteFile(ext, []byte(configFor("127.0.0.1:18432")), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := startServer(context.Background(),
		ServerOptions{ConfigPath: ext},
		selfBinary(t),
		configFor("127.0.0.1:18431"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if s.ConfigPath != ext || s.Addr != "127.0.0.1:18432" {
		t.Errorf("used config %q at %q, want the external one", s.ConfigPath, s.Addr)
	}
	if err := s.Client.Health(context.Background()); err != nil {
		t.Errorf("Health: %v", err)
	}
	if _, err := os.Stat(ext); err != nil {
		t.Errorf("the user's config must be left alone: %v", err)
	}
}

func TestStartServerErrors(t *testing.T) {
	t.Setenv(fakeEnv, "serve")
	if _, err := startServer(context.Background(),
		ServerOptions{ConfigPath: filepath.Join(t.TempDir(), "missing.toml")},
		selfBinary(t),
		""); err == nil {
		t.Error("a missing external config must be an error")
	}

	t.Setenv(fakeEnv, "exit")
	_, err := startServer(context.Background(),
		ServerOptions{ReadyTimeout: 5 * time.Second},
		selfBinary(t),
		configFor("127.0.0.1:18433"))
	if err == nil || !strings.Contains(err.Error(), "exited at start-up") {
		t.Errorf("a server that exits at start-up: got %v", err)
	}
}

func TestStartServerStopsWithItsContext(t *testing.T) {
	t.Setenv(fakeEnv, "serve")
	ctx, cancel := context.WithCancel(context.Background())
	s, err := startServer(ctx,
		ServerOptions{},
		selfBinary(t),
		configFor("127.0.0.1:18434"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan struct{})
	go func() {
		_ = s.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the server was not killed when its context ended")
	}
	_ = s.Stop()
}

func TestBinaryName(t *testing.T) {
	if got := binaryName("windows"); got != "sargo.exe" {
		t.Fatalf("windows: got %q", got)
	}
	if got := binaryName("linux"); got != "sargo" {
		t.Fatalf("linux: got %q", got)
	}
}
