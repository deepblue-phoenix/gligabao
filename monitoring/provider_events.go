package monitoring

import (
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/gbruntime"
	log "github.com/sirupsen/logrus"
)

// Provider events: what the compute providers behind the ML sidecar (Modal today, others later) report about their work:
// lifecycle (a job started, ended, was asked to stop), progress (a training step) and log lines. Unlike system events, they are
// delivered asynchronously: a Hooks.ProviderEvents callback is given a channel once, when the server starts (see Start), and
// reads the events from it whenever it likes.

// Provider types: the ProviderType of an event says which provider it belongs to.
const (
	ProviderModal = "modal"
)

// Kinds of a ProviderEvent.
const (
	KindLifecycle = "lifecycle"
	KindProgress  = "progress"
	KindLog       = "log"
)

// ProviderEventsBuffer is the depth of the channel each ProviderEvents callback gets. An event that finds it full is dropped
// (with a warning): a slow consumer never slows the server.
const ProviderEventsBuffer = 1000

// ProviderEvent is one event of a compute provider.
type ProviderEvent struct {
	// ProviderType is the provider the event belongs to (ProviderModal, ...).
	ProviderType string `json:"provider_type"`
	// Type is what happened, "<subject>.<what>", e.g. "training_job.started", "training_job.status", "training_job.progress".
	Type string `json:"type"`
	// Kind is KindLifecycle, KindProgress or KindLog.
	Kind string `json:"kind"`
	// Time is when the event happened.
	Time time.Time `json:"time"`
	// UserID is the user the event belongs to (the owner of the training job): the provenance of the event. Every event of a job has
	// it; it is empty only for a job made before jobs had a user.
	UserID string `json:"user_id"`
	// Data are the details (run_id, job_id, status, step, loss, ...). The same map goes to every callback: read-only.
	Data Event `json:"data,omitempty"`
}

// ProviderEventsHook is the callback of Hooks.ProviderEvents. It is called once, when the server starts (after the OnStart hooks,
// before requests are accepted), with the runtime and the channel the events arrive on; it must return quickly (start a goroutine
// that ranges over the channel). Events emitted before it is called wait in the channel. The channel is closed when the hooks are
// removed (Serve returns), which ends that goroutine. Each callback has a channel of its own.
type ProviderEventsHook func(rt *gbruntime.Runtime,
	events <-chan ProviderEvent)

// providerSub is one ProviderEvents callback and the channel that is its own.
type providerSub struct {
	hook ProviderEventsHook
	ch   chan ProviderEvent
}

var (
	providerMu   sync.RWMutex
	providerSubs []providerSub
)

// installProviderEvents closes the channels of the previous hooks and makes a new one for each hook of h that wants provider
// events. The callbacks get them later, in startProviderEvents.
func installProviderEvents(h []Hooks) {
	fresh := make([]providerSub, 0, len(h))
	for _, set := range h {
		if set.ProviderEvents != nil {
			fresh = append(fresh, providerSub{
				hook: set.ProviderEvents,
				ch:   make(chan ProviderEvent, ProviderEventsBuffer),
			})
		}
	}
	providerMu.Lock()
	for _, sub := range providerSubs {
		close(sub.ch)
	}
	providerSubs = fresh
	providerMu.Unlock()
}

// startProviderEvents hands every callback its channel. A panicking callback is contained.
func startProviderEvents(rt *gbruntime.Runtime) {
	providerMu.RLock()
	subs := append([]providerSub(nil), providerSubs...)
	providerMu.RUnlock()
	for _, sub := range subs {
		callProviderHook(sub.hook,
			rt,
			sub.ch)
	}
}

func callProviderHook(f ProviderEventsHook,
	rt *gbruntime.Runtime,
	ch <-chan ProviderEvent) {
	defer func() { _ = recover() }()
	f(rt, ch)
}

// EmitProviderEvent sends ev to the channel of every installed ProviderEvents callback; Time is set to now if zero. It never
// blocks: where a channel is full the event is dropped. A no-op when no callback is installed.
func EmitProviderEvent(ev ProviderEvent) {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	providerMu.RLock()
	defer providerMu.RUnlock()
	for _, sub := range providerSubs {
		select {
		case sub.ch <- ev:
		default:
			log.WithFields(log.Fields{
				"provider_type": ev.ProviderType,
				"event_type":    ev.Type,
			}).Warn("provider event queue full, dropping event")
		}
	}
}
