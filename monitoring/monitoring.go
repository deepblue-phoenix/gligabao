// Package monitoring is the repo-wide system-event emitter.
//
// Code anywhere in gligabao calls EmitSystemEvent; the embedding program (e.g. gligabao_int) decides what
// happens to those events by passing Hooks to Serve (see SetHooks) (log them, ship them to a monitoring service, ...).
// With no hook installed, events are dropped.
//
// This package is outside internal/ on purpose so that embedding programs can import it.
package monitoring

import (
	"context"
	"sync/atomic"
)

// Event is the arbitrary key/value data attached to a system event.
type Event map[string]any

// SystemEventHook receives every emitted system event. It is called synchronously from EmitSystemEvent, so
// implementations should be fast and must be safe for concurrent use. The event map must not be modified.
type SystemEventHook func(ctx context.Context, eventType string, event Event)

// Hooks is one set of callbacks supplied by the embedding program. Nil fields are skipped.
type Hooks struct {
	// SystemEvent is called for every system event.
	SystemEvent SystemEventHook
}

var hooks atomic.Pointer[[]Hooks]

// SetHooks replaces the installed hook sets (a nil or empty list removes them all). Serve calls this with
// ServeOptions.Hooks.
func SetHooks(h []Hooks) {
	if len(h) == 0 {
		hooks.Store(nil)
		return
	}
	c := append([]Hooks(nil), h...)
	hooks.Store(&c)
}

// EmitSystemEvent emits a system event of the given type (e.g. "remember.completed") carrying event (may be nil)
// to the SystemEvent callback of every installed Hooks, in order. It is a no-op when none are installed.
// A panicking callback never propagates to the caller or stops the remaining callbacks.
func EmitSystemEvent(ctx context.Context, eventType string, event Event) {
	list := hooks.Load()
	if list == nil {
		return
	}
	for _, h := range *list {
		if h.SystemEvent != nil {
			callSafe(h.SystemEvent, ctx, eventType, event)
		}
	}
}

func callSafe(f SystemEventHook, ctx context.Context, eventType string, event Event) {
	defer func() { _ = recover() }()
	f(ctx, eventType, event)
}
