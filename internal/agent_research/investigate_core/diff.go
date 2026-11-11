package investigate_core

import (
	"reflect"
	"slices"
)

// State is everything the tools read and change: the frontier, the notes (claims, sources and the rest) and the challenges of one run. It is
// an immutable value; a handler returns a new one.
type State struct {
	Limits     Limits
	Frontier   Frontier
	Notes      Notes
	Challenges []Challenge
}

// NewState is the state of a run that starts: the root question (the objective) and empty notes. It also returns the transitions of the root.
func NewState(objective string, limits Limits) (State, []Transition, error) {
	f, ts, err := NewRoot(objective)
	if err != nil {
		return State{}, nil, err
	}
	return State{Limits: limits, Frontier: f, Notes: Notes{Objective: clean(objective)}}, ts, nil
}

// Effect is a change to the database or the notes file that a handler asks the effect layer to make. The handlers do not make them.
type Effect interface{ isEffect() }

// UpsertQuestion writes the row of a question (not its dependencies or attempts, which have their own effects).
type UpsertQuestion struct{ Question Question }

// AddDependency writes the edge "QuestionID depends on DependsOnID".
type AddDependency struct{ QuestionID, DependsOnID string }

// InsertAttempt appends an attempt to the log of a question.
type InsertAttempt struct {
	QuestionID string
	Attempt    Attempt
}

// RecordTransitions appends transition rows; the store computes their seq in its transaction and the executor sets their StateSeq.
type RecordTransitions struct{ Transitions []Transition }

// UpsertSource writes a source.
type UpsertSource struct{ Source Source }

// UpsertClaim writes a claim or datapoint and its source links.
type UpsertClaim struct{ Claim Claim }

// UpsertChallenge writes a challenge (opened or closed), its cited sources and its attempt link (by the attempt seq in the question's log).
type UpsertChallenge struct{ Challenge Challenge }

// WriteNotes adds a version row with the whole notes text; the file is written after the transaction commits.
type WriteNotes struct {
	Text   string
	Reason string
}

func (UpsertQuestion) isEffect()    {}
func (AddDependency) isEffect()     {}
func (InsertAttempt) isEffect()     {}
func (RecordTransitions) isEffect() {}
func (UpsertSource) isEffect()      {}
func (UpsertClaim) isEffect()       {}
func (UpsertChallenge) isEffect()   {}
func (WriteNotes) isEffect()        {}

// row is a question without what has its own effects, to tell whether the row changed.
func row(q Question) Question {
	q.DependsOn, q.Attempts, q.TransSeq = nil, nil, 0
	return q
}

// Diff is the list of effects that takes the stored state before to after: question rows, new dependency edges, new attempts, the
// transitions, sources, claims and challenges that are new or changed, and (if notesReason is not "" and the rendered notes changed) the
// notes version. The order is the one the foreign keys need. It is pure: handlers describe the change by the two values.
func Diff(before, after State, transitions []Transition, notesReason string) []Effect {
	var effects []Effect
	for _, q := range after.Frontier.Questions {
		old, existed := before.Frontier.Get(q.ID)
		if !existed || !reflect.DeepEqual(row(old), row(q)) {
			effects = append(effects, UpsertQuestion{Question: q})
		}
	}
	for _, q := range after.Frontier.Questions {
		old, _ := before.Frontier.Get(q.ID)
		for _, d := range q.DependsOn {
			if !slices.Contains(old.DependsOn, d) {
				effects = append(effects, AddDependency{QuestionID: q.ID, DependsOnID: d})
			}
		}
	}
	for _, q := range after.Frontier.Questions {
		old, _ := before.Frontier.Get(q.ID)
		for _, a := range q.Attempts[min(len(old.Attempts), len(q.Attempts)):] {
			effects = append(effects, InsertAttempt{QuestionID: q.ID, Attempt: a})
		}
	}
	if len(transitions) > 0 {
		effects = append(effects, RecordTransitions{Transitions: slices.Clone(transitions)})
	}
	for _, s := range after.Notes.Sources {
		if old, ok := findSource(before.Notes.Sources, s.ID); !ok || old != s {
			effects = append(effects, UpsertSource{Source: s})
		}
	}
	for _, c := range append(slices.Clone(after.Notes.Claims), after.Notes.Datapoints...) {
		if old, ok := before.Notes.Claim(c.ID); !ok || !reflect.DeepEqual(old, c) {
			effects = append(effects, UpsertClaim{Claim: c})
		}
	}
	for _, c := range after.Challenges {
		i := slices.IndexFunc(before.Challenges, func(o Challenge) bool { return o.ID == c.ID })
		if i < 0 || !reflect.DeepEqual(before.Challenges[i], c) {
			effects = append(effects, UpsertChallenge{Challenge: c})
		}
	}
	if text := Render(after.Notes); notesReason != "" && text != Render(before.Notes) {
		effects = append(effects, WriteNotes{Text: text, Reason: notesReason})
	}
	return effects
}
