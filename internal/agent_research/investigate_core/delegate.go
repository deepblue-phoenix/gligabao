package investigate_core

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Delegation: the code of the investigative loop hands independent questions of the frontier to workers (sub-agents with a clean context,
// docs/agents/subagents.md). This file is the pure side of it: which questions go (SelectBatch, Delegate), what a worker is told
// (WorkerBrief), what it hands back (Findings, ParseFindings) and what the supervisor does with it (Reduce). Only the supervisor writes the
// frontier, the notes and the claims; a worker returns findings, and Reduce turns them into the usual state changes, so every gate still
// applies (a claim of a worker is UNVERIFIED: only CanVerify makes one VERIFIED).

// The statuses of the findings of a worker.
const (
	FindingAnswered     = "answered"
	FindingPartial      = "partial"
	FindingUnresolvable = "unresolvable"
)

// The bounds of the findings: a worker that goes over them is told (by the error of ParseFindings) to compress.
const (
	MaxFindingAnswerChars = 800
	MaxFindingClaims      = 12
	MaxFindingSources     = 5 // per claim
	MaxFindingQueries     = 15
	MaxFindingGaps        = 5
	MaxFindingNotesChars  = 1000
	MaxFindingsChars      = 14000
)

// Findings is what a worker hands back for its question (the input of its submit_findings tool).
type Findings struct {
	// Status is FindingAnswered, FindingPartial or FindingUnresolvable.
	Status string `json:"status"`
	// Answer is the short answer, citing the keys of the claims.
	Answer         string         `json:"answer"`
	Claims         []FindingClaim `json:"claims"`
	Queries        []FindingQuery `json:"queries"`
	DiscoveredGaps []FindingGap   `json:"discovered_gaps"`
	// Notes are the caveats and the dead ends.
	Notes string `json:"notes"`
}

// FindingClaim is a claim (or datapoint: Kind "datapoint" with Value and Unit) of a worker, with its sources. Key is the worker's own name for it
// (c1, c2, ...), used in the answer.
type FindingClaim struct {
	Key     string          `json:"key"`
	Text    string          `json:"text"`
	Kind    string          `json:"kind"`
	Value   string          `json:"value"`
	Unit    string          `json:"unit"`
	Sources []FindingSource `json:"sources"`
}

// FindingSource is a page a claim rests on: what it says (Quote), whether it supports or contradicts the claim and whether it is
// independent of the other sources of the claim.
type FindingSource struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Stance      string `json:"stance"`
	Independent bool   `json:"independent"`
	Quote       string `json:"quote"`
}

// FindingQuery is a query the worker tried.
type FindingQuery struct {
	Query   string `json:"query"`
	Tool    string `json:"tool"`
	Outcome string `json:"outcome"`
}

// FindingGap is a question that the worker found and did not follow.
type FindingGap struct {
	Question  string  `json:"question"`
	Rationale string  `json:"rationale"`
	Priority  float64 `json:"priority"`
	Kind      string  `json:"kind"`
}

// FindingsSchema is the JSON schema of the input of submit_findings.
func FindingsSchema() map[string]any {
	src := obj(props{
		"url":         str("the page"),
		"title":       str("its title"),
		"stance":      enum("what it says about the claim", "supports", "contradicts"),
		"independent": props{"type": "boolean", "description": "true if it does not quote another source of this claim"},
		"quote":       str("the sentence or figure that says it, short"),
	}, "url")
	claim := obj(props{
		"key":     str("your name for the claim: c1, c2, ... (the answer cites it)"),
		"text":    str("one atomic claim"),
		"kind":    enum("claim, or datapoint for a number (then value and unit)", "claim", "datapoint"),
		"value":   str("datapoint only: the number, without spaces"),
		"unit":    str("datapoint only: its unit"),
		"sources": props{"type": "array", "items": src, "description": "at least one source"},
	}, "key", "text", "sources")
	return obj(props{
		"status": enum("answered: the validation criteria are met; partial: found something, not enough; unresolvable: cannot be answered with what is available", FindingAnswered, FindingPartial, FindingUnresolvable),
		"answer": str(fmt.Sprintf("the short answer (at most %d characters), citing the keys of the claims", MaxFindingAnswerChars)),
		"claims": props{"type": "array", "items": claim, "description": fmt.Sprintf("at most %d", MaxFindingClaims)},
		"queries": props{"type": "array", "description": "the searches you made", "items": obj(props{
			"query": str("the query or the URL"), "tool": str("web_search, fetch_url or query"), "outcome": str("what it gave, in a few words")}, "query")},
		"discovered_gaps": props{"type": "array", "description": fmt.Sprintf("questions you found and did not follow, at most %d", MaxFindingGaps), "items": obj(props{
			"question": str("the question"), "rationale": str("why it matters"), "priority": num("0 to 1"), "kind": enum("question or hypothesis", "question", "hypothesis")}, "question")},
		"notes": str(fmt.Sprintf("caveats and dead ends, short (at most %d characters)", MaxFindingNotesChars)),
	}, "status", "answer")
}

// ParseFindings reads and checks the findings of a worker. The error says what is wrong, in words the worker can act on (it gets one
// repair turn).
func ParseFindings(raw json.RawMessage) (Findings, error) {
	var f Findings
	if err := decode(raw, &f); err != nil {
		return Findings{}, err
	}
	f.Status, f.Answer, f.Notes = strings.ToLower(strings.TrimSpace(f.Status)), strings.TrimSpace(f.Answer), strings.TrimSpace(f.Notes)
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	switch f.Status {
	case FindingAnswered, FindingPartial, FindingUnresolvable:
	default:
		bad("status %q must be answered, partial or unresolvable", f.Status)
	}
	if f.Answer == "" && f.Status != FindingUnresolvable {
		bad("the answer is empty")
	}
	if n := len([]rune(f.Answer)); n > MaxFindingAnswerChars {
		bad("the answer is %d characters, at most %d: compress it", n, MaxFindingAnswerChars)
	}
	if n := len([]rune(f.Notes)); n > MaxFindingNotesChars {
		bad("the notes are %d characters, at most %d: compress them", n, MaxFindingNotesChars)
	}
	if len(f.Claims) > MaxFindingClaims {
		bad("%d claims, at most %d: keep the ones that matter", len(f.Claims), MaxFindingClaims)
	}
	if len(f.Queries) > MaxFindingQueries {
		f.Queries = f.Queries[:MaxFindingQueries] // not worth an error: the log keeps the first ones
	}
	if len(f.DiscoveredGaps) > MaxFindingGaps {
		bad("%d gaps, at most %d: keep the ones that matter", len(f.DiscoveredGaps), MaxFindingGaps)
	}
	if f.Status == FindingAnswered && len(f.Claims) == 0 {
		bad("an answered question needs at least one claim with a source")
	}
	keys := map[string]bool{}
	for i, c := range f.Claims {
		label := fmt.Sprintf("claim %d", i+1)
		key := strings.TrimSpace(c.Key)
		switch {
		case key == "":
			bad("%s has no key", label)
		case keys[key]:
			bad("%s: the key %q is used twice", label, key)
		}
		keys[key] = true
		if strings.TrimSpace(c.Text) == "" {
			bad("%s has no text", label)
		}
		if len(c.Sources) == 0 {
			bad("%s has no source: every claim needs at least one (a page, with its URL)", label)
		}
		if len(c.Sources) > MaxFindingSources {
			bad("%s has %d sources, at most %d", label, len(c.Sources), MaxFindingSources)
		}
		for _, s := range c.Sources {
			if _, _, err := AddSource(nil, s.URL, s.Title, ""); err != nil {
				bad("%s: %v", label, err)
			}
			switch strings.ToLower(strings.TrimSpace(s.Stance)) {
			case "", "supports", "contradicts":
			default:
				bad("%s: stance %q must be supports or contradicts", label, s.Stance)
			}
		}
	}
	if len(problems) == 0 {
		if b, _ := json.Marshal(f); len(b) > MaxFindingsChars {
			bad("the findings are %d characters, at most %d: compress them", len(b), MaxFindingsChars)
		}
	}
	if len(problems) > 0 {
		return Findings{}, errors.New("the findings are not valid: " + strings.Join(problems, "; "))
	}
	return f, nil
}

// delegable reports whether the question can be handed to a worker now: it is a pending question (not the root) whose dependencies are
// resolved, with validation criteria, of at least the minimum priority.
func delegable(f Frontier, q Question, minPriority float64) bool {
	return q.ParentID != "" && f.Actionable(q) && strings.TrimSpace(q.ValidationCriteria) != "" && q.Priority >= minPriority
}

// SelectBatch picks the questions to run in workers at once: at most n of those that can go (pending, every dependency resolved, with
// criteria, not below minPriority), highest priority first, by the order of the selection rule.
func SelectBatch(f Frontier, n int, minPriority float64) []Question {
	var out []Question
	for _, q := range f.pendingByOrder() {
		if len(out) >= n {
			break
		}
		if delegable(f, q, minPriority) {
			out = append(out, q)
		}
	}
	return out
}

// Delegate marks the questions ids in progress for their workers (delegated transitions, by code).
func Delegate(f Frontier, ids []string) (Frontier, []Transition, error) {
	var ts []Transition
	for _, id := range ids {
		q, err := mustBe(f, id, StatusPending)
		if err != nil {
			return f, nil, err
		}
		q, t := move(q, StatusInProgress, TriggerDelegated, ActorCode, "handed to a worker", "")
		f, ts = f.with(q), append(ts, t)
	}
	return f, ts, nil
}

// DelegateBudgetOK reports whether the run can afford workers: exploration has not spent more than 60% of what its token and cost limits allow
// (a worker costs as much as a dozen model calls, and the reserve of the synthesis must stay).
func DelegateBudgetOK(ls LoopState, l Limits) bool {
	const share = 0.6
	if l.MaxCostUSD > 0 && ls.CostUSD >= share*ExplorationLimit(l.MaxCostUSD, l.SynthesisReserve) {
		return false
	}
	if l.MaxTotalTokens > 0 && float64(ls.Tokens) >= share*ExplorationLimit(float64(l.MaxTotalTokens), l.SynthesisReserve) {
		return false
	}
	return true
}

// WorkerCostShare is the cost cap of each of n workers started now: what exploration may still spend of the cost limit, shared by them (0:
// there is no cost limit). A worker that reaches it makes its last call and hands in what it has; one model call can still pass it.
func WorkerCostShare(ls LoopState, l Limits, n int) float64 {
	if l.MaxCostUSD <= 0 || n <= 0 {
		return 0
	}
	return max(0.0001, (ExplorationLimit(l.MaxCostUSD, l.SynthesisReserve)-ls.CostUSD)/float64(n))
}

// WorkerBrief is what a worker is told about the question qid: the objective, the question with its rationale, criteria and target
// sources, the queries already tried, and a slice of the notes: the answers and claims of the questions it depends on (and of its ancestors),
// cut to about tokens tokens.
func WorkerBrief(s State, qid string, tokens int) string {
	q, ok := s.Frontier.Get(qid)
	if !ok {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Objective of the investigation: %s\n\nYour question (%s, %s): %s\n", s.Notes.Objective, q.ID, q.Kind, q.Question)
	if q.Rationale != "" {
		fmt.Fprintf(&b, "Why it matters: %s\n", q.Rationale)
	}
	fmt.Fprintf(&b, "It is answered when: %s\n", q.ValidationCriteria)
	if len(q.TargetSources) > 0 {
		fmt.Fprintf(&b, "Look at: %s\n", strings.Join(q.TargetSources, ", "))
	}
	if q.Kind == KindHypothesis {
		b.WriteString("This is a hypothesis: look for evidence that confirms it and for evidence that refutes it, and report both.\n")
	}
	if len(q.Attempts) > 0 {
		b.WriteString("\nAlready tried (do not repeat):\n")
		for _, a := range q.Attempts {
			fmt.Fprintf(&b, "- %s\n", Excerpt(a.Query, 150))
		}
	}
	if context := briefContext(s, q, tokens); context != "" {
		b.WriteString("\nWhat is known already:\n" + context)
	}
	return strings.TrimSpace(b.String())
}

// briefContext is the answers of the dependencies and the ancestors of q and the claims of the notes about those questions, in lines, until
// tokens (a token is 4 characters) are spent.
func briefContext(s State, q Question, tokens int) string {
	related := map[string]bool{}
	var order []string
	add := func(id string) {
		if id != "" && !related[id] {
			related[id] = true
			order = append(order, id)
		}
	}
	for _, d := range q.DependsOn {
		add(d)
	}
	for p := q.ParentID; p != ""; {
		pq, ok := s.Frontier.Get(p)
		if !ok {
			break
		}
		add(pq.ID)
		p = pq.ParentID
	}
	budget := tokens * 4
	var b strings.Builder
	line := func(text string) bool {
		text = truncate(text, 400)
		if budget < len([]rune(text))+1 {
			return false
		}
		budget -= len([]rune(text)) + 1
		b.WriteString(text + "\n")
		return true
	}
	for _, id := range order {
		rq, _ := s.Frontier.Get(id)
		if rq.Status == StatusResolved && rq.ParentID != "" && !line(fmt.Sprintf("- %s answer: %s", rq.ID, Excerpt(rq.Answer, 300))) {
			return b.String()
		}
	}
	for _, c := range append(slices.Clone(s.Notes.Claims), s.Notes.Datapoints...) {
		if related[c.QuestionID] && c.Status != ClaimRefuted && c.Status != ClaimRetracted && !line(claimLine(c)) {
			break
		}
	}
	return b.String()
}

// Reduction is what Reduce did with the findings of one worker, for the log of the loop.
type Reduction struct {
	// ClaimIDs are the ids the claims got in the notes; Dropped the claims that could not be recorded, with why.
	ClaimIDs []string
	Dropped  []string
	// GapIDs are the questions added for the gaps; GapsOpen the gaps that could not be added (they are in the notes as open gaps).
	GapIDs   []string
	GapsOpen []string
	// Status is the status the question has after the reduction; Note says why it is not the one the findings ask for.
	Status Status
	Note   string
}

// Reduce is the supervisor's reducer: it applies the findings of the worker of question qid to the state, with the checks of the tools.
//
//  1. The sources go to the notes (one per URL), the claims to Unverified (or Datapoints) with their links, tied to the question.
//  2. The queries are logged as attempts of the question (when it goes back to pending, only as many as leave room for the attempt that the
//     return itself logs, so that the attempt cap is the count of the tries and not of the queries).
//  3. The gaps are added as children of the question (by code, caused by it); one that the depth, the question cap or a duplicate rejects
//     becomes an open gap in the notes.
//  4. The question: answered is resolved if the checks of resolve_question pass, else it stays in progress with the answer logged, for
//     the supervisor to finish (the challenge of its answer is its job); partial goes back to pending (Retry: at the attempt cap it is
//     unresolvable); unresolvable is that, with the notes as the assumption.
func Reduce(s State, qid string, fs Findings) (State, []Transition, Reduction) {
	var out Reduction
	q, ok := s.Frontier.Get(qid)
	if !ok || q.Status != StatusInProgress {
		out.Note = fmt.Sprintf("%s is not in progress", qid)
		return s, nil, out
	}
	n, keyIDs := recordClaims(s.Notes, qid, fs.Claims, &out)
	s.Notes = n

	f := s.Frontier
	queries := fs.Queries
	if fs.Status == FindingPartial {
		queries = queries[:min(len(queries), max(0, s.Limits.MaxAttempts-len(q.Attempts)-1))]
	}
	for _, fq := range queries {
		if cur, _ := f.Get(qid); strings.TrimSpace(fq.Query) == "" || slices.ContainsFunc(cur.Attempts, func(a Attempt) bool { return Normalize(a.Query) == Normalize(fq.Query) }) {
			continue
		}
		tool := orElse(strings.TrimSpace(fq.Tool), "web_search")
		if nf, _, err := AddAttempt(f, qid, Attempt{Query: strings.TrimSpace(fq.Query), Tool: tool, Outcome: orElse(clean(fq.Outcome), "(worker)")}); err == nil {
			f = nf
		}
	}

	var ts []Transition
	for _, g := range fs.DiscoveredGaps {
		kind := Kind(strings.ToLower(strings.TrimSpace(g.Kind)))
		nf, gts, err := Add(f, qid, AddInput{Kind: kind, Question: g.Question, Rationale: g.Rationale, Priority: min(1, max(0, g.Priority)),
			ValidationCriteria: "answered with sourced claims", Actor: ActorCode, CausedBy: qid}, s.Limits)
		if err != nil {
			out.GapsOpen = append(out.GapsOpen, strings.TrimSpace(g.Question))
			if nn, _, aerr := Append(s.Notes, SectionAssumptions, []Entry{{QuestionID: qid, Text: "open gap (found by the worker of " + qid + "): " + g.Question}}); aerr == nil {
				s.Notes = nn
			}
			continue
		}
		f, ts = nf, append(ts, gts...)
		out.GapIDs = append(out.GapIDs, f.Questions[len(f.Questions)-1].ID)
	}
	if note := strings.TrimSpace(fs.Notes); note != "" && fs.Status != FindingUnresolvable {
		if nn, err := SetScratch(s.Notes, strings.TrimSpace(s.Notes.Scratch+"\n"+clean("worker of "+qid+": "+note))); err == nil {
			s.Notes = nn
		}
	}
	s.Frontier = f
	s, ts2, out := finishQuestion(s, qid, fs, keyIDs, out)
	return s, append(ts, ts2...), out
}

// finishQuestion is the last step of Reduce: the status of the question.
func finishQuestion(s State, qid string, fs Findings, keyIDs map[string]string, out Reduction) (State, []Transition, Reduction) {
	q, _ := s.Frontier.Get(qid)
	answer := substituteKeys(fs.Answer, keyIDs)
	switch fs.Status {
	case FindingAnswered:
		in := ResolveInput{Answer: answer, ClaimIDs: out.ClaimIDs}
		if problems := checkResolve(s, q, in); len(problems) > 0 {
			f, _, _ := AddAttempt(s.Frontier, qid, Attempt{Query: "worker's answer", Tool: "worker", Outcome: Excerpt(answer, 200)})
			s.Frontier = f
			out.Status, out.Note = StatusInProgress, "left in progress for the supervisor: "+strings.Join(problems, "; ")
			return s, nil, out
		}
		f, ts, err := Resolve(s.Frontier, qid, in)
		if err != nil {
			out.Status, out.Note = StatusInProgress, "left in progress for the supervisor: "+err.Error()
			return s, nil, out
		}
		s.Frontier, out.Status = f, StatusResolved
		return s, ts, out
	case FindingUnresolvable:
		assumption := orElse(clean(fs.Notes), "the worker found nothing that answers it")
		return markUnresolvable(s, qid, "the worker could not answer it: "+assumption, out)
	}
	// partial
	return retryQuestion(s, qid, orElse(clean(fs.Notes), "the worker found only part of the answer"), out)
}

// WorkerFailed is what happens to the question of a worker that failed (an error, a timeout, findings that stayed invalid): it goes back to
// pending with the failure logged as an attempt, and becomes unresolvable at the attempt cap.
func WorkerFailed(s State, qid, why string) (State, []Transition, Reduction) {
	if q, ok := s.Frontier.Get(qid); !ok || q.Status != StatusInProgress {
		return s, nil, Reduction{Note: fmt.Sprintf("%s is not in progress", qid)}
	}
	return retryQuestion(s, qid, "worker failed: "+clean(why), Reduction{})
}

// retryQuestion sends the in-progress question back to pending with why as the outcome of a new attempt (Retry); at the attempt cap it
// becomes unresolvable and why is its assumption, which goes to the notes too.
func retryQuestion(s State, qid, why string, out Reduction) (State, []Transition, Reduction) {
	q, _ := s.Frontier.Get(qid)
	f, ts, err := Retry(s.Frontier, qid, fmt.Sprintf("delegated run %d: %s", len(q.Attempts)+1, Excerpt(why, 80)), why, why, s.Limits)
	if err != nil {
		out.Note = err.Error()
		return s, nil, out
	}
	s.Frontier = f
	q, _ = f.Get(qid)
	out.Status = q.Status
	if q.Status == StatusUnresolvable {
		if n, _, aerr := Append(s.Notes, SectionAssumptions, []Entry{{QuestionID: qid, Text: q.Answer}}); aerr == nil {
			s.Notes = n
		}
	}
	return s, ts, out
}

// markUnresolvable ends the question as unresolvable with the assumption, in the notes too.
func markUnresolvable(s State, qid, assumption string, out Reduction) (State, []Transition, Reduction) {
	q, err := mustBe(s.Frontier, qid, StatusInProgress)
	if err != nil {
		out.Note = err.Error()
		return s, nil, out
	}
	q.Answer = assumption
	q, t := move(q, StatusUnresolvable, TriggerAttemptsExhausted, ActorCode, truncate(assumption, 200), "")
	f, more := Reblock(s.Frontier.with(q))
	s.Frontier = f
	if n, _, aerr := Append(s.Notes, SectionAssumptions, []Entry{{QuestionID: qid, Text: assumption}}); aerr == nil {
		s.Notes = n
	}
	out.Status = StatusUnresolvable
	return s, append([]Transition{t}, more...), out
}

// recordClaims adds the sources and the claims of the findings to the notes and returns the new notes and the id each claim key got. A claim
// that cannot be recorded is dropped (out.Dropped says why); the others are.
func recordClaims(n Notes, qid string, claims []FindingClaim, out *Reduction) (Notes, map[string]string) {
	ids := map[string]string{}
	for _, c := range claims {
		next := n
		var links []Link
		var err error
		for _, fs := range c.Sources {
			var src Source
			if next.Sources, src, err = AddSource(next.Sources, fs.URL, fs.Title, Excerpt(fs.Quote, 160)); err != nil {
				break
			}
			stance := StanceSupports
			if strings.EqualFold(strings.TrimSpace(fs.Stance), "contradicts") {
				stance = StanceContradicts
			}
			links = append(links, Link{SourceID: src.ID, Stance: stance, Independent: fs.Independent})
		}
		if err != nil {
			out.Dropped = append(out.Dropped, fmt.Sprintf("%s: %v", c.Key, err))
			continue
		}
		entry := Entry{Text: c.Text, QuestionID: qid, Sources: links}
		section := SectionUnverified
		if strings.EqualFold(strings.TrimSpace(c.Kind), "datapoint") {
			value := strings.TrimSpace(c.Value)
			if value != "" && !strings.ContainsAny(value, " \t\r\n|") {
				section, entry.Value, entry.Unit = SectionDatapoints, value, c.Unit
			} else { // a datapoint needs a value without spaces: record it as a claim, the figure in the text
				entry.Text = strings.TrimSpace(c.Text + " " + value + " " + c.Unit)
			}
		}
		var got []string
		if next, got, err = Append(next, section, []Entry{entry}); err != nil {
			out.Dropped = append(out.Dropped, fmt.Sprintf("%s: %v", c.Key, err))
			continue
		}
		n = next
		ids[strings.TrimSpace(c.Key)] = got[0]
		out.ClaimIDs = append(out.ClaimIDs, got[0])
	}
	return n, ids
}

// substituteKeys replaces the keys of the worker's claims in text by the ids they got in the notes (longest keys first, whole words only).
func substituteKeys(text string, ids map[string]string) string {
	keys := make([]string, 0, len(ids))
	for k := range ids {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return len(b) - len(a) })
	for _, k := range keys {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(k) + `\b`)
		text = re.ReplaceAllLiteralString(text, ids[k])
	}
	return text
}
