package model

import (
	"encoding/json"
	"time"
)

//---------------------------------------------------------------------------------------------
// Remember runs (gb_remember_runs, see sqlite/db_schema.go)

// Statuses of a remember run as a whole and of each of its stages.
const (
	// RememberNotStarted is the status of a stage that has not begun, because the pipeline has not reached it or because
	// the call does not use it (no web search was asked for, ...).
	RememberNotStarted = "not_started"
	// RememberStarted is the status of a run, or a stage, that is executing now. A run that is left in it, after the process
	// is gone, died without finishing.
	RememberStarted = "started"
	// RememberSucceeded is the status of a run, or a stage, that ended without an error.
	RememberSucceeded = "succeeded"
	// RememberFailed is the status of a run, or a stage, that ended with an error (see RememberRun.Error and
	// RememberStage.Error).
	RememberFailed = "failed"
)

// RememberStatuses lists the statuses.
var RememberStatuses = []string{RememberNotStarted, RememberStarted, RememberSucceeded, RememberFailed}

// The stages of the remember pipeline, in pipeline order (RememberStages). Each is a column group of gb_remember_runs.
// The names of the stages that run a model are those of gb_inferences.stage (see memory_core.Stages).
const (
	// RememberStageClassifyComplexity is the judgement of Jev (system 1) of how complex the text is.
	RememberStageClassifyComplexity = "classify_complexity"
	// RememberStageChunkText is the split of a text of more than one chunk.
	RememberStageChunkText = "chunk_text"
	// RememberStageExtractGraph is the extraction of the entities and edges from the text (or each chunk).
	RememberStageExtractGraph = "extract_graph"
	// RememberStageSummarizeChunk is the summary of each chunk of a chunked text.
	RememberStageSummarizeChunk = "summarize_chunk"
	// RememberStageLoadCandidates is the lookup of the stored facts the new ones may repeat or contradict (it embeds the
	// endpoints of the new facts, with vectors).
	RememberStageLoadCandidates = "load_candidates"
	// RememberStageResolveEdges is the comparison of the new edges with the candidates.
	RememberStageResolveEdges = "resolve_edges"
	// RememberStageStore is the storage of the artifact (each chunk too), its entities and edges, and their embeddings.
	RememberStageStore = "store"
	// RememberStageStoreSeries is the storage of the timeseries the caller supplied.
	RememberStageStoreSeries = "store_series"
	// RememberStageResolveGeo is the detection of the entities that are places and the lookup of their coordinates (it
	// includes the detect_geo model call).
	RememberStageResolveGeo = "resolve_geo"
	// RememberStageWebSearch is the web search for the key entities, and what is stored from its results.
	RememberStageWebSearch = "web_search"
	// RememberStageFetchSeries is the fetch of series from the providers for the key entities (it includes the
	// resolve_series model call).
	RememberStageFetchSeries = "fetch_series"
	// RememberStageObserveSeries is the observation text made of each stored series, and what is stored from it.
	RememberStageObserveSeries = "observe_series"
)

// RememberStageDef describes a stage of the pipeline.
type RememberStageDef struct {
	Name string
	// Model says that the stage runs models (LLM calls, embedding requests, web searches): it records their inferences.
	Model bool
}

// RememberStages lists the stages of the remember pipeline, in order.
var RememberStages = []RememberStageDef{
	{RememberStageClassifyComplexity, true},
	{RememberStageChunkText, false},
	{RememberStageExtractGraph, true},
	{RememberStageSummarizeChunk, true},
	{RememberStageLoadCandidates, true},
	{RememberStageResolveEdges, true},
	{RememberStageStore, true},
	{RememberStageStoreSeries, false},
	{RememberStageResolveGeo, true},
	{RememberStageWebSearch, true},
	{RememberStageFetchSeries, true},
	{RememberStageObserveSeries, true},
}

// RememberStageDefOf returns the definition of the stage called name.
func RememberStageDefOf(name string) (RememberStageDef, bool) {
	for _, s := range RememberStages {
		if s.Name == name {
			return s, true
		}
	}
	return RememberStageDef{}, false
}

// Columns of a stage in gb_remember_runs are <stage>_<suffix>.
const (
	RememberColStatus       = "status"
	RememberColError        = "error"
	RememberColStartedAt    = "started_at"
	RememberColFinishedAt   = "finished_at"
	RememberColInferenceIDs = "inference_ids"
)

// RememberStage is how one stage of a remember run went.
type RememberStage struct {
	// Status is RememberNotStarted, RememberStarted, RememberSucceeded or RememberFailed.
	Status string `json:"status"`
	// Error is why the stage failed; nil (null) if it did not.
	Error *string `json:"error"`
	// StartedAt and FinishedAt are when the stage began and when it last ended; nil until then. A stage that is run again
	// for each chunk of a text spans all of them: from the start of the first to the end of the last.
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	// InferenceIDs are the inferences (gb_inferences) the stage made, in order: one for each LLM call, embedding request or
	// web search. Always empty for a stage that runs no model. They are a list because a stage makes one for each chunk.
	InferenceIDs []int64 `json:"inference_ids"`
}

// RememberRun is one execution of the remember pipeline (gb_remember_runs): the authoritative record of how it went, stage
// by stage.
type RememberRun struct {
	ID      int64  `json:"id"`
	UserID  string `json:"user_id"`
	Dataset string `json:"dataset"` // the name
	Kind    string `json:"kind"`    // the artifact kind
	// Params is how the call was made: the reference time, the session, the models and reasoning levels per stage, the chunk
	// size and which optional stages were asked for.
	Params map[string]any `json:"params"`
	// ArtifactID is the artifact the text was stored as (the parent, for a chunked text); nil until it is stored.
	ArtifactID *int64 `json:"artifact_id"`
	// Status is that of the call as a whole: RememberStarted, then RememberSucceeded if it returned a result or
	// RememberFailed if it returned an error. It does not look at the stages that never fail the call (the web search, ...):
	// one of them can be failed in a run that succeeded.
	Status string `json:"status"`
	// Error is why the call failed; nil if it did not.
	Error      *string    `json:"error"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	// Stages has every stage of RememberStages.
	Stages map[string]RememberStage `json:"stages"`
}

// NewRememberRun returns a run that has just started, all of whose stages are not started.
func NewRememberRun(userID, dataset, kind string, params map[string]any, startedAt time.Time) RememberRun {
	r := RememberRun{UserID: userID, Dataset: dataset, Kind: kind, Params: params, Status: RememberStarted, StartedAt: startedAt,
		Stages: make(map[string]RememberStage, len(RememberStages))}
	for _, s := range RememberStages {
		r.Stages[s.Name] = RememberStage{Status: RememberNotStarted, InferenceIDs: []int64{}}
	}
	return r
}

// RememberRunFilter selects remember runs; an empty field matches all.
type RememberRunFilter struct {
	UserID  string
	Dataset string
	Status  string // of the run as a whole
}

//---------------------------------------------------------------------------------------------
// Stage outputs (gb_remember_run_stage_outputs, see sqlite/db_schema.go)

// RememberStageOutput is one output of a stage of a remember run, kept for debugging: what a step of the stage produced (the
// entities the LLM extracted, the candidate facts found, the places a resolver returned, ...). They are in a table of their
// own so that they can be cleared or garbage collected without touching the run log.
//
// A stage has any number of them: one for each step it takes, and each step is taken again for each chunk of a text (or
// each entity searched for, ...). The stages of the enrichment jobs (web_search, fetch_series, observe_series) hold the
// outputs of the steps of the texts they store too, told apart by Name.
type RememberStageOutput struct {
	ID    int64  `json:"id"`
	RunID int64  `json:"run_id"`
	Stage string `json:"stage"`
	// Name is the step that made the output (extract_graph, resolve_edges, detect_geo, ...): a stage can have several.
	Name string `json:"name"`
	// Label says what the output is about when the step ran more than once ("chunk 2 of 5", `web search for "Berlin"`); "" if
	// it did not.
	Label string `json:"label"`
	// Output is the JSON the step produced. Its shape depends on Name.
	Output    json.RawMessage `json:"output"`
	CreatedAt time.Time       `json:"created_at"`
}
