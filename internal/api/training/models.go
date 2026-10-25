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
// "temperature": 0.7} (only the prompt is required): it runs the prompt through trained model {id} and answers 200 with {"id",
// "model_id", "response", "status", "duration_ms", "hook"} (id: the stored request, see ListInferencesOf; failed ones are stored
// too). Where it runs is decided by router: on the inference hook that targets the model's name if there is one (ServeOptions.
// TrainedModelInferenceHooks; the sidecar is bypassed and "hook" is its name), else on the weights of the model on the sidecar ("hook"
// is ""). It can take minutes when the sidecar has to load the model. 404 is for an unknown model, 400 for a bad body, 503 when
// the model has no hook and there is no sidecar, 502 when the hook or the sidecar fails (with its message when the generation
// itself failed, e.g. the weights are gone from the sidecar's volume).
func InferOf(db *sql.DB, router *core.InferRouter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
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
		inf, hook := router.For(m)
		if inf == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not active")
			return
		}
		out, err := core.Infer(r.Context(), db, inf, m, req)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"id": out.ID, "model_id": out.ModelID, "response": out.Response,
			"status": trainingdb.TrainedModelInferenceCompleted, "input_tokens": out.InputTokens, "output_tokens": out.OutputTokens,
			"duration_ms": out.DurationMS, "cold_start": out.ColdStart, "load_seconds": out.LoadSeconds, "hook": hook})
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

// DeployOf serves POST /v1/gb/training/models/{id}/deployments with a JSON body {"deployer": "...", "options": {...}} (only the
// deployer is required; options is a free-form object that is handed to the deployer). It fetches the weights of trained model {id}
// from the sidecar into a directory on the server's disk, calls the named deployer (a hook of the embedding program, see
// ServeOptions.TrainedModelDeployers) with it, waits for it and answers 200 with the stored deployment (trainingdb.TrainedModelDeployment:
// status "completed", ref, details, dir, ...). The request takes as long as fetching and the deployer take, minutes for a large
// model. 404 is for an unknown model, 400 for a bad body, an unknown deployer or none registered, 503 for a nil fetcher (no
// sidecar), 502 when fetching the files or the deployer fails (the failed deployment is stored: see ListDeploymentsOf).
func DeployOf(db *sql.DB, f core.ModelFileFetcher, deployers []core.TrainedModelDeployer, dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in struct {
			Deployer string         `json:"deployer"`
			Options  map[string]any `json:"options"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, core.MaxDeployOptionsBytes+1024)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
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
		out, err := core.DeployModel(r.Context(), db, f, deployers, dir, m, core.DeployRequest{Deployer: in.Deployer, Options: in.Options})
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// ListDeploymentsOf serves GET /v1/gb/training/models/{id}/deployments[?limit=<n>]: {"deployments": [...]}, the deployments of trained model {id}
// (trainingdb.TrainedModelDeployment: deployer, status, ref, details, error, ...), newest first, at most limit (default 100, at most
// 500), failed ones included. 404 for an unknown model.
func ListDeploymentsOf(db *sql.DB) http.HandlerFunc {
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
		list, err := core.ListDeployments(r.Context(), db, id, limit)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"deployments": list})
	}
}

// ListDeployersOf serves GET /v1/gb/training/deployers: {"deployers": [{"name": "...", "description": "..."}, ...]}, the trained model deployers the
// embedding program registered (ServeOptions.TrainedModelDeployers), sorted by name; an empty list if there are none. The admin UI
// shows them and asks for a deployment with each.
func ListDeployersOf(deployers []core.TrainedModelDeployer) http.HandlerFunc {
	type deployer struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	list := make([]deployer, 0, len(deployers))
	for _, e := range deployers {
		list = append(list, deployer{Name: e.Name, Description: e.Description})
	}
	return func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"deployers": list})
	}
}

// ListInferenceHooksOf serves GET /v1/gb/training/inference-hooks: {"hooks": [{"name": "...", "description": "...", "model": "..."}, ...]},
// the trained model inference hooks the embedding program registered (ServeOptions.TrainedModelInferenceHooks), sorted by name, each
// with the name of the trained model it targets; an empty list if there are none. The admin UI uses it to say where a model's prompts run.
func ListInferenceHooksOf(hooks []core.TrainedModelInferenceHook) http.HandlerFunc {
	type hook struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Model       string `json:"model"`
	}
	list := make([]hook, 0, len(hooks))
	for _, h := range hooks {
		list = append(list, hook{Name: h.Name, Description: h.Description, Model: h.Model})
	}
	return func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"hooks": list})
	}
}
