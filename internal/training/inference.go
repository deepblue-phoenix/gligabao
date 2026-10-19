package training

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// Inferer runs a prompt through a trained model on the sidecar; *sidecar.Client is one.
type Inferer interface {
	Infer(ctx context.Context, req sidecar.InferRequest) (sidecar.InferResponse, error)
}

const (
	// InferTimeout bounds one inference call: the model loads on a GPU container first, which takes minutes when cold.
	InferTimeout = 15 * time.Minute
	// MaxInferTokens is the most tokens an answer may have.
	MaxInferTokens = 8192
)

// GetModel reads trained model id of n: a KindNotFound or KindInternal Error if it cannot.
func GetModel(ctx context.Context, n Database, id int64) (*trainingdb.TrainedModel, error) {
	tm, err := trainingdb.GetTrainedModel(ctx, n.DB, id)
	if errors.Is(err, relational.ErrNotFound) {
		return nil, notFoundErr("unknown trained model")
	} else if err != nil {
		log.WithError(err).Error("training: read trained model failed")
		return nil, internalErr("failed to read the trained model", err)
	}
	return tm, nil
}

// ValidateGeneration checks the settings of an answer (max tokens, temperature), as a KindInvalid Error.
func ValidateGeneration(maxTokens int, temperature *float64) error {
	switch {
	case maxTokens < 0 || maxTokens > MaxInferTokens:
		return invalidErr("max_tokens must be between 1 and " + strconv.Itoa(MaxInferTokens))
	case temperature != nil && (*temperature <= 0 || *temperature > 2):
		return invalidErr("temperature must be above 0 and at most 2")
	}
	return nil
}

// InferRequest is a prompt for a trained model.
type InferRequest struct {
	Prompt      string
	MaxTokens   int
	Temperature *float64
}

// Validate checks the request: a KindInvalid Error if it is not acceptable.
func (r InferRequest) Validate() error {
	if r.Prompt == "" {
		return invalidErr("prompt is required")
	}
	return ValidateGeneration(r.MaxTokens, r.Temperature)
}

// InferResult is the answer of a trained model, and the ID of the stored request.
type InferResult struct {
	ID           int64
	ModelID      int64
	Response     string
	InputTokens  int
	OutputTokens int
	DurationMS   int64
	ColdStart    *bool
	LoadSeconds  float64
}

// Infer runs req through the weights of trained model tm on the sidecar sc. The request is stored before it goes to the
// sidecar and updated when it ends, so it can be reviewed later whatever happens to it (a failure, the caller giving up, a
// restart of the server). Errors are *Error: KindInvalid for a bad request, KindUpstream when the sidecar fails, KindInternal.
func Infer(ctx context.Context, n Database, sc Inferer, tm *trainingdb.TrainedModel, req InferRequest) (*InferResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	dbCtx := context.WithoutCancel(ctx) // the writes must outlive the request
	reqID, err := trainingdb.RecordTrainedModelInference(dbCtx, n.DB, trainingdb.TrainedModelInference{
		TrainedModelID: tm.ID, Prompt: req.Prompt, ModelSize: tm.ModelSize, ModelPath: tm.Path, MaxTokens: req.MaxTokens, Temperature: req.Temperature})
	if err != nil {
		log.WithError(err).WithField("model_id", tm.ID).Error("training: recording the inference request failed")
		return nil, internalErr("failed to record the inference request", err)
	}
	finish := func(status, response, errText string, inTok, outTok int, cold *bool, loadS float64, started time.Time) {
		if err := trainingdb.FinishTrainedModelInference(dbCtx, n.DB, reqID, status, response, errText, inTok, outTok, cold, loadS, time.Since(started).Milliseconds()); err != nil {
			log.WithError(err).WithField("request_id", reqID).Error("training: updating the inference request failed")
		}
	}
	started := time.Now()
	ictx, cancel := context.WithTimeout(ctx, InferTimeout)
	defer cancel()
	out, err := sc.Infer(ictx, sidecar.InferRequest{Prompt: req.Prompt, ModelSize: tm.ModelSize, ModelPath: tm.Path,
		MaxTokens: req.MaxTokens, Temperature: req.Temperature})
	if err != nil {
		var ie *sidecar.InferError
		var se *sidecar.StatusError
		switch {
		case errors.As(err, &ie):
			finish(trainingdb.TrainedModelInferenceFailed, "", ie.Message, 0, 0, nil, 0, started)
			return nil, &Error{KindUpstream, ie.Message, err}
		case errors.As(err, &se) && se.Code == http.StatusBadRequest:
			finish(trainingdb.TrainedModelInferenceFailed, "", se.Message(), 0, 0, nil, 0, started)
			return nil, &Error{KindInvalid, se.Message(), err}
		default:
			log.WithError(err).WithField("model_id", tm.ID).Error("training: inference failed")
			finish(trainingdb.TrainedModelInferenceFailed, "", err.Error(), 0, 0, nil, 0, started)
			return nil, &Error{KindUpstream, "sidecar failed to run the inference", err}
		}
	}
	finish(trainingdb.TrainedModelInferenceCompleted, out.Response, "", out.InputTokens, out.OutputTokens, out.ColdStart, out.LoadSeconds, started)
	return &InferResult{ID: reqID, ModelID: tm.ID, Response: out.Response, InputTokens: out.InputTokens, OutputTokens: out.OutputTokens,
		DurationMS: time.Since(started).Milliseconds(), ColdStart: out.ColdStart, LoadSeconds: out.LoadSeconds}, nil
}
