// Package datasets contains the HTTP handlers for managing datasets.
package datasets

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
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
		ds, err := memory.CreateDataset(r.Context(), d, req.UserID, req.Name, req.Description, req.Tags)
		switch {
		case errors.Is(err, memory.ErrInvalidDataset), errors.Is(err, memory.ErrInvalidUser):
			httputil.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, memory.ErrDatasetExists):
			httputil.Error(w, http.StatusConflict, "dataset already exists")
		case err != nil:
			log.Printf("create dataset: %v", err)
			httputil.Error(w, http.StatusInternalServerError, "failed to create dataset")
		default:
			httputil.WriteJSON(w, http.StatusCreated, ds)
		}
	}
}

// List serves GET /v1/gb/datasets?user_id=... (&include_deleted=true also returns soft-deleted datasets).
func List(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		list, err := memory.ListDatasets(r.Context(), d, q.Get("user_id"), q.Get("include_deleted") == "true")
		if errors.Is(err, memory.ErrInvalidUser) {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			log.Printf("list datasets: %v", err)
			httputil.Error(w, http.StatusInternalServerError, "failed to list datasets")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"datasets": list})
	}
}

// Delete serves DELETE /v1/gb/datasets/{name}?user_id=...: a soft delete (the row is kept with deleted = true).
// The dataset's vector namespace (memory.VectorNamespace) is removed from vectors, which may be nil.
func Delete(d *sql.DB, vectors vector.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := memory.DeleteDataset(r.Context(), d, vectors, r.URL.Query().Get("user_id"), r.PathValue("name"))
		switch {
		case errors.Is(err, memory.ErrInvalidUser):
			httputil.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, memory.ErrInvalidDataset), errors.Is(err, memory.ErrDatasetNotFound):
			httputil.Error(w, http.StatusNotFound, "dataset not found")
		case err != nil:
			log.Printf("delete dataset: %v", err)
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
		case errors.Is(err, memory.ErrInvalidUser):
			httputil.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, memory.ErrInvalidDataset), errors.Is(err, memory.ErrDatasetNotFound):
			httputil.Error(w, http.StatusNotFound, "dataset not found")
		case err != nil:
			log.Printf("export graph: %v", err)
			httputil.Error(w, http.StatusInternalServerError, "failed to export graph")
		default:
			httputil.WriteJSON(w, http.StatusOK, g)
		}
	}
}
