package model

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

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
	// Classes, ClassPick, TopClasses, OtherClass, MinEdges, MinGraphsPerClass, ValidationFraction and SplitSeed are the fields of
	// modeltrain.Options.
	Classes            []string `json:"classes"`
	ClassPick          string   `json:"class_pick"`
	TopClasses         int      `json:"top_classes"`
	OtherClass         bool     `json:"other_class"`
	MinEdges           int      `json:"min_edges"`
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
	// Run is the ID of the record of the training in the history (empty if there is no history directory or it could not be written).
	Run string `json:"run,omitempty"`
}

// HistoryService is a Service that also says where the history of the trainings on datasets is kept (a directory; "" means none).
type HistoryService interface {
	Service
	HistoryDir() string
}

func historyDirOf(svc Service) string {
	if h, ok := svc.(HistoryService); ok {
		return h.HistoryDir()
	}
	return ""
}

// TrainDataset serves POST /v1/gb/model/train/dataset: it trains the model of the user of the request on the graphs of one of their datasets.
// The classes of the graphs are the ones an LLM gave them when they were remembered (classify_graph, read from gb_artifact_classes with
// SQL); hook (may be nil) picks them instead, if the program that embeds gligabao set the classifying training hook. The graphs are
// partitioned per class into a training and a validation set (validation_fraction, split_seed) and both are sent to sargo, which
// reports the accuracy on each in the job (GET /v1/gb/model/train). 404 for no such dataset, 422 if no graph has a class.
func TrainDataset(svc Service,
	d *sql.DB,
	hook memory_core.ClassifyingTrainingHook,
	rec *Recorder) http.HandlerFunc {
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
		opts := modeltrain.Options{
			Classes:            req.Classes,
			ClassPick:          req.ClassPick,
			TopClasses:         req.TopClasses,
			OtherClass:         req.OtherClass,
			MinEdges:           req.MinEdges,
			MinGraphsPerClass:  req.MinGraphsPerClass,
			ValidationFraction: req.ValidationFraction,
			Seed:               req.SplitSeed,
		}
		dataset, err := memory_core.ResolveDatasetName(req.Dataset)
		if err != nil {
			failBuild(w, r, req.Dataset, err)
			return
		}
		sets, err := modeltrain.Build(r.Context(),
			d,
			hook,
			userID,
			dataset,
			opts)
		if err != nil {
			failBuild(w, r, req.Dataset, err)
			return
		}
		params := modeltrain.TrainParams{
			Epochs: req.Epochs,
			LR:     req.LR,
			Batch:  req.Batch,
			Hidden: req.Hidden,
			Depth:  req.Depth,
			Seed:   req.Seed,
		}
		run := modeltrain.NewRun(userID,
			dataset,
			time.Now(),
			opts,
			params,
			sets)
		claim, err := rec.Claim(r.Context(),
			userID,
			trainInfo{
				Dataset:          dataset,
				Run:              run.ID,
				Params:           params,
				Graphs:           len(sets.Train),
				ValidationGraphs: len(sets.Validation),
			})
		if err != nil {
			failClaim(w, r, err)
			return
		}
		resp, err := svc.GCNIITrain(r.Context(),
			&sargo.GCNIITrainRequest{
				UserID:     userID,
				Graphs:     sets.Train,
				Validation: sets.Validation,
				Epochs:     params.Epochs,
				LR:         params.LR,
				Batch:      params.Batch,
				Hidden:     params.Hidden,
				Depth:      params.Depth,
				Seed:       params.Seed,
			})
		if err != nil {
			rec.Abort(claim, err)
			fail(w, r, "model train dataset", err)
			return
		}
		runID := saveRun(svc,
			run)
		rec.Watch(claim,
			userID,
			resp.Job)
		httputil.WriteJSON(w,
			http.StatusAccepted,
			datasetTrainResponse{
				GCNIITrainResponse: resp,
				Dataset:            dataset,
				Run:                runID,
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
	case errors.Is(err, modeltrain.ErrNoGraphs), errors.Is(err, modeltrain.ErrTooFewClasses):
		httputil.Error(w,
			http.StatusUnprocessableEntity,
			err.Error()+" (remember the dataset with classify_graph on)")
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

// saveRun writes the run to the history of svc and returns its ID; a history that cannot be written is logged, not an error of the
// request (the training is already running). "" if there is no history.
func saveRun(svc Service,
	run modeltrain.Run) string {
	dir := historyDirOf(svc)
	if dir == "" {
		return ""
	}
	if err := modeltrain.SaveRun(dir, run); err != nil {
		log.WithError(err).WithFields(log.Fields{"user_id": run.UserID, "dataset": run.Dataset}).Warn("model train dataset: saving the run history failed")
		return ""
	}
	return run.ID
}

// TrainDatasetRuns serves GET /v1/gb/model/train/dataset/runs?dataset=...: the history of the trainings on the dataset of the user of
// the request, the newest first: {"runs": [...]}. Empty without a history directory.
func TrainDatasetRuns(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := user(w, r)
		if !ok {
			return
		}
		dataset, err := memory_core.ResolveDatasetName(r.URL.Query().Get("dataset"))
		if err != nil {
			httputil.Error(w,
				http.StatusBadRequest,
				err.Error())
			return
		}
		runs := []modeltrain.Run{}
		if dir := historyDirOf(svc); dir != "" {
			runs, err = modeltrain.ListRuns(dir,
				userID,
				dataset)
			if err != nil {
				log.WithError(err).WithField("user_id", userID).Error("model train dataset runs failed")
				httputil.Error(w,
					http.StatusInternalServerError,
					"failed to read the history")
				return
			}
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			map[string][]modeltrain.Run{"runs": runs})
	}
}
