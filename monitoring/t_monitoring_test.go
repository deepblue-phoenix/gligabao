package monitoring

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/gbruntime"
)

func TestEmitCallsHook(t *testing.T) {
	var gotType string
	var gotEvent Event
	SetHooks([]Hooks{{
		SystemEvent: func(_ context.Context,
			_ *gbruntime.Runtime,
			typ string,
			ev Event) {
			gotType, gotEvent = typ, ev
		},
	}})
	t.Cleanup(func() { SetHooks(nil) })

	EmitSystemEvent(context.Background(),
		"x.happened",
		Event{"k": "v"})
	if gotType != "x.happened" || gotEvent["k"] != "v" {
		t.Fatalf("got %q %v",
			gotType,
			gotEvent)
	}
}

func TestEmitWithoutHookOrPanickingHook(t *testing.T) {
	SetHooks(nil)
	EmitSystemEvent(context.Background(),
		"x",
		nil)

	n := 0
	SetHooks([]Hooks{
		{
			SystemEvent: func(context.Context,
				*gbruntime.Runtime,
				string,
				Event) {
				panic("boom")
			},
		},
		{},
		{
			SystemEvent: func(context.Context,
				*gbruntime.Runtime,
				string,
				Event) {
				n++
			},
		},
	})
	t.Cleanup(func() { SetHooks(nil) })
	EmitSystemEvent(context.Background(),
		"x",
		nil)
	if n != 1 {
		t.Fatalf("later hook called %d times", n)
	}
}

func TestStartCallsOnStartInOrderAndSystemEventsGetRuntime(t *testing.T) {
	rt := &gbruntime.Runtime{DBBackend: gbruntime.BackendSQLite}
	var order []string
	var seen *gbruntime.Runtime
	SetHooks([]Hooks{
		{OnStart: func(_ context.Context, got *gbruntime.Runtime) error {
			order = append(order, "a")
			if got != rt {
				t.Errorf("OnStart got %v", got)
			}
			return nil
		}},
		{},
		{
			OnStart: func(context.Context, *gbruntime.Runtime) error {
				order = append(order, "b")
				return nil
			},
			SystemEvent: func(_ context.Context, got *gbruntime.Runtime, _ string, _ Event) { seen = got },
		},
	})
	t.Cleanup(func() { SetHooks(nil) })

	EmitSystemEvent(context.Background(), "early", nil)
	if seen != nil {
		t.Fatalf("runtime before Start: %v", seen)
	}
	if err := Start(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "a,b" {
		t.Fatalf("order %v", order)
	}
	EmitSystemEvent(context.Background(), "late", nil)
	if seen != rt {
		t.Fatalf("system event got %v", seen)
	}
	Stop()
	EmitSystemEvent(context.Background(), "stopped", nil)
	if seen != nil {
		t.Fatalf("runtime after Stop: %v", seen)
	}
}

func TestStartStopsAtFirstOnStartErrorOrPanic(t *testing.T) {
	boom := errors.New("boom")
	called := false
	SetHooks([]Hooks{
		{OnStart: func(context.Context, *gbruntime.Runtime) error { return boom }},
		{OnStart: func(context.Context, *gbruntime.Runtime) error { called = true; return nil }},
	})
	t.Cleanup(func() { SetHooks(nil) })
	if err := Start(context.Background(), &gbruntime.Runtime{}); !errors.Is(err, boom) || called {
		t.Fatalf("err %v, second called %v", err, called)
	}

	SetHooks([]Hooks{{OnStart: func(context.Context, *gbruntime.Runtime) error { panic("bad") }}})
	if err := Start(context.Background(), &gbruntime.Runtime{}); err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("panic not turned into an error: %v", err)
	}
}
