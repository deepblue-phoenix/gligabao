package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// The rerun of a case (the button of the case modal of the evals view, admin_ui/src/evals.ts): the case is run again with another model
// for the remember pipeline and/or for the query, and recorded as a run in the experiment run that owns the case run (see
// evals/knowledge.Rerunner, which does it). It runs in the background: the endpoint answers when the new case run is recorded, and the
// UI follows it through GET case_runs/{id}/debug, which says of a case run that is a rerun what it repeats and with which models.

// EvalRerunner reruns case runs of the eval database. It is the seam of an injected dependency: the real implementation is
// evals/knowledge.Rerunner, but this package cannot import it. Package knowledge runs each eval case behind an in-process server
// (it imports internal/sys/server), and the server imports this package to serve the admin UI, so an import of knowledge from here
// (or from the server) would be a cycle (admin -> knowledge -> server -> admin). The interface is therefore defined here, on the
// consumer's side, in terms of types that both sides may import (core, ai, memory_core); the handlers only ever see this interface, and
// the binary hands the server a factory of the implementation (server.Options.EvalRerunner, set in the root package). It also keeps the
// handlers testable with a fake (see the tests), without an LLM or a database to write to. A nil EvalRerunner means "cannot rerun".
type EvalRerunner interface {
	// Models are the LLM models a rerun can use: the ones of the price table.
	Models() []ai.ModelInfo
	// Rerun starts the rerun of a case run with the given model for every LLM stage of the remember pipeline and of the query ("" keeps
	// the models as they were). It returns the new case run, already recorded (running), while the rerun goes on in the background. Its
	// errors: evalsdb.ErrNotFound (unknown case run), core.ErrRerunRefused (it cannot be rerun), core.ErrRerunBusy (one is running),
	// memory_core.ErrInvalidModels (a model name is malformed).
	Rerun(ctx context.Context, caseRun core.CaseRunID, rememberModel, queryModel string) (core.CaseRunID, error)
}

// evalRerunInfo is the part of the debug view about reruns: what can be picked.
type evalRerunInfo struct {
	// Models are the names of the models a rerun can use, sorted.
	Models []string `json:"models"`
}

// rerunView says of a case run that it is a rerun: the case run it repeated and the models it was given (empty: the stages kept
// the models they had).
type rerunView struct {
	Of            int64  `json:"of"`
	RememberModel string `json:"remember_model,omitempty"`
	QueryModel    string `json:"query_model,omitempty"`
}

// rerunOf reads, from the params of the eval run of a case run, whether it is a rerun; nil if it is not.
func rerunOf(p core.Params) *rerunView {
	var of int64
	switch v := p[core.ParamRerunOf].(type) {
	case int64:
		of = v
	case int:
		of = int64(v)
	case float64:
		of = int64(v)
	}
	if of == 0 {
		return nil
	}
	rememberModel, _ := p[core.ParamRememberModel].(string)
	queryModel, _ := p[core.ParamQueryModel].(string)
	return &rerunView{Of: of, RememberModel: rememberModel, QueryModel: queryModel}
}

// rerunInfo is what the debug view tells about the reruns the server can make (nil: none).
func rerunInfo(rr EvalRerunner) *evalRerunInfo {
	if rr == nil {
		return nil
	}
	info := &evalRerunInfo{Models: []string{}}
	for _, m := range rr.Models() {
		info.Models = append(info.Models, m.Name)
	}
	slices.Sort(info.Models)
	return info
}

// rerunRequest is the body of POST /admin/api/evals/case_runs/{id}/rerun.
type rerunRequest struct {
	// RememberModel is the model of every LLM stage of the remember pipeline, QueryModel the one of the query (its answering call and
	// the detection of places). Each must be a model of the price table; empty keeps the models the case run had.
	RememberModel string `json:"remember_model,omitempty"`
	QueryModel    string `json:"query_model,omitempty"`
}

// rerunStarted is the answer of POST /admin/api/evals/case_runs/{id}/rerun.
type rerunStarted struct {
	// CaseRunID is the new case run (running; read it with GET case_runs/{id}/debug), EvalRunID the eval run that holds it and
	// ExperimentRunID the experiment run that eval run is in (absent if the case run it repeats was not in one).
	CaseRunID       int64  `json:"case_run_id"`
	EvalRunID       int64  `json:"eval_run_id"`
	ExperimentRunID *int64 `json:"experiment_run_id,omitempty"`
}

// EvalCaseRunRerun serves POST /admin/api/evals/case_runs/{id}/rerun: it runs the case of a case run that has ended again, with the
// models of the body ({"remember_model": "..", "query_model": ".."}, each one of the models of the price table, either may be left out),
// as a new eval run in the experiment run of that case run, so the rerun is listed with the experiment's. It answers 202 with the new
// case run (rerunStarted) once it is recorded; the rerun goes on in the background and takes as long as the case does. Answers: 404 for
// an unknown or non-knowledge case run, 400 for a model that is not in the price table, 409 for a case run that cannot be rerun (it has
// not ended, its experiment run is still running, its case is gone) or when another rerun is in progress, 503 when the server cannot
// rerun (no LLM, or no eval database to write to).
func EvalCaseRunRerun(d *sql.DB, s evalsdb.Store, rr EvalRerunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var req rerunRequest
		if body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16)); err != nil {
			httputil.Error(w, http.StatusBadRequest, "request body too large or unreadable")
			return
		} else if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				httputil.Error(w, http.StatusBadRequest, "request body is not valid JSON")
				return
			}
		}
		if rr == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "reruns are not available (no LLM is configured: the Anthropic API key is missing)")
			return
		}
		req.RememberModel, req.QueryModel = strings.TrimSpace(req.RememberModel), strings.TrimSpace(req.QueryModel)
		known := rerunInfo(rr).Models
		for _, m := range []struct{ what, model string }{{"remember_model", req.RememberModel}, {"query_model", req.QueryModel}} {
			if m.model != "" && !slices.Contains(known, m.model) {
				httputil.Error(w, http.StatusBadRequest, m.what+" "+m.model+" is not a model of the price table")
				return
			}
		}
		ctx := r.Context()
		if _, err := knowledgeCaseRunOf(ctx, d, s, id); err != nil {
			evalFail(w, err, "case run")
			return
		}
		newID, err := rr.Rerun(ctx, core.CaseRunID(id), req.RememberModel, req.QueryModel)
		switch {
		case errors.Is(err, evalsdb.ErrNotFound):
			httputil.Error(w, http.StatusNotFound, "case run not found")
			return
		case errors.Is(err, core.ErrRerunRefused), errors.Is(err, core.ErrRerunBusy):
			httputil.Error(w, http.StatusConflict, err.Error())
			return
		case errors.Is(err, memory_core.ErrInvalidModels):
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		case err != nil:
			log.WithError(err).WithField("case_run_id", id).Error("admin: eval rerun failed")
			httputil.Error(w, http.StatusInternalServerError, "the rerun could not be started: "+err.Error())
			return
		}
		out := rerunStarted{CaseRunID: int64(newID)}
		// The rerun is recorded by a connection of its own: read where it was put from this one (it is committed before Rerun returns).
		c, err := s.GetCaseRun(ctx, d, newID)
		if err == nil {
			out.EvalRunID = int64(c.EvalRunID)
			var er *core.EvalRun
			if er, err = s.GetEvalRun(ctx, d, c.EvalRunID); err == nil && er.ExperimentRunID != nil {
				x := int64(*er.ExperimentRunID)
				out.ExperimentRunID = &x
			}
		}
		if err != nil {
			evalFail(w, err, "rerun")
			return
		}
		httputil.WriteJSON(w, http.StatusAccepted, out)
	}
}
