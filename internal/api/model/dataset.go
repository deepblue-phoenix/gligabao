package model

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/modeltrain"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// datasetTrainRequest is the body of POST /v1/gb/model/train/dataset: which dataset (and how to cut it up) plus the training options
// of POST /v1/gb/model/train.
type datasetTrainRequest struct {
	ModelType string `json:"model_type"`
	// Dataset is the name of a live dataset of the user (empty: the default one).
	Dataset string `json:"dataset"`
	// Classes, MinGraphsPerClass, ValidationFraction and SplitSeed are the fields of modeltrain.Options.
	Classes            []string `json:"classes"`
	MinGraphsPerClass  int      `json:"min_graphs_per_class"`
	ValidationFraction float64  `json:"validation_fraction"`
	SplitSeed          uint64   `json:"split_seed"`
	Epochs             int      `json:"epochs"`
	LR                 float64  `json:"lr"`
	Batch              int      `json:"batch"`
	Hidden             int      `json:"hidden"`
	Depth              int      `json:"depth"`
	Seed               *uint64  `json:"seed"`
}

// datasetTrainResponse is the 202 answer: the job and what the training and validation sets are made of.
type datasetTrainResponse struct {
	*sargo.GCNIITrainResponse
	Dataset string `json:"dataset"`
	// Train and Validation are how many graphs each set has; Classes the classes of both.
	Train      int      `json:"train"`
	Validation int      `json:"validation"`
	Classes    []string `json:"classes"`
}

// TrainDataset serves POST /v1/gb/model/train/dataset: it trains the model of the user of the request on the graphs of one of their datasets.
// The classes of the graphs are the ones an LLM gave them when they were remembered (classify_graph, read from gb_artifact_classes with
// SQL); hook (may be nil) picks them instead, if the program that embeds gligabao set the classifying training hook. The graphs are
// partitioned per class into a training and a validation set (validation_fraction, split_seed) and both are sent to sargo, which
// reports the accuracy on each in the job (GET /v1/gb/model/train). 404 for no such dataset, 422 if no graph has a class.
func TrainDataset(svc Service,
	d *sql.DB,
	hook memory_core.ClassifyingTrainingHook) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := user(w, r)
		if !ok {
			return
		}
		var req datasetTrainRequest
		if !decode(w, r, &req) {
			return
		}
		sets, err := modeltrain.Build(r.Context(),
			d,
			hook,
			userID,
			req.Dataset,
			modeltrain.Options{
				Classes:            req.Classes,
				MinGraphsPerClass:  req.MinGraphsPerClass,
				ValidationFraction: req.ValidationFraction,
				Seed:               req.SplitSeed,
			})
		if err != nil {
			failBuild(w, r, req.Dataset, err)
			return
		}
		resp, err := svc.GCNIITrain(r.Context(),
			&sargo.GCNIITrainRequest{
				UserID:     userID,
				Graphs:     sets.Train,
				Validation: sets.Validation,
				Epochs:     req.Epochs,
				LR:         req.LR,
				Batch:      req.Batch,
				Hidden:     req.Hidden,
				Depth:      req.Depth,
				Seed:       req.Seed,
			})
		if err != nil {
			fail(w, r, "model train dataset", err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusAccepted,
			datasetTrainResponse{
				GCNIITrainResponse: resp,
				Dataset:            req.Dataset,
				Train:              len(sets.Train),
				Validation:         len(sets.Validation),
				Classes:            sets.Classes,
			})
	}
}

// failBuild answers an error of modeltrain.Build.
func failBuild(w http.ResponseWriter,
	r *http.Request,
	dataset string,
	err error) {
	switch {
	case errors.Is(err, modeltrain.ErrInvalidOptions), errors.Is(err, memory_core.ErrInvalidDataset):
		httputil.Error(w,
			http.StatusBadRequest,
			err.Error())
	case errors.Is(err, memory_core.ErrDatasetNotFound):
		httputil.Error(w,
			http.StatusNotFound,
			"dataset not found")
	case errors.Is(err, modeltrain.ErrNoGraphs):
		httputil.Error(w,
			http.StatusUnprocessableEntity,
			err.Error()+": remember the dataset with classify_graph on")
	case errors.Is(err, memory_core.ErrInvalidTrainingDataset):
		httputil.Error(w,
			http.StatusBadGateway,
			err.Error())
	default:
		log.WithError(err).WithFields(log.Fields{"user_id": httputil.UserID(r), "dataset": dataset}).Error("model train dataset: build failed")
		httputil.Error(w,
			http.StatusInternalServerError,
			"failed to build the training set")
	}
}
