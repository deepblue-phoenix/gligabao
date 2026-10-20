package core

import (
	"encoding/json"
	"time"
)

// CaseDef is the definition of a case, held in the DB: the part that every type of eval shares. A case belongs to exactly
// one EvalDef; its Name is unique within that eval, and its Type is that of the eval. What is specific to the Type (the input,
// what is expected of it, the settings of the check) is in Meta, which has its own table per Type.
type CaseDef struct {
	ID        CaseDefID `json:"id"`
	EvalDefID EvalDefID `json:"eval_def_id"`
	// Type is the Type of the EvalDef (kept here so that the type-specific tables can be tied to the right kind of case).
	Type Type   `json:"type"`
	Name string `json:"name"`
	// Position orders the case within its eval, from 0.
	Position    int    `json:"position"`
	Description string `json:"description,omitempty"`
	// Tags are free labels, to select and group cases.
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Meta is the type-specific part (KnowledgeCaseDefMeta, TrainingCaseDefMeta, ...), one row of the table of its Type. Its
	// CaseType is this case's Type. Filled by getters only; not stored with the universal row.
	Meta CaseDefMeta `json:"meta,omitempty"`
}

// CaseDefMeta is the type-specific part of a CaseDef. Each Type has one struct for it, stored in a table of its own that
// is keyed by the CaseDefID. A new Type adds a struct (and a table) here; CaseDef and its table do not change.
type CaseDefMeta interface {
	// CaseType is the Type whose cases this is the metadata of.
	CaseType() Type
}

// CaseRun is one execution of a CaseDef within an EvalRun: the part that every type of eval shares, which is what happened
// and how it went. What is specific to the Type (what was given, what came back, the details of the judgement) is in Meta,
// which has its own table per Type.
type CaseRun struct {
	ID        CaseRunID `json:"id"`
	DefID     CaseDefID `json:"def_id"`
	EvalRunID EvalRunID `json:"eval_run_id"`
	// Type is the Type of the CaseDef (kept here so that the type-specific tables can be tied to the right kind of run).
	Type Type `json:"type"`

	// Status is the lifecycle: did it run to the end. Error says why not (StatusErrored only).
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`

	// The outcome of a COMPLETED run; zero when it did not complete.
	// Verdict is pass or fail against what was expected (VerdictNone if the case is only scored).
	Verdict Verdict `json:"verdict,omitempty"`
	// Score is a number in [0, 1] (nil if the case has none); the Type says how it is computed.
	Score *float64 `json:"score,omitempty"`

	CostUSD float64 `json:"cost_usd"`
	Timing

	// Meta is the type-specific part (KnowledgeCaseRunMeta, TrainingCaseRunMeta, ...), one row of the table of its Type. Its
	// CaseType is this run's Type. Filled by getters only; not stored with the universal row.
	Meta CaseRunMeta `json:"meta,omitempty"`
}

// CaseRunMeta is the type-specific part of a CaseRun. Each Type has one struct for it, stored in a table of its own that
// is keyed by the CaseRunID. A new Type adds a struct (and a table) here; CaseRun and its table do not change.
type CaseRunMeta interface {
	// CaseType is the Type whose case runs this is the metadata of.
	CaseType() Type
}

//-----------------------------------------------------

// TypeKnowledge is the Type of the knowledge evals (the files of bin/evals_knowledge): remember some texts in order, then
// ask a question about them and check what was stored and what is answered.
const TypeKnowledge Type = "knowledge"

// KnowledgeCase is a case of a knowledge eval, as it is in the "cases" of a file of bin/evals_knowledge. Its three parts are
// embedded, so the JSON stays flat: together they are the columns of KnowledgeCaseDefMeta.
type KnowledgeCase struct {
	Name string `json:"name"`
	KnowledgeInput
	KnowledgeExpected
	KnowledgeParams
}

// KnowledgeCaseDefMeta is the metadata of a knowledge CaseDef (table gb_neo_case_def_meta_knowledge): the three parts of a
// KnowledgeCase, flat in the JSON.
type KnowledgeCaseDefMeta struct {
	CaseDefID CaseDefID `json:"case_def_id"`
	KnowledgeInput
	KnowledgeExpected
	KnowledgeParams
}

// CaseType implements CaseDefMeta.
func (KnowledgeCaseDefMeta) CaseType() Type { return TypeKnowledge }

// KnowledgeCaseRunMeta is the metadata of a knowledge CaseRun (table gb_neo_knowledge_case_run_meta). The Verdict of the case
// run is pass if Problems is empty.
type KnowledgeCaseRunMeta struct {
	CaseRunID CaseRunID `json:"case_run_id"`
	// Input is what was actually given: the def's input, with the text of every TextFile read in. The def may change after
	// the run; this stays as it was.
	Input KnowledgeInput `json:"input"`
	// Answer is the answer of the recall ("" if the case has no Query).
	Answer string `json:"answer,omitempty"`
	// Problems are the checks that did not hold, and PassedChecks the ones that did.
	Problems     []string `json:"problems,omitempty"`
	PassedChecks []string `json:"passed_checks,omitempty"`
	// DatasetID is the dataset the case ran in, which holds the graph it built (nil if it was never created).
	DatasetID *int64 `json:"dataset_id,omitempty"`
	// Steps is the total of ATIF steps over the trajectories of the case.
	Steps int `json:"steps"`

	// Trajectories are the ATIF trajectories of the case, in call order. Filled by getters only (table
	// gb_neo_knowledge_case_run_trajectories).
	Trajectories []KnowledgeTrajectory `json:"trajectories,omitempty"`
}

// CaseType implements CaseRunMeta.
func (KnowledgeCaseRunMeta) CaseType() Type { return TypeKnowledge }

// KnowledgeTrajectory is the reference to an ATIF trajectory (internal/ai/atif) of a knowledge case run. Only the reference
// is kept in the DB.
type KnowledgeTrajectory struct {
	// Seq is the order of the call within the case run, from 0.
	Seq int `json:"seq"`
	// Kind is "remember" or "recall".
	Kind         string  `json:"kind"`
	TrajectoryID string  `json:"trajectory_id"`
	AgentName    string  `json:"agent_name,omitempty"`
	Steps        int     `json:"steps"`
	CostUSD      float64 `json:"cost_usd"`
	// Path is the JSON file the trajectory was stored in ("" if none).
	Path string `json:"path,omitempty"`
	// ArtifactIDs are the artifacts the call is about (remember: its input; recall: the sources of the evidence).
	ArtifactIDs []int64   `json:"artifact_ids,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
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
// embedded, so the JSON stays flat: together they are the columns of TrainingCaseDefMeta.
type TrainingCase struct {
	Name string `json:"name"`
	TrainingInput
	TrainingExpected
}

// TrainingCaseDefMeta is the metadata of a training CaseDef (table gb_neo_case_def_meta_training): the two parts of a
// TrainingCase, flat in the JSON. A training case has no params.
type TrainingCaseDefMeta struct {
	CaseDefID CaseDefID `json:"case_def_id"`
	TrainingInput
	TrainingExpected
}

// CaseType implements CaseDefMeta.
func (TrainingCaseDefMeta) CaseType() Type { return TypeTraining }

// TrainingCaseRunMeta is the metadata of a training CaseRun (table gb_neo_training_case_run_meta). The CaseRun has a Score
// in [0, 1] and no Verdict; its Score is JudgeScore / 5.
type TrainingCaseRunMeta struct {
	CaseRunID CaseRunID `json:"case_run_id"`
	// Instruction, Input and Reference are what was actually asked and the expected answer, as they were in the def when
	// the case ran.
	Instruction string `json:"instruction"`
	Input       string `json:"input,omitempty"`
	Reference   string `json:"reference,omitempty"`
	// Response is the answer of the trained model ("" if it failed).
	Response string `json:"response,omitempty"`
	// Batch is the judge batch that scored the case, from 1 (0 if it was not judged).
	Batch int `json:"batch"`
	// JudgeScore is the score of the judge, 0 to 5 (nil unless judged); JudgeReason is its explanation.
	JudgeScore  *int   `json:"judge_score,omitempty"`
	JudgeReason string `json:"judge_reason,omitempty"`
	// InputTokens, OutputTokens and DurationMS are those of the answer of the model.
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	DurationMS   int `json:"duration_ms"`
}

// CaseType implements CaseRunMeta.
func (TrainingCaseRunMeta) CaseType() Type { return TypeTraining }

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
