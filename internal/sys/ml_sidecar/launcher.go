package ml_sidecar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The launcher runs the Python ML sidecar (ml_sidecar/main_ml_sidecar.py) as a child process of the server, so that training mode
// needs nothing started by hand. It only launches a local ML sidecar: from a URL that points to another machine nothing is started.

// ErrNotLocal is returned by Launch when the ML sidecar URL is not on this machine (loopback), so the server cannot start it.
var ErrNotLocal = errors.New("ML sidecar: the URL is not local, the ML sidecar is not started by this process")

// ErrDirNotFound is returned by Launch when the ml_sidecar directory (with main_ml_sidecar.py) is in none of the places it looks in.
var ErrDirNotFound = errors.New("ML sidecar: the ml_sidecar directory (main_ml_sidecar.py) was not found")

// mainScript is the entrypoint of the ML sidecar, in its directory.
const mainScript = "main_ml_sidecar.py"

// defaultReadyTimeout bounds the wait for the launched ML sidecar to answer /health (the first start imports the Modal SDK).
const defaultReadyTimeout = 60 * time.Second

// stopGrace is how long the ML sidecar has to exit after the interrupt signal before it is killed.
const stopGrace = 5 * time.Second

// LaunchOptions configures Launch.
type LaunchOptions struct {
	// URL is the base URL the server will talk to (DefaultBaseURL when empty); the ML sidecar is told to listen on its host and port.
	URL string
	// Dir is the ml_sidecar directory; when empty, Launch looks in the places of SearchDirs.
	Dir string
	// Env is added to the environment the child inherits (KEY=VALUE).
	Env []string
	// Stdout and Stderr receive the output of the ML sidecar (nil: the server's own os.Stdout and os.Stderr respectively, so whoever
	// watches the server sees the log of the ML sidecar too; io.Discard drops it).
	Stdout io.Writer
	Stderr io.Writer
	// ReadyTimeout is how long to wait for /health (0: 60 seconds).
	ReadyTimeout time.Duration
}

// Process is a launched ML sidecar.
type Process struct {
	// Client talks to it.
	Client *Client
	// Dir is the ml_sidecar directory it runs from; Python is the interpreter.
	Dir    string
	Python string

	done    chan struct{}
	waitErr error
}

// Wait blocks until the process has exited and returns how.
func (p *Process) Wait() error {
	<-p.done
	return p.waitErr
}

// Launch starts the ML sidecar and returns once it answers /health. ctx bounds its life: when it ends, the child gets an interrupt
// (and is killed after a grace period).
func Launch(ctx context.Context,
	opts LaunchOptions) (*Process, error) {
	host, port, err := localHostPort(opts.URL)
	if err != nil {
		return nil, err
	}
	dir, err := resolveDir(opts.Dir,
		SearchDirs(),
		isMainScriptDir)
	if err != nil {
		return nil, err
	}
	python := pythonFor(dir,
		runtime.GOOS,
		fileExists)

	cmd := exec.CommandContext(ctx,
		python,
		mainScript)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(),
		"ML_SIDECAR_HOST="+host,
		"ML_SIDECAR_PORT="+port),
		opts.Env...)
	cmd.Stdout = writerOr(opts.Stdout,
		os.Stdout)
	cmd.Stderr = writerOr(opts.Stderr,
		os.Stderr)
	cmd.Cancel = interruptOrKill(cmd)
	cmd.WaitDelay = stopGrace
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ML sidecar: start %s %s: %w",
			python,
			mainScript,
			err)
	}
	p := &Process{
		Client: New(opts.URL),
		Dir:    dir,
		Python: python,
		done:   make(chan struct{}),
	}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()

	timeout := opts.ReadyTimeout
	if timeout <= 0 {
		timeout = defaultReadyTimeout
	}
	if err := p.waitReady(ctx,
		timeout); err != nil {
		_ = cmd.Process.Kill()
		<-p.done
		return nil, err
	}
	return p, nil
}

// waitReady polls /health until the ML sidecar answers, it exits, or the timeout passes.
func (p *Process) waitReady(ctx context.Context,
	timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		healthCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := p.Client.Health(healthCtx)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-p.done:
			return fmt.Errorf("ML sidecar: it exited at startup (%v); are MODAL_TOKEN_ID / MODAL_TOKEN_SECRET set and its requirements installed (%s)?",
				p.waitErr,
				filepath.Join(p.Dir, "requirements_ml_sidecar.txt"))
		case <-ctx.Done():
			return fmt.Errorf("ML sidecar: waiting for it: %w", ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("ML sidecar: not healthy after %s: %w",
				timeout,
				err)
		case <-tick.C:
		}
	}
}

// localHostPort is the host and port the ML sidecar must listen on for the URL (DefaultBaseURL when empty); ErrNotLocal if the host is not
// the loopback of this machine.
func localHostPort(rawURL string) (string, string, error) {
	if rawURL == "" {
		rawURL = DefaultBaseURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("ML sidecar: bad URL %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if !isLoopback(host) {
		return "", "", ErrNotLocal
	}
	port := u.Port()
	if port == "" {
		port = "8787"
	}
	return host, port, nil
}

// isLoopback is true for localhost and the loopback IP addresses.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SearchDirs are the places the ml_sidecar directory is looked for, in order: next to the executable, in the working directory, and
// the sibling gligabao checkout (where gligabao_int is built from).
func SearchDirs() []string {
	exeDir := "."
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	return []string{
		filepath.Join(exeDir, "ml_sidecar"),
		"ml_sidecar",
		filepath.Join("..", "gligabao", "ml_sidecar"),
	}
}

// resolveDir is dir when it is given (it must hold the main script), else the first of candidates for which ok is true.
func resolveDir(dir string,
	candidates []string,
	ok func(string) bool) (string, error) {
	if dir != "" {
		if !ok(dir) {
			return "", fmt.Errorf("%w: %s", ErrDirNotFound, dir)
		}
		return dir, nil
	}
	for _, c := range candidates {
		if ok(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("%w (looked in %s; set ml_sidecar_dir / ML_SIDECAR_DIR)",
		ErrDirNotFound,
		strings.Join(candidates, ", "))
}

// pythonFor is the interpreter of the ML sidecar: the virtual environment of the directory (.venv) if there is one, else python3 (python
// on Windows) from the PATH.
func pythonFor(dir string,
	goos string,
	exists func(string) bool) string {
	venv := filepath.Join(dir, ".venv", "bin", "python")
	system := "python3"
	if goos == "windows" {
		venv = filepath.Join(dir, ".venv", "Scripts", "python.exe")
		system = "python"
	}
	if exists(venv) {
		return venv
	}
	return system
}

// isMainScriptDir is true if dir holds the main script of the ML sidecar.
func isMainScriptDir(dir string) bool {
	return fileExists(filepath.Join(dir, mainScript))
}

// fileExists is true if path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writerOr is w, or fallback when w is nil.
func writerOr(w io.Writer,
	fallback io.Writer) io.Writer {
	if w == nil {
		return fallback
	}
	return w
}

// interruptOrKill is the Cancel of the command: an interrupt, so that the ML sidecar ends properly; a kill where interrupts are not
// supported (Windows).
func interruptOrKill(cmd *exec.Cmd) func() error {
	return func() error {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
