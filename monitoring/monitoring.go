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
	"fmt"
	"sync/atomic"

	"github.com/deepblue-phoenix/gligabao/gbruntime"
)

// Event is the arbitrary key/value data attached to a system event.
type Event map[string]any

// SystemEventHook receives every emitted system event. It is called synchronously from EmitSystemEvent, so
// implementations should be fast and must be safe for concurrent use. The event map must not be modified. rt is the runtime of
// the running server; it is nil only when an event is emitted while no server runs (tests).
type SystemEventHook func(ctx context.Context,
	rt *gbruntime.Runtime,
	eventType string,
	event Event)

// OnStartHook is called once when the server starts, after gligabao has set everything up (the databases are open, the
// providers and managers exist) and before it accepts requests (before it listens, or before Mount returns). It is the place to
// prepare what the other hooks of the program need (create tables, warm up clients) from the clients in rt. A returned error (or a
// panic) aborts the start: Serve or Mount returns it, nothing is served.
type OnStartHook func(ctx context.Context,
	rt *gbruntime.Runtime) error

// Hooks is one set of callbacks supplied by the embedding program. Nil fields are skipped. Every callback gets the Runtime.
type Hooks struct {
	// OnStart is called once when the server starts, before it accepts requests; see OnStartHook.
	OnStart OnStartHook
	// SystemEvent is called for every system event.
	SystemEvent SystemEventHook
	// TrainingEvents receives the channel the events of the training jobs arrive on; see training_events.go.
	TrainingEvents TrainingEventsHook
}

var hooks atomic.Pointer[[]Hooks]

// current is the runtime of the running server, set by Start.
var current gbruntime.Ref

// SetHooks replaces the installed hook sets (a nil or empty list removes them all). Serve calls this with
// ServeOptions.Hooks.
func SetHooks(h []Hooks) {
	current.Set(nil)
	installTrainingEvents(h)
	if len(h) == 0 {
		hooks.Store(nil)
		return
	}
	c := append([]Hooks(nil), h...)
	hooks.Store(&c)
}

// Start tells the installed hooks that the server is up: it makes rt the runtime the hooks get, calls the OnStart callback of every
// Hooks in order and then hands each TrainingEvents callback its channel (events that were emitted before are waiting in it). It
// stops at the first OnStart error (a panic counts as one) and returns it; the server must then not start. Call it once, after
// SetHooks, when everything the hooks may use is ready and before requests are accepted.
func Start(ctx context.Context,
	rt *gbruntime.Runtime) error {
	current.Set(rt)
	list := hooks.Load()
	if list != nil {
		for i, h := range *list {
			if h.OnStart == nil {
				continue
			}
			if err := callOnStart(h.OnStart,
				ctx,
				rt); err != nil {
				return fmt.Errorf("hooks[%d].OnStart: %w", i, err)
			}
		}
	}
	startTrainingEvents(rt)
	return nil
}

// Stop forgets the runtime (the server is stopping and its clients are about to close).
func Stop() { current.Set(nil) }

func callOnStart(f OnStartHook,
	ctx context.Context,
	rt *gbruntime.Runtime) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return f(ctx, rt)
}

// EmitSystemEvent emits a system event of the given type (e.g. "remember.completed") carrying event (may be nil)
// to the SystemEvent callback of every installed Hooks, in order. It is a no-op when none are installed.
// A panicking callback never propagates to the caller or stops the remaining callbacks.
func EmitSystemEvent(ctx context.Context,
	eventType string,
	event Event) {
	list := hooks.Load()
	if list == nil {
		return
	}
	rt := current.Get()
	for _, h := range *list {
		if h.SystemEvent != nil {
			callSafe(h.SystemEvent,
				ctx,
				rt,
				eventType,
				event)
		}
	}
}

func callSafe(f SystemEventHook,
	ctx context.Context,
	rt *gbruntime.Runtime,
	eventType string,
	event Event) {
	defer func() { _ = recover() }()
	f(ctx,
		rt,
		eventType,
		event)
}
