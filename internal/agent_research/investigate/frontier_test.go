package investigate

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func mustRoot(t *testing.T) Frontier {
	t.Helper()
	f, ts, err := NewRoot("Is X true?")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].Trigger != TriggerCreated || ts[0].From != "" || ts[0].Seq != 1 || ts[1].Trigger != TriggerSelected || ts[1].Seq != 2 {
		t.Fatalf("root transitions: %+v", ts)
	}
	return f
}

func in(text string, p float64) AddInput {
	return AddInput{Question: text, Priority: p, ValidationCriteria: "criteria for " + text}
}

func mustAdd(t *testing.T, f Frontier, parent string, i AddInput) (Frontier, string) {
	t.Helper()
	f, ts, err := Add(f, parent, i, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return f, ts[0].QuestionID
}

func TestAddAssignsIDAndDepth(t *testing.T) {
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("first", 0.5))
	f, b := mustAdd(t, f, a, in("second", 0.5))
	qa, _ := f.Get(a)
	qb, _ := f.Get(b)
	if a != "q_002" || b != "q_003" || qa.Depth != 1 || qb.Depth != 2 || qa.Status != StatusPending || qb.ParentID != a {
		t.Fatalf("got %+v %+v", qa, qb)
	}
}

func TestAddRejections(t *testing.T) {
	limits := DefaultLimits()
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("first", 0.5))
	f, b := mustAdd(t, f, a, in("second", 0.5))
	f, c := mustAdd(t, f, b, in("third", 0.5)) // depth 3
	cases := []struct {
		name   string
		parent string
		in     AddInput
		want   error
		limits Limits
	}{
		{"depth cap", c, in("fourth", 0.5), ErrDepthCap, limits},
		{"duplicate normalized", "q_001", in("  FIRST?! ", 0.5), ErrDuplicateQuestion, limits},
		{"unknown parent", "q_099", in("x", 0.5), ErrUnknownQuestion, limits},
		{"unknown dep", "q_001", AddInput{Question: "y", Priority: 0.5, DependsOn: []string{"q_099"}}, ErrUnknownQuestion, limits},
		{"empty", "q_001", in(" ", 0.5), ErrInvalid, limits},
		{"priority", "q_001", in("z", 1.5), ErrInvalid, limits},
		{"kind", "q_001", AddInput{Question: "k", Priority: 0.5, Kind: "wish"}, ErrInvalid, limits},
		{"too many", "q_001", in("one more", 0.5), ErrTooManyQuestions, Limits{MaxDepth: 3, MaxQuestions: 4}},
	}
	for _, tc := range cases {
		_, _, err := Add(f, tc.parent, tc.in, tc.limits)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, _, err := Add(f, c, in("fourth", 0.5), limits); err == nil || err.Error() != "pruned by depth cap; record it as an open gap in notes instead" {
		t.Errorf("depth cap text: %v", err)
	}
}

func TestAddCycle(t *testing.T) {
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("a", 0.5))
	f, b := mustAdd(t, f, "q_001", AddInput{Question: "b", Priority: 0.5, DependsOn: []string{a}})
	// a child of a that blocks a and depends on b, which depends on a: a -> child -> b -> a
	_, _, err := Add(f, a, AddInput{Question: "c", Priority: 0.5, DependsOn: []string{b}, BlocksParent: true}, DefaultLimits())
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("want cycle, got %v", err)
	}
	if _, err := addDep(f, a, b); !errors.Is(err, ErrCycle) {
		t.Fatalf("a -> b makes a cycle: %v", err)
	}
	if _, err := addDep(f, a, a); !errors.Is(err, ErrCycle) {
		t.Fatalf("self dependency: %v", err)
	}
}

func TestBlocksParent(t *testing.T) {
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("a", 0.7))
	f, _, _ = Select(f)
	f, ts, err := Add(f, a, AddInput{Question: "prerequisite", Priority: 0.2, ValidationCriteria: "c", BlocksParent: true}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	child, _ := f.Get("q_003")
	parent, _ := f.Get(a)
	if child.Priority != 0.7 || parent.Status != StatusPending || len(parent.DependsOn) != 1 || parent.DependsOn[0] != "q_003" {
		t.Fatalf("child %+v parent %+v", child, parent)
	}
	if len(ts) != 2 || ts[1].Trigger != TriggerDependencyAdded || ts[1].Actor != ActorCode {
		t.Fatalf("transitions %+v", ts)
	}
	// Selection takes the child first, because the parent waits on it.
	_, q, _ := Select(f)
	if q == nil || q.ID != "q_003" {
		t.Fatalf("selected %+v", q)
	}
}

func TestSelectOrder(t *testing.T) {
	f := mustRoot(t)
	f, lowDeep := mustAdd(t, f, "q_001", in("low", 0.3))
	f, hi := mustAdd(t, f, "q_001", in("high", 0.9))
	f, hiDeeper := mustAdd(t, f, hi, in("high but deeper", 0.9))
	f, hiSecond := mustAdd(t, f, "q_001", in("high second", 0.9))
	_ = lowDeep
	order := []string{hi, hiSecond, hiDeeper, lowDeep}
	for _, want := range order {
		var q *Question
		f, q, _ = Select(f)
		if q == nil || q.ID != want {
			t.Fatalf("want %s got %+v", want, q)
		}
		if q.Status != StatusInProgress {
			t.Fatalf("status %s", q.Status)
		}
		f, _, _ = Resolve(f, q.ID, ResolveInput{Answer: "done"})
	}
	if _, q, _ := Select(f); q != nil {
		t.Fatalf("nothing left, got %+v", q)
	}
}

func TestSelectTakesDependencyAndRaisesIt(t *testing.T) {
	f := mustRoot(t)
	f, dep := mustAdd(t, f, "q_001", in("dep", 0.1))
	f, ts0, err := Add(f, "q_001", AddInput{Question: "top", Priority: 0.9, DependsOn: []string{dep}}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	top := ts0[0].QuestionID
	if d, _ := f.Get(dep); d.Priority != 0.9 || len(ts0) != 2 || ts0[1].Trigger != TriggerReprioritized {
		t.Fatalf("add raises the floor of the dependencies: %+v %+v", d, ts0)
	}
	// Lower the dependency again (as if it had been added before) so that selection has to raise it.
	d, _ := f.Get(dep)
	d.Priority = 0.1
	f = f.with(d)
	f2, q, ts := Select(f)
	if q == nil || q.ID != dep || q.Priority != 0.9 {
		t.Fatalf("selected %+v", q)
	}
	if len(ts) != 2 || ts[0].Trigger != TriggerReprioritized || ts[0].From != ts[0].To || ts[0].CausedBy != top || ts[1].Trigger != TriggerSelected {
		t.Fatalf("transitions %+v", ts)
	}
	_ = f2
}

func TestSelectSkipsWhatWaitsOnInProgress(t *testing.T) {
	f := mustRoot(t)
	f, dep := mustAdd(t, f, "q_001", in("dep", 0.9))
	f, _ = mustAdd(t, f, "q_001", AddInput{Question: "waits", Priority: 0.95, DependsOn: []string{dep}})
	f, other := mustAdd(t, f, "q_001", in("other", 0.2))
	f, q, _ := Select(f) // takes dep (for waits)
	if q.ID != dep {
		t.Fatalf("got %s", q.ID)
	}
	if _, q, _ := Select(f); q == nil || q.ID != other {
		t.Fatalf("want %s, got %+v", other, q)
	}
}

func inProgress(t *testing.T, f Frontier, id string) Frontier {
	t.Helper()
	q, _ := f.Get(id)
	q, _ = move(q, StatusInProgress, TriggerSelected, ActorCode, "", "")
	return f.with(q)
}

func TestResolve(t *testing.T) {
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("a", 0.5))
	if _, _, err := Resolve(f, a, ResolveInput{Answer: "x"}); !errors.Is(err, ErrBadStatus) {
		t.Fatalf("pending cannot be resolved: %v", err)
	}
	f = inProgress(t, f, a)
	if _, _, err := Resolve(f, a, ResolveInput{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty answer: %v", err)
	}
	f, ts, err := Resolve(f, a, ResolveInput{Answer: "yes", ClaimIDs: []string{"c_001", "c_002"}, Reason: "why not"})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := f.Get(a)
	if q.Status != StatusResolved || q.Answer != "yes [c_001, c_002] (reason: why not)" || ts[0].Trigger != TriggerResolved || !strings.Contains(ts[0].Reason, "why not") {
		t.Fatalf("%+v %+v", q, ts)
	}
	if refs := ClaimRefs(q.Answer); len(refs) != 2 {
		t.Fatalf("refs %v", refs)
	}
	// No criteria, no resolution.
	f, b := mustAdd(t, f, "q_001", AddInput{Question: "b", Priority: 0.5})
	f = inProgress(t, f, b)
	if _, _, err := Resolve(f, b, ResolveInput{Answer: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("criteria: %v", err)
	}
}

func TestRetryAndAttemptCap(t *testing.T) {
	limits := DefaultLimits()
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("a", 0.5))
	for i := 1; i <= limits.MaxAttempts; i++ {
		f = inProgress(t, f, a)
		var err error
		f, _, err = Retry(f, a, fmt.Sprintf("query number %d", i), "nothing", "", limits)
		if err != nil {
			t.Fatal(err)
		}
		if q, _ := f.Get(a); q.Status != StatusPending || q.AttemptCount() != i {
			t.Fatalf("after retry %d: %+v", i, q)
		}
	}
	f = inProgress(t, f, a)
	if _, _, err := Retry(f, a, "query number 1", "x", "", limits); !errors.Is(err, ErrInvalid) {
		t.Fatalf("at the cap an assumption is required: %v", err)
	}
	f, ts, err := Retry(f, a, "whatever", "still nothing", "assume 2x", limits)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := f.Get(a)
	if q.Status != StatusUnresolvable || q.Answer != "assume 2x" || ts[0].Trigger != TriggerAttemptsExhausted || ts[0].Actor != ActorCode || q.AttemptCount() != 4 {
		t.Fatalf("%+v %+v", q, ts)
	}
}

func TestRetryRejectsRepeatedQuery(t *testing.T) {
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("a", 0.5))
	f = inProgress(t, f, a)
	f, _, _ = Retry(f, a, "Memory use of U-Net?", "no", "", DefaultLimits())
	f = inProgress(t, f, a)
	if _, _, err := Retry(f, a, "memory use of u-net", "no", "", DefaultLimits()); !errors.Is(err, ErrDuplicateQuestion) {
		t.Fatalf("got %v", err)
	}
	if _, _, err := Retry(f, a, "other", "  ", "", DefaultLimits()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("why is required: %v", err)
	}
}

func TestReopen(t *testing.T) {
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("a", 0.5))
	if _, _, err := Reopen(f, a, "why", 0, ""); !errors.Is(err, ErrBadStatus) {
		t.Fatalf("pending: %v", err)
	}
	f = inProgress(t, f, a)
	f, _, _ = Resolve(f, a, ResolveInput{Answer: "yes"})
	f, ts, err := Reopen(f, a, "contradicted", 0, "ch_001")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := f.Get(a)
	if q.Status != StatusPending || q.Priority != 0.7 || q.Answer != "" || ts[0].Trigger != TriggerReopened || ts[0].CausedBy != "ch_001" {
		t.Fatalf("%+v %+v", q, ts)
	}
	if _, _, err := Reopen(f, a, "", 0, ""); !errors.Is(err, ErrBadStatus) {
		t.Fatalf("%v", err)
	}
}

func TestSetPriorityRaisesDependencies(t *testing.T) {
	f := mustRoot(t)
	f, dep := mustAdd(t, f, "q_001", in("dep", 0.2))
	f, top := mustAdd(t, f, "q_001", AddInput{Question: "top", Priority: 0.3, DependsOn: []string{dep}})
	f, ts, err := SetPriority(f, top, 0.8, "important")
	if err != nil {
		t.Fatal(err)
	}
	d, _ := f.Get(dep)
	if d.Priority != 0.8 || len(ts) != 2 || ts[0].Trigger != TriggerReprioritized || ts[1].QuestionID != dep {
		t.Fatalf("%+v %+v", d, ts)
	}
	if f2, ts, _ := SetPriority(f, top, 0.8, "same"); len(ts) != 0 || len(f2.Questions) != len(f.Questions) {
		t.Fatalf("same priority is a no-op")
	}
	if _, _, err := SetPriority(f, top, 2, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
}

func TestPruneBranchAndReblock(t *testing.T) {
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("a", 0.5))
	f, a1 := mustAdd(t, f, a, in("a1", 0.5))
	f, a11 := mustAdd(t, f, a1, in("a11", 0.5))
	f, other := mustAdd(t, f, "q_001", AddInput{Question: "other", Priority: 0.5, DependsOn: []string{a1}})
	f = inProgress(t, f, a)
	f, ts, err := PruneBranch(f, a, "irrelevant")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, a1, a11} {
		if q, _ := f.Get(id); q.Status != StatusPruned {
			t.Fatalf("%s: %s", id, q.Status)
		}
	}
	o, _ := f.Get(other)
	if o.Status != StatusBlocked {
		t.Fatalf("other should be blocked: %s", o.Status)
	}
	triggers := map[string]Trigger{}
	for _, tr := range ts {
		triggers[tr.QuestionID] = tr.Trigger
	}
	if triggers[a] != TriggerPrunedScope || triggers[a1] != TriggerPrunedBranch || triggers[a11] != TriggerPrunedBranch || triggers[other] != TriggerDependencyFailed || len(ts) != 4 {
		t.Fatalf("%+v", ts)
	}
	if q, _ := f.Get(a1); !strings.Contains(q.Answer, "irrelevant") {
		t.Fatalf("reason stored: %q", q.Answer)
	}
	if _, _, err := PruneBranch(f, "q_001", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("root: %v", err)
	}
	if _, _, err := PruneBranch(f, a, "x"); !errors.Is(err, ErrBadStatus) {
		t.Fatalf("already pruned: %v", err)
	}
	// Unblock: a blocked question whose dependencies are resolved again becomes pending.
	g := f.with(func() Question { q, _ := f.Get(a1); q.Status = StatusResolved; return q }())
	g, ts2 := Reblock(g)
	if o, _ := g.Get(other); o.Status != StatusPending || len(ts2) != 1 || ts2[0].Trigger != TriggerDependencyMet {
		t.Fatalf("%+v %+v", o, ts2)
	}
	// Ids are never reused.
	g, id := mustAdd(t, g, "q_001", in("new", 0.5))
	if id != "q_006" {
		t.Fatalf("id reused: %s", id)
	}
}

func TestSummary(t *testing.T) {
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("alpha question", 0.9))
	f, _ = mustAdd(t, f, "q_001", in("beta question", 0.6))
	f, c := mustAdd(t, f, "q_001", AddInput{Question: "gamma question", Priority: 0.5, DependsOn: []string{a}})
	f = inProgress(t, f, a)
	f, _, _ = Retry(f, a, "q one", "meh", "", DefaultLimits())
	f = inProgress(t, f, a)
	got := Summary(f, a, DefaultLimits())
	for _, want := range []string{"Frontier: 2 in_progress, 2 pending  (max_depth 3)", "Current: [q_002] (P 0.90, depth 1) alpha question", "criteria: criteria for alpha question",
		`tried: "q one"`, "Next up:", "- [q_003] (P 0.60) beta question"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, c+"]") && strings.Contains(got, "Next up:\n- ["+c) {
		t.Errorf("a question that waits is not next up:\n%s", got)
	}
	// Blocked line.
	b, _, _ := PruneBranch(f, a, "gone")
	if s := Summary(b, "", DefaultLimits()); !strings.Contains(s, "Blocked: [q_004] waits on q_002") {
		t.Errorf("blocked line:\n%s", s)
	}
}

// The most important invariant of the stage: whatever sequence of operations is applied, the latest transition of every question equals its
// current status, the seqs of a question are 1, 2, 3, ..., and each transition starts where the one before ended.
func TestTransitionInvariantRandomOperations(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		r := rand.New(rand.NewSource(seed))
		limits := Limits{MaxDepth: 3, MaxAttempts: 3, MaxQuestions: 25, NotesMaxTokens: 6000}
		f, ts, _ := NewRoot("objective")
		log := ts
		pick := func() string { return f.Questions[r.Intn(len(f.Questions))].ID }
		apply := func(nf Frontier, nts []Transition, err error) {
			if err == nil {
				f, log = nf, append(log, nts...)
			}
		}
		for step := 0; step < 80; step++ {
			switch r.Intn(9) {
			case 0, 1:
				var deps []string
				if r.Intn(3) == 0 {
					deps = []string{pick()}
				}
				apply(Add(f, pick(), AddInput{Question: fmt.Sprintf("question %d %d", seed, step), Priority: r.Float64(), DependsOn: deps,
					BlocksParent: r.Intn(4) == 0, ValidationCriteria: "c"}, limits))
			case 2, 3:
				nf, q, nts := Select(f)
				if q != nil {
					f, log = nf, append(log, nts...)
				}
			case 4:
				apply(Resolve(f, pick(), ResolveInput{Answer: "a"}))
			case 5:
				apply(Retry(f, pick(), fmt.Sprintf("query %d", step), "why", "assume", limits))
			case 6:
				apply(Reopen(f, pick(), "why", r.Float64(), ""))
			case 7:
				apply(SetPriority(f, pick(), r.Float64(), "why"))
			case 8:
				apply(PruneBranch(f, pick(), "why"))
			}
			checkInvariant(t, seed, step, f, log)
		}
	}
}

func checkInvariant(t *testing.T, seed int64, step int, f Frontier, log []Transition) {
	t.Helper()
	last := map[string]Transition{}
	for _, tr := range log {
		prev, seen := last[tr.QuestionID]
		switch {
		case !seen && (tr.Seq != 1 || tr.From != "" || tr.Trigger != TriggerCreated):
			t.Fatalf("seed %d step %d: first transition of %s is %+v", seed, step, tr.QuestionID, tr)
		case seen && (tr.Seq != prev.Seq+1 || tr.From != prev.To):
			t.Fatalf("seed %d step %d: %s: %+v after %+v", seed, step, tr.QuestionID, tr, prev)
		case tr.Trigger == TriggerReprioritized && tr.From != tr.To:
			t.Fatalf("seed %d step %d: priority transition changes status: %+v", seed, step, tr)
		case tr.Trigger != TriggerReprioritized && seen && tr.From == tr.To:
			t.Fatalf("seed %d step %d: status transition without a change: %+v", seed, step, tr)
		}
		last[tr.QuestionID] = tr
	}
	for _, q := range f.Questions {
		tr, ok := last[q.ID]
		if !ok || tr.To != q.Status || tr.Seq != q.TransSeq || tr.Priority != q.Priority {
			t.Fatalf("seed %d step %d: %s is %s (seq %d, P %v) but its last transition is %+v", seed, step, q.ID, q.Status, q.TransSeq, q.Priority, tr)
		}
	}
	if len(last) != len(f.Questions) {
		t.Fatalf("seed %d step %d: transitions for %d questions, frontier has %d", seed, step, len(last), len(f.Questions))
	}
}
