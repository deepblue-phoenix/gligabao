package training

// The trained models: listing them, reading one, inferences with one.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

const (
	trainedModelsDefaultLimit = 100
	trainedModelsMaxLimit     = 500
	maxInferBody              = 1 << 16
)

// ListModelsOf serves GET /v1/gb/training/models[?limit=<n>]: {"models": [...]}, the trained models,
// newest first, at most limit (default 100, at most 500).
func ListModelsOf(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, ok := limitParam(w, r, trainedModelsDefaultLimit, trainedModelsMaxLimit)
		if !ok {
			return
		}
		models, err := trainingdb.ListTrainedModels(r.Context(), db, limit)
		if err != nil {
			log.WithError(err).Error("training: list trained models failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the trained models")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"models": models})
	}
}

// GetModelOf serves GET /v1/gb/training/models/{id}: one trained model (trainingdb.TrainedModel), 404 if unknown.
func GetModelOf(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		m, err := core.GetModel(r.Context(), db, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, m)
	}
}

// InferOf serves POST /v1/gb/training/models/{id}/infer with a JSON body {"prompt": "...", "max_tokens": 1024,
// "temperature": 0.7} (only the prompt is required): it runs the prompt through the weights of trained model {id} on the
// sidecar and answers 200 with {"id", "model_id", "response", "status", "duration_ms"} (id: the stored request, see
// ListInferencesOf; failed ones are stored too). It can take minutes when the model has to be loaded. 404 is for
// an unknown model, 400 for a bad body, 503 for a nil sc (no sidecar), 502 when the sidecar fails (with its message when the
// generation itself failed, e.g. the weights are gone from its volume).
func InferOf(db *sql.DB, sc core.Inferer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if sc == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not active")
			return
		}
		var in struct {
			Prompt      string   `json:"prompt"`
			MaxTokens   int      `json:"max_tokens"`
			Temperature *float64 `json:"temperature"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxInferBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		req := core.InferRequest{Prompt: in.Prompt, MaxTokens: in.MaxTokens, Temperature: in.Temperature}
		if err := req.Validate(); err != nil {
			writeErr(w, err)
			return
		}
		m, err := core.GetModel(r.Context(), db, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		out, err := core.Infer(r.Context(), db, sc, m, req)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"id": out.ID, "model_id": out.ModelID, "response": out.Response,
			"status": trainingdb.TrainedModelInferenceCompleted, "input_tokens": out.InputTokens, "output_tokens": out.OutputTokens,
			"duration_ms": out.DurationMS, "cold_start": out.ColdStart, "load_seconds": out.LoadSeconds})
	}
}

// ListInferencesOf serves GET /v1/gb/training/models/{id}/inferences[?limit=<n>]: {"inferences": [...]}, the
// inference requests made to trained model {id} (trainingdb.TrainedModelInference: prompt, response, status, error, ...),
// newest first, at most limit (default 100, at most 500). 404 for an unknown model.
func ListInferencesOf(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		limit, ok := limitParam(w, r, trainedModelsDefaultLimit, trainedModelsMaxLimit)
		if !ok {
			return
		}
		if _, err := core.GetModel(r.Context(), db, id); err != nil {
			writeErr(w, err)
			return
		}
		list, err := trainingdb.ListTrainedModelInferences(r.Context(), db, id, limit)
		if err != nil {
			log.WithError(err).Error("training: list inference requests failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the inference requests")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"inferences": list})
	}
}

// ExportOf serves POST /v1/gb/training/models/{id}/exports with a JSON body {"exporter": "...", "options": {...}} (only the
// exporter is required; options is a free-form object that is handed to the exporter). It fetches the weights of trained model {id}
// from the sidecar into a directory on the server's disk, calls the named exporter (a hook of the embedding program, see
// ServeOptions.TrainedModelExporters) with it, waits for it and answers 200 with the stored export (trainingdb.TrainedModelExport:
// status "completed", ref, details, dir, ...). The request takes as long as fetching and the exporter take, minutes for a large
// model. 404 is for an unknown model, 400 for a bad body, an unknown exporter or none registered, 503 for a nil fetcher (no
// sidecar), 502 when fetching the files or the exporter fails (the failed export is stored: see ListExportsOf).
func ExportOf(db *sql.DB, f core.ModelFileFetcher, exporters []core.TrainedModelExporter, dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in struct {
			Exporter string         `json:"exporter"`
			Options  map[string]any `json:"options"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, core.MaxExportOptionsBytes+1024)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		m, err := core.GetModel(r.Context(), db, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		if f == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not active")
			return
		}
		out, err := core.ExportModel(r.Context(), db, f, exporters, dir, m, core.ExportRequest{Exporter: in.Exporter, Options: in.Options})
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// ListExportsOf serves GET /v1/gb/training/models/{id}/exports[?limit=<n>]: {"exports": [...]}, the exports of trained model {id}
// (trainingdb.TrainedModelExport: exporter, status, ref, details, error, ...), newest first, at most limit (default 100, at most
// 500), failed ones included. 404 for an unknown model.
func ListExportsOf(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		limit, ok := limitParam(w, r, trainedModelsDefaultLimit, trainedModelsMaxLimit)
		if !ok {
			return
		}
		if _, err := core.GetModel(r.Context(), db, id); err != nil {
			writeErr(w, err)
			return
		}
		list, err := core.ListExports(r.Context(), db, id, limit)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"exports": list})
	}
}
