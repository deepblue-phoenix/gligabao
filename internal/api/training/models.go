package training

// The trained models: listing them, reading one, inferences with one.

import (
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

// ListModelsOf serves GET /v1/gb/training/models[?db=<name>][&limit=<n>]: {"models": [...]}, the trained models of one of dbs
// (the first without db), newest first, at most limit (default 100, at most 500).
func ListModelsOf(dbs []core.Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pickDB(w, r, dbs)
		if !ok {
			return
		}
		limit, ok := limitParam(w, r, trainedModelsDefaultLimit, trainedModelsMaxLimit)
		if !ok {
			return
		}
		models, err := trainingdb.ListTrainedModels(r.Context(), n.DB, limit)
		if err != nil {
			log.WithError(err).Error("training: list trained models failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the trained models")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"models": models})
	}
}

// GetModelOf serves GET /v1/gb/training/models/{id}[?db=<name>]: one trained model (trainingdb.TrainedModel), 404 if unknown.
func GetModelOf(dbs []core.Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pickDB(w, r, dbs)
		if !ok {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		m, err := core.GetModel(r.Context(), n, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, m)
	}
}

// InferOf serves POST /v1/gb/training/models/{id}/infer[?db=<name>] with a JSON body {"prompt": "...", "max_tokens": 1024,
// "temperature": 0.7} (only the prompt is required): it runs the prompt through the weights of trained model {id} on the
// sidecar and answers 200 with {"id", "model_id", "response", "status", "duration_ms"} (id: the stored request, see
// ListInferencesOf; failed ones are stored too). It can take minutes when the model has to be loaded. 404 is for
// an unknown model, 400 for a bad body, 503 for a nil sc (no sidecar), 502 when the sidecar fails (with its message when the
// generation itself failed, e.g. the weights are gone from its volume).
func InferOf(dbs []core.Database, sc core.Inferer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pickDB(w, r, dbs)
		if !ok {
			return
		}
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
		m, err := core.GetModel(r.Context(), n, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		out, err := core.Infer(r.Context(), n, sc, m, req)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"id": out.ID, "model_id": out.ModelID, "response": out.Response,
			"status": trainingdb.TrainedModelInferenceCompleted, "input_tokens": out.InputTokens, "output_tokens": out.OutputTokens,
			"duration_ms": out.DurationMS, "cold_start": out.ColdStart, "load_seconds": out.LoadSeconds})
	}
}

// ListInferencesOf serves GET /v1/gb/training/models/{id}/inferences[?db=<name>][&limit=<n>]: {"inferences": [...]}, the
// inference requests made to trained model {id} (trainingdb.TrainedModelInference: prompt, response, status, error, ...),
// newest first, at most limit (default 100, at most 500). 404 for an unknown model.
func ListInferencesOf(dbs []core.Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pickDB(w, r, dbs)
		if !ok {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		limit, ok := limitParam(w, r, trainedModelsDefaultLimit, trainedModelsMaxLimit)
		if !ok {
			return
		}
		if _, err := core.GetModel(r.Context(), n, id); err != nil {
			writeErr(w, err)
			return
		}
		list, err := trainingdb.ListTrainedModelInferences(r.Context(), n.DB, id, limit)
		if err != nil {
			log.WithError(err).Error("training: list inference requests failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the inference requests")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"inferences": list})
	}
}
