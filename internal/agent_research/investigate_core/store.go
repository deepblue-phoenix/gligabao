package investigate_core

import (
	"context"
	"errors"
)

// Loaded is a run's state as the store holds it: the frontier and the challenges as values, and the text of the latest notes version (""
// if the notes were never written).
type Loaded struct {
	Frontier   Frontier
	NotesText  string
	Challenges []Challenge
}

// NotesVersion is a row of gb_agent_notes_versions.
type NotesVersion struct {
	Seq      int
	StateSeq int
	Reason   string
	Text     string
}

// Tx is what the effects need of the database inside one transaction: one method per effect. They are executed by Execute; the
// implementations are in db/sqlite and db/postgres (investigate_store.go).
type Tx interface {
	UpsertQuestion(ctx context.Context, runID int64, q Question) error
	AddDependency(ctx context.Context, runID int64, questionID, dependsOnID string) error
	InsertAttempt(ctx context.Context, runID int64, questionID string, a Attempt) error
	// InsertTransition appends a transition row; its seq is computed in the transaction (the highest of the question plus one) and returned.
	InsertTransition(ctx context.Context, runID int64, t Transition) (seq int, err error)
	UpsertSource(ctx context.Context, runID int64, s Source) error
	UpsertClaim(ctx context.Context, runID int64, c Claim) error
	UpsertChallenge(ctx context.Context, runID int64, c Challenge) error
	// AddNotesVersion appends a version row (seq is the highest of the run plus one) and returns the seq.
	AddNotesVersion(ctx context.Context, runID int64, text, reason string, stateSeq int) (seq int, err error)
}

// Store is the database as the effect layer needs it (no database types).
type Store interface {
	// InTx runs fn in one transaction: it commits if fn returns nil, else rolls back.
	InTx(ctx context.Context, fn func(Tx) error) error
	// Load reads the frontier, the challenges and the latest notes text of a run.
	Load(ctx context.Context, runID int64) (Loaded, error)
	// LatestNotesVersion is the latest version of the notes of a run (ok is false if there is none).
	LatestNotesVersion(ctx context.Context, runID int64) (v NotesVersion, ok bool, err error)
	// StateSeq is the seq of the last research state of the run (0 if none): the state_seq of what is written now.
	StateSeq(ctx context.Context, runID int64) (int, error)
}

// ErrNotesFile is returned by Execute when everything was stored but writing the notes file failed. The version is in the database, so the
// loop restores the file from it at the next turn (stage 2).
var ErrNotesFile = errors.New("the notes were stored but the notes file could not be written")

// NotesStore is the small read/write interface the notes live behind, so that they can move (an object store) without touching the loop.
type NotesStore interface {
	Read(path string) (string, error)
	Write(path, text string) error
}
