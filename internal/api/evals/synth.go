package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/synth"
	"github.com/deepblue-phoenix/gligabao/internal/evals/usereval"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

// maxSynthBody is the size of the body of a request that defines a synthetic eval (two prompts of 8 KiB at most, and the rest).
const maxSynthBody = 64 << 10

// synthInfo is the summary of the definition sd with the state of its newest generation.
func (s *Service) synthInfo(ctx context.Context,
	sd core.SynthDef) (*SynthInfo, error) {
	gens, err := s.Store.ListSynthGenerations(ctx,
		s.DB,
		sd.UserID,
		sd.EvalDefID,
		1)
	if err != nil {
		return nil, err
	}
	info := &SynthInfo{
		Version:     sd.Version,
		Model:       sd.Model,
		NumExamples: sd.NumExamples,
		Mode:        sd.Mode,
	}
	if len(gens) > 0 {
		info.Generation, info.GenerationError = gens[0].Status, gens[0].Error
	}
	return info, nil
}

// synthReady answers 503 and returns false if the server has no synthetic evals (no manager, or no LLM for what needs one).
func (s *Service) synthReady(w http.ResponseWriter,
	needLLM bool) bool {
	switch {
	case s.Synth == nil:
		httputil.Error(w,
			http.StatusServiceUnavailable,
			"synthetic evals are not available")
		return false
	case needLLM && !s.Synth.CanGenerate():
		fail(w,
			core.ErrNoLLM,
			"synthetic eval")
		return false
	}
	return true
}

// decodeBody reads the JSON body (at most limit bytes) into v, strictly (an unknown key is a 400).
func decodeBody(w http.ResponseWriter,
	r *http.Request,
	limit int,
	v any) bool {
	data, err := readBody(w,
		r,
		limit)
	if err != nil {
		fail(w,
			err,
			"request")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httputil.WriteJSON(w,
			http.StatusBadRequest,
			map[string]any{
				"error":    "invalid request",
				"problems": []string{err.Error()},
			})
		return false
	}
	return true
}

// synthListResponse is GET /v1/gb/evals/synth.
type synthListResponse struct {
	Evals []EvalView `json:"evals"`
	// Models are the models a definition may name; CanGenerate says whether the server has an LLM.
	Models      []string `json:"models"`
	CanGenerate bool     `json:"can_generate"`
	// MaxExamples is the most cases a synthetic eval may have.
	MaxExamples int `json:"max_examples"`
	// Defaults are the settings a new definition starts from.
	Defaults core.SynthSettings `json:"defaults"`
}

// SynthList serves GET /v1/gb/evals/synth: the synthetic evals of the owner (as in the list of the evals, with their origin and their
// definition), and what a form that creates one needs: the models, the limits and the defaults.
func (s *Service) SynthList(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		if !s.synthReady(w, false) {
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "evals")
		if !ok {
			return
		}
		defs, err := s.Store.ListEvalDefs(r.Context(),
			s.DB,
			owner)
		if err != nil {
			fail(w,
				err,
				"evals")
			return
		}
		views, _, err := s.EvalViews(r.Context(), defs)
		if err != nil {
			fail(w,
				err,
				"evals")
			return
		}
		out := []EvalView{}
		for _, v := range views {
			if v.Origin == core.OriginSynthetic {
				out = append(out, v)
			}
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			synthListResponse{
				Evals:       out,
				Models:      s.Synth.Models(),
				CanGenerate: s.Synth.CanGenerate(),
				MaxExamples: s.Mgr.Env().Limits.MaxCases,
				Defaults:    synth.Defaults(),
			})
	}
}

// synthCreateBody is the body of POST /v1/gb/evals/synth.
type synthCreateBody struct {
	Name string `json:"name"`
	synth.Patch
}

// generationResponse is the answer of the requests that start a generation.
type generationResponse struct {
	Name       string                `json:"name"`
	Version    int                   `json:"version"`
	Generation *core.SynthGeneration `json:"generation,omitempty"`
}

// SynthCreate serves POST /v1/gb/evals/synth: it creates the synthetic eval `name` from the definition of the body (`description`,
// `prompt`, `corpus_prompt`, `model`, `num_examples`, `mode`, `bulk_size`) and starts the generation of its cases in the
// background: 202 with the generation, which the detail of the eval follows. 400 for a definition that is not valid, 409 if the name is
// taken, 429 over the cost cap of the day, 503 without an LLM.
func (s *Service) SynthCreate(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		if !s.synthReady(w, true) {
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "synthetic eval")
		if !ok {
			return
		}
		var body synthCreateBody
		if !decodeBody(w,
			r,
			maxSynthBody,
			&body) {
			return
		}
		if err := s.Mgr.CheckBudget(r.Context(), owner); err != nil {
			fail(w,
				err,
				"synthetic eval")
			return
		}
		gen, err := s.Synth.Create(r.Context(),
			owner,
			body.Name,
			body.Patch)
		if err != nil {
			fail(w,
				err,
				"synthetic eval")
			return
		}
		httputil.WriteJSON(w,
			http.StatusAccepted,
			generationResponse{
				Name:       body.Name,
				Version:    gen.Version,
				Generation: gen,
			})
	}
}

// SynthGet serves GET /v1/gb/evals/synth/{name}: the definition, its versions, the latest generations and the cases with where each
// comes from (generated, edited or user). 404 if there is no such synthetic eval.
func (s *Service) SynthGet(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		if !s.synthReady(w, false) {
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "synthetic eval")
		if !ok {
			return
		}
		d, err := s.Synth.Get(r.Context(),
			owner,
			r.PathValue("name"))
		if err != nil {
			fail(w,
				err,
				"synthetic eval")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			d)
	}
}

// synthUpdateBody is the body of PUT /v1/gb/evals/synth/{name}: the fields of the definition to change, and whether to generate the
// cases again from the new version (`keep_edited`, true if not given: see SynthGenerate).
type synthUpdateBody struct {
	synth.Patch
	Regenerate bool  `json:"regenerate"`
	KeepEdited *bool `json:"keep_edited"`
}

// SynthUpdate serves PUT /v1/gb/evals/synth/{name}: it changes the definition; a change makes the next version (`version` + 1). With
// `regenerate` it also starts a generation from it (202), else the answer is 200 and the cases stay as they are.
func (s *Service) SynthUpdate(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		if !s.synthReady(w, false) {
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "synthetic eval")
		if !ok {
			return
		}
		var body synthUpdateBody
		if !decodeBody(w,
			r,
			maxSynthBody,
			&body) {
			return
		}
		if body.Regenerate {
			if !s.synthReady(w, true) {
				return
			}
			if err := s.Mgr.CheckBudget(r.Context(), owner); err != nil {
				fail(w,
					err,
					"synthetic eval")
				return
			}
		}
		def, gen, err := s.Synth.Update(r.Context(),
			owner,
			r.PathValue("name"),
			body.Patch,
			body.Regenerate,
			body.KeepEdited == nil || *body.KeepEdited)
		if err != nil {
			fail(w,
				err,
				"synthetic eval")
			return
		}
		status := http.StatusOK
		if gen != nil {
			status = http.StatusAccepted
		}
		httputil.WriteJSON(w,
			status,
			generationResponse{
				Name:       r.PathValue("name"),
				Version:    def.Version,
				Generation: gen,
			})
	}
}

// synthGenerateBody is the body of POST /v1/gb/evals/synth/generate/{name}.
type synthGenerateBody struct {
	KeepEdited *bool `json:"keep_edited"`
}

// SynthGenerate serves POST /v1/gb/evals/synth/generate/{name}: it generates the cases again from the current definition, in the
// background (202). With `keep_edited` (the default) the cases the user edited or added stay and only the others are generated again;
// with `"keep_edited": false` every case is replaced. 404, 409 if a generation of the eval is going, 429, 503.
func (s *Service) SynthGenerate(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		if !s.synthReady(w, true) {
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "synthetic eval")
		if !ok {
			return
		}
		var body synthGenerateBody
		if r.ContentLength != 0 {
			if !decodeBody(w,
				r,
				maxSynthBody,
				&body) {
				return
			}
		}
		if err := s.Mgr.CheckBudget(r.Context(), owner); err != nil {
			fail(w,
				err,
				"synthetic eval")
			return
		}
		name := r.PathValue("name")
		gen, err := s.Synth.Regenerate(r.Context(),
			owner,
			name,
			body.KeepEdited == nil || *body.KeepEdited)
		if err != nil {
			fail(w,
				err,
				"synthetic eval")
			return
		}
		httputil.WriteJSON(w,
			http.StatusAccepted,
			generationResponse{
				Name:       name,
				Version:    gen.Version,
				Generation: gen,
			})
	}
}

// SynthEditCase serves PUT /v1/gb/evals/synth/cases/{name}/{case}: the user changes one case of a synthetic eval. The body is the case
// (the JSON of a case of an eval file; the name is the one of the path). The case is validated like any eval is; from then on it counts as
// "edited" and a regeneration keeps it. 200 with the case, 400 with the problems, 404.
func (s *Service) SynthEditCase(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		if !s.synthReady(w, false) {
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "synthetic eval")
		if !ok {
			return
		}
		data, err := readBody(w,
			r,
			s.Mgr.Env().Limits.MaxFileBytes)
		if err != nil {
			if errors.Is(err, usereval.ErrTooLarge) {
				fail(w,
					err,
					"case")
				return
			}
			httputil.Error(w,
				http.StatusBadRequest,
				"the request body could not be read")
			return
		}
		c, err := s.Synth.EditCase(r.Context(),
			owner,
			r.PathValue("name"),
			r.PathValue("case"),
			data)
		if err != nil {
			fail(w,
				err,
				"synthetic eval or case")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			c)
	}
}
