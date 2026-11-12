package investigate_core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Session drives the tools of a run against the store: the effect layer around the pure handlers. Each call loads the state, runs the
// handler, executes its effects and returns the text for the model. It holds no state between calls.
type Session struct {
	Env      Env
	Registry Registry
	Limits   Limits
}

// Env is what the effect layer works with: the store, the notes file and the run.
type Env struct {
	Store Store
	// Files is the notes file; nil means the notes are only stored in the database.
	Files     NotesStore
	RunID     int64
	NotesPath string
}

// Execute runs the effects in order inside one store transaction; an effect that fails aborts the whole list. The transitions get stateSeq.
// A notes write adds its version row in the same transaction; the working-copy file is written after the commit, never inside it (if that
// fails, the error wraps ErrNotesFile and the database is already right).
func Execute(ctx context.Context,
	env Env,
	stateSeq int,
	effects []Effect) error {
	if len(effects) == 0 {
		return nil
	}
	var notes *WriteNotes
	err := env.Store.InTx(ctx, func(tx Tx) error {
		notes = nil
		for _, e := range effects {
			if err := apply(ctx,
				tx,
				env.RunID,
				stateSeq,
				e); err != nil {
				return err
			}
			if w, ok := e.(WriteNotes); ok {
				notes = &w
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if notes != nil && env.Files != nil {
		if err := env.Files.Write(env.NotesPath, notes.Text); err != nil {
			return fmt.Errorf("%w: %w",
				ErrNotesFile,
				err)
		}
	}
	return nil
}

func apply(ctx context.Context,
	tx Tx,
	runID int64,
	stateSeq int,
	e Effect) error {
	switch e := e.(type) {
	case UpsertQuestion:
		return tx.UpsertQuestion(ctx,
			runID,
			e.Question)
	case AddDependency:
		return tx.AddDependency(ctx,
			runID,
			e.QuestionID,
			e.DependsOnID)
	case InsertAttempt:
		return tx.InsertAttempt(ctx,
			runID,
			e.QuestionID,
			e.Attempt)
	case RecordTransitions:
		for _, t := range e.Transitions {
			t.StateSeq = stateSeq
			if _, err := tx.InsertTransition(ctx,
				runID,
				t); err != nil {
				return err
			}
		}
		return nil
	case UpsertSource:
		return tx.UpsertSource(ctx,
			runID,
			e.Source)
	case UpsertClaim:
		return tx.UpsertClaim(ctx,
			runID,
			e.Claim)
	case UpsertChallenge:
		return tx.UpsertChallenge(ctx,
			runID,
			e.Challenge)
	case WriteNotes:
		_, err := tx.AddNotesVersion(ctx,
			runID,
			e.Text,
			e.Reason,
			stateSeq)
		return err
	}
	return fmt.Errorf("unknown effect %T", e)
}

// Start records a new run in the store: the root question for the objective.
func (s Session) Start(ctx context.Context,
	objective string) error {
	st, ts, err := NewState(objective, s.Limits)
	if err != nil {
		return err
	}
	seq, err := s.Env.Store.StateSeq(ctx, s.Env.RunID)
	if err != nil {
		return err
	}
	return Execute(ctx,
		s.Env,
		seq,
		Diff(State{},
			st,
			ts,
			""))
}

// Load is the state of the run as the store holds it.
func (s Session) Load(ctx context.Context) (State, error) {
	l, err := s.Env.Store.Load(ctx, s.Env.RunID)
	if err != nil {
		return State{}, err
	}
	notes, errs := Parse(l.NotesText)
	if len(errs) > 0 {
		return State{}, fmt.Errorf("the stored notes do not parse: %w", errs[0])
	}
	if root, ok := l.Frontier.Root(); ok && notes.Objective == "" {
		notes.Objective = clean(root.Question)
	}
	return State{
		Limits:     s.Limits,
		Frontier:   l.Frontier,
		Notes:      notes,
		Challenges: l.Challenges,
	}, nil
}

// Call runs the tool name with the arguments. An error from the tool or the store is returned (the text of a tool error is what the model
// should read).
func (s Session) Call(ctx context.Context,
	name string,
	args json.RawMessage) (string, error) {
	tool, ok := s.Registry.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	st, err := s.Load(ctx)
	if err != nil {
		return "", err
	}
	res, err := tool.Handler(st, args)
	if err != nil {
		return "", err
	}
	seq, err := s.Env.Store.StateSeq(ctx, s.Env.RunID)
	if err != nil {
		return "", err
	}
	if err := Execute(ctx,
		s.Env,
		seq,
		res.Effects); err != nil {
		return res.Text, err
	}
	return res.Text, nil
}

// Apply runs a pure change of the state as one list of effects.
func (s Session) Apply(ctx context.Context,
	notesReason string,
	fn func(State) (State, []Transition)) error {
	st, err := s.Load(ctx)
	if err != nil {
		return err
	}
	next, ts := fn(st)
	seq, err := s.Env.Store.StateSeq(ctx, s.Env.RunID)
	if err != nil {
		return err
	}
	if err := Execute(ctx,
		s.Env,
		seq,
		Diff(st,
			next,
			ts,
			notesReason)); err != nil && !errors.Is(err, ErrNotesFile) {
		return err
	}
	return nil
}
