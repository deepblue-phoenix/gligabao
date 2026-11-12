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
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/evals/commentary"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// The debug view of a case run (the commentary modal of the evals view, admin_ui/src/evals.ts) puts everything about one case in one
// place: the checks that failed and the ones that held, the case as it was run and as it is defined now, and the LLM commentaries on
// the failure (package evals/commentary, the same prompt as the runner's -commentary). The graph and the temporal plot it links to
// are the endpoints of the case run (EvalCaseRunGraph, EvalCaseRunTemporal).

// evalCaseDebugResponse is GET /admin/api/evals/case_runs/{id}/debug.
type evalCaseDebugResponse struct {
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
	Rerun *rerunView `json:"rerun,omitempty"`
	// Reruns are the reruns of this case run (and of those reruns), oldest first, each with the models it was given and how it ended;
	// empty when there are none.
	Reruns []rerunListItem `json:"reruns"`
	// RerunOptions is what a rerun of this case run can pick from (the models of the price table); absent when the server cannot rerun.
	RerunOptions *evalRerunInfo `json:"rerun_options,omitempty"`
}

// commentable is whether a case run failed (a check did not hold) or errored: the cases a commentary is for.
func commentable(c *core.CaseRun) bool {
	return c.Status == core.StatusErrored || (c.Status == core.StatusCompleted && c.Verdict == core.VerdictFail)
}

// knowledgeCaseRunOf reads the case run id, which must be a knowledge one (anything else is not found).
func knowledgeCaseRunOf(ctx context.Context,
	d *sql.DB,
	s evalsdb.Store,
	id int64) (*core.CaseRun, error) {
	c, err := s.GetCaseRun(ctx,
		d,
		core.CaseRunID(id))
	if err == nil && c.Type != core.TypeKnowledge {
		err = evalsdb.ErrNotFound
	}
	return c, err
}

// caseDefOf returns the name of the eval of case run c, its case def (with its knowledge Meta; nil if it is gone) and the eval run
// that holds the case run.
func caseDefOf(ctx context.Context,
	d *sql.DB,
	s evalsdb.Store,
	c *core.CaseRun) (eval string, def *core.CaseDef, er *core.EvalRun, err error) {
	er, err = s.GetEvalRun(ctx,
		d,
		c.EvalRunID)
	if err != nil {
		return "", nil, nil, err
	}
	ed, err := s.GetEvalDef(ctx,
		d,
		er.DefID)
	if err != nil {
		return "", nil, nil, err
	}
	full, err := s.GetEvalDefByName(ctx,
		d,
		ed.Name)
	if errors.Is(err, evalsdb.ErrNotFound) {
		return ed.Name, nil, er, nil
	}
	if err != nil {
		return "", nil, nil, err
	}
	for i := range full.Cases {
		if full.Cases[i].ID == c.DefID {
			return ed.Name, &full.Cases[i], er, nil
		}
	}
	return ed.Name, nil, er, nil
}

// EvalCaseRunDebug serves GET /admin/api/evals/case_runs/{id}/debug: the checks of the case run (failed and held), its input, what
// its def expects and the commentaries made on it, newest first. llm says whether a commentary can be asked for (nil: it cannot), rr
// whether (and with which models) the case can be rerun (nil: it cannot). dir is the directory of the ATIF files (see EvalTrajectory), where
// the warnings of the calls are read from. It also serves a case run that is still running (a rerun):
// the checks are then empty. An unknown or non-knowledge case run is 404.
func EvalCaseRunDebug(d *sql.DB,
	s evalsdb.Store,
	dir string,
	llm memory_core.LLM,
	rr EvalRerunner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		c, err := knowledgeCaseRunOf(ctx,
			d,
			s,
			id)
		if err != nil {
			evalFail(w,
				err,
				"case run")
			return
		}
		eval, def, er, err := caseDefOf(ctx,
			d,
			s,
			c)
		if err != nil {
			evalFail(w,
				err,
				"case run")
			return
		}
		m := knowledgeMeta(c)
		out := evalCaseDebugResponse{
			CaseRunID:         id,
			Eval:              eval,
			Status:            c.Status,
			Verdict:           c.Verdict,
			Error:             c.Error,
			Answer:            m.Answer,
			Problems:          orEmpty(m.Problems),
			PassedChecks:      orEmpty(m.PassedChecks),
			Input:             m.Input,
			DatasetID:         m.DatasetID,
			Commentable:       commentable(c),
			CommentaryEnabled: llm != nil,
			DefaultModel:      commentary.DefaultModel,
			CostUSD:           c.CostUSD,
			StartedAt:         timeOr(c.StartedAt, c.CreatedAt),
			FinishedAt:        c.FinishedAt,
			Rerun:             rerunOf(er.Params),
			RerunOptions:      rerunInfo(rr),
		}
		out.Warnings = trajectoryWarnings(m.Trajectories, dir)
		if def != nil {
			out.Name = def.Name
			if dm, ok := def.Meta.(*core.KnowledgeCaseDefMeta); ok && dm != nil {
				out.Expected, out.Params = &dm.KnowledgeExpected, &dm.KnowledgeParams
			}
		}
		if out.Reruns, err = rerunsOf(ctx,
			d,
			s,
			er,
			c.ID); err != nil {
			evalFail(w,
				err,
				"reruns")
			return
		}
		if out.Commentaries, err = s.ListCaseCommentaries(ctx,
			d,
			c.ID); err != nil {
			evalFail(w,
				err,
				"commentaries")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			out)
	}
}

// commentaryRequest is the optional body of POST /admin/api/evals/case_runs/{id}/commentary.
type commentaryRequest struct {
	// Model is the model that writes the commentary (default commentary.DefaultModel).
	Model string `json:"model,omitempty"`
}

// EvalCaseRunCommentary serves POST /admin/api/evals/case_runs/{id}/commentary: it sends the failed (or errored) case run, with the
// ATIF trajectories of its calls, to the LLM, as the runner's -commentary does, stores the commentary with the case run and returns
// it (a core.CaseCommentary). It waits for the LLM. Asking again adds another commentary. The body is optional ({"model": ".."}).
// Answers: 404 for an unknown case run, 400 for a case that did not fail or error, 503 without an LLM, 502 if the LLM call failed.
// dir is the directory of the ATIF files (see EvalTrajectory); a trajectory whose file cannot be read is left out of the prompt.
func EvalCaseRunCommentary(d *sql.DB,
	s evalsdb.Store,
	dir string,
	llm memory_core.LLM) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var req commentaryRequest
		if body, err := io.ReadAll(http.MaxBytesReader(w,
			r.Body,
			1<<16)); err != nil {
			httputil.Error(w,
				http.StatusBadRequest,
				"request body too large or unreadable")
			return
		} else if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				httputil.Error(w,
					http.StatusBadRequest,
					"request body is not valid JSON")
				return
			}
		}
		if llm == nil {
			httputil.Error(w,
				http.StatusServiceUnavailable,
				"no LLM is configured (the Anthropic API key is missing)")
			return
		}
		ctx := r.Context()
		c, err := knowledgeCaseRunOf(ctx,
			d,
			s,
			id)
		if err != nil {
			evalFail(w,
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
		eval, def, _, err := caseDefOf(ctx,
			d,
			s,
			c)
		if err != nil {
			evalFail(w,
				err,
				"case run")
			return
		}
		m := knowledgeMeta(c)
		f := commentary.Failure{
			Input:        m.Input,
			Problems:     m.Problems,
			Passed:       m.PassedChecks,
			Answer:       m.Answer,
			Trajectories: loadTrajectories(m.Trajectories, dir),
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
			llm,
			model,
			0,
			f)
		if err != nil {
			log.WithError(err).WithField("case_run_id", id).Error("admin: eval commentary failed")
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
		if err := s.CreateCaseCommentary(context.WithoutCancel(ctx),
			d,
			&out); err != nil {
			evalFail(w,
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
func trajectoryWarnings(refs []core.KnowledgeTrajectory,
	dir string) []string {
	var out []string
	for _, t := range loadTrajectories(refs, dir) {
		ws, _ := t.Extra["warnings"].([]any)
		for _, w := range ws {
			if msg, ok := w.(string); ok && !slices.Contains(out, msg) {
				out = append(out, msg)
			}
		}
	}
	return out
}

// loadTrajectories reads the ATIF files of the trajectory references (see readTrajectoryFile); the ones that cannot be read or
// decoded are left out.
func loadTrajectories(refs []core.KnowledgeTrajectory,
	dir string) []*atif.Trajectory {
	var out []*atif.Trajectory
	for i := range refs {
		data, err := readTrajectoryFile(&refs[i], dir)
		if err != nil {
			log.WithField("trajectory_id", refs[i].TrajectoryID).Warn("admin: eval commentary: trajectory file not found")
			continue
		}
		var t atif.Trajectory
		if err := json.Unmarshal(data, &t); err != nil {
			log.WithError(err).WithField("trajectory_id", refs[i].TrajectoryID).Warn("admin: eval commentary: trajectory file is corrupt")
			continue
		}
		out = append(out, &t)
	}
	return out
}
