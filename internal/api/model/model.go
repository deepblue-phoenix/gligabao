// Package model contains the HTTP handlers for training and running per-user models. The model type is an argument of the request
// ("model_type", for now only "gcnii", the GCNII* graph classifier). The work is done by sargo (rust/sargo), which gligabao starts once
// and then talks to over HTTP: these handlers take the user of the request (never from the body), check the input and pass it, with the
// user ID, to sargo. Each user has their own model: training one user does not touch another's.
//
//	POST /v1/gb/model/train                  body {"model_type": "gcnii", "graphs": [{n, edges, nodes?, class}, ...], epochs?, lr?, batch?, hidden?, depth?, seed?} -> 202 job
//	GET  /v1/gb/model/train?model_type=gcnii the state of the training of the user -> {"user_id", "trained", "job"}
//	POST /v1/gb/model/train/dataset          body {"model_type": "gcnii", "dataset"?, "classes"?, "min_graphs_per_class"?, "validation_fraction"?, "split_seed"?, epochs?, ...} -> 202 job
//	                                         trains on the graphs of a dataset with the classes an LLM gave them (see TrainDataset)
//	POST /v1/gb/model/infer                  body {"model_type": "gcnii", "graph": {n, edges, nodes?}, "classes"?} -> the class of the graph
//	POST /v1/gb/model/embed                  body {"model_type": "gcnii", "graph": {n, edges, nodes?}, "normalize"?} -> the embedding of the graph
package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// ModelGCNII is the model type of the GCNII* graph classifier, the only one so far.
const ModelGCNII = "gcnii"

// maxBodyBytes bounds a request body; it is also sargo's default limit for one request (10 MiB), minus room for the user ID.
const maxBodyBytes = 10<<20 - 4<<10

// Service is what the handlers need from sargo; *sargo.Client is one.
type Service interface {
	GCNIITrain(ctx context.Context,
		req *sargo.GCNIITrainRequest) (*sargo.GCNIITrainResponse, error)
	GCNIITrainStatus(ctx context.Context,
		userID string) (*sargo.GCNIIStatus, error)
	GCNIIInfer(ctx context.Context,
		req *sargo.GCNIIInferRequest) (*sargo.GCNIIInferResponse, error)
	GCNIIEmbed(ctx context.Context,
		req *sargo.GCNIIEmbedRequest) (*sargo.GCNIIEmbedResponse, error)
}

// Train serves POST /v1/gb/model/train: it starts the training of the model of the model_type of the body, for the user of the request.
func Train(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := user(w, r)
		if !ok {
			return
		}
		var req sargo.GCNIITrainRequest
		if !decode(w, r, &req) {
			return
		}
		req.UserID = userID
		resp, err := svc.GCNIITrain(r.Context(), &req)
		if err != nil {
			fail(w, r, "model train", err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusAccepted,
			resp)
	}
}

// TrainStatus serves GET /v1/gb/model/train?model_type=...: the state of the training of the user of the request.
func TrainStatus(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := user(w, r)
		if !ok {
			return
		}
		if !checkType(w, r.URL.Query().Get("model_type")) {
			return
		}
		resp, err := svc.GCNIITrainStatus(r.Context(), userID)
		if err != nil {
			fail(w, r, "model train status", err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			resp)
	}
}

// Infer serves POST /v1/gb/model/infer: it runs the model of the model_type of the body on a graph, for the user of the request (404 if
// they have not trained one).
func Infer(svc Service) http.HandlerFunc {
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

// Embed serves POST /v1/gb/model/embed: it embeds a graph with the model of the model_type of the body, for the user of the request (404 if
// they have not trained one).
func Embed(svc Service) http.HandlerFunc {
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

// checkType is true if modelType is a model type that exists; if not, the 400 is already written.
func checkType(w http.ResponseWriter,
	modelType string) bool {
	if modelType == ModelGCNII {
		return true
	}
	httputil.Error(w,
		http.StatusBadRequest,
		`model_type is required and must be "`+ModelGCNII+`"`)
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
