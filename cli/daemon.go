package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao"
)

// startTimeout is how long the CLI waits for a daemon it started to answer (opening the databases can be slow for a big store).
const startTimeout = 60 * time.Second

// LocalUser is the user_id of the CLI against the local daemon (--user, GLIGABAO_USER).
const LocalUser = "local_user"

// daemon is the local gligabao server of the CLI: a `serve` process in the background, on 127.0.0.1, with no authentication (it
// is only reachable from this machine).
type daemon struct {
	layout layout
	cfg    *gligabao.Config
	port   int
}

func newDaemon(l layout) (*daemon, error) {
	cfg, err := l.load()
	if err != nil {
		return nil, err
	}
	return &daemon{layout: l, cfg: cfg, port: cfg.Port}, nil
}

func (d *daemon) url() string { return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(d.port)) }

func (d *daemon) logPath() string {
	return filepath.Join(d.layout.StateDir, fmt.Sprintf("daemon-%d.log", d.port))
}
func (d *daemon) pidPath() string {
	return filepath.Join(d.layout.StateDir, fmt.Sprintf("daemon-%d.pid", d.port))
}

// running says whether a gligabao server answers on the port. A different program on it is an error.
func (d *daemon) running(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", d.url()+"/version", nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, nil // nothing listens
	}
	defer resp.Body.Close()
	var v struct {
		Gligabao json.RawMessage `json:"gligabao"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&v) != nil || v.Gligabao == nil {
		return false, fmt.Errorf("port %d is used by another program, not by gligabao (set another port in the config, or use --server)", d.port)
	}
	return true, nil
}

// ensure makes sure the daemon is running: if one answers it is used as it is, otherwise it is started.
func (d *daemon) ensure(ctx context.Context) error {
	ok, err := d.running(ctx)
	if err != nil || ok {
		return err
	}
	return d.start(ctx)
}

// args is the command line of the daemon: `serve` of this very binary (so that a program embedding gligabao starts its own).
func (d *daemon) args() []string {
	args := []string{}
	if d.layout.ConfigPath != "" {
		args = append(args, "--config", d.layout.ConfigPath)
	}
	args = append(args, "serve", "--host", "127.0.0.1")
	if d.layout.DBPath != "" {
		args = append(args, "--db", d.layout.DBPath)
	}
	return args
}

func (d *daemon) start(ctx context.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find the gligabao binary to start the daemon: %w", err)
	}
	if err := os.MkdirAll(d.layout.StateDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(d.logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, d.args()...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the daemon: %w", err)
	}
	if err := os.WriteFile(d.pidPath(), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.After(startTimeout)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case waitErr := <-exited:
			os.Remove(d.pidPath())
			return &exitError{code: exitUnavailable, msg: fmt.Sprintf("the gligabao daemon stopped right after starting (%v); its log %s ends with:\n%s", waitErr, d.logPath(), tail(d.logPath(), 6))}
		case <-deadline:
			return &exitError{code: exitUnavailable, msg: fmt.Sprintf("the gligabao daemon did not answer on %s within %s; see its log %s", d.url(), startTimeout, d.logPath())}
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if ok, _ := d.running(ctx); ok {
				return nil
			}
		}
	}
}

// stop asks the daemon to shut down, if the CLI started it (its pid file).
func (d *daemon) stop() (bool, error) {
	b, err := os.ReadFile(d.pidPath())
	if err != nil {
		return false, nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return false, fmt.Errorf("bad pid file %s", d.pidPath())
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	stopErr := terminate(proc)
	os.Remove(d.pidPath())
	return stopErr == nil, nil
}
