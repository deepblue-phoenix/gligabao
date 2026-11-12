package investigate_core

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// --- helpers ---

func callRaw(t *testing.T, s State, name string, args any) State {
	t.Helper()
	s, _ = ok(t, s, name, args)
	return s
}

// ran says that the search of a challenge was made (the effect layer links it).
func ran(t *testing.T, s State, id string) State {
	t.Helper()
	var err error
	if s.Frontier, s.Challenges, err = LinkAttempt(s.Frontier, s.Challenges, id, "web_search", "found nothing"); err != nil {
		t.Fatal(err)
	}
	return s
}

func withQuestion(t *testing.T) State {
	t.Helper()
	s := newState(t)
	s = callRaw(t, s, "add_question", m{"parent_id": "q_001", "question": "weight size?", "priority": 0.8, "validation_criteria": "bytes"})
	s = callRaw(t, s, "frontier_next", m{})
	return s
}

func testPrompt() Prompt {
	var cat []ToolSpec
	for _, n := range []string{"web_search", "fetch_page"} {
		cat = append(cat, ToolSpec{
			Name:        n,
			Description: n,
			Schema:      obj(props{}),
		})
	}
	for _, tl := range LoopRegistry().Tools() {
		cat = append(cat, ToolSpec{
			Name:        tl.Name,
			Description: tl.Description,
			Schema:      tl.Schema,
		})
	}
	return Prompt{
		System:   "You are a researcher.",
		Task:     "Does X fit in 16 GB?",
		Catalog:  cat,
		External: []string{"web_search", "fetch_page"},
	}
}

func names(r Request) []string {
	out := make([]string, len(r.Tools))
	for i, t := range r.Tools {
		out[i] = t.Name
	}
	return out
}

func turnWith(n int, calls ...Call) Turn {
	return Turn{
		N:       n,
		Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"x"}`)},
		Calls:   calls,
	}
}

func fetchCall(id string, size int) Call {
	return Call{
		ID:     id,
		Name:   "fetch_page",
		Input:  json.RawMessage(`{"url":"https://a.com/p"}`),
		Output: strings.Repeat("x", size),
	}
}

// --- loop state ---

func TestPhaseTransitions(t *testing.T) {
	ls := StartLoop()
	if ls.Phase != PhaseExploration || ls.StopReason != "" {
		t.Fatalf("start: %+v", ls)
	}
	lim := DefaultLimits()
	if ls.CanReopen(lim) {
		t.Error("exploration cannot reopen")
	}
	ls = ls.AddTurn(turnWith(1, fetchCall("a", 10)), []Pending{{
		Turn: 1,
	}})
	cut := ls.ToSynthesis(StopCost)
	if cut.Phase != PhaseSynthesis || cut.StopReason != StopCost || cut.Cut != StopCost || len(cut.Window) != 0 || len(cut.Pending) != 0 {
		t.Fatalf("breaker: %+v", cut)
	}
	if ls.Phase != PhaseExploration || len(ls.Window) != 1 {
		t.Fatal("the transition changed its argument")
	}
	if g := ls.ToSynthesis(""); g.Cut != "" || g.StopReason != "" {
		t.Fatalf("gate: %+v", g)
	}
	syn := ls.ToSynthesis(StopFrontierExhausted)
	if syn.Cut != "" {
		t.Error("exhaustion is not a cut")
	}
	if !syn.CanReopen(lim) {
		t.Fatal("synthesis may reopen once")
	}
	back := syn.Reopen()
	if back.Phase != PhaseExploration || back.Reopens != 1 || back.StopReason != "" || back.CanReopen(lim) {
		t.Fatalf("reopen: %+v", back)
	}
	if again := back.ToSynthesis(""); again.CanReopen(lim) {
		t.Error("max_reopens is 1")
	}
	if f := syn.Finalized(); !f.Done || f.StopReason != StopFrontierExhausted {
		t.Errorf("a cut reason is kept: %+v", f)
	}
	if f := ls.ToSynthesis("").Finalized(); f.StopReason != StopFinalized {
		t.Errorf("finalized: %+v", f)
	}
	if f := syn.Failed(StopTokens); !f.Done || f.StopReason != StopTokens {
		t.Errorf("failed: %+v", f)
	}
	spent := StartLoop().AfterModel(100, 20, 0.5).Spend(5, 5, 0.25)
	if spent.Turns != 1 || spent.Tokens != 130 || spent.CostUSD != 0.75 {
		t.Errorf("spend: %+v", spent)
	}
}

// --- eviction ---

func TestEvict(t *testing.T) {
	ls := StartLoop()
	for i := 1; i <= 6; i++ {
		ls = ls.AddTurn(turnWith(i), nil)
	}
	if got := Evict(ls, 2); len(got.Window) != 2 || got.Window[0].N != 5 || got.Window[1].N != 6 {
		t.Fatalf("committed turns older than keep_turns go: %+v", got.Window)
	}
	// An uncommitted result survives eviction; a committed one does not.
	held := ls.AddTurn(turnWith(7, fetchCall("c", 5000)), []Pending{{
		CallID: "c",
		Tool:   "fetch_page",
		Turn:   7,
		Size:   5000,
	}})
	for i := 8; i <= 11; i++ {
		held = held.AddTurn(turnWith(i), nil)
	}
	got := Evict(held, 2)
	if !slices.ContainsFunc(got.Window, func(x Turn) bool { return x.N == 7 }) || len(got.Window) != 3 {
		t.Fatalf("uncommitted survives: %+v", turnNumbers(got.Window))
	}
	got = Evict(held.CommitUpTo(7), 2)
	if len(got.Window) != 2 || got.Window[0].N != 10 {
		t.Fatalf("committed goes: %+v", turnNumbers(got.Window))
	}
	if fewer := Evict(StartLoop().AddTurn(turnWith(1), nil), 2); len(fewer.Window) != 1 {
		t.Error("nothing to evict")
	}
}

func turnNumbers(w []Turn) []int {
	out := make([]int, len(w))
	for i, t := range w {
		out[i] = t.N
	}
	return out
}

func TestNeedsExtraction(t *testing.T) {
	ls := StartLoop().AfterModel(1, 1, 0)
	small := fetchCall("a", 100)
	big := fetchCall("b", 5000)
	if len(PendingOf(turnWith(1, small, Call{
		ID:    "e",
		Name:  "fetch_page",
		IsErr: true,
	}, Call{
		ID:   "n",
		Name: "notes_read",
	}), "q_002")) != 1 {
		t.Error("only a successful raw tool result is pending")
	}
	ls = ls.AddTurn(turnWith(1, small), PendingOf(turnWith(1, small), "q_002"))
	if _, ok := NeedsExtraction(ls, 4000, 2); ok {
		t.Error("a page below the threshold is not forced")
	}
	ls = ls.AfterModel(1, 1, 0).AddTurn(turnWith(2, big), PendingOf(turnWith(2, big), "q_002"))
	p, ok := NeedsExtraction(ls, 4000, 2)
	if !ok || p.CallID != "b" && p.CallID != "a" {
		t.Fatalf("above the threshold it is: %+v %v", p, ok)
	}
	// A small result that would leave the window is forced too: the commit point comes before the window moves on.
	aged := StartLoop().AfterModel(1, 1, 0).AfterModel(1, 1, 0)
	aged = aged.AddTurn(turnWith(1, small), PendingOf(turnWith(1, small), "q_002"))
	if _, ok := NeedsExtraction(aged, 4000, 2); !ok {
		t.Error("a small pending result about to be evicted is extracted")
	}
	if Commits(turnWith(3, Call{
		Name: "notes_append",
	}), TurnExplore) != true || Commits(turnWith(3, Call{
		Name:  "notes_append",
		IsErr: true,
	}), TurnExplore) {
		t.Error("a notes write that succeeded commits")
	}
	if !Commits(turnWith(3), TurnExtract) || Commits(turnWith(3), TurnExplore) {
		t.Error("an extraction turn commits, a plain one does not")
	}
}

// --- gates ---

func TestExplorationGate(t *testing.T) {
	s := withQuestion(t)
	s.Limits.MinSources, s.Limits.MinDomains = 2, 2
	missing := ExplorationGate(s)
	joined := strings.Join(missing, "\n")
	for _, want := range []string{"root question q_001", "sources", "domains", "Conclusion:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	// Build the passing state step by step.
	s = callRaw(t, s, "notes_append", m{"section": "Sources", "entries": []m{{"url": "https://a.com/x", "title": "A"}, {"url": "https://b.org/y", "title": "B"}}})
	s = callRaw(t, s, "notes_append", m{"section": "Unverified", "entries": []m{{"text": "weights are 8 GB", "question_id": "q_002",
		"sources": []m{{"source_id": "s_001", "independent": true}, {"source_id": "s_002", "independent": true}}}}})
	s = callRaw(t, s, "challenge", m{"target_type": "claim", "target_id": "c_001", "negation": "weights are not 8 GB", "strategy": "negation", "expected_if_false": "e", "query": "weights not 8 GB errata"})
	if g := strings.Join(ExplorationGate(s), "\n"); !strings.Contains(g, "challenge ch_001 is open") {
		t.Errorf("an open challenge blocks:\n%s", g)
	}
	s = ran(t, s, "ch_001")
	s = callRaw(t, s, "record_challenge_result", m{"challenge_id": "ch_001", "outcome": "survived", "evidence": "none"})
	s = callRaw(t, s, "notes_update", m{"id": "c_001", "change": m{"status": "VERIFIED"}})
	s = callRaw(t, s, "resolve_question", m{"id": "q_002", "answer": "8 GB", "claim_ids": []string{"c_001"}, "reason": "shallow"})
	// A question resolved without a VERIFIED claim blocks the gate, unless it stores a reason.
	bare := s
	q2, _ := bare.Frontier.Get("q_002")
	q2.Answer = "8 GB"
	bare.Frontier = bare.Frontier.with(q2)
	if g := strings.Join(ExplorationGate(bare), "\n"); !strings.Contains(g, "q_002 is resolved without a VERIFIED claim") {
		t.Errorf("a resolved question needs a VERIFIED claim:\n%s", g)
	}
	q2.Answer = "8 GB " + ReasonMarker + " no better source)"
	bare.Frontier = bare.Frontier.with(q2)
	if g := strings.Join(ExplorationGate(bare), "\n"); strings.Contains(g, "q_002 is resolved without") {
		t.Errorf("a stored reason is enough:\n%s", g)
	}
	s = callRaw(t, s, "resolve_question", m{"id": "q_001", "answer": "fits [c_001]", "claim_ids": []string{"c_001"}, "reason": "test"})
	s = callRaw(t, s, "notes_scratch", m{"text": "Conclusion: it fits."})
	if g := strings.Join(ExplorationGate(s), "\n"); !strings.Contains(g, "no closed challenge with outcome survived") {
		t.Errorf("the conclusion needs a survived challenge:\n%s", g)
	}
	s = callRaw(t, s, "challenge", m{"target_type": "conclusion", "negation": "it does not fit", "strategy": "negation", "expected_if_false": "e", "query": "does not fit 16 GB official"})
	s = ran(t, s, "ch_002")
	s = callRaw(t, s, "record_challenge_result", m{"challenge_id": "ch_002", "outcome": "survived"})
	if g := ExplorationGate(s); len(g) != 0 {
		t.Fatalf("the gate should pass: %v", g)
	}
	tool, _ := LoopRegistry().Get(ToolFinishExploration)
	if _, err := tool.Handler(s, nil); err != nil {
		t.Fatal(err)
	}
	// An unaddressed contradiction and a pending question of high priority block it again.
	c := s
	c.Notes.Contradictions = []Contradiction{{
		A: "c_001",
		B: "c_001",
	}}
	if g := strings.Join(ExplorationGate(c), "\n"); !strings.Contains(g, "contradiction c_001 vs c_001") {
		t.Errorf("contradiction: %s", g)
	}
	p := callRaw(t, s, "add_question", m{"parent_id": "q_001", "question": "what else", "priority": 0.7, "validation_criteria": "c"})
	if _, err := tool.Handler(p, nil); err == nil || !strings.Contains(err.Error(), "pending with priority 0.70") {
		t.Errorf("a pending question at or above gate_priority blocks: %v", err)
	}
	low := callRaw(t, s, "add_question", m{"parent_id": "q_001", "question": "minor", "priority": 0.2, "validation_criteria": "c"})
	if g := ExplorationGate(low); len(g) != 0 {
		t.Errorf("below gate_priority it does not: %v", g)
	}
	// A weakened conclusion fails the gate.
	w := s
	w.Challenges = slices.Clone(s.Challenges)
	w.Challenges[1].Outcome = OutcomeWeakened
	if g := ExplorationGate(w); len(g) == 0 {
		t.Error("a weakened conclusion keeps the gate closed")
	}
}

func TestFinalizationGate(t *testing.T) {
	s := withQuestion(t)
	s = callRaw(t, s, "notes_append", m{"section": "Sources", "entries": []m{{"url": "https://a.com/x", "title": "A"}}})
	s = callRaw(t, s, "notes_append", m{"section": "Unverified", "entries": []m{{"text": "weights are 8 GB", "question_id": "q_002", "sources": []m{{"source_id": "s_001"}}}}})
	s = callRaw(t, s, "notes_append", m{"section": "Unverified", "entries": []m{{"text": "weights are 9 GB", "question_id": "q_002", "sources": []m{{"source_id": "s_001"}}}}})
	s = callRaw(t, s, "notes_update", m{"id": "c_002", "change": m{"status": "REFUTED", "source_id": "s_001", "reason": "wrong"}})
	s = callRaw(t, s, "retry_question", m{"id": "q_002", "new_query": "other", "why": "w"})
	good := "Summary: 8 GB (c_001, s_001).\n## Gaps\nnone"
	if p := FinalizationGate(s, good); len(p) != 0 {
		t.Fatalf("good report: %v", p)
	}
	for report, want := range map[string]string{
		"": "empty", "uses s_009": "s_009", "uses c_002": "REFUTED", "uses c_077": "c_077",
	} {
		if p := strings.Join(FinalizationGate(s, report), "\n"); !strings.Contains(p, want) {
			t.Errorf("%q: %s", report, p)
		}
	}
	// An unresolvable question must be in the report.
	u := s
	u.Frontier, _, _ = Add(u.Frontier, "q_001", AddInput{
		Question:           "hard one",
		Priority:           0.5,
		ValidationCriteria: "c",
	}, u.Limits)
	q, _ := u.Frontier.Get("q_003")
	q.Status = StatusUnresolvable
	u.Frontier = u.Frontier.with(q)
	if p := strings.Join(FinalizationGate(u, good), "\n"); !strings.Contains(p, "q_003") {
		t.Errorf("gap: %s", p)
	}
	if p := FinalizationGate(u, good+"\n- q_003: could not be settled"); len(p) != 0 {
		t.Errorf("gap listed: %v", p)
	}
	if got := RenderReport("see s_001, [s_001](https://a.com/x) and s_009", s.Notes); got != "see [s_001](https://a.com/x), [s_001](https://a.com/x) and s_009" {
		t.Errorf("render: %s", got)
	}
}

func TestFinalCheckQueuesUnverifiedClaims(t *testing.T) {
	s := withQuestion(t)
	s = callRaw(t, s, "notes_append", m{"section": "Sources", "entries": []m{{"url": "https://a.com/x", "title": "A"}}})
	s = callRaw(t, s, "notes_append", m{"section": "Unverified", "entries": []m{{"text": "weights are 8 GB", "question_id": "q_002", "sources": []m{{"source_id": "s_001"}}}}})
	s = callRaw(t, s, "resolve_question", m{"id": "q_002", "answer": "8 GB", "claim_ids": []string{"c_001"}, "reason": "r"})
	next, ts, ids := FinalCheck(s)
	if len(ids) != 1 || len(ts) == 0 {
		t.Fatalf("one verification question: %v", ids)
	}
	q, _ := next.Frontier.Get(ids[0])
	if q.Status != StatusPending || q.Priority != 0.6 || !strings.Contains(q.Question, "c_001") {
		t.Errorf("%+v", q)
	}
	// It ends: the same claim is not queued twice.
	if _, _, again := FinalCheck(next); len(again) != 0 {
		t.Errorf("queued twice: %v", again)
	}
}

// --- breakers ---

func TestCheckBreakersAndReserve(t *testing.T) {
	lim := Limits{
		MaxIterations:    20,
		MaxTotalTokens:   1000,
		MaxCostUSD:       1,
		SynthesisReserve: 0.2,
	}
	if n := ExplorationTurns(20, 0.2); n != 16 {
		t.Fatalf("reserve of turns: %d", n)
	}
	explore := func(turns, tokens int, cost float64) LoopState {
		return LoopState{
			Phase:   PhaseExploration,
			Turns:   turns,
			Tokens:  tokens,
			CostUSD: cost,
		}
	}
	if bs := CheckBreakers(explore(15, 799, 0.79), lim); len(bs) != 0 {
		t.Fatalf("below every limit: %v", bs)
	}
	for _, c := range []struct {
		ls   LoopState
		want string
	}{
		{explore(16, 0, 0), "iterations"}, {explore(0, 800, 0), "tokens"}, {explore(0, 0, 0.8), "cost"},
	} {
		bs := CheckBreakers(c.ls, lim)
		if len(bs) != 1 || bs[0].Name != c.want || bs[0].StopReason() != "breaker:"+c.want {
			t.Errorf("%s: %v", c.want, bs)
		}
	}
	// Synthesis uses the whole limit: the same counts that stop exploration do not stop it.
	syn := func(turns, tokens int, cost float64) LoopState {
		return LoopState{
			Phase:   PhaseSynthesis,
			Turns:   turns,
			Tokens:  tokens,
			CostUSD: cost,
		}
	}
	if bs := CheckBreakers(syn(16, 800, 0.8), lim); len(bs) != 0 {
		t.Errorf("the reserve is spendable by synthesis: %v", bs)
	}
	if bs := CheckBreakers(syn(20, 1000, 1), lim); len(bs) != 3 {
		t.Errorf("the reserve is exhausted: %v", bs)
	}
	if bs := CheckBreakers(explore(1000, 1<<30, 1e6), Limits{}); len(bs) != 0 {
		t.Errorf("0 is no limit: %v", bs)
	}
	if !IsLastTurn(syn(19, 0, 0), lim) || IsLastTurn(syn(18, 0, 0), lim) {
		t.Error("the last turn is the one that reaches max_iterations")
	}
}

func TestCutShortNotesTheGaps(t *testing.T) {
	s := withQuestion(t)
	s = callRaw(t, s, "add_question", m{"parent_id": "q_001", "question": "left over", "priority": 0.3, "validation_criteria": "c"})
	cut := CutShort(s, Breaker{
		Name:  "cost",
		Limit: 1,
		Used:  1.2,
	})
	if !strings.Contains(cut.Notes.Scratch, "cut short by breaker: cost") {
		t.Errorf("scratch: %q", cut.Notes.Scratch)
	}
	gaps := Render(cut.Notes)
	for _, id := range []string{"q_002", "q_003"} {
		if !strings.Contains(gaps, "open gap ("+id+")") {
			t.Errorf("%s not listed:\n%s", id, gaps)
		}
	}
}

// --- turn builder ---

func TestBuildRequestIsBoundedByTheWindow(t *testing.T) {
	s := withQuestion(t)
	p := testPrompt()
	short := StartLoop()
	long := StartLoop()
	for i := 1; i <= 200; i++ { // a long history: committed turns
		long = long.AfterModel(1, 1, 0).AddTurn(turnWith(i, Call{
			ID:     fmt.Sprintf("c%d", i),
			Name:   "notes_read",
			Output: strings.Repeat("old output ", 50),
		}), nil)
		long = Evict(long, 2)
	}
	for i := 199; i <= 200; i++ {
		short = short.AddTurn(turnWith(i, Call{
			ID:     fmt.Sprintf("c%d", i),
			Name:   "notes_read",
			Output: strings.Repeat("old output ", 50),
		}), nil)
	}
	a, b := BuildRequest(p, TurnExplore, short, s), BuildRequest(p, TurnExplore, long, s)
	if a.Size() != b.Size() {
		t.Fatalf("the prompt grows with the history: %d vs %d", a.Size(), b.Size())
	}
	if len(b.Messages) != 1+2*2 {
		t.Errorf("task + the last two turns: %d messages", len(b.Messages))
	}
	if first := b.Messages[0].(map[string]any); first["role"] != "user" {
		t.Error("the first message is the task")
	}
	if last := b.Messages[len(b.Messages)-1].(map[string]any); last["role"] != "user" {
		t.Error("the last message is the user's")
	}
}

func TestBuildRequestContents(t *testing.T) {
	s := withQuestion(t)
	s = callRaw(t, s, "notes_append", m{"section": "Sources", "entries": []m{{"url": "https://a.com/x", "title": "A"}}})
	s = callRaw(t, s, "notes_append", m{"section": "Unverified", "entries": []m{{"text": "weights are 8 GB", "question_id": "q_002", "sources": []m{{"source_id": "s_001"}}}}})
	s = callRaw(t, s, "challenge", m{"target_type": "claim", "target_id": "c_001", "negation": "weights are not 8 GB", "strategy": "negation", "expected_if_false": "e", "query": "weights not 8 GB errata"})
	p := testPrompt()
	req := BuildRequest(p, TurnExplore, StartLoop(), s)
	text, _ := json.Marshal(req.Messages)
	for _, want := range []string{"Does X fit in 16 GB?", "weights are 8 GB", "Current question", "[q_002]", "criteria: bytes", "Open challenge [ch_001]", "depth 1", "weights not 8 GB errata"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("the explore prompt lacks %q", want)
		}
	}
	if !strings.HasPrefix(req.System, "You are a researcher.") || !strings.Contains(req.System, "Never write the report") {
		t.Errorf("system: %s", req.System)
	}
	if n := names(req); !slices.Contains(n, "web_search") || !slices.Contains(n, ToolFinishExploration) || slices.Contains(n, "frontier_next") || slices.Contains(n, ToolFinalizeReport) {
		t.Errorf("explore tools: %v", n)
	}
	// Forced extraction: only the notes tools, and the instruction names the question.
	ls := StartLoop().AfterModel(1, 1, 0)
	ls = ls.AddTurn(turnWith(1, fetchCall("a", 5000)), PendingOf(turnWith(1, fetchCall("a", 5000)), "q_002"))
	if NextKind(ls, s) != TurnExtract {
		t.Fatal("a large result forces an extraction")
	}
	ex := BuildRequest(p, TurnExtract, ls, s)
	if n := names(ex); !slices.Equal(n, notesToolNames) {
		t.Errorf("extraction tools: %v", n)
	}
	if text, _ := json.Marshal(ex.Messages); !strings.Contains(string(text), "Extract what matters for question q_002") {
		t.Errorf("extraction instruction: %s", text)
	}
	// Synthesis: no search tool, the gaps and the cut are in the prompt, the last turn is forced.
	cutState := StartLoop().ToSynthesis(StopCost)
	s2 := CutShort(s, Breaker{
		Name: "cost",
	})
	syn := BuildRequest(p, TurnSynthesize, cutState, s2)
	if n := names(syn); slices.Contains(n, "web_search") || slices.Contains(n, "fetch_page") || slices.Contains(n, "challenge") || !slices.Contains(n, ToolFinalizeReport) || !slices.Contains(n, "reopen_question") {
		t.Errorf("synthesis tools: %v", n)
	}
	if text, _ := json.Marshal(syn.Messages); !strings.Contains(string(text), "cut short by `breaker:cost`") || !strings.Contains(string(text), "still") {
		t.Errorf("synthesis prompt: %s", text)
	}
	if syn.ForceTool != "" {
		t.Error("only the last turn is forced")
	}
	last := BuildRequest(p, TurnFinal, cutState, s2)
	if pl := last.Payload(); pl["tool_choice"].(map[string]any)["name"] != ToolFinalizeReport {
		t.Errorf("last turn: %v", pl["tool_choice"])
	}
	// Planning: the first level is asked for and frontier_next is there for the atomic case.
	root := newState(t)
	if NextKind(StartLoop(), root) != TurnPlanning {
		t.Error("planning while the root has no child")
	}
	if n := names(BuildRequest(p, TurnPlanning, StartLoop(), root)); !slices.Contains(n, "add_question") || !slices.Contains(n, "frontier_next") {
		t.Errorf("planning tools: %v", n)
	}
}

func TestNextKindConsolidation(t *testing.T) {
	s := withQuestion(t)
	s.Limits.NotesMaxTokens = 10
	ls := StartLoop()
	if NextKind(ls, s) != TurnConsolidate {
		t.Fatal("over budget: consolidate")
	}
	size := TokenEstimate(s.Notes)
	// One turn, and a second only if it did not reduce the size.
	reduced := AfterConsolidation(ls, size, size-5, s.Limits)
	if NeedsConsolidation(reduced, s) != false && size-5 >= size {
		t.Fatal("unreachable")
	}
	still := AfterConsolidation(ls, size, size, s.Limits)
	if !NeedsConsolidation(still, s) {
		t.Error("it did not reduce: a second one")
	}
	twice := AfterConsolidation(still, size, size, s.Limits)
	if NeedsConsolidation(twice, s) {
		t.Error("no third")
	}
	s.Limits.NotesMaxTokens = 6000
	if NextKind(twice, s) == TurnConsolidate || AfterConsolidation(twice, size, 1, s.Limits).Consol != 0 {
		t.Error("under budget resets")
	}
}

func TestReminderRepeatsTheOpenFrontier(t *testing.T) {
	s := withQuestion(t)
	r := Reminder(TurnExplore, s)
	if !strings.Contains(r, "does not end") || !strings.Contains(r, "q_002") {
		t.Errorf("reminder: %s", r)
	}
	if !strings.Contains(Reminder(TurnSynthesize, s), "finalize_report") {
		t.Error("synthesis reminder")
	}
}

// --- the invariant with the loop's own operations ---

// The loop adds its own changes (the final check, the cut note, reblocking) to the tools of stage 1: whatever random sequence of them is
// applied, the transition log keeps the invariant of stage 1.
func TestTransitionInvariantWithLoopOperations(t *testing.T) {
	for seed := int64(0); seed < 150; seed++ {
		r := rand.New(rand.NewSource(seed))
		s := newState(t)
		s.Limits.MaxAttempts, s.Limits.MaxQuestions = 3, 20
		_, log, _ := NewRoot("x")
		pick := func() string { return s.Frontier.Questions[r.Intn(len(s.Frontier.Questions))].ID }
		run := func(name string, args any) {
			tool, _ := LoopRegistry().Get(name)
			raw, _ := json.Marshal(args)
			res, err := tool.Handler(s, raw)
			if err != nil {
				return
			}
			for _, e := range res.Effects {
				if rt, ok := e.(RecordTransitions); ok {
					log = append(log, rt.Transitions...)
				}
			}
			s = res.State
		}
		for step := 0; step < 70; step++ {
			switch r.Intn(10) {
			case 0, 1:
				run("add_question", m{"parent_id": pick(), "question": fmt.Sprintf("q %d %d", seed, step), "priority": r.Float64(), "validation_criteria": "c"})
			case 2:
				run("frontier_next", m{})
			case 3:
				run("resolve_question", m{"id": pick(), "answer": "a", "reason": "r"})
			case 4:
				run("retry_question", m{"id": pick(), "new_query": fmt.Sprintf("query %d", step), "why": "w", "assumption": "a"})
			case 5:
				run("reopen_question", m{"id": pick(), "why": "w"})
			case 6:
				run("prune_branch", m{"id": pick(), "reason": "r"})
			case 7:
				f, ts := Reblock(s.Frontier)
				s, log = s.with(f), append(log, ts...)
			case 8:
				var ts []Transition
				var ids []string
				s, ts, ids = FinalCheck(s)
				_ = ids
				log = append(log, ts...)
			case 9:
				run("notes_append", m{"section": "Unverified", "entries": []m{{"text": fmt.Sprintf("claim %d", step), "question_id": pick()}}})
			}
			checkInvariant(t, seed, step, s.Frontier, log)
		}
	}
}
