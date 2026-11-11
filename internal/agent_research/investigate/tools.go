package investigate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Result is what a handler returns: the new state, the compact text the model reads (never the full frontier) and the effects to execute.
type Result struct {
	State   State
	Text    string
	Effects []Effect
}

// Tool is the spec of a tool the model can call and its pure handler: (state, args) -> (state, text, effects). A handler that returns an
// error changed nothing; the error text is what the model reads.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON schema of the arguments (an object).
	Schema  map[string]any
	Handler func(s State, args json.RawMessage) (Result, error)
}

// Registry is a set of tools by name. No agent definition uses it in stage 1.
type Registry struct{ tools []Tool }

// Tools are the tools in registration order.
func (r Registry) Tools() []Tool { return slices.Clone(r.tools) }

// Get returns the tool with that name.
func (r Registry) Get(name string) (Tool, bool) {
	i := slices.IndexFunc(r.tools, func(t Tool) bool { return t.Name == name })
	if i < 0 {
		return Tool{}, false
	}
	return r.tools[i], true
}

// Names are the names of the tools.
func (r Registry) Names() []string {
	return mapSlice(r.tools, func(t Tool) string { return t.Name })
}

// InvestigativeRegistry is the registry of "the tools of the investigative loop": the frontier, notes and challenge tools of stage 1.
func InvestigativeRegistry() Registry {
	return Registry{tools: []Tool{
		frontierNextTool, frontierViewTool, addQuestionTool, retryQuestionTool, resolveQuestionTool, reopenQuestionTool, setPriorityTool, pruneBranchTool,
		notesReadTool, notesAppendTool, notesUpdateTool, notesScratchTool,
		challengeTool, recordChallengeResultTool, challengesViewTool,
	}}
}

type props map[string]any

func str(desc string) props { return props{"type": "string", "description": desc} }
func strList(desc string) props {
	return props{"type": "array", "items": props{"type": "string"}, "description": desc}
}
func num(desc string) props { return props{"type": "number", "description": desc} }
func enum(desc string, values ...string) props {
	return props{"type": "string", "enum": values, "description": desc}
}

func obj(p props, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": map[string]any(p), "required": required}
}

func decode(raw json.RawMessage, into any) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// done builds the result of a handler from the old and new state.
func done(before, after State, ts []Transition, notesReason, text string) Result {
	return Result{State: after, Text: text, Effects: Diff(before, after, ts, notesReason)}
}

// questionLine is the compact one-line form of a question.
func questionLine(q Question) string {
	return fmt.Sprintf("[%s] %s P %.2f depth %d: %s", q.ID, q.Status, q.Priority, q.Depth, q.Question)
}

// questionView is the full form of one question, with its attempt log.
func questionView(q Question) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n  kind: %s\n", questionLine(q), q.Kind)
	if q.Rationale != "" {
		fmt.Fprintf(&b, "  rationale: %s\n", q.Rationale)
	}
	if q.ValidationCriteria != "" {
		fmt.Fprintf(&b, "  criteria: %s\n", q.ValidationCriteria)
	}
	if len(q.TargetSources) > 0 {
		fmt.Fprintf(&b, "  target sources: %s\n", strings.Join(q.TargetSources, ", "))
	}
	if len(q.DependsOn) > 0 {
		fmt.Fprintf(&b, "  depends on: %s\n", strings.Join(q.DependsOn, ", "))
	}
	if q.Answer != "" {
		fmt.Fprintf(&b, "  answer: %s\n", q.Answer)
	}
	for _, a := range q.Attempts {
		fmt.Fprintf(&b, "  attempt %d: %q -> %s\n", a.Seq, a.Query, a.Outcome)
	}
	return b.String()
}

// changedLines describes the questions whose status or priority changed between two frontiers, one line each: the compact summary of
// what a mutation did.
func changedLines(before, after Frontier) string {
	var lines []string
	for _, q := range after.Questions {
		old, ok := before.Get(q.ID)
		switch {
		case !ok:
			lines = append(lines, "added "+questionLine(q))
		case old.Status != q.Status || old.Priority != q.Priority:
			lines = append(lines, fmt.Sprintf("[%s] %s -> %s, P %.2f", q.ID, old.Status, q.Status, q.Priority))
		}
	}
	return strings.Join(lines, "\n")
}

func (s State) with(f Frontier) State { s.Frontier = f; return s }

// --- frontier tools ---

const atomicPrefix = "Atomic objective:"

func isAtomic(n Notes) bool {
	return slices.ContainsFunc(strings.Split(n.Scratch, "\n"), func(l string) bool { return strings.HasPrefix(l, atomicPrefix) })
}

var frontierNextTool = Tool{
	Name:        "frontier_next",
	Description: "Select the next question to work on (highest priority actionable one) and mark it in_progress. Rejected until the objective has at least one sub-question (add_question), or you say the objective is atomic with atomic_reason.",
	Schema:      obj(props{"atomic_reason": str("only if the objective cannot be split into sub-questions: why. Stored in the notes.")}),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			AtomicReason string `json:"atomic_reason"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		root, ok := s.Frontier.Root()
		if !ok {
			return Result{}, errors.New("the run has no root question")
		}
		next, reason := s, ""
		if len(s.Frontier.Children(root.ID)) == 0 {
			switch {
			case isAtomic(s.Notes):
			case strings.TrimSpace(a.AtomicReason) != "":
				scratch := strings.TrimSpace(s.Notes.Scratch + "\n" + atomicPrefix + " " + clean(a.AtomicReason))
				n, err := SetScratch(s.Notes, scratch)
				if err != nil {
					return Result{}, err
				}
				next.Notes, reason = n, "notes_scratch"
			default:
				return Result{}, errors.New("the objective has no sub-questions yet: first expand it with add_question (typically 3 to 8, each with criteria and priority), or call frontier_next with atomic_reason if it cannot be split")
			}
			return done(s, next, nil, reason, "The objective is atomic; work on it directly.\n"+questionView(root)), nil
		}
		if cur, ok := s.Frontier.Current(); ok && cur.ParentID != "" {
			return Result{}, fmt.Errorf("%s is still in progress: resolve_question, retry_question or prune_branch it first", cur.ID)
		}
		f, q, ts := Select(s.Frontier)
		if q == nil {
			return Result{State: s, Text: "No actionable question left.\n" + Summary(s.Frontier, root.ID, s.Limits)}, nil
		}
		next = s.with(f)
		return done(s, next, ts, "", "Selected:\n"+questionView(*q)+"\n"+Summary(f, q.ID, s.Limits)), nil
	},
}

var frontierViewTool = Tool{
	Name:        "frontier_view",
	Description: "Inspect the frontier: one question with its attempt log (id), the questions of a status (status), or the summary.",
	Schema: obj(props{"status": enum("only the questions with this status", "pending", "in_progress", "resolved", "blocked", "unresolvable", "pruned"),
		"id": str("one question, in full")}),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			Status string `json:"status"`
			ID     string `json:"id"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		if a.ID != "" {
			q, ok := s.Frontier.Get(a.ID)
			if !ok {
				return Result{}, fmt.Errorf("unknown question %q", a.ID)
			}
			return Result{State: s, Text: questionView(q)}, nil
		}
		var lines []string
		for _, q := range s.Frontier.Questions {
			if a.Status == "" || string(q.Status) == a.Status {
				lines = append(lines, questionLine(q))
			}
		}
		cur, _ := s.Frontier.Current()
		text := Summary(s.Frontier, cur.ID, s.Limits)
		if a.Status != "" {
			text = strings.Join(lines, "\n")
			if text == "" {
				text = "no " + a.Status + " questions"
			}
		}
		return Result{State: s, Text: text}, nil
	},
}

var addQuestionTool = Tool{
	Name:        "add_question",
	Description: "Add a sub-question or hypothesis to the frontier when a document brings a contradiction, an unexpected dependency or a gap that blocks the criteria. The code assigns the id and depth and rejects duplicates, questions deeper than the depth cap and more than the question cap. Use blocks_parent when the parent cannot be answered before this one: it then gets at least the parent's priority.",
	Schema: obj(props{
		"parent_id":           str("the question this one comes from (default: the current question)"),
		"question":            str("the question, or the hypothesis statement"),
		"rationale":           str("why it matters for the objective"),
		"priority":            num("0 to 1: how critical the answer is"),
		"validation_criteria": str("what counts as an acceptable answer (for a hypothesis: what confirms and what refutes it)"),
		"target_sources":      strList("kinds of source to look for: benchmarks, source_code, paper_tables, official_docs, ..."),
		"depends_on":          strList("ids of questions that must be resolved first"),
		"kind":                enum("question or hypothesis", "question", "hypothesis"),
		"blocks_parent":       props{"type": "boolean", "description": "the parent depends on this question"},
	}, "question", "priority", "validation_criteria"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			ParentID           string   `json:"parent_id"`
			Question           string   `json:"question"`
			Rationale          string   `json:"rationale"`
			Priority           *float64 `json:"priority"`
			ValidationCriteria string   `json:"validation_criteria"`
			TargetSources      []string `json:"target_sources"`
			DependsOn          []string `json:"depends_on"`
			Kind               Kind     `json:"kind"`
			BlocksParent       bool     `json:"blocks_parent"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		if a.Priority == nil {
			return Result{}, errors.New("priority is required (0 to 1)")
		}
		if strings.TrimSpace(a.ValidationCriteria) == "" {
			return Result{}, errors.New("validation_criteria is required: say what counts as an acceptable answer")
		}
		parent := a.ParentID
		if parent == "" {
			cur, ok := s.Frontier.Current()
			if !ok {
				return Result{}, errors.New("parent_id is required: no question is in progress")
			}
			parent = cur.ID
		}
		f, ts, err := Add(s.Frontier, parent, AddInput{Kind: a.Kind, Question: a.Question, Rationale: a.Rationale, Priority: *a.Priority,
			ValidationCriteria: a.ValidationCriteria, TargetSources: a.TargetSources, DependsOn: a.DependsOn, BlocksParent: a.BlocksParent}, s.Limits)
		if err != nil {
			return Result{}, err
		}
		return done(s, s.with(f), ts, "", changedLines(s.Frontier, f)), nil
	},
}

var retryQuestionTool = Tool{
	Name:        "retry_question",
	Description: "Record an inconclusive attempt on the question in progress and the new query to try: it is logged and the question returns to pending. The new query must differ from the earlier ones. After the maximum number of attempts the question becomes unresolvable instead, and you must give the assumption you make.",
	Schema: obj(props{"id": str("the question"), "new_query": str("the next query formulation"), "why": str("what the last attempt yielded and why it was inconclusive"),
		"assumption": str("required when the attempts are used up: the assumption made in place of an answer")}, "id", "new_query", "why"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			ID         string `json:"id"`
			NewQuery   string `json:"new_query"`
			Why        string `json:"why"`
			Assumption string `json:"assumption"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		f, ts, err := Retry(s.Frontier, a.ID, a.NewQuery, a.Why, a.Assumption, s.Limits)
		if err != nil {
			return Result{}, err
		}
		next, reason := s.with(f), ""
		q, _ := f.Get(a.ID)
		text := changedLines(s.Frontier, f)
		if q.Status == StatusUnresolvable {
			next.Notes, _, err = Append(s.Notes, SectionAssumptions, []Entry{{QuestionID: q.ID, Text: q.Answer}})
			if err != nil {
				return Result{}, err
			}
			reason = "notes_append"
			text += "\nrecorded in notes: Assumptions & gaps"
		} else {
			text += fmt.Sprintf("\nattempts: %d of %d", q.AttemptCount(), s.Limits.MaxAttempts)
		}
		return done(s, next, ts, reason, text), nil
	},
}

// refutingAttempts is the number of attempts on a question that are the searches of challenges (they look for evidence against).
func refutingAttempts(s State, q Question) int {
	seqs := map[int]bool{}
	for _, c := range s.Challenges {
		if c.QuestionID == q.ID && c.AttemptSeq > 0 {
			seqs[c.AttemptSeq] = true
		}
	}
	return len(seqs)
}

// checkResolve is the pure checks of resolve_question that need more than the frontier. It returns what is missing (empty if nothing).
func checkResolve(s State, q Question, in ResolveInput) []string {
	var problems []string
	hasReason := strings.TrimSpace(in.Reason) != ""
	for _, id := range in.ClaimIDs {
		c, ok := s.Notes.Claim(id)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("claim %s does not exist (record it in the notes first)", id))
		case c.Status == ClaimRefuted || c.Status == ClaimRetracted:
			problems = append(problems, fmt.Sprintf("claim %s is %s: a cited claim must be at least UNVERIFIED", id, c.Status))
		}
	}
	if q.Kind == KindHypothesis && !hasReason {
		refuting := refutingAttempts(s, q)
		if confirming := q.AttemptCount() - refuting; confirming < 1 || refuting < 1 {
			problems = append(problems, "a hypothesis needs a confirming and a refuting search in its attempt log (or pass reason to say why not)")
		}
	}
	if RequiresAnswerChallenge(q) && !hasReason && !AnswerChallengeClosed(s.Challenges, q.ID) {
		problems = append(problems, fmt.Sprintf("%s has depth %d: its answer needs a closed challenge (challenge with target_type question_answer), or pass reason to say why not", q.ID, q.Depth))
	}
	return problems
}

var resolveQuestionTool = Tool{
	Name:        "resolve_question",
	Description: "Mark the question in progress resolved: its validation criteria are met. The code checks that the cited claims exist and are at least UNVERIFIED, that a hypothesis was searched for and against, and that a question of depth 0 or 1 has a closed challenge of its answer (or you give a reason).",
	Schema: obj(props{"id": str("the question"), "answer": str("the short answer"), "claim_ids": strList("ids of the claims and datapoints it rests on"),
		"reason": str("only to replace a missing hypothesis search or answer challenge: why it is not needed")}, "id", "answer"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			ID       string   `json:"id"`
			Answer   string   `json:"answer"`
			ClaimIDs []string `json:"claim_ids"`
			Reason   string   `json:"reason"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		q, ok := s.Frontier.Get(a.ID)
		if !ok {
			return Result{}, fmt.Errorf("unknown question %q", a.ID)
		}
		in := ResolveInput{Answer: a.Answer, ClaimIDs: a.ClaimIDs, Reason: a.Reason}
		if q.Status == StatusInProgress {
			if problems := checkResolve(s, q, in); len(problems) > 0 {
				return Result{}, fmt.Errorf("cannot resolve %s: %s", a.ID, strings.Join(problems, "; "))
			}
		}
		f, ts, err := Resolve(s.Frontier, a.ID, in)
		if err != nil {
			return Result{}, err
		}
		return done(s, s.with(f), ts, "", changedLines(s.Frontier, f)), nil
	},
}

var reopenQuestionTool = Tool{
	Name:        "reopen_question",
	Description: "Self-correction: reopen a resolved question because a later finding contradicts its answer. It re-enters the queue with a raised priority.",
	Schema:      obj(props{"id": str("the resolved question"), "why": str("what contradicts the answer"), "priority": num("0 to 1, optional: at least the old priority plus 0.2 is used")}, "id", "why"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			ID       string  `json:"id"`
			Why      string  `json:"why"`
			Priority float64 `json:"priority"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		f, ts, err := Reopen(s.Frontier, a.ID, a.Why, a.Priority, "")
		if err != nil {
			return Result{}, err
		}
		return done(s, s.with(f), ts, "", changedLines(s.Frontier, f)), nil
	},
}

var setPriorityTool = Tool{
	Name:        "set_priority",
	Description: "Change the priority of a question that is not finished. The dependencies of the question are raised to at least that priority.",
	Schema:      obj(props{"id": str("the question"), "priority": num("0 to 1"), "why": str("why")}, "id", "priority", "why"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			ID       string   `json:"id"`
			Priority *float64 `json:"priority"`
			Why      string   `json:"why"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		if a.Priority == nil {
			return Result{}, errors.New("priority is required (0 to 1)")
		}
		f, ts, err := SetPriority(s.Frontier, a.ID, *a.Priority, a.Why)
		if err != nil {
			return Result{}, err
		}
		text := changedLines(s.Frontier, f)
		if text == "" {
			text = "priority unchanged"
		}
		return done(s, s.with(f), ts, "", text), nil
	},
}

var pruneBranchTool = Tool{
	Name:        "prune_branch",
	Description: "Drop a question and all its pending and blocked descendants because its answer would not alter the conclusion (or the branch is invalid). The reason is stored.",
	Schema:      obj(props{"id": str("the question to drop"), "reason": str("why")}, "id", "reason"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		f, ts, err := PruneBranch(s.Frontier, a.ID, a.Reason)
		if err != nil {
			return Result{}, err
		}
		return done(s, s.with(f), ts, "", changedLines(s.Frontier, f)), nil
	},
}

// --- notes tools ---

var notesReadTool = Tool{
	Name:        "notes_read",
	Description: "Read the notes: verified and unverified claims, contradictions, datapoints, sources, assumptions and gaps, and the scratch text.",
	Schema:      obj(props{}),
	Handler: func(s State, _ json.RawMessage) (Result, error) {
		return Result{State: s, Text: Render(s.Notes)}, nil
	},
}

// sizeNote is the warning appended to a notes result when the notes are over their budget.
func sizeNote(s State) string {
	if t := TokenEstimate(s.Notes); t > s.Limits.NotesMaxTokens {
		return fmt.Sprintf("\nwarning: the notes are about %d tokens, over the budget of %d: merge duplicates and shorten Scratch", t, s.Limits.NotesMaxTokens)
	}
	return ""
}

var notesAppendTool = Tool{
	Name:        "notes_append",
	Description: "Add records to a section of the notes: Unverified (claims: text, question_id, sources, check), Datapoints (value, unit, text = context, sources), Sources (url, title, note), Contradictions (a, b = claim ids, question_id), Assumptions & gaps (text, question_id). Ids are assigned by the code and returned. Claims start UNVERIFIED; Verified takes no entries.",
	Schema: obj(props{"section": enum("the section", "Unverified", "Datapoints", "Sources", "Contradictions", "Assumptions & gaps"),
		"entries": props{"type": "array", "description": "the records", "items": obj(props{
			"text": str("claim text, datapoint context, or assumption"), "value": str("datapoint value, no spaces"), "unit": str("datapoint unit"),
			"question_id": str("the question it belongs to"), "check": str("what would verify the claim"),
			"sources": props{"type": "array", "description": "links to sources", "items": obj(props{"source_id": str("s_001"),
				"stance": enum("what the source says", "supports", "contradicts"), "independent": props{"type": "boolean", "description": "the source does not quote another source already listed for this claim"}}, "source_id")},
			"url": str("source URL"), "title": str("source title"), "note": str("e.g. read, nothing relevant"),
			"a": str("first claim id"), "b": str("second claim id")})}}, "section", "entries"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			Section string  `json:"section"`
			Entries []Entry `json:"entries"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		section, ok := ParseSection(a.Section)
		if !ok {
			return Result{}, fmt.Errorf("unknown section %q", a.Section)
		}
		for _, e := range a.Entries {
			if e.QuestionID != "" {
				if _, ok := s.Frontier.Get(e.QuestionID); !ok {
					return Result{}, fmt.Errorf("unknown question %q", e.QuestionID)
				}
			}
		}
		n, ids, err := Append(s.Notes, section, a.Entries)
		if err != nil {
			return Result{}, err
		}
		next := s
		next.Notes = n
		return done(s, next, nil, "notes_append", fmt.Sprintf("added to %s: %s%s", section, strings.Join(ids, ", "), sizeNote(next))), nil
	},
}

// onRefuted is what the code does after a claim becomes REFUTED: it queues a high-priority question to resolve the contradiction (or notes
// the gap if the question cannot be added) and reopens every resolved question that cited the claim in its answer. causedBy is the challenge
// id, or "". It returns the new state, the transitions and what it did, in words.
func onRefuted(s State, claimID, why, causedBy string) (State, []Transition, []string) {
	var ts []Transition
	var said []string
	claim, _ := s.Notes.Claim(claimID)
	parent := claim.QuestionID
	if q, ok := s.Frontier.Get(parent); !ok || q.Status == StatusPruned {
		parent = conclusionQuestion(s.Frontier)
	}
	f, added, err := Add(s.Frontier, parent, AddInput{Question: fmt.Sprintf("Resolve the contradiction: claim %s (%s) was refuted: %s. What is correct?", claimID, claim.Text, why),
		Rationale: "a refuted claim must be replaced by what the evidence supports", Priority: 0.9,
		ValidationCriteria: "an answer supported by independent sources that settles the contradiction", Actor: ActorCode, CausedBy: orElse(causedBy, claimID)}, s.Limits)
	switch {
	case err != nil:
		n, _, aerr := Append(s.Notes, SectionAssumptions, []Entry{{Text: clean(fmt.Sprintf("claim %s was refuted and no question could be queued to resolve it: %v", claimID, err))}})
		if aerr == nil {
			s.Notes = n
		}
		said = append(said, "could not queue a question for the contradiction ("+err.Error()+"); noted as a gap")
	default:
		s.Frontier, ts = f, append(ts, added...)
		said = append(said, "queued "+added[0].QuestionID+" to resolve the contradiction")
	}
	for _, q := range s.Frontier.Questions {
		if q.Status == StatusResolved && slices.Contains(ClaimRefs(q.Answer), claimID) {
			f, rt, rerr := Reopen(s.Frontier, q.ID, fmt.Sprintf("its answer cites %s, which was refuted", claimID), 0, orElse(causedBy, claimID))
			if rerr == nil {
				s.Frontier, ts = f, append(ts, rt...)
				said = append(said, "reopened "+q.ID)
			}
		}
	}
	return s, ts, said
}

func orElse(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var notesUpdateTool = Tool{
	Name:        "notes_update",
	Description: "Change a record: the status of a claim (VERIFIED needs enough independent sources and a survived challenge, and is checked by the code; REFUTED needs source_id and reason; RETRACTED needs reason), add a source link to a claim, amend text, check or note, or change the note or title of a source.",
	Schema: obj(props{"id": str("c_001, d_001 or s_001"), "change": obj(props{
		"status": enum("new status", "VERIFIED", "UNVERIFIED", "REFUTED", "RETRACTED"), "source_id": str("the source that refutes (for REFUTED)"), "reason": str("why"),
		"add_source": obj(props{"source_id": str("s_001"), "stance": enum("what the source says", "supports", "contradicts"),
			"independent": props{"type": "boolean", "description": "the source does not quote another source already listed for this claim"}}, "source_id"),
		"text": str("amended text"), "check": str("what would verify it"), "note": str("note"), "title": str("title of a source")})}, "id", "change"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			ID     string `json:"id"`
			Change Change `json:"change"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		n, err := Update(s.Notes, a.ID, a.Change, s.Challenges, s.Limits)
		if err != nil {
			return Result{}, err
		}
		next := s
		next.Notes = n
		text := "updated " + a.ID
		var ts []Transition
		if a.Change.Status == ClaimRefuted {
			var said []string
			next, ts, said = onRefuted(next, a.ID, a.Change.Reason, "")
			text += "; " + strings.Join(said, "; ")
		}
		return done(s, next, ts, "notes_update", text+sizeNote(next)), nil
	},
}

var notesScratchTool = Tool{
	Name:        "notes_scratch",
	Description: "Replace the Scratch section of the notes: free text for reasoning, plans and (when you state it) the conclusion of the run, which the challenge tool can attack. Bounded in size.",
	Schema:      obj(props{"text": str("the whole new scratch text")}, "text"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			Text string `json:"text"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		n, err := SetScratch(s.Notes, a.Text)
		if err != nil {
			return Result{}, err
		}
		next := s
		next.Notes = n
		return done(s, next, nil, "notes_scratch", fmt.Sprintf("scratch replaced (%d characters)%s", len([]rune(n.Scratch)), sizeNote(next))), nil
	},
}

// --- challenge tools ---

var challengeTool = Tool{
	Name:        "challenge",
	Description: "Register an adversarial query: a search whose purpose is to find evidence AGAINST a claim, a question's answer or the conclusion. State the negation and the strategy first; the query must use terms of the negation or the strategy that the target's text does not have, differ from every earlier query, and the same strategy is not used twice on a target. This runs nothing: afterwards call web_search with the registered query, then record_challenge_result.",
	Schema: obj(props{"target_type": enum("what is attacked", "claim", "question_answer", "conclusion"), "target_id": str("claim id or question id (empty for the conclusion)"),
		"negation": str("the belief inverted into the statement to be proven"), "strategy": enum("how", mapSlice(Strategies, func(x Strategy) string { return string(x) })...),
		"expected_if_false": str("what the evidence would look like if the belief were wrong"), "query": str("the search text, built from the negation and the strategy")},
		"target_type", "negation", "strategy", "expected_if_false", "query"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			TargetType      TargetType `json:"target_type"`
			TargetID        string     `json:"target_id"`
			Negation        string     `json:"negation"`
			Strategy        Strategy   `json:"strategy"`
			ExpectedIfFalse string     `json:"expected_if_false"`
			Query           string     `json:"query"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		chs, ch, err := Open(s.Challenges, OpenInput{TargetType: a.TargetType, TargetID: a.TargetID, Negation: a.Negation, Strategy: a.Strategy,
			ExpectedIfFalse: a.ExpectedIfFalse, Query: a.Query}, s.Frontier, s.Notes, s.Limits)
		if err != nil {
			return Result{}, err
		}
		next := s
		next.Challenges = chs
		return done(s, next, nil, "", fmt.Sprintf("challenge %s registered (%s on %s %s). Now run web_search with exactly: %q, then call record_challenge_result.", ch.ID, ch.Strategy, ch.TargetType, ch.TargetID, ch.Query)), nil
	},
}

var recordChallengeResultTool = Tool{
	Name:        "record_challenge_result",
	Description: "Close a challenge with its verdict after its search ran: survived (nothing credible against the target), weakened (qualifications: scope is added to the claim), refuted (credible independent evidence against it: claim REFUTED, a question queued, answers reopened) or inconclusive. weakened and refuted need evidence and source ids; survived needs the search to have run.",
	Schema: obj(props{"challenge_id": str("ch_001"), "outcome": enum("verdict", "survived", "weakened", "refuted", "inconclusive"), "evidence": str("what was found, short"),
		"source_ids": strList("sources of the evidence"), "result_claim_id": str("the claim the counter-evidence produced, if any")}, "challenge_id", "outcome"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			ChallengeID   string   `json:"challenge_id"`
			Outcome       Outcome  `json:"outcome"`
			Evidence      string   `json:"evidence"`
			SourceIDs     []string `json:"source_ids"`
			ResultClaimID string   `json:"result_claim_id"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		for _, id := range a.SourceIDs {
			if _, ok := findSource(s.Notes.Sources, id); !ok {
				return Result{}, fmt.Errorf("unknown source %q (add it to the Sources section first)", id)
			}
		}
		if a.ResultClaimID != "" {
			if _, ok := s.Notes.Claim(a.ResultClaimID); !ok {
				return Result{}, fmt.Errorf("unknown claim %q", a.ResultClaimID)
			}
		}
		chs, ch, err := Close(s.Challenges, a.ChallengeID, a.Outcome, a.Evidence, a.SourceIDs, a.ResultClaimID)
		if err != nil {
			return Result{}, err
		}
		next := s
		next.Challenges = chs
		next, ts, said, err := applyOutcome(next, ch)
		if err != nil {
			return Result{}, err
		}
		text := fmt.Sprintf("challenge %s closed: %s", ch.ID, ch.Outcome)
		if len(said) > 0 {
			text += "; " + strings.Join(said, "; ")
		}
		return done(s, next, ts, "notes_update", text), nil
	},
}

// applyOutcome makes the effects of a closed challenge on its target (the design's table of outcomes).
func applyOutcome(s State, ch Challenge) (State, []Transition, []string, error) {
	var ts []Transition
	var said []string
	switch {
	case ch.Outcome == OutcomeWeakened && ch.TargetType == TargetClaim:
		c, _ := s.Notes.Claim(ch.TargetID)
		c.Text = clean(c.Text + " [scope: " + ch.Evidence + "]")
		if c.Status == ClaimVerified {
			c.Status, c.Note = ClaimUnverified, "weakened by "+ch.ID
		}
		s.Notes = s.Notes.withClaim(c)
		said = append(said, fmt.Sprintf("%s amended with the scope and is %s", c.ID, c.Status))
	case ch.Outcome == OutcomeRefuted && ch.TargetType == TargetClaim:
		c, _ := s.Notes.Claim(ch.TargetID)
		c, err := Refute(c, ch.SourceIDs[0], ch.Evidence, s.Notes.Sources)
		if err != nil {
			return s, nil, nil, err
		}
		s.Notes = s.Notes.withClaim(c)
		said = append(said, c.ID+" is REFUTED")
		var more []string
		s, ts, more = onRefuted(s, c.ID, ch.Evidence, ch.ID)
		said = append(said, more...)
		if ch.ResultClaimID != "" {
			if n, _, err := Append(s.Notes, SectionContradictions, []Entry{{A: c.ID, B: ch.ResultClaimID}}); err == nil {
				s.Notes = n
			}
		}
	case ch.Outcome == OutcomeRefuted && ch.TargetType == TargetQuestionAnswer:
		if q, ok := s.Frontier.Get(ch.TargetID); ok && q.Status == StatusResolved {
			f, rt, err := Reopen(s.Frontier, q.ID, "its answer was refuted: "+ch.Evidence, 0, ch.ID)
			if err != nil {
				return s, nil, nil, err
			}
			s.Frontier, ts = f, rt
			said = append(said, "reopened "+q.ID)
		} else {
			said = append(said, "the answer of "+ch.TargetID+" is refuted: do not resolve it with that answer; retry_question")
		}
	case (ch.Outcome == OutcomeRefuted || ch.Outcome == OutcomeWeakened) && ch.TargetType == TargetConclusion:
		f, added, err := Add(s.Frontier, conclusionQuestion(s.Frontier), AddInput{Question: fmt.Sprintf("Settle the %s conclusion: %s", ch.Outcome, ch.Evidence),
			Rationale: "the conclusion of the run was " + string(ch.Outcome) + " by " + ch.ID, Priority: 0.9,
			ValidationCriteria: "the conclusion is restated so that the evidence found no longer contradicts it", Actor: ActorCode, CausedBy: ch.ID}, s.Limits)
		if err != nil {
			said = append(said, "could not queue a question ("+err.Error()+")")
		} else {
			s.Frontier, ts = f, added
			said = append(said, "queued "+added[0].QuestionID+"; exploration continues")
		}
	}
	return s, ts, said, nil
}

var challengesViewTool = Tool{
	Name:        "challenges_view",
	Description: "List the challenges (compact), e.g. the open ones or those on a target.",
	Schema:      obj(props{"target_id": str("a claim id or question id"), "status": enum("open or closed", "open", "closed")}),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			TargetID string `json:"target_id"`
			Status   string `json:"status"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		var lines []string
		for _, c := range s.Challenges {
			if (a.TargetID != "" && c.TargetID != a.TargetID) || (a.Status != "" && string(c.Status) != a.Status) {
				continue
			}
			l := fmt.Sprintf("[%s] %s on %s %s (%s) %q", c.ID, c.Status, c.TargetType, c.TargetID, c.Strategy, c.Query)
			if c.Outcome != "" {
				l += " -> " + string(c.Outcome)
			}
			lines = append(lines, l)
		}
		sort.Strings(lines)
		if len(lines) == 0 {
			return Result{State: s, Text: "no challenges"}, nil
		}
		return Result{State: s, Text: strings.Join(lines, "\n")}, nil
	},
}
