package sargo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	sargoembed "github.com/deepblue-phoenix/gligabao/rust/sargo"
)

// Server mode: the sargo binary embedded in the gligabao binary (rust/sargo/embed.go) is extracted to a private temporary directory
// and run there as a child process that serves the HTTP API, and the Client talks to it. The child is stopped by Stop (or when the
// context given to StartServer ends) and the directory is removed.

// ErrNotEmbedded is returned by StartServer when this gligabao binary was built without sargo (see ./run_build.sh --sargo).
var ErrNotEmbedded = errors.New("sargo: this gligabao binary was built without the sargo binary (build it with ./run_build.sh --sargo)")

// ServerOptions configures StartServer. The zero value runs sargo with its embedded default config.
type ServerOptions struct {
	// ConfigPath is a sargo config file given by the user; it is used instead of the embedded default config (and read as is by
	// sargo, so relative paths in it, e.g. [gcnii] weights, resolve against the working directory of gligabao).
	ConfigPath string
	// Env is added to the environment the server inherits (KEY=VALUE). The API keys of the providers are read from the environment.
	Env []string
	// Stdout and Stderr receive the output of sargo (nil: discarded and os.Stderr).
	Stdout io.Writer
	Stderr io.Writer
	// ReadyTimeout is how long to wait for the server to answer /health (0: 10 seconds).
	ReadyTimeout time.Duration
}

// Server is a running embedded sargo server.
type Server struct {
	// Client talks to the server.
	Client *Client
	// Addr is the address the server listens on (server.address of its config).
	Addr string
	// ConfigPath is the config file sargo runs with: the user's, or the extracted default.
	ConfigPath string

	cmd      *exec.Cmd
	dir      string
	cancel   context.CancelFunc
	done     chan struct{}
	waitErr  error
	stopOnce sync.Once
}

// defaultReadyTimeout bounds the wait for the server to be healthy.
const defaultReadyTimeout = 10 * time.Second

// defaultAddress is server.address when the config does not set it (the default of sargo's config).
const defaultAddress = "127.0.0.1:8431"

// StartServer extracts the embedded sargo and runs it as a server, and returns once it answers /health. ctx bounds the life of the
// server too: when it ends, the server is killed. Call Stop to stop it and remove the extracted files.
func StartServer(ctx context.Context,
	opts ServerOptions) (*Server, error) {
	if !sargoembed.Available() {
		return nil, ErrNotEmbedded
	}
	bin, err := sargoembed.Binary()
	if err != nil {
		return nil, fmt.Errorf("sargo: read the embedded binary: %w", err)
	}
	return startServer(ctx,
		opts,
		bin,
		sargoembed.Config)
}

// startServer is StartServer with the binary and the default config given (the tests run a fake binary).
func startServer(ctx context.Context,
	opts ServerOptions,
	bin []byte,
	defaultConfig string) (*Server, error) {
	configText, err := configText(opts.ConfigPath,
		defaultConfig)
	if err != nil {
		return nil, err
	}
	addr, err := configAddress(configText)
	if err != nil {
		return nil, err
	}
	client, err := New(baseURL(addr))
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "gligabao-sargo-*")
	if err != nil {
		return nil, fmt.Errorf("sargo: temporary directory: %w", err)
	}
	binPath := filepath.Join(dir, binaryName(runtime.GOOS))
	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(dir, "config.toml")
	}
	if err := writeFiles(binPath,
		bin,
		configPath,
		configText,
		opts.ConfigPath == ""); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	procCtx, cancel := context.WithCancel(ctx)
	// sargo takes the config path as its first argument.
	cmd := exec.CommandContext(procCtx,
		binPath,
		configPath)
	cmd.Env = append(os.Environ(), opts.Env...)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("sargo: start the server: %w", err)
	}
	s := &Server{
		Client:     client,
		Addr:       addr,
		ConfigPath: configPath,
		cmd:        cmd,
		dir:        dir,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	go func() {
		s.waitErr = cmd.Wait()
		close(s.done)
	}()

	timeout := opts.ReadyTimeout
	if timeout <= 0 {
		timeout = defaultReadyTimeout
	}
	if err := s.waitReady(ctx,
		timeout); err != nil {
		_ = s.Stop()
		return nil, err
	}
	return s, nil
}

// Wait blocks until the server process has exited and returns how (nil if it was stopped with Stop).
func (s *Server) Wait() error {
	<-s.done
	return s.waitErr
}

// Stop kills the server, waits for it and removes the extracted files. It is safe to call more than once.
func (s *Server) Stop() error {
	s.stopOnce.Do(func() {
		s.cancel()
		<-s.done
	})
	return os.RemoveAll(s.dir)
}

// waitReady polls /health until the server answers, it exits, or the timeout passes.
func (s *Server) waitReady(ctx context.Context,
	timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		healthCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := s.Client.Health(healthCtx)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-s.done:
			return fmt.Errorf("sargo: the server exited at start-up: %v", s.waitErr)
		case <-ctx.Done():
			return fmt.Errorf("sargo: waiting for the server: %w", ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("sargo: the server at %s is not healthy after %s: %w",
				s.Addr,
				timeout,
				err)
		case <-tick.C:
		}
	}
}

// configText is the text of the config sargo will run with: the user's file if a path is given, else the default.
func configText(path string,
	defaultConfig string) (string, error) {
	if path == "" {
		return defaultConfig, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("sargo: read config: %w", err)
	}
	return string(data), nil
}

// configAddress is server.address of a sargo config (defaultAddress if it is not set).
func configAddress(text string) (string, error) {
	var parsed struct {
		Server struct {
			Address string `toml:"address"`
		} `toml:"server"`
	}
	if _, err := toml.Decode(text, &parsed); err != nil {
		return "", fmt.Errorf("sargo: parse config: %w", err)
	}
	if parsed.Server.Address == "" {
		return defaultAddress, nil
	}
	return parsed.Server.Address, nil
}

// baseURL is the URL a client reaches a server that listens on addr: a wildcard host (0.0.0.0, ::, none) becomes loopback.
func baseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// writeFiles writes the executable and, if writeConfig, the default config (the user's own config file is never touched).
func writeFiles(binPath string,
	bin []byte,
	configPath string,
	configText string,
	writeConfig bool) error {
	if err := os.WriteFile(binPath, bin, 0o700); err != nil {
		return fmt.Errorf("sargo: extract the binary: %w", err)
	}
	if !writeConfig {
		return nil
	}
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		return fmt.Errorf("sargo: extract the config: %w", err)
	}
	return nil
}

// binaryName is the file name the embedded executable is extracted to: Windows only runs files that end in .exe.
func binaryName(goos string) string {
	if goos == "windows" {
		return "sargo.exe"
	}
	return "sargo"
}
