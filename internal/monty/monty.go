// Package monty runs the code of the run_code tool in Pydantic Monty, a sandboxed Python interpreter, through a sidecar: a child process that
// runs the embedded Python bridge (bridge.py) and speaks JSON lines with this package (docs/agents/monty-code-execution.md).
//
// The functions the code may call back are Go functions (package hostfn); Run calls them through a HostFunc, on the goroutine that called Run.
package monty

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
)

// Protocol is the version of the bridge protocol; a bridge that answers another one is unavailable.
const Protocol = 1

//go:embed bridge.py
var bridgeScript string

// Error kinds of a failed run.
const (
	KindSyntax       = "syntax"
	KindType         = "type"
	KindRuntime      = "runtime"
	KindTimeout      = "timeout"
	KindMemory       = "memory"
	KindCrashed      = "crashed"
	KindHostFunction = "host_function"
	KindUnavailable  = "unavailable"
)

// Error is a run that failed, with the kind (one of the Kind constants), the message and the line of the program (0 if unknown).
type Error struct {
	Kind, Message string
	Line          int
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s: %s (line %d)", e.Kind, e.Message, e.Line)
	}
	return e.Kind + ": " + e.Message
}

// Limits are the resource limits of one run (0: the sandbox's own default).
type Limits struct {
	MaxDurationS float64 `json:"max_duration_s,omitempty"`
	MaxMemoryMB  int     `json:"max_memory_mb,omitempty"`
	MaxRecursion int     `json:"max_recursion,omitempty"`
}

// Request is one program to run in a fresh session.
type Request struct {
	Code   string
	Inputs map[string]any
	Limits Limits
	// Functions are the names the code may call and Stubs their Python declarations (for the type check).
	Functions []string
	Stubs     string
	TypeCheck bool
}

// Stats are what the run used.
type Stats struct {
	WallMS    int `json:"wall_ms"`
	HostCalls int `json:"host_calls"`
}

// Result is a program that finished.
type Result struct {
	Value          any
	Stdout, Stderr string
	Stats          Stats
}

// HostFunc runs a host function call of the code. An error becomes an exception inside the sandbox.
type HostFunc func(ctx context.Context, name string, args []any, kwargs map[string]any) (any, error)

// Runner runs programs. *Sidecar is one; tests use fakes.
type Runner interface {
	Run(ctx context.Context, req Request, host HostFunc) (Result, error)
}

// Config says where the sidecar's pieces are. The zero value is usable: python3 and the monty binary found by the bridge.
type Config struct {
	// Python is the interpreter that runs the bridge (has pydantic_monty installed); "" is python3.
	Python string
	// Bin is the monty worker binary; "" lets pydantic_monty look it up (next to the interpreter, then PATH).
	Bin string
	// Workers is the most worker processes (0: 2).
	Workers int
	// RequestTimeoutS is the parent-side deadline that kills a stuck worker (0: 90).
	RequestTimeoutS int
	// Command, if set, replaces "python3 -c <bridge>" (tests use it to start a fake bridge).
	Command []string
}

// ConfigFromEnv fills the unset fields of c from GLIGABAO_MONTY_PYTHON and MONTY_BIN.
func ConfigFromEnv(c Config) Config {
	if c.Python == "" {
		c.Python = os.Getenv("GLIGABAO_MONTY_PYTHON")
	}
	if c.Bin == "" {
		c.Bin = os.Getenv("MONTY_BIN")
	}
	return c
}

func (c Config) python() string {
	if c.Python != "" {
		return c.Python
	}
	return "python3"
}

func (c Config) workers() int {
	if c.Workers > 0 {
		return c.Workers
	}
	return 2
}

func (c Config) requestTimeoutS() int {
	if c.RequestTimeoutS > 0 {
		return c.RequestTimeoutS
	}
	return 90
}

// command is the argv of the bridge and its environment additions.
func (c Config) command() ([]string, []string) {
	argv := c.Command
	if len(argv) == 0 {
		argv = []string{c.python(), "-c", bridgeScript}
	}
	env := []string{
		fmt.Sprintf("GLIGABAO_MONTY_WORKERS=%d", c.workers()),
		fmt.Sprintf("GLIGABAO_MONTY_REQUEST_TIMEOUT=%d", c.requestTimeoutS()),
	}
	if c.Bin != "" {
		env = append(env, "MONTY_BIN="+c.Bin)
	}
	return argv, env
}

var errClosed = &Error{Kind: KindUnavailable, Message: "the code sandbox is shut down"}

// IsKind reports whether err is an *Error of that kind.
func IsKind(err error, kind string) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == kind
}
