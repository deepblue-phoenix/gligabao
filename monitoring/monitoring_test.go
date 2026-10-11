package monitoring

import (
	"context"
	"testing"
)

func TestEmitCallsHook(t *testing.T) {
	var gotType string
	var gotEvent Event
	SetHooks([]Hooks{{SystemEvent: func(_ context.Context, typ string, ev Event) { gotType, gotEvent = typ, ev }}})
	t.Cleanup(func() { SetHooks(nil) })

	EmitSystemEvent(context.Background(), "x.happened", Event{"k": "v"})
	if gotType != "x.happened" || gotEvent["k"] != "v" {
		t.Fatalf("got %q %v", gotType, gotEvent)
	}
}

func TestEmitWithoutHookOrPanickingHook(t *testing.T) {
	SetHooks(nil)
	EmitSystemEvent(context.Background(), "x", nil)

	n := 0
	SetHooks([]Hooks{
		{SystemEvent: func(context.Context, string, Event) { panic("boom") }},
		{},
		{SystemEvent: func(context.Context, string, Event) { n++ }},
	})
	t.Cleanup(func() { SetHooks(nil) })
	EmitSystemEvent(context.Background(), "x", nil)
	if n != 1 {
		t.Fatalf("later hook called %d times", n)
	}
}
