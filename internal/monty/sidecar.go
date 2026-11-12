package monty

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// maxFailures is how many failed starts or crashes of the sidecar in a row are tolerated before it is unavailable until the process restarts.
	maxFailures  = 3
	startTimeout = 30 * time.Second
	stopGrace    = 2 * time.Second
)

// FuncDecl is a function of the sandbox's world as the hello message tells the bridge.
type FuncDecl struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
}

// inMsg is a message from the bridge (the union of the types of the protocol).
type inMsg struct {
	Type    string `json:"type"`
	ID      int64  `json:"id"`
	CallID  int64  `json:"call_id"`
	Name    string `json:"name"`
	Args    []any  `json:"args"`
	Kwargs  map[string]any
	Value   any    `json:"value"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Stats   Stats  `json:"stats"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Line    int    `json:"line"`
	// ready
	Protocol int    `json:"protocol"`
	Version  string `json:"monty_version"`
	Err      string `json:"error"`
}

// Sidecar supervises the bridge process: it starts it at the first run, restarts it after a crash (a few times in a row), and stops it with Close.
// It is safe for concurrent use; the runs share one bridge.
type Sidecar struct {
	cfg   Config
	hello []FuncDecl

	nextID atomic.Int64

	mu       sync.Mutex
	p        *proc
	failures int
	lastErr  error
	closed   bool
}

// NewSidecar returns a sidecar that has not started anything yet. fns are the functions told to the bridge in the hello message.
func NewSidecar(cfg Config,
	fns []FuncDecl) *Sidecar {
	return &Sidecar{
		cfg:   cfg,
		hello: fns,
	}
}

// Version is the Monty version of the running bridge ("" if it did not start yet).
func (s *Sidecar) Version() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return ""
	}
	return s.p.version
}

type runEntry struct {
	ch   chan inMsg
	quit chan struct{}
}

// proc is one running bridge process.
type proc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	version string
	dead    chan struct{}

	wmu sync.Mutex // serialises the writes to stdin

	mu   sync.Mutex
	runs map[int64]runEntry
}

func (p *proc) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.stdin.Write(append(b, '\n'))
	return err
}

func (p *proc) alive() bool {
	select {
	case <-p.dead:
		return false
	default:
		return true
	}
}

// dispatch hands a message of a run to its Run call (dropped if the run is gone).
func (p *proc) dispatch(m inMsg) {
	p.mu.Lock()
	e, ok := p.runs[m.ID]
	p.mu.Unlock()
	if !ok {
		return
	}
	select {
	case e.ch <- m:
	case <-e.quit:
	case <-p.dead:
	}
}

// start launches the bridge and waits for its ready message.
func start(ctx context.Context,
	cfg Config,
	hello []FuncDecl) (*proc, error) {
	argv, env := cfg.command()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(cmd.Environ(), env...)
	cmd.Stderr = io.Discard
	ownGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot start %s: %w",
			argv[0],
			err)
	}
	p := &proc{
		cmd:   cmd,
		stdin: stdin,
		dead:  make(chan struct{}),
		runs:  map[int64]runEntry{},
	}
	ready := make(chan inMsg, 1)
	go func() {
		defer func() {
			close(p.dead)
			killGroup(cmd)
			_ = cmd.Wait()
		}()
		rd := bufio.NewReaderSize(stdout, 1<<20)
		for first := true; ; first = false {
			line, rerr := rd.ReadBytes('\n')
			var m inMsg
			if len(strings.TrimSpace(string(line))) > 0 && json.Unmarshal(line, &m) == nil {
				if m.Type == "ready" && first {
					ready <- m
				} else {
					p.dispatch(m)
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	select {
	case m := <-ready:
		switch {
		case m.Err != "":
			p.stop()
			return nil, errors.New(m.Err)
		case m.Protocol != Protocol:
			p.stop()
			return nil, fmt.Errorf("the bridge speaks protocol %d, gligabao protocol %d",
				m.Protocol,
				Protocol)
		}
		p.version = m.Version
	case <-p.dead:
		return nil, errors.New("the bridge exited before it was ready (is pydantic-monty installed for " + argv[0] + "?)")
	case <-time.After(startTimeout):
		p.stop()
		return nil, errors.New("the bridge did not become ready in time")
	case <-ctx.Done():
		p.stop()
		return nil, ctx.Err()
	}
	if err := p.send(map[string]any{"type": "hello", "protocol": Protocol, "functions": hello}); err != nil {
		p.stop()
		return nil, err
	}
	return p, nil
}

// stop ends the process: shutdown by closing its stdin, then the group is killed if it does not leave.
func (p *proc) stop() {
	_ = p.send(map[string]any{"type": "shutdown"})
	_ = p.stdin.Close()
	select {
	case <-p.dead:
	case <-time.After(stopGrace):
		killGroup(p.cmd)
		<-p.dead
	}
}

// get returns the running bridge, starting it if needed.
func (s *Sidecar) get(ctx context.Context) (*proc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return nil, errClosed
	case s.p != nil && s.p.alive():
		return s.p, nil
	case s.p != nil:
		s.failures++ // it died by itself
		s.lastErr = errors.New("the bridge process died")
		s.p = nil
	}
	if s.failures >= maxFailures {
		return nil, &Error{
			Kind: KindUnavailable,
			Message: fmt.Sprintf("the code sandbox failed %d times in a row (%v); it stays off until gligabao restarts",
				s.failures,
				s.lastErr),
		}
	}
	p, err := start(ctx,
		s.cfg,
		s.hello)
	if err != nil {
		s.failures++
		s.lastErr = err
		return nil, &Error{
			Kind:    KindUnavailable,
			Message: err.Error(),
		}
	}
	s.p = p
	return p, nil
}

func (s *Sidecar) healthy() {
	s.mu.Lock()
	s.failures = 0
	s.mu.Unlock()
}

// Check starts the bridge if it is not running and reports why it cannot (the interpreter, the pydantic_monty import, the monty binary,
// the protocol).
func (s *Sidecar) Check(ctx context.Context) error {
	_, err := s.get(ctx)
	return err
}

// Close stops the bridge and its workers; later runs fail with unavailable.
func (s *Sidecar) Close() {
	s.mu.Lock()
	s.closed = true
	p := s.p
	s.p = nil
	s.mu.Unlock()
	if p != nil {
		p.stop()
	}
}

type runMsg struct {
	Type      string         `json:"type"`
	ID        int64          `json:"id"`
	Code      string         `json:"code"`
	Inputs    map[string]any `json:"inputs,omitempty"`
	Limits    Limits         `json:"limits"`
	Functions []string       `json:"functions"`
	Stubs     string         `json:"stubs,omitempty"`
	TypeCheck bool           `json:"type_check"`
}

// Run runs the program. The host functions it calls are answered by host, called on the goroutine of Run, one at a time. A run whose context
// ends is cancelled in the bridge and returns the context's error.
func (s *Sidecar) Run(ctx context.Context,
	req Request,
	host HostFunc) (Result, error) {
	p, err := s.get(ctx)
	if err != nil {
		return Result{}, err
	}
	id := s.nextID.Add(1)
	entry := runEntry{
		ch:   make(chan inMsg),
		quit: make(chan struct{}),
	}
	p.mu.Lock()
	p.runs[id] = entry
	p.mu.Unlock()
	defer func() {
		close(entry.quit)
		p.mu.Lock()
		delete(p.runs, id)
		p.mu.Unlock()
	}()
	if err := p.send(runMsg{
		Type:      "run",
		ID:        id,
		Code:      req.Code,
		Inputs:    req.Inputs,
		Limits:    req.Limits,
		Functions: req.Functions,
		Stubs:     req.Stubs,
		TypeCheck: req.TypeCheck,
	}); err != nil {
		return Result{}, &Error{
			Kind:    KindCrashed,
			Message: "the sandbox is not reachable: " + err.Error(),
		}
	}
	for {
		select {
		case <-ctx.Done():
			_ = p.send(map[string]any{"type": "cancel", "id": id})
			return Result{}, ctx.Err()
		case <-p.dead:
			return Result{}, &Error{
				Kind:    KindCrashed,
				Message: "the sandbox process ended while the program ran",
			}
		case m := <-entry.ch:
			switch m.Type {
			case "call":
				ok, val, errText := callHost(ctx,
					host,
					m)
				if err := p.send(map[string]any{"type": "result", "id": id, "call_id": m.CallID, "ok": ok, "value": val, "error": errText}); err != nil {
					return Result{}, &Error{
						Kind:    KindCrashed,
						Message: "the sandbox is not reachable: " + err.Error(),
					}
				}
			case "done":
				s.healthy()
				return Result{
					Value:  m.Value,
					Stdout: m.Stdout,
					Stderr: m.Stderr,
					Stats:  m.Stats,
				}, nil
			case "failed":
				if m.Kind != KindCrashed {
					s.healthy()
				}
				return Result{}, &Error{
					Kind:    m.Kind,
					Message: m.Message,
					Line:    m.Line,
				}
			}
		}
	}
}

// callHost answers one call of the code; a host function that panics or fails becomes an exception of the sandbox, never a crash.
func callHost(ctx context.Context,
	host HostFunc,
	m inMsg) (ok bool, val any, errText string) {
	defer func() {
		if r := recover(); r != nil {
			ok, val, errText = false, nil, fmt.Sprintf("%s failed: %v",
				m.Name,
				r)
		}
	}()
	if host == nil {
		return false, nil, "no host functions are available"
	}
	v, err := host(ctx,
		m.Name,
		m.Args,
		m.Kwargs)
	if err != nil {
		return false, nil, err.Error()
	}
	return true, v, ""
}
