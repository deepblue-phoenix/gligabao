package memory_pipeline

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// events is an Observer that notes what it was told.
type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string)           { e.mu.Lock(); e.log = append(e.log, s); e.mu.Unlock() }
func (e *events) Start(stages ...string) { e.add(fmt.Sprint("start ", stages)) }
func (e *events) Done(err error, stages ...string) {
	if err != nil {
		e.add(fmt.Sprint("fail ", stages, ": ", err))
		return
	}
	e.add(fmt.Sprint("done ", stages))
}
func (e *events) Skip(stages ...string) { e.add(fmt.Sprint("skip ", stages)) }
func (e *events) AddInference(stage string, id int64) {
	e.add(fmt.Sprint("inference ", stage, " ", id))
}

type state struct {
	items []int
	i     int
	ran   []string
	off   bool
}

func stage(name string, f func(*state) error) Stage[state] {
	return Stage[state]{Name: name, Run: func(s *state, _ context.Context) error {
		s.ran = append(s.ran, name)
		if f != nil {
			return f(s)
		}
		return nil
	}}
}

func TestRunTellsTheObserverOfEveryStage(t *testing.T) {
	var ev events
	off := stage("off", nil)
	off.Enabled = func(s *state) bool { return !s.off }
	s := &state{off: true}
	err := Run(context.Background(), []Step[state]{stage("a", nil), off, stage("b", nil)}, s, &ev)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"start [a]", "done [a]", "skip [off]", "start [b]", "done [b]"}
	if !reflect.DeepEqual(ev.log, want) {
		t.Errorf("events = %v, want %v", ev.log, want)
	}
	if !reflect.DeepEqual(s.ran, []string{"a", "b"}) {
		t.Errorf("ran = %v", s.ran)
	}
}

func TestFatalErrorEndsThePipelineAdvisoryDoesNot(t *testing.T) {
	boomErr := errors.New("boom")
	var ev events
	adv := stage("adv", func(*state) error { return boomErr })
	adv.Policy = Advisory
	s := &state{}
	if err := Run(context.Background(), []Step[state]{adv, stage("b", nil)}, s, &ev); err != nil {
		t.Fatalf("advisory error returned: %v", err)
	}
	want := []string{"start [adv]", "fail [adv]: boom", "start [b]", "done [b]"}
	if !reflect.DeepEqual(ev.log, want) {
		t.Errorf("events = %v, want %v", ev.log, want)
	}

	ev = events{}
	s = &state{}
	err := Run(context.Background(), []Step[state]{stage("a", func(*state) error { return boomErr }), stage("b", nil)}, s, &ev)
	if !errors.Is(err, boomErr) {
		t.Fatalf("err = %v, want boom", err)
	}
	if want := []string{"start [a]", "fail [a]: boom"}; !reflect.DeepEqual(ev.log, want) {
		t.Errorf("events = %v, want %v", ev.log, want)
	}
	if !reflect.DeepEqual(s.ran, []string{"a"}) {
		t.Errorf("a stage ran after the failure: %v", s.ran)
	}
}

func TestStopEndsWithoutErrorAndSkipsTheRest(t *testing.T) {
	var ev events
	steps := []Step[state]{
		stage("a", func(*state) error { return Stop }),
		stage("b", nil),
		Each[state]{Count: func(*state) int { return 2 }, Set: func(*state, int) {}, Steps: []Step[state]{stage("c", nil), stage("d", nil)}},
	}
	if err := Run(context.Background(), steps, &state{}, &ev); err != nil {
		t.Fatal(err)
	}
	want := []string{"start [a]", "done [a]", "skip [b]", "skip [c d]"}
	if !reflect.DeepEqual(ev.log, want) {
		t.Errorf("events = %v, want %v", ev.log, want)
	}
}

func TestEachRunsAllTheStepsForOneItemBeforeTheNext(t *testing.T) {
	s := &state{items: []int{10, 20}}
	steps := []Step[state]{Each[state]{
		Count: func(s *state) int { return len(s.items) },
		Set:   func(s *state, i int) { s.i = i },
		Steps: []Step[state]{
			stage("x", func(s *state) error { s.ran = append(s.ran, fmt.Sprint(s.items[s.i])); return nil }),
			stage("y", nil),
		},
	}}
	if err := Run(context.Background(), steps, s, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"x", "10", "y", "x", "20", "y"}; !reflect.DeepEqual(s.ran, want) {
		t.Errorf("ran = %v, want %v", s.ran, want)
	}
}

func TestAlsoStartsAndEndsTheOtherStagesWithTheStage(t *testing.T) {
	var ev events
	st := stage("store", nil)
	st.Also = func(*state) []string { return []string{"store_series"} }
	if err := Run(context.Background(), []Step[state]{st}, &state{}, &ev); err != nil {
		t.Fatal(err)
	}
	want := []string{"start [store store_series]", "done [store store_series]"}
	if !reflect.DeepEqual(ev.log, want) {
		t.Errorf("events = %v, want %v", ev.log, want)
	}
}

func TestInferencesAreFiledUnderTheStageThatMadeThem(t *testing.T) {
	var ev events
	probe := Stage[state]{Name: "probe", Run: func(_ *state, ctx context.Context) error {
		memory_core.NoteInference(ctx, 42) // what memory_core.RecordRun does once it has recorded a run
		memory_core.NoteInference(ctx, 0)  // a run that could not be recorded
		return nil
	}}
	if err := Run(context.Background(), []Step[state]{probe}, &state{}, &ev); err != nil {
		t.Fatal(err)
	}
	if want := []string{"start [probe]", "inference probe 42", "done [probe]"}; !reflect.DeepEqual(ev.log, want) {
		t.Errorf("events = %v, want %v", ev.log, want)
	}
}

func TestNamesListsTheStagesInOrderEachOnce(t *testing.T) {
	steps := []Step[state]{
		stage("a", nil),
		Each[state]{Steps: []Step[state]{stage("b", nil), stage("c", nil)}},
		stage("b", nil),
		Each[state]{Steps: []Step[state]{stage("c", nil), stage("d", nil)}},
	}
	if got, want := Names(steps), []string{"a", "b", "c", "d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names = %v, want %v", got, want)
	}
}

func TestNilObserver(t *testing.T) {
	s := &state{}
	if err := Run(context.Background(), []Step[state]{stage("a", nil)}, s, nil); err != nil || len(s.ran) != 1 {
		t.Fatalf("err = %v, ran = %v", err, s.ran)
	}
}
