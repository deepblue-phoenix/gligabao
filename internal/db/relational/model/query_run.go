package model

import "time"

//---------------------------------------------------------------------------------------------
// Query runs (gb_query_runs, see sqlite/db_schema.go)

// The stages of the query pipeline, in pipeline order (QueryStages). Each is a column group of gb_query_runs. The statuses of a
// query run and of its stages are those of the remember runs (RememberStarted, RememberSkipped, ...).
const (
	// QueryStageRetrieveGraph is the retrieval of the subgraph around the entities that match the question, and of the
	// passages close to it (it embeds the question, with vectors).
	QueryStageRetrieveGraph = "retrieve_graph"
	// QueryStageDetectGeo is the detection of the places the question mentions (only with a geo resolver).
	QueryStageDetectGeo = "detect_geo"
	// QueryStageNearbyGeo is the search of the geo entities near those places and near the entities that matched the
	// question (only with a geo resolver and the proximity search on).
	QueryStageNearbyGeo = "nearby_geo"
	// QueryStageQuery is the LLM call that answers the question from what was retrieved. Its name is that of the stage of its
	// inference (gb_inferences.stage).
	QueryStageQuery = "query"
)

// QueryStageDef describes a stage of the query pipeline.
type QueryStageDef struct {
	Name string
	// Model says that the stage runs models (LLM calls, embedding requests): it records their inferences.
	Model bool
}

// QueryStages lists the stages of the query pipeline, in order.
var QueryStages = []QueryStageDef{
	{QueryStageRetrieveGraph, true},
	{QueryStageDetectGeo, true},
	{QueryStageNearbyGeo, false},
	{QueryStageQuery, true},
}

// QueryStage is how one stage of a query run went; it has the same fields as a stage of a remember run.
type QueryStage = RememberStage

// QueryRun is one execution of the query pipeline (gb_query_runs): the authoritative record of how it went, stage by stage.
type QueryRun struct {
	ID      int64  `json:"id"`
	UserID  string `json:"user_id"`
	Dataset string `json:"dataset"` // the name
	// Question is the question that was asked.
	Question string `json:"question"`
	// Params is how the call was made: the session, the models and reasoning levels per stage, and the options of the geo
	// search.
	Params map[string]any `json:"params"`
	// SessionID is the session of the call (WithSessionID); "" if it had none.
	SessionID string `json:"session_id"`
	// Status is that of the call as a whole: RememberStarted, then RememberSucceeded if it returned an answer or RememberFailed
	// if it returned an error. It does not look at the stages that never fail the call (detect_geo, nearby_geo): one of them
	// can be failed in a run that succeeded.
	Status string `json:"status"`
	// Error is why the call failed; nil if it did not.
	Error      *string    `json:"error"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	// Stages has every stage of QueryStages.
	Stages map[string]QueryStage `json:"stages"`
}

// NewQueryRun returns a run that has just started, all of whose stages are not started.
func NewQueryRun(userID, dataset, question, sessionID string, params map[string]any, startedAt time.Time) QueryRun {
	r := QueryRun{UserID: userID, Dataset: dataset, Question: question, SessionID: sessionID, Params: params, Status: RememberStarted,
		StartedAt: startedAt, Stages: make(map[string]QueryStage, len(QueryStages))}
	for _, s := range QueryStages {
		r.Stages[s.Name] = QueryStage{Status: RememberNotStarted, InferenceIDs: []int64{}}
	}
	return r
}

// QueryRunFilter selects query runs; an empty field matches all.
type QueryRunFilter struct {
	UserID  string
	Dataset string
	Status  string // of the run as a whole
}
