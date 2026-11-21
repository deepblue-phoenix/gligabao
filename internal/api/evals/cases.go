package evals

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/atif/atifstore"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/sys/userpath"
	log "github.com/sirupsen/logrus"
)

// goneMessage is the answer for the graph or the temporal changes of a run whose eval datasets were deleted (by the retention).
const goneMessage = "the graph of this run was deleted (retention)"

// TemporalArtifact is an artifact the temporal events of a run belong to: the run's own, and those of its chunks and of the web
// searches and series observations made for it. ValidAt is its event time (the reference time of the text).
type TemporalArtifact struct {
	ID        int64     `json:"id"`
	ParentID  int64     `json:"parent_id,omitempty"`
	Kind      string    `json:"kind"`
	ValidAt   time.Time `json:"valid_at"`
	CreatedAt time.Time `json:"created_at"`
}

// CaseTemporalResponse is the temporal state changes of one case run (same events and artifacts as the temporal view of a remember
// run, for all the remember calls of the case together).
type CaseTemporalResponse struct {
	CaseRunID int64 `json:"case_run_id"`
	// ArtifactIDs are the artifacts the remember calls of the case stored, in call order.
	ArtifactIDs []int64            `json:"artifact_ids"`
	Artifacts   []TemporalArtifact `json:"artifacts"`
	// Events are what the remember calls of the case did to the facts, oldest first, each with its fact in its current state.
	Events []graph.EdgeEvent `json:"events"`
}

// TemporalArtifacts returns the artifacts the events belong to, by ID (each in the dataset of its events, which may be several: a
// research agent remembers into any); empty for no events.
func TemporalArtifacts(ctx context.Context,
	d *sql.DB,
	events []graph.EdgeEvent) ([]TemporalArtifact, error) {
	out := []TemporalArtifact{}
	var datasets []int64
	ids := map[int64][]int64{}
	seen := map[int64]bool{}
	for _, e := range events {
		if e.ArtifactID == 0 || seen[e.ArtifactID] {
			continue
		}
		seen[e.ArtifactID] = true
		if _, ok := ids[e.DatasetID]; !ok {
			datasets = append(datasets, e.DatasetID)
		}
		ids[e.DatasetID] = append(ids[e.DatasetID], e.ArtifactID)
	}
	for _, ds := range datasets {
		arts, err := graph.ArtifactsByID(ctx,
			d,
			ds,
			ids[ds])
		if err != nil {
			return nil, err
		}
		for _, a := range arts {
			out = append(out, TemporalArtifact{
				ID:        a.ID,
				ParentID:  a.ParentID,
				Kind:      a.Kind,
				ValidAt:   a.ValidAt,
				CreatedAt: a.CreatedAt,
			})
		}
	}
	return out, nil
}

// knowledgeCaseRunOf reads the case run id of the owner, which must be a knowledge one (anything else is not found).
func (s *Service) knowledgeCaseRunOf(ctx context.Context,
	owner string,
	id int64) (*core.CaseRun, error) {
	c, err := s.Store.GetCaseRun(ctx,
		s.DB,
		owner,
		core.CaseRunID(id))
	if err == nil && c.Type != core.TypeKnowledge {
		err = evalsdb.ErrNotFound
	}
	return c, err
}

// caseDefOf returns the name of the eval of case run c, its case def (with its knowledge Meta; nil if it is gone) and the eval run
// that holds the case run.
func (s *Service) caseDefOf(ctx context.Context,
	owner string,
	c *core.CaseRun) (eval string, def *core.CaseDef, er *core.EvalRun, err error) {
	er, err = s.Store.GetEvalRun(ctx,
		s.DB,
		owner,
		c.EvalRunID)
	if err != nil {
		return "", nil, nil, err
	}
	ed, err := s.Store.GetEvalDef(ctx,
		s.DB,
		owner,
		er.DefID)
	if err != nil {
		return "", nil, nil, err
	}
	full, err := s.Store.GetEvalDefByName(ctx,
		s.DB,
		owner,
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

// caseWithDataset reads the case run id of the owner that has a dataset (anything else is not found) and its eval dataset: nil if the
// dataset was deleted (the retention).
func (s *Service) caseWithDataset(ctx context.Context,
	owner string,
	id int64) (*core.CaseRun, *relational.Dataset, error) {
	c, err := s.knowledgeCaseRunOf(ctx,
		owner,
		id)
	if err == nil && KnowledgeMeta(c).DatasetID == nil {
		err = evalsdb.ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	live, err := relational.ListEvalDatasets(ctx,
		s.DB,
		owner,
		int64(c.EvalRunID))
	if err != nil {
		return nil, nil, err
	}
	i := slices.IndexFunc(live, func(ds relational.Dataset) bool { return ds.ID == *KnowledgeMeta(c).DatasetID })
	if i < 0 {
		return c, nil, nil
	}
	return c, &live[i], nil
}

// GetCase serves GET /v1/gb/evals/case_runs/{id}: the case run with its trajectory references. 404 for an unknown one.
func (s *Service) GetCase(own Owner) http.HandlerFunc {
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
		item := CaseOf{
			Eval:          eval,
			Run:           *c,
			EvalRunParams: er.Params,
		}
		if def != nil {
			item.Name = def.Name
		}
		v, err := ViewWithTrajectories(ctx,
			s.DB,
			&item)
		if err != nil {
			fail(w,
				err,
				"case run")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			v)
	}
}

// GraphView turns the graph of a case run into what the route answers (the admin API adds the in-context counts).
type GraphView func(ctx context.Context,
	d *sql.DB,
	g *memory.GraphExport) (any, error)

// Graph serves GET /v1/gb/evals/case_runs/{id}/graph: the knowledge graph the case built, read from the eval dataset it ran in. 404 for
// an unknown case run or one without a dataset, 410 once the dataset was deleted (retention). view nil: the graph as it is.
func (s *Service) Graph(own Owner,
	view GraphView) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := PathID(w, r)
		if !ok {
			return
		}
		owner, ok := ownerOf(w, r, own, KindCaseRun, r.PathValue("id"), "graph")
		if !ok {
			return
		}
		ctx := r.Context()
		_, ds, err := s.caseWithDataset(ctx,
			owner,
			id)
		if err != nil {
			fail(w,
				err,
				"graph")
			return
		}
		if ds == nil {
			httputil.Error(w,
				http.StatusGone,
				goneMessage)
			return
		}
		g, err := memory.ExportDatasetGraph(ctx,
			s.DB,
			ds)
		if err != nil {
			fail(w,
				err,
				"graph")
			return
		}
		var out any = g
		if view != nil {
			if out, err = view(ctx, s.DB, g); err != nil {
				fail(w,
					err,
					"graph")
				return
			}
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			out)
	}
}

// Temporal serves GET /v1/gb/evals/case_runs/{id}/temporal: what the remember calls of the case did to the temporal state of the facts
// (gb_edge_events of the artifacts the trajectories of kind remember stored, and of their chunks and enrichments), with the reference
// times of the texts. 404 for an unknown case run or one without a dataset, 410 once the dataset was deleted (retention).
func (s *Service) Temporal(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := PathID(w, r)
		if !ok {
			return
		}
		owner, ok := ownerOf(w, r, own, KindCaseRun, r.PathValue("id"), "temporal changes")
		if !ok {
			return
		}
		ctx := r.Context()
		c, ds, err := s.caseWithDataset(ctx,
			owner,
			id)
		if err != nil {
			fail(w,
				err,
				"temporal changes")
			return
		}
		if ds == nil {
			httputil.Error(w,
				http.StatusGone,
				goneMessage)
			return
		}
		out := CaseTemporalResponse{
			CaseRunID:   id,
			ArtifactIDs: []int64{},
			Artifacts:   []TemporalArtifact{},
			Events:      []graph.EdgeEvent{},
		}
		for _, t := range KnowledgeMeta(c).Trajectories {
			if t.Kind != "remember" {
				continue
			}
			for _, aid := range t.ArtifactIDs {
				events, err := graph.ListEdgeEvents(ctx,
					s.DB,
					aid)
				if err != nil {
					fail(w,
						err,
						"temporal changes")
					return
				}
				out.ArtifactIDs = append(out.ArtifactIDs, aid)
				out.Events = append(out.Events, events...)
			}
		}
		// The trees of the artifacts do not overlap; the events of all the calls are in the order they were made.
		slices.SortFunc(out.Events, func(a,
			b graph.EdgeEvent) int {
			return cmp.Compare(a.ID, b.ID)
		})
		if out.Artifacts, err = TemporalArtifacts(ctx,
			s.DB,
			out.Events); err != nil {
			fail(w,
				err,
				"temporal changes")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			out)
	}
}

// Trajectory serves GET /v1/gb/evals/trajectories/{id}: the ATIF trajectory JSON of a trajectory that a case run of the owner
// recorded. The ID must be one of the recorded references of the owner.
func (s *Service) Trajectory(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, ok := ownerOf(w, r, own, KindTrajectory, r.PathValue("id"), "trajectory")
		if !ok {
			return
		}
		ref, err := s.Store.FindKnowledgeTrajectory(r.Context(),
			s.DB,
			owner,
			r.PathValue("id"))
		if err != nil {
			fail(w,
				err,
				"trajectory")
			return
		}
		data, err := s.ReadTrajectory(r.Context(),
			&ref.KnowledgeTrajectory,
			owner)
		if err != nil {
			httputil.Error(w,
				http.StatusNotFound,
				"trajectory file not found")
			return
		}
		if !json.Valid(data) {
			log.WithField("trajectory_id", ref.TrajectoryID).Error("evals: stored trajectory is not valid JSON")
			httputil.Error(w,
				http.StatusInternalServerError,
				"stored trajectory is corrupt")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

// ReadTrajectory reads the JSON file of a recorded trajectory of owner (see ReadTrajectoryFile) with the directory and the store of the
// service.
func (s *Service) ReadTrajectory(ctx context.Context,
	ref *core.KnowledgeTrajectory,
	owner string) ([]byte, error) {
	store := s.Trajectories
	if store == nil {
		store = atifstore.Default
	}
	return ReadTrajectoryFile(ctx,
		ref,
		s.TrajectoryDir,
		owner,
		store)
}

// ReadTrajectoryFile reads the JSON file of a recorded trajectory: from the trajectory directory dir first (when not empty), then from
// the path stored with the reference (a file, or the s3:// location of a trace in store). With an owner (not ""), a stored path outside
// dir is refused, and so is an S3 location that is not in the directory of that user. It fails if the file cannot be read.
func ReadTrajectoryFile(ctx context.Context,
	ref *core.KnowledgeTrajectory,
	dir,
	owner string,
	store atifstore.Store) ([]byte, error) {
	var candidates []string
	if dir != "" {
		candidates = append(candidates, filepath.Join(dir, filepath.Base(ref.TrajectoryID)+".json"))
	}
	if ref.Path != "" && !atifstore.IsS3Location(ref.Path) && (owner == "" || within(dir, ref.Path)) {
		candidates = append(candidates, ref.Path)
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			return data, nil
		}
	}
	// A trace kept in S3 (atif_s3_bucket) has its s3:// location as the path.
	if store != nil && atifstore.IsS3Location(ref.Path) && (owner == "" || inUserDir(ref.Path, owner)) {
		readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if data, err := store.Read(readCtx, ref.Path); err == nil {
			return data, nil
		}
	}
	return nil, os.ErrNotExist
}

// within says whether the file p is inside the directory dir (false if dir is empty).
func within(dir,
	p string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// inUserDir says whether an S3 location "s3://bucket/prefix/<user>/<name>" is in the directory of the user.
func inUserDir(location,
	user string) bool {
	seg, err := userpath.Segment(user)
	return err == nil && path.Base(path.Dir(location)) == seg
}
