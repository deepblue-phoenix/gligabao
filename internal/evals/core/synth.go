package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Origin says who wrote the cases of an eval: a person (OriginHuman, the default) or an LLM from a SynthDef (OriginSynthetic). It is
// not a Type: a synthetic eval has the Type of its cases (knowledge) and runs like any other eval. An eval def is synthetic when it
// has a SynthDef (table gb_synth_defs).
type Origin string

// The origins of an eval.
const (
	OriginHuman     Origin = "human"
	OriginSynthetic Origin = "synthetic"
)

// SynthMode is how the generator asks the LLM for the cases.
type SynthMode string

const (
	// SynthSingle asks for one case per LLM call; every call is shown the queries of the cases so far, to avoid repeats.
	SynthSingle SynthMode = "single"
	// SynthBulk asks for SynthDef.BulkSize cases per call.
	SynthBulk SynthMode = "bulk"
)

// Valid says whether m is one of the modes.
func (m SynthMode) Valid() bool { return m == SynthSingle || m == SynthBulk }

// DefaultBulkSize is the number of cases per call of the bulk mode when none is given.
const DefaultBulkSize = 10

// SynthSettings are the parts of the definition of a synthetic eval that a version holds: changing any of them makes a new version.
type SynthSettings struct {
	// Prompt describes the eval dataset: what the cases test.
	Prompt string `json:"prompt"`
	// CorpusPrompt describes the synthetic data corpus the cases run on (the texts they remember); empty leaves it to the model.
	CorpusPrompt string `json:"corpus_prompt,omitempty"`
	// Model is the LLM model that generates the cases.
	Model string `json:"model"`
	// NumExamples is the number of cases of the eval.
	NumExamples int       `json:"num_examples"`
	Mode        SynthMode `json:"mode"`
	// BulkSize is the number of cases per call in the bulk mode.
	BulkSize int `json:"bulk_size"`
}

// Equal says whether two settings are the same definition.
func (s SynthSettings) Equal(o SynthSettings) bool { return s == o }

// SynthDef is the definition of a synthetic eval (table gb_synth_defs; its history is in gb_synth_def_versions). EvalDefID is the
// eval def whose cases it generates.
type SynthDef struct {
	EvalDefID EvalDefID `json:"eval_def_id"`
	UserID    string    `json:"user_id,omitempty"`
	// Version is the number of the current settings, from 1; every change of the settings adds 1.
	Version int `json:"version"`
	SynthSettings
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SynthVersion is one version of the settings of a synthetic eval (table gb_synth_def_versions).
type SynthVersion struct {
	EvalDefID EvalDefID `json:"eval_def_id"`
	Version   int       `json:"version"`
	SynthSettings
	CreatedAt time.Time `json:"created_at"`
}

// SynthGenerationID is the ID of a SynthGeneration.
type SynthGenerationID int64

// SynthGenerationStatus is the lifecycle of a SynthGeneration.
type SynthGenerationStatus string

// The statuses of a generation. There is no cancelled: it is short, and a server that dies leaves it errored.
const (
	SynthPending   SynthGenerationStatus = "pending"
	SynthRunning   SynthGenerationStatus = "running"
	SynthCompleted SynthGenerationStatus = "completed"
	SynthErrored   SynthGenerationStatus = "errored"
)

// Finished says whether the status is a final one.
func (s SynthGenerationStatus) Finished() bool { return s == SynthCompleted || s == SynthErrored }

// SynthGeneration is one run of the generator (table gb_synth_generations).
type SynthGeneration struct {
	ID        SynthGenerationID `json:"id"`
	UserID    string            `json:"user_id,omitempty"`
	EvalDefID EvalDefID         `json:"eval_def_id"`
	// Version is the version of the settings it used.
	Version int       `json:"version"`
	Model   string    `json:"model"`
	Mode    SynthMode `json:"mode"`

	Status SynthGenerationStatus `json:"status"`
	Error  string                `json:"error,omitempty"`
	// Requested is the number of cases to generate, Generated the number that were, Kept the number of cases of the user that it
	// left as they were.
	Requested    int     `json:"requested"`
	Generated    int     `json:"generated"`
	Kept         int     `json:"kept"`
	Calls        int     `json:"calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Timing
	// UpdatedAt is the last progress.
	UpdatedAt time.Time `json:"updated_at"`
}

// SynthCase says which generation made a case, and what it was when it did (table gb_synth_cases).
type SynthCase struct {
	EvalDefID    EvalDefID         `json:"eval_def_id"`
	CaseName     string            `json:"case_name"`
	GenerationID SynthGenerationID `json:"generation_id"`
	Version      int               `json:"version"`
	// ContentHash is CaseHash of the case as it was generated.
	ContentHash string    `json:"content_hash"`
	CreatedAt   time.Time `json:"created_at"`
}

// ErrSynthConflict means a generation of the eval is already going (HTTP 409).
var ErrSynthConflict = errors.New("evals: a generation of this eval is already in progress")

// CaseHash is the hash of the content of a knowledge case (the JSON of its fields, the name included), to tell whether it changed.
// Two cases with the same fields have the same hash whatever the formatting of the file they come from.
func CaseHash(c KnowledgeCase) string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
