package monitoring

import (
	"testing"
	"time"
)

func TestProviderEventsReachEveryCallbackAndChannelClosesOnRemoval(t *testing.T) {
	got := make([]chan ProviderEvent, 2)
	for i := range got {
		got[i] = make(chan ProviderEvent, 1)
	}
	hooks := []Hooks{}
	for i := range got {
		i := i
		hooks = append(hooks, Hooks{ProviderEvents: func(ch <-chan ProviderEvent) {
			go func() {
				for ev := range ch {
					got[i] <- ev
				}
				close(got[i])
			}()
		}})
	}
	SetHooks(hooks)
	t.Cleanup(func() { SetHooks(nil) })

	EmitProviderEvent(ProviderEvent{ProviderType: ProviderModal, Type: "x.y", Kind: KindLifecycle})
	for i, c := range got {
		select {
		case ev := <-c:
			if ev.ProviderType != ProviderModal || ev.Type != "x.y" || ev.Time.IsZero() {
				t.Fatalf("callback %d got %+v", i, ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("callback %d got nothing", i)
		}
	}
	SetHooks(nil)
	for i, c := range got {
		select {
		case _, ok := <-c:
			if ok {
				t.Fatalf("callback %d: unexpected event", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("callback %d: channel not closed", i)
		}
	}
}

func TestProviderEventsDropWhenFullAndNeverBlock(t *testing.T) {
	var ch <-chan ProviderEvent
	SetHooks([]Hooks{{ProviderEvents: func(c <-chan ProviderEvent) { ch = c }}})
	t.Cleanup(func() { SetHooks(nil) })

	for range ProviderEventsBuffer + 50 {
		EmitProviderEvent(ProviderEvent{ProviderType: ProviderModal, Type: "x"})
	}
	if len(ch) != ProviderEventsBuffer {
		t.Fatalf("queued %d, want %d", len(ch), ProviderEventsBuffer)
	}
}

func TestProviderEventsNoHookOrPanickingHook(t *testing.T) {
	SetHooks(nil)
	EmitProviderEvent(ProviderEvent{Type: "x"})
	SetHooks([]Hooks{{ProviderEvents: func(<-chan ProviderEvent) { panic("boom") }}})
	t.Cleanup(func() { SetHooks(nil) })
	EmitProviderEvent(ProviderEvent{Type: "x"})
}
