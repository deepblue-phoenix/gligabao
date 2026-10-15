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

// ModelRunGroups sums all the model runs by type, stage, model and thinking level (largest cost first).
func ModelRunGroups(ctx context.Context, d *sql.DB) ([]ModelRunGroup, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ModelRunGroups(ctx, d)
	case config.DBBackendPostgres:
		return postgres.ModelRunGroups(ctx, d)
	}
	return nil, unsupported()
}

// ListModelRuns returns the model runs that match f, newest first, with their model filled in: at most limit of them,
// after skipping offset. total is how many match f, whatever limit and offset.
func ListModelRuns(ctx context.Context, d *sql.DB, f ModelRunFilter, limit, offset int) (runs []ModelRun, total int, err error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListModelRuns(ctx, d, f, limit, offset)
	case config.DBBackendPostgres:
		return postgres.ListModelRuns(ctx, d, f, limit, offset)
	}
	return nil, 0, unsupported()
}
