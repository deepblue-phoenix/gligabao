package monty

import (
	"context"
	"errors"
	"github.com/deepblue-phoenix/gligabao/internal/monty/hostfn"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// realConfig is the config for the integration tests; they are skipped when pydantic_monty is not installed for the interpreter.
func realConfig(t *testing.T) Config {
	t.Helper()
	cfg := ConfigFromEnv(Config{})
	if err := exec.Command(cfg.python(), "-c", "import pydantic_monty").Run(); err != nil {
		t.Skipf("pydantic_monty is not available for %s: %v", cfg.python(), err)
	}
	return cfg
}

func echoHost(_ context.Context, name string, args []any, kwargs map[string]any) (any, error) {
	switch name {
	case "double":
		return args[0].(float64) * 2, nil
	case "boom":
		return nil, errors.New("it broke")
	}
	return nil, errors.New("unknown " + name)
}

func TestIntegration(t *testing.T) {
	s := NewSidecar(realConfig(t), nil)
	defer s.Close()
	ctx := context.Background()
	lim := Limits{MaxDurationS: 2, MaxMemoryMB: 64, MaxRecursion: 200}
	run := func(code string, inputs map[string]any) (Result, error) {
		return s.Run(ctx, Request{Code: code, Inputs: inputs, Limits: lim, Functions: []string{"double", "boom"}}, echoHost)
	}

	t.Run("arithmetic and print", func(t *testing.T) {
		r, err := run("print('hi')\nn * 2", map[string]any{"n": 21})
		if err != nil || r.Value != float64(42) || r.Stdout != "hi\n" {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("host function round trip", func(t *testing.T) {
		r, err := run("double(4) + 1", nil)
		if err != nil || r.Value != float64(9) || r.Stats.HostCalls != 1 {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("host error is catchable", func(t *testing.T) {
		r, err := run("try:\n    boom()\nexcept Exception as e:\n    print('caught', e)\n1", nil)
		if err != nil || !strings.Contains(r.Stdout, "caught it broke") {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("host error uncaught", func(t *testing.T) {
		_, err := run("boom()", nil)
		if !IsKind(err, KindHostFunction) {
			t.Fatalf("%v", err)
		}
	})
	for _, c := range []struct{ name, code, kind string }{
		{"syntax", "(", KindSyntax},
		{"runtime", "1/0", KindRuntime},
		{"timeout", "while True:\n    pass", KindTimeout},
		{"memory", "x = [0] * (10**9)", KindMemory},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := run(c.code, nil)
			if !IsKind(err, c.kind) {
				t.Fatalf("want %s, got %v", c.kind, err)
			}
		})
	}
	t.Run("concurrent runs", func(t *testing.T) {
		errs := make(chan error, 4)
		for i := 0; i < 4; i++ {
			go func() { _, err := run("double(2)", nil); errs <- err }()
		}
		for i := 0; i < 4; i++ {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("cancel", func(t *testing.T) {
		cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		_, err := s.Run(cctx, Request{Code: "while True:\n    pass", Limits: Limits{MaxDurationS: 3}}, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%v", err)
		}
	})
}

func TestUnavailableInterpreter(t *testing.T) {
	s := NewSidecar(Config{Python: "/nonexistent/python"}, nil)
	defer s.Close()
	err := s.Check(context.Background())
	if !IsKind(err, KindUnavailable) {
		t.Fatalf("%v", err)
	}
	// After maxFailures the sidecar stays off without trying again.
	for i := 0; i < maxFailures; i++ {
		_ = s.Check(context.Background())
	}
	if err := s.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "in a row") {
		t.Fatalf("%v", err)
	}
}

func TestMissingBinary(t *testing.T) {
	cfg := realConfig(t)
	cfg.Bin = "/nonexistent/monty"
	s := NewSidecar(cfg, nil)
	defer s.Close()
	if err := s.Check(context.Background()); !IsKind(err, KindUnavailable) {
		t.Fatalf("%v", err)
	}
}

// fakeBridge is a bridge in a few lines of shell-free Python: protocol tests without Monty.
func fakeConfig(t *testing.T, script string) Config {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	return Config{Command: []string{"python3", "-c", script}}
}

func TestProtocolMismatch(t *testing.T) {
	s := NewSidecar(fakeConfig(t, `import sys,json
print(json.dumps({"type":"ready","protocol":99}),flush=True)
sys.stdin.read()`), nil)
	defer s.Close()
	err := s.Check(context.Background())
	if !IsKind(err, KindUnavailable) || !strings.Contains(err.Error(), "protocol 99") {
		t.Fatalf("%v", err)
	}
}

func TestBridgeDiesMidCall(t *testing.T) {
	s := NewSidecar(fakeConfig(t, `import sys,json,os
print(json.dumps({"type":"ready","protocol":1}),flush=True)
for line in sys.stdin:
    if json.loads(line)["type"]=="run":
        os._exit(1)`), nil)
	defer s.Close()
	_, err := s.Run(context.Background(), Request{Code: "x"}, nil)
	if !IsKind(err, KindCrashed) {
		t.Fatalf("%v", err)
	}
}

func TestFakeBridgeCallRoundTrip(t *testing.T) {
	s := NewSidecar(fakeConfig(t, `import sys,json
def send(m): print(json.dumps(m),flush=True)
send({"type":"ready","protocol":1})
for line in sys.stdin:
    m=json.loads(line)
    if m["type"]=="run":
        send({"type":"call","id":m["id"],"call_id":1,"name":"f","args":[1],"kwargs":{}})
    elif m["type"]=="result":
        send({"type":"done","id":m["id"],"value":m["value"],"stdout":"","stderr":"","stats":{"wall_ms":1,"host_calls":1}})
    elif m["type"]=="shutdown":
        break`), nil)
	defer s.Close()
	r, err := s.Run(context.Background(), Request{Code: "x"}, func(_ context.Context, name string, args []any, _ map[string]any) (any, error) {
		return map[string]any{"name": name}, nil
	})
	if err != nil || r.Value.(map[string]any)["name"] != "f" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRestartAfterCrash(t *testing.T) {
	s := NewSidecar(fakeConfig(t, `import sys,json,os
def send(m): print(json.dumps(m),flush=True)
send({"type":"ready","protocol":1})
for line in sys.stdin:
    m=json.loads(line)
    if m["type"]=="run":
        if "die" in m["code"]:
            os._exit(1)
        send({"type":"done","id":m["id"],"value":1,"stdout":"","stderr":"","stats":{}})`), nil)
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Run(ctx, Request{Code: "die"}, nil); !IsKind(err, KindCrashed) {
		t.Fatalf("%v", err)
	}
	if r, err := s.Run(ctx, Request{Code: "ok"}, nil); err != nil || r.Value != float64(1) {
		t.Fatalf("restart failed: %+v %v", r, err)
	}
}

// The real sandbox with the real registry: the code reads the state through the host functions.
func TestIntegrationStandardRegistry(t *testing.T) {
	s := NewSidecar(realConfig(t), nil)
	defer s.Close()
	reg := hostfn.Standard()
	env := hostfn.Env{Notes: "n", Questions: []map[string]any{{"id": "q_001", "status": "open"}, {"id": "q_002", "status": "resolved"}}}
	host := func(ctx context.Context, name string, args []any, kwargs map[string]any) (any, error) {
		return reg.Call(ctx, env, name, args, kwargs, 1000)
	}
	r, err := s.Run(context.Background(), Request{
		Code:      "qs = frontier_list(status='resolved')\nlog('x')\nlen(qs) * 10 + len(notes_read())",
		Functions: reg.Names(), Stubs: reg.Stubs(), TypeCheck: true, Limits: Limits{MaxDurationS: 5},
	}, host)
	if err != nil || r.Value != float64(11) {
		t.Fatalf("%+v %v", r, err)
	}
	// A function that is not in the run's list does not exist in the sandbox.
	_, err = s.Run(context.Background(), Request{Code: "notes_read()", Functions: []string{"log"}, Limits: Limits{MaxDurationS: 5}}, host)
	if !IsKind(err, KindRuntime) {
		t.Fatalf("%v", err)
	}
}
