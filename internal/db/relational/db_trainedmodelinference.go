package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// Inference requests to trained models (the gb_trained_model_inferences table, see sqlite/db_schema.go)

// RecordTrainedModelInference stores a new request (status "running" unless r.Status says otherwise) and returns its ID.
// r.ID, r.CreatedAt and the completion fields are ignored.
func RecordTrainedModelInference(ctx context.Context, d *sql.DB, r TrainedModelInference) (int64, error) {
	if r.Status == "" {
		r.Status = TrainedModelInferenceRunning
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertTrainedModelInference(ctx, d, r)
	case config.DBBackendPostgres:
		return postgres.InsertTrainedModelInference(ctx, d, r)
	}
	return 0, unsupported()
}

// FinishTrainedModelInference ends request id: status is TrainedModelInferenceCompleted (with the response) or TrainedModelInferenceFailed (with the
// error text), inputTokens and outputTokens the counts the model reported (0 if none), durationMS how long it took. ErrNotFound if there is no such request.
func FinishTrainedModelInference(ctx context.Context, d *sql.DB, id int64, status, response, errText string, inputTokens, outputTokens int, durationMS int64) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FinishTrainedModelInference(ctx, d, id, status, response, errText, inputTokens, outputTokens, durationMS)
	case config.DBBackendPostgres:
		return postgres.FinishTrainedModelInference(ctx, d, id, status, response, errText, inputTokens, outputTokens, durationMS)
	}
	return unsupported()
}

// GetTrainedModelInference returns the request with that ID, or ErrNotFound.
func GetTrainedModelInference(ctx context.Context, d *sql.DB, id int64) (*TrainedModelInference, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainedModelInference(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainedModelInference(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainedModelInferences returns the newest requests first, at most limit of them (all for limit <= 0): those of trained
// model modelID, or of every model when modelID is 0.
func ListTrainedModelInferences(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]TrainedModelInference, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelInferences(ctx, d, modelID, limit)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelInferences(ctx, d, modelID, limit)
	}
	return nil, unsupported()
}
