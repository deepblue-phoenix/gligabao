// Package admin contains the HTTP handlers behind the admin UI. Unlike the user-facing API they are not scoped to
// a user_id: they see every user's datasets. They are only mounted when the admin UI is enabled.
package admin

import (
	"database/sql"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
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
	// Totals covers the whole system, deleted datasets included (their rows are kept).
	Totals Counts `json:"totals"`
}

// Overview serves GET /admin/api/overview[?include_deleted=true]: the datasets of all users, each with its
// artifact/entity/edge counts, plus the totals for the whole system.
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
		}
		t, err := graph.TotalCounts(ctx, d)
		if err != nil {
			log.WithError(err).Error("admin: total counts failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to count system contents")
			return
		}
		out.Totals = Counts{t.Artifacts, t.Entities, t.Edges}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}
