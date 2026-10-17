package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// Inference requests to trained models (the gb_inference_requests table, see sqlite/db_schema.go)

// RecordInferenceRequest stores a new request (status "running" unless r.Status says otherwise) and returns its ID.
// r.ID, r.CreatedAt and the completion fields are ignored.
func RecordInferenceRequest(ctx context.Context, d *sql.DB, r InferenceRequest) (int64, error) {
	if r.Status == "" {
		r.Status = InferenceRunning
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertInferenceRequest(ctx, d, r)
	case config.DBBackendPostgres:
		return postgres.InsertInferenceRequest(ctx, d, r)
	}
	return 0, unsupported()
}

// FinishInferenceRequest ends request id: status is InferenceCompleted (with the response) or InferenceFailed (with the
// error text), durationMS how long it took. ErrNotFound if there is no such request.
func FinishInferenceRequest(ctx context.Context, d *sql.DB, id int64, status, response, errText string, durationMS int64) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FinishInferenceRequest(ctx, d, id, status, response, errText, durationMS)
	case config.DBBackendPostgres:
		return postgres.FinishInferenceRequest(ctx, d, id, status, response, errText, durationMS)
	}
	return unsupported()
}

// GetInferenceRequest returns the request with that ID, or ErrNotFound.
func GetInferenceRequest(ctx context.Context, d *sql.DB, id int64) (*InferenceRequest, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetInferenceRequest(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetInferenceRequest(ctx, d, id)
	}
	return nil, unsupported()
}

// ListInferenceRequests returns the newest requests first, at most limit of them (all for limit <= 0): those of trained
// model modelID, or of every model when modelID is 0.
func ListInferenceRequests(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]InferenceRequest, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListInferenceRequests(ctx, d, modelID, limit)
	case config.DBBackendPostgres:
		return postgres.ListInferenceRequests(ctx, d, modelID, limit)
	}
	return nil, unsupported()
}
