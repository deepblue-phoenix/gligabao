package evals

import (
	"net/http"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

// bookmarkRequest is the body of POST /v1/gb/evals/bookmarks: the kind of the object and its id (TargetID is an experiment run id, an eval
// run id, a case (case def) id or a case run id; for a fact it is the id of its case run and EdgeID and Fact say which fact).
type bookmarkRequest struct {
	Kind     core.BookmarkKind    `json:"kind"`
	TargetID int64                `json:"target_id"`
	EdgeID   int64                `json:"edge_id"`
	Fact     *core.BookmarkedFact `json:"fact"`
	Note     string               `json:"note"`
}

// AddBookmark answers POST /v1/gb/evals/bookmarks: bookmarks an object of the eval results (an experiment run, an eval run, a case, a case run
// or a fact of a case run, see core.EvalBookmark) for later review. The levels above it are filled by the server. Bookmarking the same
// object again only updates its note. Answers the bookmark (201), 400 for an invalid body, 404 for an unknown object.
func (s *Service) AddBookmark(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		var req bookmarkRequest
		if !readJSON(w, r, &req) {
			return
		}
		b := core.EvalBookmark{
			Kind: req.Kind,
			Fact: req.Fact,
			Note: req.Note,
		}
		switch req.Kind {
		case core.BookmarkExperimentRun:
			b.ExperimentRunID = core.ExperimentRunID(req.TargetID)
		case core.BookmarkEvalRun:
			b.EvalRunID = core.EvalRunID(req.TargetID)
		case core.BookmarkCaseDef:
			b.CaseDefID = core.CaseDefID(req.TargetID)
		case core.BookmarkCaseRun:
			b.CaseRunID = core.CaseRunID(req.TargetID)
		case core.BookmarkFact:
			b.CaseRunID = core.CaseRunID(req.TargetID)
			b.EdgeID = req.EdgeID
			if req.EdgeID <= 0 || req.Fact == nil || strings.TrimSpace(req.Fact.Source) == "" || strings.TrimSpace(req.Fact.Relation) == "" || strings.TrimSpace(req.Fact.Target) == "" {
				httputil.Error(w,
					http.StatusBadRequest,
					"a fact needs edge_id and fact {source, relation, target}")
				return
			}
		default:
			httputil.Error(w,
				http.StatusBadRequest,
				"kind must be one of experiment_run, eval_run, case_def, case_run, fact")
			return
		}
		if req.TargetID <= 0 {
			httputil.Error(w,
				http.StatusBadRequest,
				"target_id is required")
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalRun, "", "bookmark")
		if !ok {
			return
		}
		if err := s.Store.CreateEvalBookmark(r.Context(),
			s.DB,
			owner,
			&b); err != nil {
			fail(w,
				err,
				string(req.Kind))
			return
		}
		httputil.WriteJSON(w,
			http.StatusCreated,
			b)
	}
}

// ListBookmarks answers GET /v1/gb/evals/bookmarks: the bookmarks of the user, newest first, as {"bookmarks": [...]}.
func (s *Service) ListBookmarks(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, ok := ownerOf(w, r, own, KindEvalRun, "", "bookmarks")
		if !ok {
			return
		}
		bs, err := s.Store.ListEvalBookmarks(r.Context(),
			s.DB,
			owner)
		if err != nil {
			fail(w,
				err,
				"bookmarks")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			map[string]any{
				"bookmarks": bs,
			})
	}
}

// DeleteBookmark answers DELETE /v1/gb/evals/bookmarks/{id} (204; 404 for an unknown bookmark).
func (s *Service) DeleteBookmark(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := PathID(w, r)
		if !ok {
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalRun, "", "bookmark")
		if !ok {
			return
		}
		if err := s.Store.DeleteEvalBookmark(r.Context(),
			s.DB,
			owner,
			id); err != nil {
			fail(w,
				err,
				"bookmark")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
