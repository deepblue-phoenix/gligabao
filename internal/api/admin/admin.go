// Package admin contains the HTTP handlers behind the admin UI. Unlike the user-facing API they are not scoped to
// a user_id: they see every user's datasets. They are only mounted when the admin UI is enabled.
package admin

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	log "github.com/sirupsen/logrus"
)

// Counts is the number of artifacts, entities and edges.
type Counts struct {
	Artifacts int `json:"artifacts"`
	Entities  int `json:"entities"`
	Edges     int `json:"edges"`
}

type datasetView struct {
	relational.Dataset
	Counts
}

type overview struct {
	Datasets []datasetView `json:"datasets"`
	// Totals is the sum over Datasets, so it matches what is listed: deleted datasets (whose rows are kept, e.g. the
	// previous runs of an eval that recreates its dataset) count only when include_deleted is set.
	Totals Counts `json:"totals"`
}

// Named is a database shown in the admin UI under a name (e.g. "standard", "eval").
type Named struct {
	Name string
	DB   *sql.DB
	// TrajectoryDir is where the ATIF trajectories of the experiments recorded in DB are stored ("" = unknown);
	// only used for the eval database.
	TrajectoryDir string
}

// Databases serves GET /admin/api/databases: the names of the databases the UI can show, in display order.
func Databases(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		names := make([]string, 0, len(dbs))
		for _, d := range dbs {
			names = append(names, d.Name)
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"databases": names})
	}
}

// OverviewOf serves GET /admin/api/overview[?db=<name>][&include_deleted=true] for one of dbs; without db it
// serves the first one.
func OverviewOf(dbs []Named) http.HandlerFunc {
	handlers := make(map[string]http.HandlerFunc, len(dbs))
	for _, d := range dbs {
		handlers[d.Name] = Overview(d.DB)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		h, ok := handlers[name]
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		h(w, r)
	}
}

// Overview serves GET /admin/api/overview[?include_deleted=true]: the datasets of all users, each with its
// artifact/entity/edge counts, plus the totals over the listed datasets.
func Overview(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		list, err := relational.ListAllDatasets(ctx, d, r.URL.Query().Get("include_deleted") == "true")
		if err != nil {
			log.WithError(err).Error("admin: list datasets failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list datasets")
			return
		}
		out := overview{Datasets: make([]datasetView, 0, len(list))}
		for _, ds := range list {
			c, err := graph.DatasetCounts(ctx, d, ds.ID)
			if err != nil {
				log.WithError(err).WithField("dataset_id", ds.ID).Error("admin: dataset counts failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to count dataset contents")
				return
			}
			out.Datasets = append(out.Datasets, datasetView{ds, Counts{c.Artifacts, c.Entities, c.Edges}})
			out.Totals.Artifacts += c.Artifacts
			out.Totals.Entities += c.Entities
			out.Totals.Edges += c.Edges
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// GraphOf serves GET /admin/api/graph?db=<name>&user_id=...&dataset=... for one of dbs (the first without db):
// the whole entity graph of a live dataset (see memory.ExportGraph).
func GraphOf(dbs []Named) http.HandlerFunc {
	handlers := make(map[string]http.HandlerFunc, len(dbs))
	for _, d := range dbs {
		handlers[d.Name] = Graph(d.DB)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		h, ok := handlers[name]
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		h(w, r)
	}
}

// Graph serves GET /admin/api/graph?user_id=...&dataset=...: the entities and edges of the dataset.
func Graph(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		g, err := memory.ExportGraph(r.Context(), d, q.Get("user_id"), q.Get("dataset"))
		switch {
		case errors.Is(err, memory.ErrInvalidUser):
			httputil.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, memory.ErrInvalidDataset), errors.Is(err, memory.ErrDatasetNotFound):
			httputil.Error(w, http.StatusNotFound, "dataset not found")
		case err != nil:
			log.WithError(err).WithFields(log.Fields{"user_id": q.Get("user_id"), "dataset": q.Get("dataset")}).Error("admin: export graph failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to export graph")
		default:
			httputil.WriteJSON(w, http.StatusOK, g)
		}
	}
}
