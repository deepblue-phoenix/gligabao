package core

import (
	"encoding/json"
	"time"
)

// CaseDef is the definition of a case, held in the DB: one input, what is expected of it, and the parameters of the check.
// A case belongs to exactly one EvalDef; its Name is unique within that eval. What Input, Expected and Params contain
// depends on the Type of the eval, which also interprets them.
type CaseDef struct {
	ID        CaseDefID `json:"id"`
	EvalDefID EvalDefID `json:"eval_def_id"`
	Name      string    `json:"name"`
	// Position orders the case within its eval, from 0.
	Position    int    `json:"position"`
	Description string `json:"description,omitempty"`
	// Tags are free labels, to select and group cases.
	Tags []string `json:"tags,omitempty"`
	// Input is what is given to the system under test.
	Input Payload `json:"input,omitempty"`
	// Expected is what a correct outcome is: a reference answer, facts to find, strings to contain, ... It may be empty
	// (a case that is only scored, or only observed).
	Expected Payload `json:"expected,omitempty"`
	// Params are the settings of this case (limits, which checks to apply, ...), over the eval's Params.
	Params    Params    `json:"params,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CaseRun is one execution of a CaseDef within an EvalRun: what was given, what came back, and how it went.
type CaseRun struct {
	ID        CaseRunID `json:"id"`
	DefID     CaseDefID `json:"def_id"`
	EvalRunID EvalRunID `json:"eval_run_id"`

	// Status is the lifecycle: did it run to the end. Error says why not (StatusErrored only).
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`

	// Input is what was actually given (the def's Input, resolved if the Type does so), and Output what came back. The
	// def may change after the run; these stay as they were.
	Input  Payload `json:"input,omitempty"`
	Output Payload `json:"output,omitempty"`

	// The outcome of a COMPLETED run; zero when it did not complete.
	// Verdict is pass or fail against Expected (VerdictNone if the case is only scored).
	Verdict Verdict `json:"verdict,omitempty"`
	// Score is a number in [0, 1] (nil if the case has none); the Type says how it is computed.
	Score *float64 `json:"score,omitempty"`
	// Details is the type-specific result: the checks that held and the ones that did not, the judge's reasoning, the
	// references to trajectories, ...
	Details Payload `json:"details,omitempty"`

	CostUSD float64 `json:"cost_usd"`
	Timing
}

//-----------------------------------------------------

// TypeKnowledge is the Type of the knowledge evals (the files of bin/evals_knowledge): remember some texts in order, then
// ask a question about them and check what was stored and what is answered.
const TypeKnowledge Type = "knowledge"

// KnowledgeCase is a case of a knowledge eval, as it is in the "cases" of a file of bin/evals_knowledge. Its three parts are
// embedded, so the JSON stays flat: each part is what a CaseDef holds in the matching field (Input, Expected, Params).
type KnowledgeCase struct {
	Name string `json:"name"`
	KnowledgeInput
	KnowledgeExpected
	KnowledgeParams
}

// KnowledgeInput is the Input of a knowledge case: what is given to the system under test.
type KnowledgeInput struct {
	// Remember are the texts to remember, in order.
	Remember []KnowledgeArtifact `json:"remember,omitempty"`
	// Query is the question asked once everything is remembered. Empty: no recall (the case checks only the storage).
	Query string `json:"query,omitempty"`
}

// KnowledgeArtifact is one text to remember.
type KnowledgeArtifact struct {
	// Kind is the artifact kind ("knowledge" or "episode"); empty uses the server default.
	Kind string `json:"kind,omitempty"`
	Text string `json:"text,omitempty"`
	// TextFile, instead of Text, names a file (relative to the evals directory, e.g. "corpus/doc.txt") holding the text.
	TextFile string `json:"text_file,omitempty"`
	// ReferenceTime (RFC 3339) is when the text was written.
	ReferenceTime string `json:"reference_time,omitempty"`
	// WebSearch makes the remember call search the web for the latest world events about its key entities.
	WebSearch bool `json:"web_search,omitempty"`
	// Timeseries is the "timeseries" of the remember request, passed as is.
	Timeseries json.RawMessage `json:"timeseries,omitempty"`
}

// KnowledgeExpected is the Expected of a knowledge case: what a correct outcome is. A check is on only if its field is set.
type KnowledgeExpected struct {
	// ExpectContains are strings the answer to Query must contain.
	ExpectContains []string `json:"expect_contains,omitempty"`
	// ExpectEdges are the facts that must be stored.
	ExpectEdges []KnowledgeExpectEdge `json:"expect_edges,omitempty"`
	// ExpectMinChunks, when > 0, turns on the chunking checks: the texts must have been split into at least this many chunks.
	ExpectMinChunks int `json:"expect_min_chunks,omitempty"`
	// ExpectWebSearches, when > 0, turns on the web search checks: at least this many web_search artifacts must be stored.
	ExpectWebSearches int `json:"expect_web_searches,omitempty"`
	// ExpectSeries are the timeseries that must be stored.
	ExpectSeries []KnowledgeExpectSeries `json:"expect_series,omitempty"`
}

// KnowledgeExpectEdge is a temporal fact that must be stored between two entities (any relation, names case-insensitive).
type KnowledgeExpectEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	// ValidAt and InvalidAt are RFC 3339; empty is not checked.
	ValidAt   string `json:"valid_at,omitempty"`
	InvalidAt string `json:"invalid_at,omitempty"`
	// Ended says whether the fact must have an invalid_at.
	Ended bool `json:"ended"`
}

// KnowledgeExpectSeries is a stored series to check, identified by Key and Field ("value" if empty).
type KnowledgeExpectSeries struct {
	Key   string `json:"key"`
	Field string `json:"field,omitempty"`
	// Points is the number of points it must have; First and Last, when given, are the values of its first and last point.
	Points int      `json:"points"`
	First  *float64 `json:"first,omitempty"`
	Last   *float64 `json:"last,omitempty"`
	// AnchorEntity, when set, is the name (case-insensitive) of an entity it must be anchored to; AnchorArtifact says it
	// must be anchored to an artifact.
	AnchorEntity   string `json:"anchor_entity,omitempty"`
	AnchorArtifact bool   `json:"anchor_artifact,omitempty"`
}

// KnowledgeParams are the Params of a knowledge case: the settings it runs with.
type KnowledgeParams struct {
	// ChunkSize overrides the chunk size (in tokens); 0 keeps the default.
	ChunkSize int `json:"chunk_size,omitempty"`
	// Models picks the LLM model of the stages of remember ("extract_graph", "resolve_edges", "extract_image").
	Models map[string]string `json:"models,omitempty"`
	// Reasoning picks the reasoning level ("none", "low", "medium", "high") of each LLM stage ("extract_graph",
	// "summarize_chunk", "resolve_edges", "resolve_series", "extract_image", "recall").
	Reasoning map[string]string `json:"reasoning,omitempty"`
}

//-----------------------------------------------------

// TypeTraining is the Type of the training evals (the files of bin/evals_training): questions about the knowledge of a
// training dataset, phrased differently from its examples, asked to a trained model and judged against the expected answer.
const TypeTraining Type = "training"

// TrainingCase is a case of a training eval, as it is in the "cases" of a file of bin/evals_training. Its parts are
// embedded, so the JSON stays flat: each part is what a CaseDef holds in the matching field (Input, Expected).
type TrainingCase struct {
	Name string `json:"name"`
	TrainingInput
	TrainingExpected
}

// TrainingInput is the Input of a training case: what the trained model is asked.
type TrainingInput struct {
	Instruction string `json:"instruction"`
	// Input is optional context for the instruction.
	Input string `json:"input,omitempty"`
}

// TrainingExpected is the Expected of a training case: the answer the judge compares the model's to.
type TrainingExpected struct {
	Output string `json:"output"`
}
