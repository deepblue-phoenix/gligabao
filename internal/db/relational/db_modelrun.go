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
// Model runs (the gb_model_runs table, see sqlite/db_schema.go)

// RecordModelRun stores one invocation of a model and returns its ID. The model (r.Model, r.Provider, r.Kind) is
// registered in gb_models first if it is not stored yet, so r.ModelID is ignored (an empty r.Model means the API did not
// report one, and the run has no model). r.Reasoning is the thinking level it ran with ("" means "none"), one of "none",
// "low", "medium" or "high". r.ID and r.CreatedAt are ignored. Call it outside any open transaction.
func RecordModelRun(ctx context.Context, d *sql.DB, r ModelRun) (int64, error) {
	r.Reasoning = strings.ToLower(strings.TrimSpace(r.Reasoning))
	switch r.Reasoning {
	case "":
		r.Reasoning = "none"
	case "none", "low", "medium", "high":
	default:
		return 0, fmt.Errorf("model run: unknown reasoning level %q", r.Reasoning)
	}
	id, err := EnsureModel(ctx, d, Model{Name: r.Model, Provider: r.Provider, Kind: r.Kind})
	if err != nil {
		return 0, err
	}
	r.ModelID = id
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertModelRun(ctx, d, r)
	case config.DBBackendPostgres:
		return postgres.InsertModelRun(ctx, d, r)
	}
	return 0, unsupported()
}

// GetModelRun returns the model run with that ID, its model filled in, or ErrNotFound.
func GetModelRun(ctx context.Context, d *sql.DB, id int64) (*ModelRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetModelRun(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetModelRun(ctx, d, id)
	}
	return nil, unsupported()
}

// ModelRunStats sums the recorded model runs by type.
func ModelRunStats(ctx context.Context, d *sql.DB) (map[string]ModelRunStat, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ModelRunStats(ctx, d)
	case config.DBBackendPostgres:
		return postgres.ModelRunStats(ctx, d)
	}
	return nil, unsupported()
}
