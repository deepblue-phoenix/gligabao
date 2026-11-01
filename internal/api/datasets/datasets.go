// Package datasets contains the HTTP handlers for managing datasets.
package datasets

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_dataset"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

const maxBodyBytes = 64 << 10 // 64 KiB

type createRequest struct {
	UserID      string   `json:"user_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

//---------------------------------------------------------------------------------------------

// Create serves POST /v1/gb/datasets.
func Create(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		//------------------
		// MEMORY DATASET
		ds, err := memory_dataset.CreateDataset(r.Context(), d, req.UserID, req.Name, req.Description, req.Tags)
		switch {
		case errors.Is(err, memory_core.ErrInvalidDataset), errors.Is(err, memory_core.ErrInvalidUser):
			httputil.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, memory_dataset.ErrDatasetExists):
			httputil.Error(w, http.StatusConflict, "dataset already exists")
		case err != nil:
			log.WithError(err).WithField("user_id", req.UserID).WithField("dataset", req.Name).Error("create dataset failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to create dataset")
		default:
			httputil.WriteJSON(w, http.StatusCreated, ds)
		}
		//------------------
	}
}

// List serves GET /v1/gb/datasets?user_id=... (&include_deleted=true also returns soft-deleted datasets).
func List(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		//------------------
		// MEMORY DATASET
		list, err := memory_dataset.ListDatasets(r.Context(), d, q.Get("user_id"), q.Get("include_deleted") == "true")
		//------------------
		if errors.Is(err, memory_core.ErrInvalidUser) {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			log.WithError(err).WithField("user_id", q.Get("user_id")).Error("list datasets failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list datasets")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"datasets": list})
	}
}

// Delete serves DELETE /v1/gb/datasets/{name}?user_id=...: a soft delete (the row is kept with deleted = true).
// The dataset's vectors (one vector store dataset per index, memory_dataset.VectorDataset) are removed from vectors, which may be nil.
func Delete(d *sql.DB, vectors vector.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		//------------------
		// MEMORY DATASET
		err := memory_dataset.DeleteDataset(r.Context(), d, vectors, r.URL.Query().Get("user_id"), r.PathValue("name"))
		//------------------
		switch {
		case errors.Is(err, memory_core.ErrInvalidUser):
			httputil.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, memory_core.ErrInvalidDataset), errors.Is(err, memory_core.ErrDatasetNotFound):
			httputil.Error(w, http.StatusNotFound, "dataset not found")
		case err != nil:
			log.WithError(err).WithFields(log.Fields{
				"user_id": r.URL.Query().Get("user_id"),
				"dataset": r.PathValue("name"),
			}).Error("delete dataset failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to delete dataset")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

// Graph serves GET /v1/gb/datasets/{name}/graph?user_id=...: the whole graph of the dataset as JSON
// (entities and edges, expired edges included).
func Graph(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g, err := memory.ExportGraph(r.Context(), d, r.URL.Query().Get("user_id"), r.PathValue("name"))
		switch {
		case errors.Is(err, memory_core.ErrInvalidUser):
			httputil.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, memory_core.ErrInvalidDataset), errors.Is(err, memory_core.ErrDatasetNotFound):
			httputil.Error(w, http.StatusNotFound, "dataset not found")
		case err != nil:
			log.WithError(err).WithFields(log.Fields{
				"user_id": r.URL.Query().Get("user_id"),
				"dataset": r.PathValue("name"),
			}).Error("export graph failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to export graph")
		default:
			httputil.WriteJSON(w, http.StatusOK, g)
		}
	}
}

type trainingRequest struct {
	UserID string `json:"user_id"`
}

// trainingResponse is what POST /v1/gb/datasets/{name}/training answers: a summary of the training dataset the hook made (not its
// artifacts and graphs, which the caller has in the graph export).
type trainingResponse struct {
	Dataset string `json:"dataset"`
	// Artifacts and Graphs are how many the hook kept, ArtifactsTotal and GraphsTotal how many the dataset has.
	Artifacts      int `json:"artifacts"`
	ArtifactsTotal int `json:"artifacts_total"`
	Graphs         int `json:"graphs"`
	GraphsTotal    int `json:"graphs_total"`
	// Classes is the class of each kept graph, by artifact ID; ClassCounts how many graphs each class has.
	Classes     map[int64]string `json:"classes"`
	ClassCounts map[string]int   `json:"class_counts"`
}

// Training serves POST /v1/gb/datasets/{name}/training: it hands the whole dataset (every artifact and the graph of each) to the
// classifying training hook and answers with a summary of the training dataset it made, a possibly filtered subset with the class of
// each graph. A failure of the hook, or a training dataset that is not valid, is a 502.
func Training(d *sql.DB, hook memory_core.ClassifyingTrainingHook) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req trainingRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		ds, err := memory.LoadKnowledgeDataset(r.Context(), d, req.UserID, r.PathValue("name"))
		if err == nil {
			var td memory_core.TrainingDataset
			td, err = memory_core.CallClassifyingTrainingHook(r.Context(), hook, ds)
			if err == nil {
				counts := map[string]int{}
				for _, class := range td.Classes {
					counts[class]++
				}
				httputil.WriteJSON(w, http.StatusOK, trainingResponse{
					Dataset: td.Dataset, Artifacts: len(td.Artifacts), ArtifactsTotal: len(ds.Artifacts),
					Graphs: len(td.Graphs), GraphsTotal: len(ds.Graphs), Classes: td.Classes, ClassCounts: counts,
				})
				return
			}
			log.WithError(err).WithFields(log.Fields{"user_id": req.UserID, "dataset": ds.Dataset}).Warn("classifying training hook failed")
			httputil.Error(w, http.StatusBadGateway, err.Error())
			return
		}
		switch {
		case errors.Is(err, memory_core.ErrInvalidUser):
			httputil.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, memory_core.ErrInvalidDataset), errors.Is(err, memory_core.ErrDatasetNotFound):
			httputil.Error(w, http.StatusNotFound, "dataset not found")
		default:
			log.WithError(err).WithFields(log.Fields{"user_id": req.UserID, "dataset": r.PathValue("name")}).Error("load knowledge dataset failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to load the dataset")
		}
	}
}
