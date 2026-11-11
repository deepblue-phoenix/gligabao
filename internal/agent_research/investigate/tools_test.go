package investigate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func newState(t *testing.T) State {
	t.Helper()
	s, _, err := NewState("Does X fit in 16 GB of VRAM?", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// call runs a tool on a state with the arguments as a Go value.
func call(t *testing.T, s State, name string, args any) (State, string, error) {
	t.Helper()
	tool, ok := InvestigativeRegistry().Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Handler(s, raw)
	if err != nil {
		return s, "", err
	}
	return res.State, res.Text, nil
}

func ok(t *testing.T, s State, name string, args any) (State, string) {
	t.Helper()
	s, text, err := call(t, s, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return s, text
}

type m = map[string]any

func wantErr(t *testing.T, s State, name string, args any, contains string) {
	t.Helper()
	next, _, err := call(t, s, name, args)
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Errorf("%s %v: err = %v, want %q", name, args, err, contains)
	}
	if fmt.Sprint(next) != fmt.Sprint(s) {
		t.Errorf("%s: a failed call changed the state", name)
	}
}

func TestRegistryHasTheStageTools(t *testing.T) {
	want := []string{"frontier_next", "frontier_view", "add_question", "retry_question", "resolve_question", "reopen_question", "set_priority", "prune_branch",
		"notes_read", "notes_append", "notes_update", "notes_scratch", "challenge", "record_challenge_result", "challenges_view"}
	got := InvestigativeRegistry().Names()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools: %v", got)
	}
	for _, tool := range InvestigativeRegistry().Tools() {
		if tool.Description == "" || tool.Schema["type"] != "object" || tool.Handler == nil {
			t.Errorf("%s: incomplete spec", tool.Name)
		}
		if _, err := json.Marshal(tool.Schema); err != nil {
			t.Errorf("%s: schema is not JSON: %v", tool.Name, err)
		}
		if _, _, err := call(t, newState(t), tool.Name, "not an object"); tool.Name != "notes_read" && (err == nil || !strings.Contains(err.Error(), "invalid arguments")) {
			t.Errorf("%s: bad arguments: %v", tool.Name, err)
		}
	}
}

func TestFrontierNextAndAdd(t *testing.T) {
	s := newState(t)
	wantErr(t, s, "frontier_next", m{}, "no sub-questions yet")
	wantErr(t, s, "add_question", m{"parent_id": "q_001", "question": "a"}, "priority is required")
	wantErr(t, s, "add_question", m{"parent_id": "q_001", "question": "a", "priority": 0.5}, "validation_criteria is required")
	wantErr(t, s, "add_question", m{"parent_id": "q_009", "question": "a", "priority": 0.5, "validation_criteria": "c"}, "unknown question")
	s, text := ok(t, s, "add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes", "target_sources": []string{"official_docs"}})
	if !strings.Contains(text, "added [q_002] pending P 0.80 depth 1") {
		t.Fatal(text)
	}
	s, _ = ok(t, s, "add_question", m{"question": "What is the activation memory?", "priority": 0.6, "validation_criteria": "profiling", "kind": "hypothesis", "depends_on": []string{"q_002"}})
	wantErr(t, s, "add_question", m{"parent_id": "q_001", "question": "what is the weight size", "priority": 0.8, "validation_criteria": "c"}, "already asks that")
	s, text = ok(t, s, "frontier_next", m{})
	if !strings.Contains(text, "Selected:") || !strings.Contains(text, "[q_002] in_progress") || !strings.Contains(text, "Frontier:") {
		t.Fatal(text)
	}
	wantErr(t, s, "frontier_next", m{}, "q_002 is still in progress")
	// A deep chain hits the depth cap with the design's text.
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_002", "question": "level two", "priority": 0.5, "validation_criteria": "c"})
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_004", "question": "level three", "priority": 0.5, "validation_criteria": "c"})
	wantErr(t, s, "add_question", m{"parent_id": "q_005", "question": "level four", "priority": 0.5, "validation_criteria": "c"}, "pruned by depth cap; record it as an open gap in notes instead")
}

func TestFrontierNextAtomic(t *testing.T) {
	s := newState(t)
	s, text := ok(t, s, "frontier_next", m{"atomic_reason": "a single fact lookup"})
	if !strings.Contains(text, "atomic") || !strings.Contains(s.Notes.Scratch, "Atomic objective: a single fact lookup") {
		t.Fatalf("%s | %q", text, s.Notes.Scratch)
	}
	if _, text, err := call(t, s, "frontier_next", m{}); err != nil || !strings.Contains(text, "atomic") {
		t.Fatalf("a later call works without the reason: %v %s", err, text)
	}
}

func TestFrontierView(t *testing.T) {
	s := newState(t)
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_001", "question": "a?", "priority": 0.5, "validation_criteria": "c"})
	_, text := ok(t, s, "frontier_view", m{})
	if !strings.Contains(text, "Frontier:") {
		t.Fatal(text)
	}
	_, text = ok(t, s, "frontier_view", m{"status": "pending"})
	if text != "[q_002] pending P 0.50 depth 1: a?" {
		t.Fatal(text)
	}
	if _, text = ok(t, s, "frontier_view", m{"status": "pruned"}); text != "no pruned questions" {
		t.Fatal(text)
	}
	if _, text = ok(t, s, "frontier_view", m{"id": "q_002"}); !strings.Contains(text, "criteria: c") {
		t.Fatal(text)
	}
	wantErr(t, s, "frontier_view", m{"id": "q_099"}, "unknown question")
}

func TestRetryAndResolveTools(t *testing.T) {
	s := newState(t)
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_001", "question": "deep one?", "priority": 0.5, "validation_criteria": "c"})
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_002", "question": "deeper one?", "priority": 0.5, "validation_criteria": "c"})
	s, _ = ok(t, s, "frontier_next", m{}) // q_002 (shallower first)
	wantErr(t, s, "retry_question", m{"id": "q_003", "new_query": "x", "why": "y"}, "must be in_progress")
	wantErr(t, s, "retry_question", m{"id": "q_002", "new_query": "x"}, "say why")
	s, text := ok(t, s, "retry_question", m{"id": "q_002", "new_query": "first formulation", "why": "no results"})
	if !strings.Contains(text, "in_progress -> pending") || !strings.Contains(text, "attempts: 1 of 4") {
		t.Fatal(text)
	}
	s, _ = ok(t, s, "frontier_next", m{})
	wantErr(t, s, "retry_question", m{"id": "q_002", "new_query": "First Formulation", "why": "again"}, "already tried")
}

func TestResolveChecks(t *testing.T) {
	s := newState(t)
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_001", "question": "shallow?", "priority": 0.9, "validation_criteria": "c"})
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_002", "question": "hypothesis: it is 11 GB", "kind": "hypothesis", "priority": 0.5, "validation_criteria": "confirm or refute"})
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_003", "question": "plain deep question", "priority": 0.1, "validation_criteria": "c"})
	// plain deep question (depth 3)
	s, _ = ok(t, s, "notes_append", m{"section": "Sources", "entries": []m{{"url": "https://a.org/1"}, {"url": "https://b.com/2"}}})
	s, _ = ok(t, s, "notes_append", m{"section": "Unverified", "entries": []m{{"text": "claim one", "sources": []m{{"source_id": "s_001", "independent": true}}}, {"text": "claim two"}}})
	s, _ = ok(t, s, "notes_update", m{"id": "c_002", "change": m{"status": "RETRACTED", "reason": "misread"}})

	// depth 3 question: claim checks only.
	setStatus := func(s State, id string, st Status) State {
		q, _ := s.Frontier.Get(id)
		q.Status = st
		s.Frontier = s.Frontier.with(q)
		return s
	}
	s = setStatus(s, "q_004", StatusInProgress)
	wantErr(t, s, "resolve_question", m{"id": "q_004", "answer": "yes", "claim_ids": []string{"c_009"}}, "claim c_009 does not exist")
	wantErr(t, s, "resolve_question", m{"id": "q_004", "answer": "yes", "claim_ids": []string{"c_002"}}, "claim c_002 is RETRACTED")
	wantErr(t, s, "resolve_question", m{"id": "q_004", "answer": ""}, "answer is empty")
	wantErr(t, s, "resolve_question", m{"id": "q_099", "answer": "x"}, "unknown question")
	s2, text := ok(t, s, "resolve_question", m{"id": "q_004", "answer": "yes", "claim_ids": []string{"c_001"}})
	if q, _ := s2.Frontier.Get("q_004"); q.Status != StatusResolved || q.Answer != "yes [c_001]" || !strings.Contains(text, "in_progress -> resolved") {
		t.Fatalf("%+v %s", q, text)
	}

	// hypothesis (depth 2): needs a confirming and a refuting search, or a reason.
	s = setStatus(s, "q_003", StatusInProgress)
	wantErr(t, s, "resolve_question", m{"id": "q_003", "answer": "confirmed"}, "confirming and a refuting search")
	if _, _, err := call(t, s, "resolve_question", m{"id": "q_003", "answer": "confirmed", "reason": "no public data to refute it"}); err != nil {
		t.Fatalf("reason replaces the searches: %v", err)
	}
	q, _ := s.Frontier.Get("q_003")
	q.Attempts = []Attempt{{Seq: 1, Query: "confirming query"}, {Seq: 2, Query: "refuting query"}}
	s.Frontier = s.Frontier.with(q)
	s.Challenges = []Challenge{{ID: "ch_001", QuestionID: "q_003", AttemptSeq: 2, Status: ChallengeClosed, Outcome: OutcomeSurvived}}
	if _, _, err := call(t, s, "resolve_question", m{"id": "q_003", "answer": "confirmed"}); err != nil {
		t.Fatalf("both searches in the log: %v", err)
	}

	// depth 1: an answer challenge, or a reason.
	s = setStatus(s, "q_002", StatusInProgress)
	wantErr(t, s, "resolve_question", m{"id": "q_002", "answer": "ok"}, "needs a closed challenge")
	if _, _, err := call(t, s, "resolve_question", m{"id": "q_002", "answer": "ok", "reason": "trivial lookup"}); err != nil {
		t.Fatalf("reason: %v", err)
	}
	s.Challenges = append(s.Challenges, Challenge{ID: "ch_002", QuestionID: "q_002", TargetType: TargetQuestionAnswer, TargetID: "q_002", Status: ChallengeClosed, Outcome: OutcomeSurvived, AttemptSeq: 1})
	if _, _, err := call(t, s, "resolve_question", m{"id": "q_002", "answer": "ok"}); err != nil {
		t.Fatalf("closed challenge: %v", err)
	}
	s.Challenges[1].Status, s.Challenges[1].Outcome = ChallengeOpen, ""
	wantErr(t, s, "resolve_question", m{"id": "q_002", "answer": "ok"}, "needs a closed challenge")
}

func TestReopenPriorityPruneTools(t *testing.T) {
	s := newState(t)
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_001", "question": "a?", "priority": 0.4, "validation_criteria": "c"})
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_002", "question": "a child?", "priority": 0.4, "validation_criteria": "c"})
	wantErr(t, s, "reopen_question", m{"id": "q_002", "why": "x"}, "must be resolved")
	wantErr(t, s, "set_priority", m{"id": "q_002", "why": "x"}, "priority is required")
	wantErr(t, s, "set_priority", m{"id": "q_002", "priority": 0.9}, "say why")
	wantErr(t, s, "set_priority", m{"id": "q_099", "priority": 0.9, "why": "x"}, "unknown question")
	s, text := ok(t, s, "set_priority", m{"id": "q_002", "priority": 0.9, "why": "key"})
	if text != "[q_002] pending -> pending, P 0.90" {
		t.Fatal(text)
	}
	if _, text = ok(t, s, "set_priority", m{"id": "q_002", "priority": 0.9, "why": "same"}); text != "priority unchanged" {
		t.Fatal(text)
	}
	wantErr(t, s, "prune_branch", m{"id": "q_001", "reason": "x"}, "root question cannot be pruned")
	wantErr(t, s, "prune_branch", m{"id": "q_002"}, "reason")
	s, text = ok(t, s, "prune_branch", m{"id": "q_002", "reason": "would not change the conclusion"})
	if !strings.Contains(text, "[q_002] pending -> pruned") || !strings.Contains(text, "[q_003] pending -> pruned") {
		t.Fatal(text)
	}
	q, _ := s.Frontier.Get("q_002")
	q.Status = StatusResolved
	s.Frontier = s.Frontier.with(q)
	s, text = ok(t, s, "reopen_question", m{"id": "q_002", "why": "contradicted"})
	if !strings.Contains(text, "resolved -> pending") {
		t.Fatal(text)
	}
}

func TestNotesTools(t *testing.T) {
	s := newState(t)
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_001", "question": "a?", "priority": 0.4, "validation_criteria": "c"})
	_, text := ok(t, s, "notes_read", m{})
	if !strings.HasPrefix(text, "# Notes: Does X fit in 16 GB of VRAM?") {
		t.Fatal(text)
	}
	wantErr(t, s, "notes_append", m{"section": "Nope", "entries": []m{{"text": "x"}}}, "unknown section")
	wantErr(t, s, "notes_append", m{"section": "Unverified", "entries": []m{{"text": "x", "question_id": "q_099"}}}, "unknown question")
	wantErr(t, s, "notes_append", m{"section": "Verified", "entries": []m{{"text": "x"}}}, "nothing is appended to Verified")
	wantErr(t, s, "notes_append", m{"section": "Unverified", "entries": []m{}}, "no entries")
	s, text = ok(t, s, "notes_append", m{"section": "Sources", "entries": []m{{"url": "https://a.org/1", "title": "A"}, {"url": "https://b.com/2"}}})
	if text != "added to Sources: s_001, s_002" {
		t.Fatal(text)
	}
	s, text = ok(t, s, "notes_append", m{"section": "unverified", "entries": []m{{"text": "claim", "question_id": "q_002", "sources": []m{{"source_id": "s_001", "independent": true}}}}})
	if text != "added to Unverified: c_001" {
		t.Fatal(text)
	}
	wantErr(t, s, "notes_update", m{"id": "c_001", "change": m{"status": "VERIFIED"}}, "cannot be VERIFIED")
	wantErr(t, s, "notes_update", m{"id": "c_099", "change": m{"note": "x"}}, "unknown record")
	s, _ = ok(t, s, "notes_update", m{"id": "c_001", "change": m{"add_source": m{"source_id": "s_002", "independent": true}, "check": "independent benchmark"}})
	if c, _ := s.Notes.Claim("c_001"); len(c.Links) != 2 || c.Check != "independent benchmark" {
		t.Fatalf("%+v", c)
	}
	s, text = ok(t, s, "notes_scratch", m{"text": "plan: check the profiler"})
	if !strings.Contains(text, "scratch replaced (24 characters)") {
		t.Fatal(text)
	}
	long := strings.Repeat("x", ScratchMaxChars+1)
	wantErr(t, s, "notes_scratch", m{"text": long}, "shorten it")
	// Over budget gives a warning.
	big := s
	big.Limits.NotesMaxTokens = 10
	if _, text = ok(t, big, "notes_scratch", m{"text": "more text"}); !strings.Contains(text, "over the budget of 10") {
		t.Fatal(text)
	}
}

func TestRefutedClaimQueuesQuestionAndReopens(t *testing.T) {
	s := newState(t)
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_001", "question": "memory?", "priority": 0.5, "validation_criteria": "c"})
	s, _ = ok(t, s, "notes_append", m{"section": "Sources", "entries": []m{{"url": "https://a.org/1"}, {"url": "https://b.com/2"}}})
	s, _ = ok(t, s, "notes_append", m{"section": "Unverified", "entries": []m{{"text": "needs 11 GB", "question_id": "q_002"}}})
	q, _ := s.Frontier.Get("q_002")
	q.Status, q.Answer = StatusResolved, "11 GB [c_001]"
	s.Frontier = s.Frontier.with(q)
	s, text := ok(t, s, "notes_update", m{"id": "c_001", "change": m{"status": "REFUTED", "source_id": "s_002", "reason": "newer figures say 14 GB"}})
	if !strings.Contains(text, "queued q_003") || !strings.Contains(text, "reopened q_002") {
		t.Fatal(text)
	}
	new3, _ := s.Frontier.Get("q_003")
	re2, _ := s.Frontier.Get("q_002")
	if new3.Priority != 0.9 || new3.ParentID != "q_002" || re2.Status != StatusPending || re2.Priority <= 0.5 {
		t.Fatalf("%+v %+v", new3, re2)
	}
}

func TestChallengeTools(t *testing.T) {
	s := newState(t)
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_001", "question": "What is the memory use?", "priority": 0.5, "validation_criteria": "c"})
	s, _ = ok(t, s, "notes_append", m{"section": "Sources", "entries": []m{{"url": "https://a.org/1"}, {"url": "https://b.com/2"}}})
	s, _ = ok(t, s, "notes_append", m{"section": "Unverified", "entries": []m{{"text": "needs 11 GB at fp16", "question_id": "q_002"}}})
	args := m{"target_type": "claim", "target_id": "c_001", "negation": "needs substantially more than 11 GB", "strategy": "discrepancy",
		"expected_if_false": "a benchmark with a higher number", "query": "peak memory conflicting benchmark figure"}
	wantErr(t, s, "challenge", m{"target_type": "claim", "target_id": "c_099"}, "unknown claim")
	bad := m{}
	for k, v := range args {
		bad[k] = v
	}
	bad["query"] = "needs 11 GB at fp16"
	wantErr(t, s, "challenge", bad, "not adversarial")
	s, text := ok(t, s, "challenge", args)
	if !strings.Contains(text, "challenge ch_001 registered") || !strings.Contains(text, "peak memory conflicting benchmark figure") {
		t.Fatal(text)
	}
	if _, text = ok(t, s, "challenges_view", m{"status": "open"}); !strings.Contains(text, "[ch_001] open on claim c_001") {
		t.Fatal(text)
	}
	if _, text = ok(t, s, "challenges_view", m{"status": "closed"}); text != "no challenges" {
		t.Fatal(text)
	}
	wantErr(t, s, "record_challenge_result", m{"challenge_id": "ch_001", "outcome": "survived"}, "never executed")
	wantErr(t, s, "record_challenge_result", m{"challenge_id": "ch_001", "outcome": "refuted", "evidence": "x"}, "at least one source id")
	wantErr(t, s, "record_challenge_result", m{"challenge_id": "ch_001", "outcome": "refuted", "evidence": "x", "source_ids": []string{"s_009"}}, "unknown source")
	wantErr(t, s, "record_challenge_result", m{"challenge_id": "ch_009", "outcome": "survived"}, "unknown challenge")

	// weakened: scope added to the claim.
	w, text := ok(t, s, "record_challenge_result", m{"challenge_id": "ch_001", "outcome": "weakened", "evidence": "only at batch size 1", "source_ids": []string{"s_002"}})
	if c, _ := w.Notes.Claim("c_001"); !strings.Contains(c.Text, "[scope: only at batch size 1]") || !strings.Contains(text, "closed: weakened") {
		t.Fatalf("%+v %s", c, text)
	}
	// refuted: claim REFUTED, a question queued.
	r, text := ok(t, s, "record_challenge_result", m{"challenge_id": "ch_001", "outcome": "refuted", "evidence": "profiling shows 14 GB", "source_ids": []string{"s_002"}})
	if c, _ := r.Notes.Claim("c_001"); c.Status != ClaimRefuted || !strings.Contains(text, "queued q_003") {
		t.Fatalf("%+v %s", c, text)
	}
	wantErr(t, r, "record_challenge_result", m{"challenge_id": "ch_001", "outcome": "inconclusive"}, "already closed")

	// survived needs an executed challenge; then the claim can be verified with two sources.
	f, chs, err := LinkAttempt(s.Frontier, s.Challenges, "ch_001", "web_search", "nothing against")
	if err != nil {
		t.Fatal(err)
	}
	s.Frontier, s.Challenges = f, chs
	s, _ = ok(t, s, "record_challenge_result", m{"challenge_id": "ch_001", "outcome": "survived"})
	s, _ = ok(t, s, "notes_update", m{"id": "c_001", "change": m{"add_source": m{"source_id": "s_001", "independent": true}}})
	s, _ = ok(t, s, "notes_update", m{"id": "c_001", "change": m{"add_source": m{"source_id": "s_002", "independent": true}}})
	s, _ = ok(t, s, "notes_update", m{"id": "c_001", "change": m{"status": "VERIFIED"}})
	if c, _ := s.Notes.Claim("c_001"); c.Status != ClaimVerified {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(Render(s.Notes), "## Verified\n- [c_001] VERIFIED") {
		t.Fatal(Render(s.Notes))
	}
}

func TestConclusionAndAnswerChallengeOutcomes(t *testing.T) {
	s := newState(t)
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_001", "question": "What is the memory use?", "priority": 0.5, "validation_criteria": "c"})
	s, _ = ok(t, s, "notes_append", m{"section": "Sources", "entries": []m{{"url": "https://a.org/1"}}})
	wantErr(t, s, "challenge", m{"target_type": "conclusion", "negation": "n", "strategy": "negation", "expected_if_false": "e", "query": "not"}, "no conclusion")
	s, _ = ok(t, s, "notes_scratch", m{"text": "Conclusion: X fits in 16 GB."})
	s, _ = ok(t, s, "challenge", m{"target_type": "conclusion", "negation": "X does not fit in 16 GB", "strategy": "negation", "expected_if_false": "OOM reports", "query": "X does not fit OOM"})
	s, text := ok(t, s, "record_challenge_result", m{"challenge_id": "ch_001", "outcome": "refuted", "evidence": "OOM at 16 GB", "source_ids": []string{"s_001"}})
	if !strings.Contains(text, "queued q_003; exploration continues") {
		t.Fatal(text)
	}
	// An answer challenge that refutes a resolved question reopens it.
	q, _ := s.Frontier.Get("q_002")
	q.Status = StatusResolved
	s.Frontier = s.Frontier.with(q)
	s, _ = ok(t, s, "challenge", m{"target_type": "question_answer", "target_id": "q_002", "negation": "the memory use is not that", "strategy": "criticism", "expected_if_false": "e", "query": "memory use problem"})
	s, text = ok(t, s, "record_challenge_result", m{"challenge_id": "ch_002", "outcome": "refuted", "evidence": "wrong", "source_ids": []string{"s_001"}})
	if got, _ := s.Frontier.Get("q_002"); got.Status != StatusPending || !strings.Contains(text, "reopened q_002") {
		t.Fatalf("%+v %s", got, text)
	}
}

func TestRetryUnresolvableRecordsAssumption(t *testing.T) {
	s := newState(t)
	s, _ = ok(t, s, "add_question", m{"parent_id": "q_001", "question": "obscure?", "priority": 0.5, "validation_criteria": "c"})
	for i := 1; i <= 4; i++ {
		s, _ = ok(t, s, "frontier_next", m{})
		s, _ = ok(t, s, "retry_question", m{"id": "q_002", "new_query": fmt.Sprintf("query %d", i), "why": "nothing"})
	}
	s, _ = ok(t, s, "frontier_next", m{})
	wantErr(t, s, "retry_question", m{"id": "q_002", "new_query": "q5", "why": "nothing"}, "state the assumption")
	s, text := ok(t, s, "retry_question", m{"id": "q_002", "new_query": "q5", "why": "nothing", "assumption": "about 2x the baseline"})
	if q, _ := s.Frontier.Get("q_002"); q.Status != StatusUnresolvable || !strings.Contains(text, "Assumptions & gaps") ||
		!strings.Contains(Render(s.Notes), "- q_002 unresolvable: about 2x the baseline") {
		t.Fatalf("%+v %s", q, text)
	}
}
