package training

// Trained models: the weights the completed training jobs saved (gb_trained_models, recorded by the Monitor). They are
// listed, and each can be picked to run inference on.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
	log "github.com/sirupsen/logrus"
)

// Inferer runs a prompt through a trained model on the sidecar; *sidecar.Client is one.
type Inferer interface {
	Infer(ctx context.Context, req sidecar.InferRequest) (sidecar.InferResponse, error)
}

const (
	trainedModelsDefaultLimit = 100
	trainedModelsMaxLimit     = 500
	// inferTimeout bounds one inference call: the model loads on a GPU container first, which takes minutes when cold.
	inferTimeout   = 15 * time.Minute
	maxInferBody   = 1 << 16
	maxInferTokens = 8192
)

// ListModelsOf serves GET /v1/gb/training/models[?db=<name>][&limit=<n>]: {"models": [...]}, the trained models of one of dbs
// (the first without db), newest first, at most limit (default 100, at most 500).
func ListModelsOf(dbs []Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		limit := trainedModelsDefaultLimit
		if s := q.Get("limit"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil || v < 1 {
				httputil.Error(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			limit = min(v, trainedModelsMaxLimit)
		}
		models, err := relational.ListTrainedModels(r.Context(), n.DB, limit)
		if err != nil {
			log.WithError(err).Error("training: list trained models failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the trained models")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"models": models})
	}
}

// GetModelOf serves GET /v1/gb/training/models/{id}[?db=<name>]: one trained model (relational.TrainedModel), 404 if unknown.
func GetModelOf(dbs []Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			httputil.Error(w, http.StatusBadRequest, "id must be a positive integer")
			return
		}
		m, err := relational.GetTrainedModel(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown trained model")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read trained model failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the trained model")
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
func InferOf(dbs []Database, sc Inferer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			httputil.Error(w, http.StatusBadRequest, "id must be a positive integer")
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
		switch {
		case in.Prompt == "":
			httputil.Error(w, http.StatusBadRequest, "prompt is required")
			return
		case in.MaxTokens < 0 || in.MaxTokens > maxInferTokens:
			httputil.Error(w, http.StatusBadRequest, "max_tokens must be between 1 and "+strconv.Itoa(maxInferTokens))
			return
		case in.Temperature != nil && (*in.Temperature <= 0 || *in.Temperature > 2):
			httputil.Error(w, http.StatusBadRequest, "temperature must be above 0 and at most 2")
			return
		}
		m, err := relational.GetTrainedModel(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown trained model")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read trained model failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the trained model")
			return
		}
		// The request is stored before it goes to the sidecar and updated when it ends, so it can be reviewed later whatever
		// happens to it (a failure, the client giving up, a restart of the server). The writes must outlive the HTTP request.
		dbCtx := context.WithoutCancel(r.Context())
		reqID, err := relational.RecordInferenceRequest(dbCtx, n.DB, relational.InferenceRequest{
			TrainedModelID: m.ID, Prompt: in.Prompt, ModelSize: m.ModelSize, ModelPath: m.Path, MaxTokens: in.MaxTokens, Temperature: in.Temperature})
		if err != nil {
			log.WithError(err).WithField("model_id", m.ID).Error("training: recording the inference request failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to record the inference request")
			return
		}
		finish := func(status, response, errText string, started time.Time) {
			if err := relational.FinishInferenceRequest(dbCtx, n.DB, reqID, status, response, errText, time.Since(started).Milliseconds()); err != nil {
				log.WithError(err).WithField("request_id", reqID).Error("training: updating the inference request failed")
			}
		}
		started := time.Now()
		ctx, cancel := context.WithTimeout(r.Context(), inferTimeout)
		defer cancel()
		out, err := sc.Infer(ctx, sidecar.InferRequest{Prompt: in.Prompt, ModelSize: m.ModelSize, ModelPath: m.Path,
			MaxTokens: in.MaxTokens, Temperature: in.Temperature})
		if err != nil {
			var ie *sidecar.InferError
			var se *sidecar.StatusError
			switch {
			case errors.As(err, &ie):
				finish(relational.InferenceFailed, "", ie.Message, started)
				httputil.Error(w, http.StatusBadGateway, ie.Message)
			case errors.As(err, &se) && se.Code == http.StatusBadRequest:
				finish(relational.InferenceFailed, "", se.Message(), started)
				httputil.Error(w, http.StatusBadRequest, se.Message())
			default:
				log.WithError(err).WithField("model_id", m.ID).Error("training: inference failed")
				finish(relational.InferenceFailed, "", err.Error(), started)
				httputil.Error(w, http.StatusBadGateway, "sidecar failed to run the inference")
			}
			return
		}
		finish(relational.InferenceCompleted, out.Response, "", started)
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"id": reqID, "model_id": m.ID, "response": out.Response,
			"status": relational.InferenceCompleted, "duration_ms": time.Since(started).Milliseconds()})
	}
}

// ListInferencesOf serves GET /v1/gb/training/models/{id}/inferences[?db=<name>][&limit=<n>]: {"inferences": [...]}, the
// inference requests made to trained model {id} (relational.InferenceRequest: prompt, response, status, error, ...),
// newest first, at most limit (default 100, at most 500). 404 for an unknown model.
func ListInferencesOf(dbs []Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			httputil.Error(w, http.StatusBadRequest, "id must be a positive integer")
			return
		}
		limit := trainedModelsDefaultLimit
		if s := q.Get("limit"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil || v < 1 {
				httputil.Error(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			limit = min(v, trainedModelsMaxLimit)
		}
		if _, err := relational.GetTrainedModel(r.Context(), n.DB, id); errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown trained model")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read trained model failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the trained model")
			return
		}
		list, err := relational.ListInferenceRequests(r.Context(), n.DB, id, limit)
		if err != nil {
			log.WithError(err).Error("training: list inference requests failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the inference requests")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"inferences": list})
	}
}
