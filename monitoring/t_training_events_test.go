package monitoring

import (
	"context"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/gbruntime"
)

func TestTrainingEventsReachEveryCallbackAndChannelClosesOnRemoval(t *testing.T) {
	got := make([]chan TrainingEvent, 2)
	for i := range got {
		got[i] = make(chan TrainingEvent, 1)
	}
	hooks := []Hooks{}
	for i := range got {
		i := i
		hooks = append(hooks, Hooks{TrainingEvents: func(_ *gbruntime.Runtime, ch <-chan TrainingEvent) {
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

	// emitted before Start: it waits in the channel until the callback gets it
	EmitTrainingEvent(TrainingEvent{ProviderType: ProviderModal, Type: EventStarted})
	if err := Start(context.Background(), &gbruntime.Runtime{}); err != nil {
		t.Fatal(err)
	}
	for i, c := range got {
		select {
		case ev := <-c:
			if ev.ProviderType != ProviderModal || ev.Type != EventStarted {
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

func TestTrainingEventsDropWhenFullAndNeverBlock(t *testing.T) {
	var ch <-chan TrainingEvent
	SetHooks([]Hooks{{TrainingEvents: func(_ *gbruntime.Runtime, c <-chan TrainingEvent) { ch = c }}})
	t.Cleanup(func() { SetHooks(nil) })
	if err := Start(context.Background(), &gbruntime.Runtime{}); err != nil {
		t.Fatal(err)
	}

	for range TrainingEventsBuffer + 50 {
		EmitTrainingEvent(TrainingEvent{ProviderType: ProviderModal, Type: "x"})
	}
	if len(ch) != TrainingEventsBuffer {
		t.Fatalf("queued %d, want %d", len(ch), TrainingEventsBuffer)
	}
}

func TestTrainingEventsNoHookOrPanickingHook(t *testing.T) {
	SetHooks(nil)
	EmitTrainingEvent(TrainingEvent{Type: "x"})
	SetHooks([]Hooks{{TrainingEvents: func(*gbruntime.Runtime, <-chan TrainingEvent) { panic("boom") }}})
	t.Cleanup(func() { SetHooks(nil) })
	if err := Start(context.Background(), &gbruntime.Runtime{}); err != nil {
		t.Fatal(err)
	}
	EmitTrainingEvent(TrainingEvent{Type: "x"})
}
