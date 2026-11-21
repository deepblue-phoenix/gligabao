package evals

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/evals/usereval"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

// listResponse is GET /v1/gb/evals.
type listResponse struct {
	Evals  []EvalView `json:"evals"`
	Totals Totals     `json:"totals"`
}

// EvalViews returns the view of each def, in the order of defs, and the totals over them. Each def is read as its own owner.
func (s *Service) EvalViews(ctx context.Context,
	defs []core.EvalDef) ([]EvalView, Totals, error) {
	out := make([]EvalView,
		0,
		len(defs))
	totals := Totals{
		Evals: len(defs),
	}
	for i := range defs {
		d := &defs[i]
		cases, err := s.Store.ListCaseDefs(ctx,
			s.DB,
			d.UserID,
			d.ID)
		if err != nil {
			return nil, totals, err
		}
		runs, err := s.Store.ListEvalRunsOfDef(ctx,
			s.DB,
			d.UserID,
			d.ID,
			0)
		if err != nil {
			return nil, totals, err
		}
		v := EvalView{
			Name:         d.Name,
			Description:  d.Description,
			Cases:        len(cases),
			Runs:         len(runs),
			RunsByStatus: map[core.Status]int{},
			UpdatedAt:    d.UpdatedAt,
		}
		for j := range runs {
			rv := ViewOfRun(&runs[j],
				d.Name)
			v.RunsByStatus[rv.Status]++
			v.CostUSD += rv.CostUSD
			AddCounts(&totals.Counts, rv.Counts)
			if j == 0 { // newest first
				v.LastRun = &rv
			}
		}
		totals.Runs += len(runs)
		totals.Cases += len(cases)
		totals.CostUSD += v.CostUSD
		out = append(out, v)
	}
	return out, totals, nil
}

// List serves GET /v1/gb/evals: the evals of the owner with their counts, and the totals over them.
func (s *Service) List(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
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
		views, totals, err := s.EvalViews(r.Context(), defs)
		if err != nil {
			fail(w,
				err,
				"evals")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			listResponse{
				Evals:  views,
				Totals: totals,
			})
	}
}

// putResponse is the answer of PUT /v1/gb/evals/{name}.
type putResponse struct {
	Name      string    `json:"name"`
	Cases     int       `json:"cases"`
	UpdatedAt time.Time `json:"updated_at"`
}

// readBody reads the body of a request of at most limit bytes (no limit if 0); more is usereval.ErrTooLarge.
func readBody(w http.ResponseWriter,
	r *http.Request,
	limit int) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r.Body)
	}
	data, err := io.ReadAll(http.MaxBytesReader(w,
		r.Body,
		int64(limit)))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, usereval.ErrTooLarge
	}
	return data, err
}

// Put serves PUT /v1/gb/evals/{name}: it validates the eval of the body, stores it and syncs it into the database. 201 for a new eval,
// 200 for a replaced one. 400 with the problems for an eval that is not valid, 413, 503 when the files cannot be stored, 500 when the
// sync fails (the write is then undone).
func (s *Service) Put(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "eval")
		if !ok {
			return
		}
		ctx, name := r.Context(), r.PathValue("name")
		env := s.Mgr.Env()
		data, err := readBody(w,
			r,
			env.Limits.MaxFileBytes)
		if err != nil {
			if errors.Is(err, usereval.ErrTooLarge) {
				fail(w,
					err,
					"eval")
				return
			}
			httputil.Error(w,
				http.StatusBadRequest,
				"the request body could not be read")
			return
		}
		created, err := usereval.Save(ctx,
			env,
			owner,
			name,
			data)
		if err != nil {
			fail(w,
				err,
				"eval")
			return
		}
		out := putResponse{
			Name: name,
		}
		if ev, parseErr := evalfile.ParseUser(data); parseErr == nil {
			out.Cases = len(ev.Cases)
		}
		if def, defErr := s.Store.GetEvalDefByName(ctx,
			s.DB,
			owner,
			name); defErr == nil {
			out.UpdatedAt = def.UpdatedAt
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		httputil.WriteJSON(w,
			status,
			out)
	}
}

// Get serves GET /v1/gb/evals/{name}: the stored eval, plus the corpus files its cases name ("files") and when it was last saved
// ("updated_at").
func (s *Service) Get(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "eval")
		if !ok {
			return
		}
		ctx, name := r.Context(), r.PathValue("name")
		stored, err := usereval.Get(ctx,
			s.Mgr.Env(),
			owner,
			name)
		if err != nil {
			fail(w,
				err,
				"eval")
			return
		}
		doc := map[string]json.RawMessage{}
		if err := json.Unmarshal(stored.JSON, &doc); err != nil {
			fail(w,
				err,
				"eval")
			return
		}
		files, _ := json.Marshal(stored.Files)
		doc["files"] = files
		if def, defErr := s.Store.GetEvalDefByName(ctx,
			s.DB,
			owner,
			name); defErr == nil {
			updated, _ := json.Marshal(def.UpdatedAt)
			doc["updated_at"] = updated
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			doc)
	}
}

// Delete serves DELETE /v1/gb/evals/{name}: the eval with its runs, their eval datasets and its stored file (the corpus files stay
// unless ?files=true). 204; 404 if there is no such eval; 409 while one of its runs is going. A failure half-way can be repeated.
func (s *Service) Delete(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "eval")
		if !ok {
			return
		}
		ctx, name := r.Context(), r.PathValue("name")
		env := s.Mgr.Env()
		if _, err := usereval.Get(ctx,
			env,
			owner,
			name); err != nil {
			fail(w,
				err,
				"eval")
			return
		}
		def, err := s.Store.GetEvalDefByName(ctx,
			s.DB,
			owner,
			name)
		switch {
		case err == nil:
			runs, err := s.Store.ListEvalRunsOfDef(ctx,
				s.DB,
				owner,
				def.ID,
				0)
			if err != nil {
				fail(w,
					err,
					"eval")
				return
			}
			for i := range runs {
				if active(&runs[i]) {
					httputil.Error(w,
						http.StatusConflict,
						"a run of the eval is in progress")
					return
				}
			}
			for i := range runs {
				if err := s.Mgr.DeleteDatasets(ctx,
					owner,
					runs[i].ID); err != nil {
					fail(w,
						err,
						"eval")
					return
				}
			}
			if _, err := s.Store.DeleteEvalDef(ctx,
				s.DB,
				owner,
				def.ID); err != nil {
				fail(w,
					err,
					"eval")
				return
			}
		case !errors.Is(err, evalsdb.ErrNotFound): // no def: the eval was never synced, only its file is left to delete
			fail(w,
				err,
				"eval")
			return
		}
		if err := usereval.Delete(ctx,
			env,
			owner,
			name,
			r.URL.Query().Get("files") == "true"); err != nil {
			fail(w,
				err,
				"eval")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// PutFile serves PUT /v1/gb/evals/{name}/files/{path...}: a corpus file of the eval (text/plain or application/json). 204; 400 for a
// bad path, content type or too many files; 404 for an unknown eval; 413.
func (s *Service) PutFile(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "eval")
		if !ok {
			return
		}
		env := s.Mgr.Env()
		limit := env.MaxFileBytes
		if limit == 0 {
			limit = env.Limits.MaxTextBytes
		}
		data, err := readBody(w,
			r,
			limit)
		if err != nil {
			if errors.Is(err, usereval.ErrTooLarge) {
				fail(w,
					err,
					"file")
				return
			}
			httputil.Error(w,
				http.StatusBadRequest,
				"the request body could not be read")
			return
		}
		if err := usereval.PutFile(r.Context(),
			env,
			owner,
			r.PathValue("name"),
			r.PathValue("path"),
			r.Header.Get("Content-Type"),
			data); err != nil {
			fail(w,
				err,
				"eval")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// DeleteFile serves DELETE /v1/gb/evals/{name}/files/{path...}. 204; 404 for an unknown eval or file.
func (s *Service) DeleteFile(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "eval")
		if !ok {
			return
		}
		if err := usereval.DeleteFile(r.Context(),
			s.Mgr.Env(),
			owner,
			r.PathValue("name"),
			r.PathValue("path")); err != nil {
			fail(w,
				err,
				"file")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
