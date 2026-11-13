package investigate_core

import (
	"encoding/json"
	"strings"
	"testing"
)

// stateWith is a state with the questions added under the root (depth 1: they need a challenge to be resolved), and deeper ones if parent
// is given: each entry is (parent, text, priority).
func stateWith(t *testing.T,
	qs ...[3]any) State {
	t.Helper()
	s := newState(t)
	for _, q := range qs {
		f, _, err := Add(s.Frontier,
			q[0].(string),
			in(q[1].(string), q[2].(float64)),
			s.Limits)
		if err != nil {
			t.Fatal(err)
		}
		s.Frontier = f
	}
	return s
}

const goodFindings = `{"status":"answered","answer":"Yes (c1), costs 3 (c2).",
 "claims":[{"key":"c1","text":"It fits","sources":[{"url":"https://a.example/x","title":"A","independent":true,"quote":"fits"}]},
           {"key":"c2","text":"Cost","kind":"datapoint","value":"3","unit":"USD","sources":[{"url":"https://b.example/y"},{"url":"https://a.example/x","stance":"contradicts"}]}],
 "queries":[{"query":"fits in vram","tool":"web_search","outcome":"2 pages"},{"query":"price"}],
 "discovered_gaps":[{"question":"What about 8 GB?","rationale":"edge","priority":0.4}],
 "notes":"one source is old"}`

func parse(t *testing.T,
	raw string) Findings {
	t.Helper()
	f, err := ParseFindings(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseFindings(t *testing.T) {
	f := parse(t, goodFindings)
	if f.Status != FindingAnswered || len(f.Claims) != 2 || len(f.Queries) != 2 || len(f.DiscoveredGaps) != 1 {
		t.Fatalf("%+v", f)
	}
	long := strings.Repeat("x", MaxFindingAnswerChars+1)
	many := strings.Repeat(`{"key":"k","text":"t","sources":[{"url":"https://a.example/x"}]},`, MaxFindingClaims+1)
	for name, c := range map[string]struct{ raw, want string }{
		"not json":           {`{`, "invalid"},
		"bad status":         {`{"status":"done","answer":"a"}`, "status"},
		"empty answer":       {`{"status":"partial","answer":""}`, "answer is empty"},
		"long answer":        {`{"status":"partial","answer":"` + long + `"}`, "compress"},
		"answered no claims": {`{"status":"answered","answer":"a"}`, "at least one claim"},
		"claim no source":    {`{"status":"partial","answer":"a","claims":[{"key":"c1","text":"t"}]}`, "no source"},
		"bad url":            {`{"status":"partial","answer":"a","claims":[{"key":"c1","text":"t","sources":[{"url":"nope"}]}]}`, "not an http(s) URL"},
		"bad stance":         {`{"status":"partial","answer":"a","claims":[{"key":"c1","text":"t","sources":[{"url":"https://a.example/x","stance":"maybe"}]}]}`, "stance"},
		"duplicate key":      {`{"status":"partial","answer":"a","claims":[{"key":"c1","text":"t","sources":[{"url":"https://a.example/x"}]},{"key":"c1","text":"u","sources":[{"url":"https://a.example/x"}]}]}`, "used twice"},
		"too many claims":    {`{"status":"partial","answer":"a","claims":[` + strings.TrimSuffix(many, ",") + `]}`, "at most"},
	} {
		if _, err := ParseFindings(json.RawMessage(c.raw)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v",
				name,
				err)
		}
	}
	// An unresolvable question needs no answer.
	if _, err := ParseFindings(json.RawMessage(`{"status":"unresolvable","answer":"","notes":"nothing"}`)); err != nil {
		t.Error(err)
	}
}

func TestSelectBatch(t *testing.T) {
	s := stateWith(t,
		[3]any{"q_001", "a", 0.9},
		[3]any{"q_001", "b", 0.5},
		[3]any{"q_001", "c", 0.7},
		[3]any{"q_001", "d", 0.8})
	ids := func(qs []Question) string {
		var out []string
		for _, q := range qs {
			out = append(out, q.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(SelectBatch(s.Frontier,
		3,
		0)); got != "q_002,q_005,q_004" { // by priority
		t.Errorf("batch: %s", got)
	}
	if got := ids(SelectBatch(s.Frontier,
		10,
		0.75)); got != "q_002,q_005" {
		t.Errorf("min priority: %s", got)
	}
	// A question that waits on another, one in progress, a blocked one and the root are not taken.
	f, _, err := Add(s.Frontier,
		"q_001",
		AddInput{
			Question:           "e",
			Priority:           0.95,
			ValidationCriteria: "c",
			DependsOn:          []string{"q_002"},
		},
		s.Limits)
	if err != nil {
		t.Fatal(err)
	}
	f = inProgress(t,
		f,
		"q_003")
	if got := ids(SelectBatch(f,
		10,
		0)); got != "q_002,q_005,q_004" {
		t.Errorf("dependencies and in progress: %s", got)
	}
	// One without validation criteria cannot be resolved by anyone: it is not delegated.
	f, _, err = Add(f,
		"q_001",
		AddInput{
			Question: "no criteria",
			Priority: 1,
		},
		s.Limits)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(SelectBatch(f,
		10,
		0)); strings.Contains(got, "q_007") {
		t.Errorf("no criteria: %s", got)
	}
}

func TestDelegateMarksInProgress(t *testing.T) {
	s := stateWith(t,
		[3]any{"q_001", "a", 0.9},
		[3]any{"q_001", "b", 0.5})
	f, ts, err := Delegate(s.Frontier, []string{"q_002", "q_003"})
	if err != nil || len(ts) != 2 || ts[0].Trigger != TriggerDelegated || ts[0].Actor != ActorCode || ts[0].From != StatusPending || ts[0].To != StatusInProgress {
		t.Fatalf("%+v %v",
			ts,
			err)
	}
	if q, _ := f.Get("q_003"); q.Status != StatusInProgress {
		t.Errorf("%+v", q)
	}
	if _, _, err := Delegate(f, []string{"q_002"}); err == nil {
		t.Error("a question in progress cannot be delegated again")
	}
}

func TestWorkerBriefHasTheSliceOfTheNotes(t *testing.T) {
	s := stateWith(t, [3]any{"q_001", "dep", 0.9})
	f := inProgress(t,
		s.Frontier,
		"q_002")
	f, _, err := Resolve(f,
		"q_002",
		ResolveInput{
			Answer: "dep answer",
		})
	if err != nil {
		t.Fatal(err)
	}
	s.Frontier = f
	n, _, err := Append(s.Notes,
		SectionSources,
		[]Entry{{
			URL: "https://a.example/x",
		}})
	if err != nil {
		t.Fatal(err)
	}
	s.Notes, _, err = Append(n,
		SectionUnverified,
		[]Entry{{
			Text:       "known claim",
			QuestionID: "q_002",
			Sources: []Link{{
				SourceID: "s_001",
			}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	s.Notes, _, err = Append(s.Notes,
		SectionUnverified,
		[]Entry{{
			Text:       "other claim",
			QuestionID: "q_009",
			Sources: []Link{{
				SourceID: "s_001",
			}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	s.Frontier, _, err = Add(s.Frontier,
		"q_001",
		AddInput{
			Question:           "target",
			Rationale:          "why",
			Priority:           0.5,
			ValidationCriteria: "crit",
			TargetSources:      []string{"arxiv"},
			DependsOn:          []string{"q_002"},
		},
		s.Limits)
	if err != nil {
		t.Fatal(err)
	}
	s.Frontier, _, _ = AddAttempt(s.Frontier,
		"q_003",
		Attempt{
			Query: "old query",
		})
	brief := WorkerBrief(s,
		"q_003",
		1500)
	for _, want := range []string{"Does X fit in 16 GB of VRAM?", "(q_003, question): target", "Why it matters: why", "It is answered when: crit", "Look at: arxiv", "old query", "q_002 answer: dep answer", "known claim"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q:\n%s",
				want,
				brief)
		}
	}
	if strings.Contains(brief, "other claim") {
		t.Errorf("a claim of an unrelated question is in the brief:\n%s", brief)
	}
	if small := WorkerBrief(s,
		"q_003",
		5); strings.Contains(small, "known claim") || !strings.Contains(small, "target") {
		t.Errorf("the slice is cut to the budget, the question is not:\n%s", small)
	}
}

// delegated is a state whose questions q_002 (depth 1) and q_003 (depth 2, a child of q_002) are with workers.
func delegated(t *testing.T,
	ids ...string) State {
	t.Helper()
	s := stateWith(t,
		[3]any{"q_001", "shallow", 0.9},
		[3]any{"q_002", "deep", 0.8})
	s.Frontier = inProgress(t,
		s.Frontier,
		"q_003")
	s.Frontier = inProgress(t,
		s.Frontier,
		"q_002")
	return s
}

func TestReduceAnsweredResolvesAQuestionThatNeedsNoChallenge(t *testing.T) {
	s := delegated(t)
	next, ts, red := Reduce(s,
		"q_003",
		parse(t, goodFindings))
	q, _ := next.Frontier.Get("q_003")
	if q.Status != StatusResolved || red.Status != StatusResolved || len(ts) < 2 {
		t.Fatalf("question: %+v, %+v",
			q,
			red)
	}
	// The answer cites the ids of the notes, with the claims behind it.
	if !strings.Contains(q.Answer, "(c_001)") || !strings.Contains(q.Answer, "d_001") || !strings.Contains(q.Answer, "[c_001, d_001]") {
		t.Errorf("answer: %q", q.Answer)
	}
	// Claims are UNVERIFIED, tied to the question; the datapoint is a datapoint; sources are shared (one per URL) with their stance.
	c, _ := next.Notes.Claim("c_001")
	d, _ := next.Notes.Claim("d_001")
	if c.Status != ClaimUnverified || c.QuestionID != "q_003" || d.Kind != KindDatapoint || d.Value != "3" || d.Unit != "USD" || len(next.Notes.Sources) != 2 {
		t.Errorf("notes: %+v %+v %+v",
			c,
			d,
			next.Notes.Sources)
	}
	if len(d.Links) != 2 || d.Links[1].Stance != StanceContradicts || !c.Links[0].Independent {
		t.Errorf("links: %+v", d.Links)
	}
	// The queries are attempts, the gap a child added by code, the notes go to the scratch.
	if len(q.Attempts) != 2 || q.Attempts[0].Query != "fits in vram" || q.Attempts[0].Outcome != "2 pages" {
		t.Errorf("attempts: %+v", q.Attempts)
	}
	gap, ok := next.Frontier.Get("q_004")
	if !ok || gap.ParentID != "q_003" || gap.Status != StatusPending || red.GapIDs[0] != "q_004" || !strings.Contains(next.Notes.Scratch, "one source is old") {
		t.Errorf("gap: %+v %+v",
			gap,
			red)
	}
	var found bool
	for _, tr := range ts {
		if tr.QuestionID == "q_004" && tr.Trigger == TriggerCreated && tr.Actor == ActorCode && tr.CausedBy == "q_003" {
			found = true
		}
	}
	if !found {
		t.Errorf("transitions: %+v", ts)
	}
	// Nothing a worker says is VERIFIED.
	for _, c := range append(next.Notes.Claims, next.Notes.Datapoints...) {
		if c.Status != ClaimUnverified {
			t.Errorf("%s is %s",
				c.ID,
				c.Status)
		}
	}
}

func TestReduceAnsweredLeavesAShallowQuestionForTheSupervisor(t *testing.T) {
	s := delegated(t)
	next, ts, red := Reduce(s,
		"q_002",
		parse(t, goodFindings)) // depth 1: its answer needs a challenge
	q, _ := next.Frontier.Get("q_002")
	if q.Status != StatusInProgress || red.Status != StatusInProgress || !strings.Contains(red.Note, "challenge") {
		t.Fatalf("%+v %+v",
			q,
			red)
	}
	for _, tr := range ts {
		if tr.QuestionID == "q_002" {
			t.Errorf("no transition for the question: %+v", tr)
		}
	}
	if last := q.Attempts[len(q.Attempts)-1]; last.Tool != "worker" || !strings.Contains(last.Outcome, "c_001") {
		t.Errorf("the answer is logged for the supervisor: %+v", q.Attempts)
	}
	if len(next.Notes.Claims) != 1 || len(next.Notes.Datapoints) != 1 {
		t.Errorf("the claims are in the notes: %+v", next.Notes)
	}
}

func TestReducePartialRetriesUntilTheAttemptCap(t *testing.T) {
	s := delegated(t)
	part := parse(t, `{"status":"partial","answer":"half","queries":[{"query":"q1"},{"query":"q2"},{"query":"q3"},{"query":"q4"},{"query":"q5"}],"notes":"not enough"}`)
	var last Reduction
	for i := 1; i <= s.Limits.MaxAttempts+1; i++ {
		var next State
		next, _, last = Reduce(s,
			"q_003",
			part)
		q, _ := next.Frontier.Get("q_003")
		if len(q.Attempts) > s.Limits.MaxAttempts {
			t.Fatalf("run %d: %d attempts, the cap is %d",
				i,
				len(q.Attempts),
				s.Limits.MaxAttempts)
		}
		if q.Status == StatusUnresolvable {
			if i < 2 || !strings.Contains(q.Answer, "not enough") || len(next.Notes.Assumptions) != 1 {
				t.Fatalf("run %d: %+v %+v",
					i,
					q,
					next.Notes.Assumptions)
			}
			return
		}
		if q.Status != StatusPending || last.Status != StatusPending {
			t.Fatalf("run %d: %+v",
				i,
				q)
		}
		s = next
		s.Frontier = inProgress(t,
			s.Frontier,
			"q_003") // the question goes out to a worker again
	}
	t.Fatalf("the question never became unresolvable: %+v", last)
}

func TestReduceUnresolvableAndFailure(t *testing.T) {
	s := delegated(t)
	next, ts, red := Reduce(s,
		"q_003",
		parse(t, `{"status":"unresolvable","answer":"","notes":"no data exists"}`))
	q, _ := next.Frontier.Get("q_003")
	if q.Status != StatusUnresolvable || !strings.Contains(q.Answer, "no data exists") || len(ts) == 0 || ts[0].Trigger != TriggerAttemptsExhausted || red.Status != StatusUnresolvable ||
		len(next.Notes.Assumptions) != 1 {
		t.Fatalf("%+v %+v %+v",
			q,
			ts,
			next.Notes.Assumptions)
	}
	failed, _, red := WorkerFailed(s,
		"q_003",
		"timeout")
	q, _ = failed.Frontier.Get("q_003")
	if q.Status != StatusPending || red.Status != StatusPending || len(q.Attempts) != 1 || !strings.Contains(q.Attempts[0].Outcome, "worker failed: timeout") {
		t.Fatalf("%+v", q)
	}
	// A question that is not in progress is not touched.
	if same, ts, red := WorkerFailed(failed,
		"q_003",
		"again"); len(ts) != 0 || red.Note == "" || same.Frontier.Questions[2].Status != StatusPending {
		t.Errorf("%+v %+v",
			ts,
			red)
	}
}

func TestReduceDropsWhatCannotBeRecordedAndKeepsOpenGaps(t *testing.T) {
	s := delegated(t)
	s.Limits.MaxDepth = 2 // q_003 is at depth 2: its gaps are over the cap
	raw := `{"status":"answered","answer":"ok (c1) (c2)","claims":[
	  {"key":"c1","text":"good","sources":[{"url":"https://a.example/x"}]},
	  {"key":"c2","text":"also good","kind":"datapoint","value":"1 000","unit":"u","sources":[{"url":"https://a.example/x"}]}],
	  "discovered_gaps":[{"question":"Deeper?","priority":0.3}]}`
	next, _, red := Reduce(s,
		"q_003",
		parse(t, raw))
	// A datapoint with a value that has spaces is recorded as a claim, with the figure in its text.
	if len(next.Notes.Claims) != 2 || len(next.Notes.Datapoints) != 0 || !strings.Contains(next.Notes.Claims[1].Text, "1 000 u") {
		t.Errorf("claims: %+v", next.Notes.Claims)
	}
	if len(red.GapsOpen) != 1 || len(red.GapIDs) != 0 || len(next.Notes.Assumptions) != 1 || !strings.Contains(next.Notes.Assumptions[0].Text, "Deeper?") {
		t.Errorf("gaps: %+v %+v",
			red,
			next.Notes.Assumptions)
	}
}

func TestDelegateBudgetAndCostShare(t *testing.T) {
	l := DefaultLimits()
	if !DelegateBudgetOK(LoopState{
		CostUSD: 100,
		Tokens:  1 << 30,
	}, l) {
		t.Error("no limits: always ok")
	}
	l.MaxCostUSD = 10 // exploration may spend 8.5; 60% of it is 5.1
	if !DelegateBudgetOK(LoopState{
		CostUSD: 5,
	}, l) || DelegateBudgetOK(LoopState{
		CostUSD: 5.2,
	}, l) {
		t.Error("cost")
	}
	l.MaxTotalTokens = 1000
	if DelegateBudgetOK(LoopState{
		Tokens: 600,
	}, l) { // 60% of 850 = 510
		t.Error("tokens")
	}
	if got := WorkerCostShare(LoopState{
		CostUSD: 2,
	},
		l,
		3); got < 2.1 || got > 2.2 { // (8.5-2)/3
		t.Errorf("share: %v", got)
	}
	if WorkerCostShare(LoopState{},
		DefaultLimits(),
		3) != 0 {
		t.Error("no limit, no share")
	}
}
