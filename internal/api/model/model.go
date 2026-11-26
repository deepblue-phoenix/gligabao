// Package model contains the HTTP handlers of the GCNII* graph classifier that are not training (the per-user models of sargo), part of the
// training endpoints (/v1/gb/training/, see Register). The work is done by sargo (rust/sargo), which gligabao starts once and then talks to
// over HTTP: these handlers take the user of the request (never from the body), check the input and pass it, with the user ID, to sargo. Each
// user has their own model: training one user does not touch another's. The body's "model_type", if sent, must be "gcnii" (the path already
// says it).
//
//	POST /v1/gb/training/gcnii/infer                  body {"graph": {n, edges, nodes?}, "classes"?} -> the class of the graph
//	POST /v1/gb/training/gcnii/embed                  body {"graph": {n, edges, nodes?}, "normalize"?} -> the embedding of the graph
//	POST /v1/gb/training/gcnii/link                   body {"graph": {n, edges, nodes?}, "node": {name?, type?, neighbors?}, "top_k"?, "method"?: "cosine"|"head"} -> link prediction: the score of a new node against every node of the graph
//	POST /v1/gb/training/gcnii/link/eval              body {"graphs": [{n, edges, nodes?}, ...], "nodes"?, "reveal"?, "seed"?} -> both ways to predict links tested on nodes held out of the graphs
//
// The training of the model is not here: it is a job like the LoRA ones, started, followed, stopped and listed by the one set of handlers of
// the training API (POST /v1/gb/training/jobs with "model_type": "gcnii", see internal/api/training), run by the GCNII* backend of the training
// pipeline (internal/training/gcnii). The trained models are listed with the ones of the other types: GET /v1/gb/training/models?model_type=gcnii.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/modeltrain"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// ModelGCNII is the model type of the GCNII* graph classifier, the only one so far.
const ModelGCNII = "gcnii"

// maxBodyBytes bounds a request body; it is also sargo's default limit for one request (10 MiB), minus room for the user ID.
const maxBodyBytes = 10<<20 - 4<<10

// Service is what the handlers (and the GCNII* training backend, internal/training/gcnii) need from sargo; *sargo.Client is one.
type Service interface {
	GCNIITrain(ctx context.Context,
		req *sargo.GCNIITrainRequest) (*sargo.GCNIITrainResponse, error)
	GCNIITrainStatus(ctx context.Context,
		userID string) (*sargo.GCNIIStatus, error)
	GCNIICancel(ctx context.Context,
		userID string) (*sargo.GCNIIStatus, error)
	GCNIIInfer(ctx context.Context,
		req *sargo.GCNIIInferRequest) (*sargo.GCNIIInferResponse, error)
	GCNIIEmbed(ctx context.Context,
		req *sargo.GCNIIEmbedRequest) (*sargo.GCNIIEmbedResponse, error)
	GCNIILink(ctx context.Context,
		req *sargo.GCNIILinkRequest) (*sargo.GCNIILinkResponse, error)
	GCNIILinkEval(ctx context.Context,
		req *sargo.GCNIILinkEvalRequest) (*sargo.GCNIILinkEvalResponse, error)
}

// WeightsService is a Service that also keeps the weights of the models it trains; the sargo service of gligabao is one.
type WeightsService interface {
	Service
	Weights() *modeltrain.Weights
}

// HistoryService is a Service that also says where the history of the trainings on datasets is kept (a directory; "" means none).
type HistoryService interface {
	Service
	HistoryDir() string
}

// Syncer makes the model in the directory of sargo the latest recorded one of the user before it is used (see gcnii.Backend.Sync); nil does
// nothing.
type Syncer interface {
	Sync(ctx context.Context,
		userID string) (bool, error)
}

// Infer serves POST /v1/gb/training/gcnii/infer: it runs the model of the model_type of the body on a graph, for the user of the request (404 if
// they have not trained one).
func Infer(svc Service,
	rec Syncer) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := user(w, r)
		if !ok {
			return
		}
		var req sargo.GCNIIInferRequest
		if !decode(w, r, &req) {
			return
		}
		req.UserID = userID
		syncModel(r.Context(), rec, userID)
		resp, err := svc.GCNIIInfer(r.Context(), &req)
		if err != nil {
			fail(w, r, "model infer", err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			resp)
	}
}

// Embed serves POST /v1/gb/training/gcnii/embed: it embeds a graph with the model of the model_type of the body, for the user of the request (404 if
// they have not trained one).
func Embed(svc Service,
	rec Syncer) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := user(w, r)
		if !ok {
			return
		}
		var req sargo.GCNIIEmbedRequest
		if !decode(w, r, &req) {
			return
		}
		req.UserID = userID
		syncModel(r.Context(), rec, userID)
		resp, err := svc.GCNIIEmbed(r.Context(), &req)
		if err != nil {
			fail(w, r, "model embed", err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			resp)
	}
}

// Link serves POST /v1/gb/training/gcnii/link: link prediction, it scores a new node against every node of a graph with the model of the user of
// the request (404 if they have not trained one).
func Link(svc Service,
	rec Syncer) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := user(w, r)
		if !ok {
			return
		}
		var req sargo.GCNIILinkRequest
		if !decode(w, r, &req) {
			return
		}
		req.UserID = userID
		syncModel(r.Context(), rec, userID)
		resp, err := svc.GCNIILink(r.Context(), &req)
		if err != nil {
			fail(w, r, "model link", err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			resp)
	}
}

// LinkEval serves POST /v1/gb/training/gcnii/link/eval: it tests both ways to predict links (the cosine of the embeddings and the link head) on
// the same nodes held out of the graphs of the body, with the model of the user of the request (404 if they have not trained one).
func LinkEval(svc Service,
	rec Syncer) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := user(w, r)
		if !ok {
			return
		}
		var req sargo.GCNIILinkEvalRequest
		if !decode(w, r, &req) {
			return
		}
		req.UserID = userID
		syncModel(r.Context(), rec, userID)
		resp, err := svc.GCNIILinkEval(r.Context(), &req)
		if err != nil {
			fail(w, r, "model link eval", err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			resp)
	}
}

// user is the checked ID of the user of the request; if it is not valid the 400 is already written and ok is false.
func user(w http.ResponseWriter,
	r *http.Request) (userID string, ok bool) {
	userID, err := memory_core.ResolveUserID(httputil.UserID(r))
	if err != nil {
		httputil.Error(w,
			http.StatusBadRequest,
			err.Error())
		return "", false
	}
	return userID, true
}

// checkType is true if modelType is empty (the path says it is GCNII*) or "gcnii"; if not, the 400 is already written.
func checkType(w http.ResponseWriter,
	modelType string) bool {
	if modelType == "" || modelType == ModelGCNII {
		return true
	}
	httputil.Error(w,
		http.StatusBadRequest,
		`model_type must be "`+ModelGCNII+`" (or left out)`)
	return false
}

// decode reads the JSON body into v, after checking its "model_type" (the "user_id" of a body is ignored: the user is the one of the
// request); on failure the 400 is already written and ok is false.
func decode(w http.ResponseWriter,
	r *http.Request,
	v any) (ok bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w,
		r.Body,
		maxBodyBytes))
	if err != nil {
		httputil.Error(w,
			http.StatusBadRequest,
			"invalid body: "+err.Error())
		return false
	}
	var head struct {
		ModelType string `json:"model_type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		httputil.Error(w,
			http.StatusBadRequest,
			"invalid JSON body: "+err.Error())
		return false
	}
	if !checkType(w, head.ModelType) {
		return false
	}
	if err := json.Unmarshal(raw, v); err != nil {
		httputil.Error(w,
			http.StatusBadRequest,
			"invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// fail answers the error of a sargo call: what sargo says is wrong with the request (400, 404, 409) and that it is not set up (503) is
// passed on; anything else is a 502 (sargo down or answered garbage).
func fail(w http.ResponseWriter,
	r *http.Request,
	what string,
	err error) {
	var apiErr *sargo.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable:
			httputil.Error(w,
				apiErr.Status,
				apiErr.Message)
			return
		}
	}
	if errors.Is(err, sargo.ErrInvalidGCNII) {
		httputil.Error(w,
			http.StatusBadRequest,
			err.Error())
		return
	}
	log.WithError(err).WithField("user_id", httputil.UserID(r)).Error(what + " failed")
	httputil.Error(w,
		http.StatusBadGateway,
		what+" failed")
}

// syncModel makes the model in the directory of sargo the latest recorded one of the user (see Syncer); a failure is logged and the request
// goes on with the model sargo has.
func syncModel(ctx context.Context,
	rec Syncer,
	userID string) {
	if rec == nil {
		return
	}
	if _, err := rec.Sync(ctx, userID); err != nil {
		log.WithError(err).WithField("user_id", userID).Error("model: syncing the latest recorded model failed")
	}
}
