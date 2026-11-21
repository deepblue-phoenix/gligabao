package evals

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/evals/commentary"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// The debug view of a case run puts everything about one case in one place: the checks that failed and the ones that held, the case as
// it was run and as it is defined now, and the LLM commentaries on the failure (package evals/commentary, the same prompt as the
// runner's -commentary). The graph and the temporal plot it links to are the endpoints of the case run (Graph, Temporal). The rerun
// runs the case again with another model and/or reasoning (thinking) level for the remember pipeline and/or for the query, and records
// it as an eval run of its own (see knowledge.Rerunner, which does it, through the run manager). It runs in the background: the
// endpoint answers when the new case run is recorded, and the client follows it through the debug view, which says of a case run that
// is a rerun what it repeats and with which models.

// DebugResponse is GET /v1/gb/evals/case_runs/{id}/debug.
type DebugResponse struct {
	CaseRunID int64        `json:"case_run_id"`
	Eval      string       `json:"eval"`
	Name      string       `json:"name"`
	Status    core.Status  `json:"status"`
	Verdict   core.Verdict `json:"verdict,omitempty"`
	Error     string       `json:"error,omitempty"`
	// Warnings are what the case run got through but the operator should know, taken from its trajectories: e.g. a model that does
	// not take the thinking level the case asked for, so its calls ran without it (or with the provider's adaptive thinking).
	Warnings []string `json:"warnings,omitempty"`
	Answer   string   `json:"answer,omitempty"`
	// Problems are the checks (assertions and conditions) that did not hold, PassedChecks the ones that did.
	Problems     []string `json:"problems"`
	PassedChecks []string `json:"passed_checks"`
	// Input is what the case was given as it ran (the text files read in).
	Input core.KnowledgeInput `json:"input"`
	// Expected and Params are what the case def says now (they may have changed since the run); nil if the def is gone.
	Expected  *core.KnowledgeExpected `json:"expected,omitempty"`
	Params    *core.KnowledgeParams   `json:"params,omitempty"`
	DatasetID *int64                  `json:"dataset_id,omitempty"`
	// Commentable says whether the case failed or errored, which is what a commentary can be asked for.
	Commentable bool `json:"commentable"`
	// CommentaryEnabled says whether the server has an LLM to write one with.
	CommentaryEnabled bool `json:"commentary_enabled"`
	// DefaultModel is the model a commentary is written by unless the request names another.
	DefaultModel string `json:"default_model"`
	// Commentaries are the commentaries made so far, newest first.
	Commentaries []core.CaseCommentary `json:"commentaries"`
	// CostUSD is what the case run cost, StartedAt and FinishedAt when it ran (FinishedAt is absent while it runs).
	CostUSD    float64    `json:"cost_usd"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Rerun is set when the case run is itself a rerun: the case run it repeated and the models it was given.
	Rerun *RerunView `json:"rerun,omitempty"`
	// Reruns are the reruns of this case run (and of those reruns), oldest first, each with the models it was given and how it ended;
	// empty when there are none.
	Reruns []RerunListItem `json:"reruns"`
	// RerunOptions is what a rerun of this case run can pick from (the models of the price table); absent when the server cannot rerun.
	RerunOptions *RerunInfo `json:"rerun_options,omitempty"`
}

// RerunInfo is the part of the debug view about reruns: what can be picked.
type RerunInfo struct {
	// Models are the names of the models a rerun can use, sorted.
	Models []string `json:"models"`
	// Reasoning are the reasoning (thinking) levels a rerun can use, from none to the most (llm.ReasoningLevels).
	Reasoning []string `json:"reasoning"`
}

// RerunListItem is one rerun in the list of the reruns of a case run: the case run it made, the case run it repeated, the models it was
// given (empty: the stages kept the models and levels they had) and how it ended (the verdict is absent until it has one).
type RerunListItem struct {
	CaseRunID int64 `json:"case_run_id"`
	// EvalRunID is the eval run that holds the rerun (a rerun is a run of its own).
	EvalRunID         int64        `json:"eval_run_id"`
	Of                int64        `json:"of"`
	RememberModel     string       `json:"remember_model,omitempty"`
	QueryModel        string       `json:"query_model,omitempty"`
	RememberReasoning string       `json:"remember_reasoning,omitempty"`
	QueryReasoning    string       `json:"query_reasoning,omitempty"`
	Status            core.Status  `json:"status"`
	Verdict           core.Verdict `json:"verdict,omitempty"`
	CostUSD           float64      `json:"cost_usd"`
}

// commentable is whether a case run failed (a check did not hold) or errored: the cases a commentary is for.
func commentable(c *core.CaseRun) bool {
	return c.Status == core.StatusErrored || (c.Status == core.StatusCompleted && c.Verdict == core.VerdictFail)
}

// rerunsOf lists the reruns of case run id, whose eval run is er: the case runs that repeated it, and those that repeated those (they
// are all the same case), oldest first. A rerun is an eval run of its own, with the one case, of the same eval, so they are found among
// the runs of that eval. Never nil.
func (s *Service) rerunsOf(ctx context.Context,
	owner string,
	er *core.EvalRun,
	id core.CaseRunID) ([]RerunListItem, error) {
	out := []RerunListItem{}
	evalRuns, err := s.Store.ListEvalRunsOfDef(ctx,
		s.DB,
		owner,
		er.DefID,
		0)
	if err != nil {
		return nil, err
	}
	slices.Reverse(evalRuns) // oldest first
	family := map[int64]bool{int64(id): true}
	// Oldest first, and a rerun is always younger than what it repeats, so one pass sees every link of a chain.
	for i := range evalRuns {
		rv := RerunOf(evalRuns[i].Params)
		if rv == nil || !family[rv.Of] {
			continue
		}
		cases, err := s.Store.ListCaseRuns(ctx,
			s.DB,
			owner,
			evalRuns[i].ID)
		if err != nil {
			return nil, err
		}
		for _, c := range cases {
			family[int64(c.ID)] = true
			out = append(out, RerunListItem{
				CaseRunID:         int64(c.ID),
				EvalRunID:         int64(c.EvalRunID),
				Of:                rv.Of,
				RememberModel:     rv.RememberModel,
				QueryModel:        rv.QueryModel,
				RememberReasoning: rv.RememberReasoning,
				QueryReasoning:    rv.QueryReasoning,
				Status:            c.Status,
				Verdict:           c.Verdict,
				CostUSD:           c.CostUSD,
			})
		}
	}
	return out, nil
}

// rerunInfo is what the debug view tells about the reruns the server can make (nil: none).
func (s *Service) rerunInfo() *RerunInfo {
	if !s.Mgr.CanRerun() {
		return nil
	}
	info := &RerunInfo{
		Models:    []string{},
		Reasoning: slices.Clone(llm.ReasoningLevels),
	}
	for _, m := range s.Mgr.Models() {
		info.Models = append(info.Models, m.Name)
	}
	slices.Sort(info.Models)
	return info
}

// Debug serves GET /v1/gb/evals/case_runs/{id}/debug: the checks of the case run (failed and held), its input, what its def expects and
// the commentaries made on it, newest first. It also serves a case run that is still running (a rerun): the checks are then empty. An
// unknown or non-knowledge case run is 404.
func (s *Service) Debug(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := PathID(w, r)
		if !ok {
			return
		}
		owner, ok := ownerOf(w, r, own, KindCaseRun, r.PathValue("id"), "case run")
		if !ok {
			return
		}
		ctx := r.Context()
		c, err := s.knowledgeCaseRunOf(ctx,
			owner,
			id)
		if err != nil {
			fail(w,
				err,
				"case run")
			return
		}
		eval, def, er, err := s.caseDefOf(ctx,
			owner,
			c)
		if err != nil {
			fail(w,
				err,
				"case run")
			return
		}
		m := KnowledgeMeta(c)
		out := DebugResponse{
			CaseRunID:         id,
			Eval:              eval,
			Status:            c.Status,
			Verdict:           c.Verdict,
			Error:             c.Error,
			Answer:            m.Answer,
			Problems:          OrEmpty(m.Problems),
			PassedChecks:      OrEmpty(m.PassedChecks),
			Input:             m.Input,
			DatasetID:         m.DatasetID,
			Commentable:       commentable(c),
			CommentaryEnabled: s.LLM != nil,
			DefaultModel:      commentary.DefaultModel,
			CostUSD:           c.CostUSD,
			StartedAt:         TimeOr(c.StartedAt, c.CreatedAt),
			FinishedAt:        c.FinishedAt,
			Rerun:             RerunOf(er.Params),
			RerunOptions:      s.rerunInfo(),
		}
		out.Warnings = s.trajectoryWarnings(ctx,
			m.Trajectories,
			owner)
		if def != nil {
			out.Name = def.Name
			if dm, ok := def.Meta.(*core.KnowledgeCaseDefMeta); ok && dm != nil {
				out.Expected, out.Params = &dm.KnowledgeExpected, &dm.KnowledgeParams
			}
		}
		if out.Reruns, err = s.rerunsOf(ctx,
			owner,
			er,
			c.ID); err != nil {
			fail(w,
				err,
				"reruns")
			return
		}
		if out.Commentaries, err = s.Store.ListCaseCommentaries(ctx,
			s.DB,
			owner,
			c.ID); err != nil {
			fail(w,
				err,
				"commentaries")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			out)
	}
}

// commentaryRequest is the optional body of POST /v1/gb/evals/case_runs/{id}/commentary.
type commentaryRequest struct {
	// Model is the model that writes the commentary (default commentary.DefaultModel).
	Model string `json:"model,omitempty"`
}

// Commentary serves POST /v1/gb/evals/case_runs/{id}/commentary: it sends the failed (or errored) case run, with the ATIF trajectories
// of its calls, to the LLM, as the runner's -commentary does, stores the commentary with the case run and returns it (a
// core.CaseCommentary). It waits for the LLM. Asking again adds another commentary; its cost counts in the cost cap of the day. The body
// is optional ({"model": ".."}). Answers: 404 for an unknown case run, 400 for a case that did not fail or error, 429 once the cost cap
// of the day is reached, 503 without an LLM, 502 if the LLM call failed. A trajectory whose file cannot be read is left out of the prompt.
func (s *Service) Commentary(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := PathID(w, r)
		if !ok {
			return
		}
		var req commentaryRequest
		if !readJSON(w, r, &req) {
			return
		}
		if s.LLM == nil {
			httputil.Error(w,
				http.StatusServiceUnavailable,
				"no LLM is configured (the Anthropic API key is missing)")
			return
		}
		owner, ok := ownerOf(w, r, own, KindCaseRun, r.PathValue("id"), "case run")
		if !ok {
			return
		}
		ctx := r.Context()
		c, err := s.knowledgeCaseRunOf(ctx,
			owner,
			id)
		if err != nil {
			fail(w,
				err,
				"case run")
			return
		}
		if !commentable(c) {
			httputil.Error(w,
				http.StatusBadRequest,
				"only a case run that failed or errored can be commented on")
			return
		}
		if err := s.Mgr.CheckBudget(ctx, owner); err != nil {
			fail(w,
				err,
				"commentary")
			return
		}
		eval, def, _, err := s.caseDefOf(ctx,
			owner,
			c)
		if err != nil {
			fail(w,
				err,
				"case run")
			return
		}
		m := KnowledgeMeta(c)
		f := commentary.Failure{
			Input:        m.Input,
			Problems:     m.Problems,
			Passed:       m.PassedChecks,
			Answer:       m.Answer,
			Trajectories: s.loadTrajectories(ctx, m.Trajectories, owner),
		}
		if c.Error != "" {
			f.Err = errors.New(c.Error)
		}
		f.Case = eval
		if def != nil {
			f.Case = eval + "/" + def.Name
			if dm, ok := def.Meta.(*core.KnowledgeCaseDefMeta); ok && dm != nil {
				f.Def = *dm
			}
		}
		model := strings.TrimSpace(req.Model)
		if model == "" {
			model = commentary.DefaultModel
		}
		text, resp, err := commentary.Run(ctx,
			s.LLM,
			model,
			0,
			f)
		if err != nil {
			log.WithError(err).WithField("case_run_id", id).Error("evals: commentary failed")
			httputil.Error(w,
				http.StatusBadGateway,
				"the commentary could not be written: "+err.Error())
			return
		}
		out := core.CaseCommentary{
			CaseRunID:    c.ID,
			Model:        resp.Model,
			Text:         strings.TrimSpace(text),
			InputTokens:  resp.InputTokens,
			OutputTokens: resp.OutputTokens,
			CostUSD:      resp.CostUSD,
		}
		if out.Model == "" {
			out.Model = model
		}
		// The commentary was paid for: store it even if the browser has gone away.
		if err := s.Store.CreateCaseCommentary(context.WithoutCancel(ctx),
			s.DB,
			owner,
			&out); err != nil {
			fail(w,
				err,
				"commentary")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			out)
	}
}

// trajectoryWarnings are the warnings (the "warnings" of the extra) of the trajectories of a case run, each once, in call order. A
// trajectory whose file cannot be read has none.
func (s *Service) trajectoryWarnings(ctx context.Context,
	refs []core.KnowledgeTrajectory,
	owner string) []string {
	var out []string
	for _, t := range s.loadTrajectories(ctx, refs, owner) {
		ws, _ := t.Extra["warnings"].([]any)
		for _, w := range ws {
			if msg, ok := w.(string); ok && !slices.Contains(out, msg) {
				out = append(out, msg)
			}
		}
	}
	return out
}

// loadTrajectories reads the ATIF files of the trajectory references (see ReadTrajectoryFile); the ones that cannot be read or decoded
// are left out.
func (s *Service) loadTrajectories(ctx context.Context,
	refs []core.KnowledgeTrajectory,
	owner string) []*atif.Trajectory {
	var out []*atif.Trajectory
	for i := range refs {
		data, err := s.ReadTrajectory(ctx,
			&refs[i],
			owner)
		if err != nil {
			log.WithField("trajectory_id", refs[i].TrajectoryID).Warn("evals: trajectory file not found")
			continue
		}
		var t atif.Trajectory
		if err := json.Unmarshal(data, &t); err != nil {
			log.WithError(err).WithField("trajectory_id", refs[i].TrajectoryID).Warn("evals: trajectory file is corrupt")
			continue
		}
		out = append(out, &t)
	}
	return out
}

// rerunRequest is the body of POST /v1/gb/evals/case_runs/{id}/rerun.
type rerunRequest struct {
	// RememberModel is the model of every LLM stage of the remember pipeline, QueryModel the one of the query (its answering call and
	// the detection of places). Each must be a model of the price table; empty keeps the models the case run had.
	RememberModel string `json:"remember_model,omitempty"`
	QueryModel    string `json:"query_model,omitempty"`
	// RememberReasoning and QueryReasoning are the same for the reasoning (thinking) level, "none", "low", "medium" or "high" (see
	// llm.ReasoningLevels); empty keeps the levels the case run had. "none" is a choice of its own: no thinking.
	RememberReasoning string `json:"remember_reasoning,omitempty"`
	QueryReasoning    string `json:"query_reasoning,omitempty"`
}

// RerunStarted is the answer of POST /v1/gb/evals/case_runs/{id}/rerun.
type RerunStarted struct {
	// CaseRunID is the new case run (running; read it with the debug view), EvalRunID the eval run that holds it and ExperimentRunID
	// the experiment run that eval run is in (absent if the case run it repeats was not in one).
	CaseRunID       int64  `json:"case_run_id"`
	EvalRunID       int64  `json:"eval_run_id"`
	ExperimentRunID *int64 `json:"experiment_run_id,omitempty"`
}

// Rerun serves POST /v1/gb/evals/case_runs/{id}/rerun: it runs the case of a case run that has ended again, with the models of the body
// ({"remember_model": "..", "query_model": "..", "remember_reasoning": "..", "query_reasoning": ".."}, each model one of the models of
// the price table and each level one of llm.ReasoningLevels, any of them may be left out), as a new eval run of the same eval. It
// answers 202 with the new case run (RerunStarted) once it is recorded; the rerun goes on in the background and takes as long as the
// case does. Answers: 404 for an unknown or non-knowledge case run, 400 for a model that is not in the price table or an unknown level,
// 409 for a case run that cannot be rerun (it has not ended, its case is gone) or when another rerun of the user is in progress, 429
// over the limits, 503 when the server cannot rerun (no LLM).
func (s *Service) Rerun(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := PathID(w, r)
		if !ok {
			return
		}
		var req rerunRequest
		if !readJSON(w, r, &req) {
			return
		}
		if !s.Mgr.CanRerun() {
			httputil.Error(w,
				http.StatusServiceUnavailable,
				"reruns are not available (no LLM is configured: the Anthropic API key is missing)")
			return
		}
		req.RememberModel, req.QueryModel = strings.TrimSpace(req.RememberModel), strings.TrimSpace(req.QueryModel)
		known := s.rerunInfo().Models
		for _, m := range []struct{ what, model string }{{"remember_model", req.RememberModel}, {"query_model", req.QueryModel}} {
			if m.model != "" && !slices.Contains(known, m.model) {
				httputil.Error(w,
					http.StatusBadRequest,
					m.what+" "+m.model+" is not a model of the price table")
				return
			}
		}
		req.RememberReasoning, req.QueryReasoning = strings.TrimSpace(req.RememberReasoning), strings.TrimSpace(req.QueryReasoning)
		for _, l := range []struct{ what, level string }{{"remember_reasoning", req.RememberReasoning}, {"query_reasoning", req.QueryReasoning}} {
			if _, valid := llm.NormalizeReasoning(l.level); !valid {
				httputil.Error(w,
					http.StatusBadRequest,
					l.what+" "+l.level+" is not a reasoning level (levels: "+strings.Join(llm.ReasoningLevels, ", ")+")")
				return
			}
		}
		owner, ok := ownerOf(w, r, own, KindCaseRun, r.PathValue("id"), "case run")
		if !ok {
			return
		}
		ctx := r.Context()
		if _, err := s.knowledgeCaseRunOf(ctx,
			owner,
			id); err != nil {
			fail(w,
				err,
				"case run")
			return
		}
		newID, err := s.Mgr.Rerun(ctx,
			owner,
			core.CaseRunID(id),
			core.RerunOptions{
				RememberModel:     req.RememberModel,
				QueryModel:        req.QueryModel,
				RememberReasoning: req.RememberReasoning,
				QueryReasoning:    req.QueryReasoning,
			})
		switch {
		case errors.Is(err, core.ErrRerunRefused), errors.Is(err, core.ErrRerunBusy):
			httputil.Error(w,
				http.StatusConflict,
				err.Error())
			return
		case errors.Is(err, memory_core.ErrInvalidModels), errors.Is(err, memory_core.ErrInvalidReasoning):
			httputil.Error(w,
				http.StatusBadRequest,
				err.Error())
			return
		case err != nil:
			fail(w,
				err,
				"rerun")
			return
		}
		out := RerunStarted{
			CaseRunID: int64(newID),
		}
		// The rerun is recorded by a connection of its own: read where it was put from this one (it is committed before Rerun returns).
		c, err := s.Store.GetCaseRun(ctx,
			s.DB,
			owner,
			newID)
		if err == nil {
			out.EvalRunID = int64(c.EvalRunID)
			var er *core.EvalRun
			if er, err = s.Store.GetEvalRun(ctx,
				s.DB,
				owner,
				c.EvalRunID); err == nil && er.ExperimentRunID != nil {
				x := int64(*er.ExperimentRunID)
				out.ExperimentRunID = &x
			}
		}
		if err != nil && !errors.Is(err, evalsdb.ErrNotFound) {
			fail(w,
				err,
				"rerun")
			return
		}
		httputil.WriteJSON(w,
			http.StatusAccepted,
			out)
	}
}
