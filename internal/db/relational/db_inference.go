package relational

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// Inferences (the gb_inferences table, see sqlite/db_schema.go)

// RecordInference stores one invocation of a model and returns its ID. The model (r.Model, r.Provider, r.Kind) is
// registered in gb_models first if it is not stored yet, so r.ModelID is ignored (an empty r.Model means the API did not
// report one, and the run has no model). r.Reasoning is the thinking level it ran with ("" means "none"), one of "none",
// "low", "medium" or "high". r.ID and r.CreatedAt are ignored. Call it outside any open transaction.
func RecordInference(ctx context.Context, d *sql.DB, r Inference) (int64, error) {
	r.Reasoning = strings.ToLower(strings.TrimSpace(r.Reasoning))
	switch r.Reasoning {
	case "":
		r.Reasoning = "none"
	case "none", "low", "medium", "high":
	default:
		return 0, fmt.Errorf("inference: unknown reasoning level %q", r.Reasoning)
	}
	id, err := EnsureModel(ctx, d, Model{Name: r.Model, Provider: r.Provider, Kind: r.Kind})
	if err != nil {
		return 0, err
	}
	r.ModelID = id
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertInference(ctx, d, r)
	case config.DBBackendPostgres:
		return postgres.InsertInference(ctx, d, r)
	}
	return 0, unsupported()
}

// GetInference returns the inference with that ID, its model filled in, or ErrNotFound.
func GetInference(ctx context.Context, d *sql.DB, id int64) (*Inference, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetInference(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetInference(ctx, d, id)
	}
	return nil, unsupported()
}

// InferenceStats sums the recorded inferences by type.
func InferenceStats(ctx context.Context, d *sql.DB) (map[string]InferenceStat, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InferenceStats(ctx, d)
	case config.DBBackendPostgres:
		return postgres.InferenceStats(ctx, d)
	}
	return nil, unsupported()
}

// InferenceGroups sums all the inferences by type, stage, model and thinking level (largest cost first).
func InferenceGroups(ctx context.Context, d *sql.DB) ([]InferenceGroup, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InferenceGroups(ctx, d)
	case config.DBBackendPostgres:
		return postgres.InferenceGroups(ctx, d)
	}
	return nil, unsupported()
}

// ListInferences returns the inferences that match f, newest first, with their model filled in: at most limit of them,
// after skipping offset. total is how many match f, whatever limit and offset.
func ListInferences(ctx context.Context, d *sql.DB, f InferenceFilter, limit, offset int) (runs []Inference, total int, err error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListInferences(ctx, d, f, limit, offset)
	case config.DBBackendPostgres:
		return postgres.ListInferences(ctx, d, f, limit, offset)
	}
	return nil, 0, unsupported()
}
