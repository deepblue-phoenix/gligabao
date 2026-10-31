package core

import "errors"

// A rerun is a case run made again, from the admin UI, with other models for the remember or the query pipeline (see
// knowledge.Rerunner). It is recorded as an EvalRun of its own, with the one case, in the experiment run that owns the case run it
// repeats (or on its own if that had none); the Params of that EvalRun say what it is.
const (
	// ParamRerunOf is the Params key, on the EvalRun of a rerun, with the ID of the CaseRun that was rerun.
	ParamRerunOf = "rerun_of"
	// ParamRememberModel and ParamQueryModel are the Params keys with the model the rerun used for every LLM stage of the remember
	// pipeline and of the query ("" or missing: the stages kept the models they had).
	ParamRememberModel = "remember_model"
	ParamQueryModel    = "query_model"
)

var (
	// ErrRerunBusy means a rerun is already running (one runs at a time).
	ErrRerunBusy = errors.New("another rerun is in progress")
	// ErrRerunRefused means the case run cannot be rerun (it has not ended, its experiment run is still running, its case no longer
	// exists, ...); the error says why.
	ErrRerunRefused = errors.New("the case run cannot be rerun")
)
