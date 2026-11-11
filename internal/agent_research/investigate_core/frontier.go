package investigate_core

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Transition is one status (or priority) change of a question: a row of gb_agent_frontier_transitions. StateSeq is set by the effect layer.
type Transition struct {
	QuestionID string
	Seq        int
	From, To   Status
	Trigger    Trigger
	Actor      Actor
	Reason     string
	Priority   float64
	Attempts   int
	CausedBy   string
	StateSeq   int
}

// Frontier is the questions of a run, in creation order. It is an immutable value: every function returns a new one.
type Frontier struct {
	Questions []Question
}

// AddInput is what the model gives to add_question (and the objective, for the root).
type AddInput struct {
	Kind               Kind
	Question           string
	Rationale          string
	Priority           float64
	ValidationCriteria string
	TargetSources      []string
	DependsOn          []string
	// BlocksParent makes the new question a dependency of its parent (the question that this one unblocks): the child gets at least the
	// priority of the parent, and a parent that is in progress goes back to pending until the child is resolved.
	BlocksParent bool
	// Actor says who adds it (the model by default) and CausedBy the question or challenge that led to it ("" if none).
	Actor    Actor
	CausedBy string
}

// ResolveInput is what the model gives to resolve_question.
type ResolveInput struct {
	Answer   string
	ClaimIDs []string
	// Reason is the explicit reason that replaces a check the tool layer made (a hypothesis without both searches, a shallow question
	// without an answer challenge); it is stored in the transition.
	Reason string
}

// Status is the lifecycle state of a question (the design's Lifecycle section).
type Status string

// The statuses of a question.
const (
	StatusPending      Status = "pending"
	StatusInProgress   Status = "in_progress"
	StatusResolved     Status = "resolved"
	StatusBlocked      Status = "blocked"
	StatusUnresolvable Status = "unresolvable"
	StatusPruned       Status = "pruned"
)

// Kind is what a question is: a plain question or a hypothesis (a statement to confirm or refute).
type Kind string

// The kinds of a question.
const (
	KindQuestion   Kind = "question"
	KindHypothesis Kind = "hypothesis"
)

// Trigger is what caused a transition: a closed vocabulary (the constructors take only these).
type Trigger string

// The triggers of a transition. TriggerDependencyAdded is not in the design's list: it is the return of an in-progress question to
// pending when a child that blocks it is added (see AddInput.BlocksParent).
const (
	TriggerCreated           Trigger = "created"
	TriggerSelected          Trigger = "selected"
	TriggerResolved          Trigger = "resolved"
	TriggerRetried           Trigger = "retried"
	TriggerAttemptsExhausted Trigger = "attempts_exhausted"
	TriggerReopened          Trigger = "reopened"
	TriggerDependencyFailed  Trigger = "dependency_failed"
	TriggerDependencyMet     Trigger = "dependency_met"
	TriggerDependencyAdded   Trigger = "dependency_added"
	TriggerReprioritized     Trigger = "reprioritized"
	TriggerPrunedBranch      Trigger = "pruned_branch"
	TriggerPrunedDepth       Trigger = "pruned_depth"
	TriggerPrunedScope       Trigger = "pruned_scope"
	TriggerContradiction     Trigger = "contradiction"
	TriggerDelegated         Trigger = "delegated"
)

// TriggerDelegated is the selection of a question for a worker (a sub-agent, see delegate.go): pending to in_progress, by code.

// Actor says who made a transition: the model through a tool, or a deterministic rule.
type Actor string

// The actors.
const (
	ActorAgent Actor = "agent"
	ActorCode  Actor = "code"
)

// Errors of the frontier. They are wrapped with the details, so test them with errors.Is.
var (
	// ErrDepthCap is returned by Add for a question deeper than MaxDepth; its text is what the model is told.
	ErrDepthCap          = errors.New("pruned by depth cap; record it as an open gap in notes instead")
	ErrDuplicateQuestion = errors.New("duplicate question")
	ErrTooManyQuestions  = errors.New("too many questions")
	ErrCycle             = errors.New("dependency cycle")
	ErrUnknownQuestion   = errors.New("unknown question")
	ErrBadStatus         = errors.New("question is not in the right status")
	ErrInvalid           = errors.New("invalid input")
)

// Attempt is one query tried on a question (a row of the attempt log).
type Attempt struct {
	// Seq is 1, 2, ... per question.
	Seq     int
	Query   string
	Tool    string
	Outcome string
}

// Question is a node of the frontier (the design's question record). Attempts are the attempt log: the number of attempts is its length.
type Question struct {
	ID                 string
	ParentID           string // "" for the root
	Depth              int
	Kind               Kind
	Question           string
	Rationale          string
	Priority           float64
	Status             Status
	ValidationCriteria string
	TargetSources      []string
	// Answer is the answer (resolved), the assumption (unresolvable) or the reason (pruned).
	Answer    string
	DependsOn []string
	Attempts  []Attempt
	// TransSeq is the seq of the last transition of the question (1 is its creation).
	TransSeq int
}

// AttemptCount is the number of distinct query formulations tried.
func (q Question) AttemptCount() int { return len(q.Attempts) }

// NewFrontier is the frontier of the questions, in creation order (by id number): for the store that loads them.
func NewFrontier(questions []Question) Frontier {
	qs := slices.Clone(questions)
	slices.SortFunc(qs, func(a, b Question) int { return idCompare(a.ID, b.ID) })
	return Frontier{Questions: qs}
}

// Get returns the question with that id.
func (f Frontier) Get(id string) (Question, bool) {
	i := f.index(id)
	if i < 0 {
		return Question{}, false
	}
	return f.Questions[i], true
}

func (f Frontier) index(id string) int {
	return slices.IndexFunc(f.Questions, func(q Question) bool { return q.ID == id })
}

// IDs are the ids of the questions.
func (f Frontier) IDs() []string {
	ids := make([]string, len(f.Questions))
	for i, q := range f.Questions {
		ids[i] = q.ID
	}
	return ids
}

// Root is the root question (the objective), if there is one.
func (f Frontier) Root() (Question, bool) {
	if len(f.Questions) == 0 {
		return Question{}, false
	}
	return f.Questions[0], true
}

// Children are the questions whose parent is id.
func (f Frontier) Children(id string) []Question {
	var out []Question
	for _, q := range f.Questions {
		if q.ParentID == id {
			out = append(out, q)
		}
	}
	return out
}

// Current is the question being worked on: the in-progress one that is not the root, else the root if it is in progress.
func (f Frontier) Current() (Question, bool) {
	var root Question
	var hasRoot bool
	for _, q := range f.Questions {
		if q.Status != StatusInProgress {
			continue
		}
		if q.ParentID == "" {
			root, hasRoot = q, true
			continue
		}
		return q, true
	}
	return root, hasRoot
}

// with returns f with q replacing the question of its id (or appended, if new).
func (f Frontier) with(q Question) Frontier {
	qs := slices.Clone(f.Questions)
	if i := f.index(q.ID); i >= 0 {
		qs[i] = q
	} else {
		qs = append(qs, q)
	}
	return Frontier{Questions: qs}
}

// Normalize is the form of a text that is compared to find duplicates: lower case, letters and digits only, single spaces.
func Normalize(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// move returns q in the status to, and the transition that records it.
func move(q Question, to Status, trigger Trigger, actor Actor, reason, causedBy string) (Question, Transition) {
	t := Transition{QuestionID: q.ID, Seq: q.TransSeq + 1, From: q.Status, To: to, Trigger: trigger, Actor: actor, Reason: reason,
		Priority: q.Priority, Attempts: len(q.Attempts), CausedBy: causedBy}
	q.Status = to
	q.TransSeq++
	return q, t
}

// reprioritize returns q with priority p, and the transition (from and to are the current status).
func reprioritize(q Question, p float64, actor Actor, reason, causedBy string) (Question, Transition) {
	q.Priority = p
	t := Transition{QuestionID: q.ID, Seq: q.TransSeq + 1, From: q.Status, To: q.Status, Trigger: TriggerReprioritized, Actor: actor, Reason: reason,
		Priority: p, Attempts: len(q.Attempts), CausedBy: causedBy}
	q.TransSeq++
	return q, t
}

// created returns the question as new (pending) and its first transition.
func created(q Question, actor Actor, reason string) (Question, Transition) {
	q.Status = StatusPending
	q.TransSeq = 1
	return q, Transition{QuestionID: q.ID, Seq: 1, From: "", To: StatusPending, Trigger: TriggerCreated, Actor: actor, Reason: reason,
		Priority: q.Priority, Attempts: 0}
}

// NewRoot creates the root question q_001 (the objective) and selects it: the objective is always being worked on, and its children are
// what the loop selects. It returns two transitions (created, selected).
func NewRoot(objective string) (Frontier, []Transition, error) {
	if strings.TrimSpace(objective) == "" {
		return Frontier{}, nil, fmt.Errorf("%w: the objective is empty", ErrInvalid)
	}
	q, t1 := created(Question{ID: NextQuestionID(nil), Kind: KindQuestion, Question: strings.TrimSpace(objective), Priority: 1,
		Rationale:          "the objective of the run",
		ValidationCriteria: "the objective is answered by verified claims, with the gaps and assumptions stated"}, ActorCode, "root question: the objective")
	q, t2 := move(q, StatusInProgress, TriggerSelected, ActorCode, "the objective is always in progress", "")
	return Frontier{}.with(q), []Transition{t1, t2}, nil
}

// Add creates a child of parentID. It assigns the id and the depth, and rejects: a depth above MaxDepth (ErrDepthCap), a question that is a
// normalized duplicate of one that exists, more than MaxQuestions questions, unknown or failed dependencies, and a dependency cycle. It
// returns the transitions it produced: the creation of the question first, then (BlocksParent) the return of the parent to pending, then
// the raises of priority of the dependencies.
func Add(f Frontier, parentID string, in AddInput, limits Limits) (Frontier, []Transition, error) {
	parent, ok := f.Get(parentID)
	if !ok {
		return f, nil, fmt.Errorf("%w: parent %q", ErrUnknownQuestion, parentID)
	}
	if parent.Status == StatusPruned {
		return f, nil, fmt.Errorf("%w: parent %s is pruned", ErrBadStatus, parentID)
	}
	if parent.Depth+1 > limits.MaxDepth {
		return f, nil, ErrDepthCap
	}
	text := strings.TrimSpace(in.Question)
	if text == "" {
		return f, nil, fmt.Errorf("%w: the question is empty", ErrInvalid)
	}
	if in.Priority < 0 || in.Priority > 1 {
		return f, nil, fmt.Errorf("%w: priority %v is not between 0 and 1", ErrInvalid, in.Priority)
	}
	kind := in.Kind
	switch kind {
	case "":
		kind = KindQuestion
	case KindQuestion, KindHypothesis:
	default:
		return f, nil, fmt.Errorf("%w: kind %q is not question or hypothesis", ErrInvalid, kind)
	}
	norm := Normalize(text)
	for _, q := range f.Questions {
		if Normalize(q.Question) == norm {
			return f, nil, fmt.Errorf("%w: %s already asks that", ErrDuplicateQuestion, q.ID)
		}
	}
	if len(f.Questions) >= limits.MaxQuestions {
		return f, nil, fmt.Errorf("%w: the run has %d questions (the most is %d); prune or resolve some", ErrTooManyQuestions, len(f.Questions), limits.MaxQuestions)
	}
	deps := dedupe(in.DependsOn)
	slices.SortFunc(deps, idCompare)
	for _, d := range deps {
		dq, ok := f.Get(d)
		if !ok {
			return f, nil, fmt.Errorf("%w: dependency %q", ErrUnknownQuestion, d)
		}
		if dq.Status == StatusPruned || dq.Status == StatusUnresolvable {
			return f, nil, fmt.Errorf("%w: dependency %s is %s and can never be resolved", ErrBadStatus, d, dq.Status)
		}
	}
	priority := in.Priority
	if in.BlocksParent {
		priority = max(priority, parent.Priority)
	}
	child := Question{ID: NextQuestionID(f.IDs()), ParentID: parentID, Depth: parent.Depth + 1, Kind: kind, Question: text,
		Rationale: strings.TrimSpace(in.Rationale), Priority: priority, ValidationCriteria: strings.TrimSpace(in.ValidationCriteria),
		TargetSources: slices.Clone(in.TargetSources), DependsOn: deps}
	actor := in.Actor
	if actor == "" {
		actor = ActorAgent
	}
	child, tr := created(child, actor, "added under "+parentID)
	tr.CausedBy = in.CausedBy
	out := f.with(child)
	ts := []Transition{tr}
	if in.BlocksParent {
		var err error
		if out, err = addDep(out, parentID, child.ID); err != nil {
			return f, nil, err
		}
		if p, _ := out.Get(parentID); p.Status == StatusInProgress {
			p, t := move(p, StatusPending, TriggerDependencyAdded, ActorCode, child.ID+" must be resolved first", child.ID)
			out = out.with(p)
			ts = append(ts, t)
		}
	}
	out, raised := raiseDeps(out, child.ID)
	return out, append(ts, raised...), nil
}

func dedupe(ids []string) []string {
	var out []string
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// addDep makes id depend on dep; it fails if that would make a cycle.
func addDep(f Frontier, id, dep string) (Frontier, error) {
	if id == dep || reaches(f, dep, id) {
		return f, fmt.Errorf("%w: %s cannot depend on %s", ErrCycle, id, dep)
	}
	q, _ := f.Get(id)
	if slices.Contains(q.DependsOn, dep) {
		return f, nil
	}
	q.DependsOn = append(slices.Clone(q.DependsOn), dep)
	slices.SortFunc(q.DependsOn, idCompare)
	return f.with(q), nil
}

// reaches reports whether to can be reached from from by following dependency edges (depth-first search).
func reaches(f Frontier, from, to string) bool {
	seen := map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if id == to {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		q, _ := f.Get(id)
		return slices.ContainsFunc(q.DependsOn, visit)
	}
	return visit(from)
}

// raiseDeps raises, recursively, every unfinished dependency of id to at least the priority of its dependant (a reprioritized
// transition each): unblocking work is never starved.
func raiseDeps(f Frontier, id string) (Frontier, []Transition) {
	q, _ := f.Get(id)
	var ts []Transition
	for _, d := range q.DependsOn {
		dq, _ := f.Get(d)
		if dq.Status == StatusResolved || dq.Status == StatusPruned || dq.Status == StatusUnresolvable || dq.Priority >= q.Priority {
			continue
		}
		dq, t := reprioritize(dq, q.Priority, ActorCode, "raised to the priority of "+id+", which depends on it", id)
		f = f.with(dq)
		var more []Transition
		f, more = raiseDeps(f, d)
		ts = append(append(ts, t), more...)
	}
	return f, ts
}

// better orders questions for selection: higher priority, then lower depth, then older.
func better(a, b Question) int {
	switch {
	case a.Priority != b.Priority:
		if a.Priority > b.Priority {
			return -1
		}
		return 1
	case a.Depth != b.Depth:
		return a.Depth - b.Depth
	}
	return trailingNumber(a.ID) - trailingNumber(b.ID)
}

// Actionable reports whether q is pending and every dependency is resolved.
func (f Frontier) Actionable(q Question) bool {
	return q.Status == StatusPending && len(f.unresolvedDeps(q)) == 0
}

func (f Frontier) unresolvedDeps(q Question) []Question {
	var out []Question
	for _, d := range q.DependsOn {
		if dq, ok := f.Get(d); ok && dq.Status != StatusResolved {
			out = append(out, dq)
		}
	}
	slices.SortFunc(out, better)
	return out
}

// pendingByOrder are the pending questions in selection order.
func (f Frontier) pendingByOrder() []Question {
	var out []Question
	for _, q := range f.Questions {
		if q.Status == StatusPending {
			out = append(out, q)
		}
	}
	slices.SortFunc(out, better)
	return out
}

// path is the chain from q down to the question that can be worked on now (q itself if it is actionable): each link is an unresolved
// pending dependency of the one before.
func (f Frontier) path(q Question) ([]string, bool) {
	deps := f.unresolvedDeps(q)
	if len(deps) == 0 {
		return []string{q.ID}, true
	}
	for _, d := range deps {
		if d.Status != StatusPending {
			continue
		}
		if rest, ok := f.path(d); ok {
			return append([]string{q.ID}, rest...), true
		}
	}
	return nil, false
}

// Select is the selection rule: among the pending questions whose dependencies are resolved, the highest priority, then the lowest depth,
// then the oldest. If the best pending question waits on a dependency, the dependency is taken instead and its priority (and that of
// every link of the chain) is raised to at least that of its dependant (reprioritized transitions). The chosen question becomes
// in_progress. It returns nil when nothing can be worked on.
func Select(f Frontier) (Frontier, *Question, []Transition) {
	for _, c := range f.pendingByOrder() {
		chain, ok := f.path(c)
		if !ok {
			continue
		}
		var ts []Transition
		for i := 0; i+1 < len(chain); i++ {
			up, _ := f.Get(chain[i])
			dep, _ := f.Get(chain[i+1])
			if dep.Priority < up.Priority {
				var t Transition
				dep, t = reprioritize(dep, up.Priority, ActorCode, "raised to the priority of "+up.ID+", which waits on it", up.ID)
				f = f.with(dep)
				ts = append(ts, t)
			}
		}
		q, _ := f.Get(chain[len(chain)-1])
		q, t := move(q, StatusInProgress, TriggerSelected, ActorCode, "highest priority actionable question", "")
		return f.with(q), &q, append(ts, t)
	}
	return f, nil, nil
}

// ReasonMarker starts the stored reason in the answer of a question that was resolved with an explicit reason ("(reason: ...)").
const ReasonMarker = "(reason:"

var claimRef = regexp.MustCompile(`\b[cd]_\d+\b`)

// ClaimRefs are the claim and datapoint ids that text cites.
func ClaimRefs(text string) []string { return dedupe(claimRef.FindAllString(text, -1)) }

// Resolve marks an in-progress question resolved. The question must have validation criteria and the answer must not be empty. The
// answer is stored with the claim ids ("answer [c_001, c_002]"). It then runs Reblock.
func Resolve(f Frontier, id string, in ResolveInput) (Frontier, []Transition, error) {
	q, err := mustBe(f, id, StatusInProgress)
	if err != nil {
		return f, nil, err
	}
	answer := strings.TrimSpace(in.Answer)
	if answer == "" {
		return f, nil, fmt.Errorf("%w: the answer is empty", ErrInvalid)
	}
	if strings.TrimSpace(q.ValidationCriteria) == "" {
		return f, nil, fmt.Errorf("%w: %s has no validation criteria, so there is nothing to meet", ErrInvalid, id)
	}
	if len(in.ClaimIDs) > 0 {
		answer += " [" + strings.Join(dedupe(in.ClaimIDs), ", ") + "]"
	}
	if r := clean(in.Reason); r != "" { // stored with the answer: the exploration gate reads it (ReasonMarker)
		answer += " " + ReasonMarker + " " + r + ")"
	}
	reason := truncate(answer, 200)
	q.Answer = answer
	q, t := move(q, StatusResolved, TriggerResolved, ActorAgent, reason, "")
	out, more := Reblock(f.with(q))
	return out, append([]Transition{t}, more...), nil
}

// Retry handles an inconclusive attempt on an in-progress question. While the question has fewer than MaxAttempts attempts, newQuery (which
// must differ from every earlier query, normalized) is logged as the next attempt with why as its outcome, and the question goes back to
// pending. At MaxAttempts it becomes unresolvable instead, and the assumption is required (it is stored as the answer).
func Retry(f Frontier, id, newQuery, why, assumption string, limits Limits) (Frontier, []Transition, error) {
	q, err := mustBe(f, id, StatusInProgress)
	if err != nil {
		return f, nil, err
	}
	why = strings.TrimSpace(why)
	if why == "" {
		return f, nil, fmt.Errorf("%w: say why the attempt was inconclusive", ErrInvalid)
	}
	if len(q.Attempts) >= limits.MaxAttempts {
		assumption = strings.TrimSpace(assumption)
		if assumption == "" {
			return f, nil, fmt.Errorf("%w: %s has used its %d attempts and becomes unresolvable: state the assumption you make instead", ErrInvalid, id, limits.MaxAttempts)
		}
		q.Answer = assumption
		q, t := move(q, StatusUnresolvable, TriggerAttemptsExhausted, ActorCode, assumption, "")
		out, more := Reblock(f.with(q))
		return out, append([]Transition{t}, more...), nil
	}
	norm := Normalize(newQuery)
	if norm == "" {
		return f, nil, fmt.Errorf("%w: the new query is empty", ErrInvalid)
	}
	for _, a := range q.Attempts {
		if Normalize(a.Query) == norm {
			return f, nil, fmt.Errorf("%w: that query was already tried (attempt %d); formulate a different one", ErrDuplicateQuestion, a.Seq)
		}
	}
	q.Attempts = append(slices.Clone(q.Attempts), Attempt{Seq: len(q.Attempts) + 1, Query: strings.TrimSpace(newQuery), Outcome: why})
	q, t := move(q, StatusPending, TriggerRetried, ActorAgent, why, "")
	return f.with(q), []Transition{t}, nil
}

// AddAttempt appends an attempt to the log of a question (no status change, so no transition).
func AddAttempt(f Frontier, id string, a Attempt) (Frontier, Attempt, error) {
	q, ok := f.Get(id)
	if !ok {
		return f, Attempt{}, fmt.Errorf("%w: %q", ErrUnknownQuestion, id)
	}
	a.Seq = len(q.Attempts) + 1
	q.Attempts = append(slices.Clone(q.Attempts), a)
	return f.with(q), a, nil
}

// Reopen returns a resolved question to pending with a raised priority (priority if it is above the current one, else 0.2 more, at most
// 1), clearing its answer; the reason and the cause (a question or challenge id, or "") go in the transition.
func Reopen(f Frontier, id, why string, priority float64, causedBy string) (Frontier, []Transition, error) {
	q, err := mustBe(f, id, StatusResolved)
	if err != nil {
		return f, nil, err
	}
	if strings.TrimSpace(why) == "" {
		return f, nil, fmt.Errorf("%w: say why %s is reopened", ErrInvalid, id)
	}
	if priority < 0 || priority > 1 {
		return f, nil, fmt.Errorf("%w: priority %v is not between 0 and 1", ErrInvalid, priority)
	}
	q.Priority = max(priority, min(1, q.Priority+0.2))
	q.Answer = ""
	q, t := move(q, StatusPending, TriggerReopened, ActorAgent, strings.TrimSpace(why), causedBy)
	out := f.with(q)
	out, raised := raiseDeps(out, id)
	return out, append([]Transition{t}, raised...), nil
}

// SetPriority changes the priority of a question that is not finished (a reprioritized transition) and raises the floor of its dependencies.
func SetPriority(f Frontier, id string, priority float64, why string) (Frontier, []Transition, error) {
	q, ok := f.Get(id)
	if !ok {
		return f, nil, fmt.Errorf("%w: %q", ErrUnknownQuestion, id)
	}
	if q.Status == StatusResolved || q.Status == StatusPruned || q.Status == StatusUnresolvable {
		return f, nil, fmt.Errorf("%w: %s is %s", ErrBadStatus, id, q.Status)
	}
	if priority < 0 || priority > 1 {
		return f, nil, fmt.Errorf("%w: priority %v is not between 0 and 1", ErrInvalid, priority)
	}
	if strings.TrimSpace(why) == "" {
		return f, nil, fmt.Errorf("%w: say why the priority changes", ErrInvalid)
	}
	if priority == q.Priority {
		return f, nil, nil
	}
	q, t := reprioritize(q, priority, ActorAgent, strings.TrimSpace(why), "")
	out, raised := raiseDeps(f.with(q), id)
	return out, append([]Transition{t}, raised...), nil
}

// PruneBranch drops a question (not the root) that is pending, blocked or in progress, and recursively all its pending and blocked
// descendants, one transition each: the question itself with trigger pruned_scope by the agent, the descendants pruned_branch by code.
// The reason is stored as the answer. It then runs Reblock.
func PruneBranch(f Frontier, id, reason string) (Frontier, []Transition, error) {
	q, ok := f.Get(id)
	if !ok {
		return f, nil, fmt.Errorf("%w: %q", ErrUnknownQuestion, id)
	}
	if q.ParentID == "" {
		return f, nil, fmt.Errorf("%w: the root question cannot be pruned", ErrInvalid)
	}
	if q.Status != StatusPending && q.Status != StatusBlocked && q.Status != StatusInProgress {
		return f, nil, fmt.Errorf("%w: %s is %s", ErrBadStatus, id, q.Status)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return f, nil, fmt.Errorf("%w: give the reason for pruning", ErrInvalid)
	}
	q.Answer = reason
	q, t := move(q, StatusPruned, TriggerPrunedScope, ActorAgent, reason, "")
	out, ts := f.with(q), []Transition{t}
	var drop func(parent string)
	drop = func(parent string) {
		for _, c := range out.Children(parent) {
			if c.Status == StatusPending || c.Status == StatusBlocked {
				c.Answer = "pruned with " + id + ": " + reason
				var tc Transition
				c, tc = move(c, StatusPruned, TriggerPrunedBranch, ActorCode, "descendant of "+id+": "+reason, id)
				out, ts = out.with(c), append(ts, tc)
				drop(c.ID)
			}
		}
	}
	drop(id)
	out, more := Reblock(out)
	return out, append(ts, more...), nil
}

// Reblock applies the automatic block and unblock: a pending question with a dependency that is pruned or unresolvable becomes blocked
// (dependency_failed), and a blocked question whose dependencies are all resolved becomes pending again (dependency_met).
func Reblock(f Frontier) (Frontier, []Transition) {
	var ts []Transition
	for _, q := range f.Questions {
		cur, _ := f.Get(q.ID)
		deps := make([]Question, 0, len(cur.DependsOn))
		for _, d := range cur.DependsOn {
			dq, _ := f.Get(d)
			deps = append(deps, dq)
		}
		failed := slices.IndexFunc(deps, func(d Question) bool { return d.Status == StatusPruned || d.Status == StatusUnresolvable })
		allResolved := !slices.ContainsFunc(deps, func(d Question) bool { return d.Status != StatusResolved })
		switch {
		case cur.Status == StatusPending && failed >= 0:
			next, t := move(cur, StatusBlocked, TriggerDependencyFailed, ActorCode, deps[failed].ID+" is "+string(deps[failed].Status), deps[failed].ID)
			f, ts = f.with(next), append(ts, t)
		case cur.Status == StatusBlocked && allResolved:
			next, t := move(cur, StatusPending, TriggerDependencyMet, ActorCode, "all dependencies are resolved", "")
			f, ts = f.with(next), append(ts, t)
		}
	}
	return f, ts
}

func mustBe(f Frontier, id string, want Status) (Question, error) {
	q, ok := f.Get(id)
	if !ok {
		return Question{}, fmt.Errorf("%w: %q", ErrUnknownQuestion, id)
	}
	if q.Status != want {
		return Question{}, fmt.Errorf("%w: %s is %s, it must be %s", ErrBadStatus, id, q.Status, want)
	}
	return q, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// Summary is the compact frontier text of the prompt (the design's Prompt contents): the counts by status, the current question with its
// criteria and the queries tried, the next three actionable ones, and the blocked ones with what they wait on.
func Summary(f Frontier, currentID string, limits Limits) string {
	counts := map[Status]int{}
	for _, q := range f.Questions {
		counts[q.Status]++
	}
	var parts []string
	for _, s := range []Status{StatusResolved, StatusInProgress, StatusPending, StatusBlocked, StatusUnresolvable, StatusPruned} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	if len(parts) == 0 {
		parts = []string{"empty"}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Frontier: %s  (max_depth %d)\n", strings.Join(parts, ", "), limits.MaxDepth)
	if cur, ok := f.Get(currentID); ok {
		fmt.Fprintf(&b, "Current: [%s] (P %.2f, depth %d) %s\n", cur.ID, cur.Priority, cur.Depth, cur.Question)
		if cur.ValidationCriteria != "" {
			fmt.Fprintf(&b, "  criteria: %s\n", cur.ValidationCriteria)
		}
		if len(cur.Attempts) > 0 {
			tried := make([]string, len(cur.Attempts))
			for i, a := range cur.Attempts {
				tried[i] = fmt.Sprintf("%q", a.Query)
			}
			fmt.Fprintf(&b, "  tried: %s\n", strings.Join(tried, ", "))
		}
	}
	var next []Question
	for _, q := range f.pendingByOrder() {
		if q.ID != currentID && f.Actionable(q) {
			next = append(next, q)
		}
	}
	if len(next) > 0 {
		b.WriteString("Next up:\n")
		for _, q := range next[:min(3, len(next))] {
			fmt.Fprintf(&b, "- [%s] (P %.2f) %s\n", q.ID, q.Priority, q.Question)
		}
	}
	for _, q := range f.Questions {
		if q.Status != StatusBlocked {
			continue
		}
		var waits []string
		for _, d := range f.unresolvedDeps(q) {
			waits = append(waits, d.ID)
		}
		fmt.Fprintf(&b, "Blocked: [%s] waits on %s\n", q.ID, strings.Join(waits, ", "))
	}
	return b.String()
}
