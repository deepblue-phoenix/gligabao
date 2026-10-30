package agent_research

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db/sqlite"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// Ops is what the Manager needs of the world around it that depends on the database: the functions that store and read the runs and
// their states (the backend).
type Ops struct {
	Store db.Store
}

// The Ops of each backend.
var (
	SQLiteOps   = Ops{Store: sqlite.Store}
	PostgresOps = Ops{Store: postgres.Store}
)

// OpsFor returns the Ops of a database backend (config.DBBackendSQLite or config.DBBackendPostgres; empty is sqlite).
func OpsFor(backend string) (Ops, error) {
	switch backend {
	case "", config.DBBackendSQLite:
		return SQLiteOps, nil
	case config.DBBackendPostgres:
		return PostgresOps, nil
	}
	return Ops{}, fmt.Errorf("unknown agent research db backend %q", backend)
}

// postgresSchemaLock is the advisory lock key that serializes the starters that apply the schema (CREATE ... IF NOT EXISTS can
// still race in PostgreSQL). Others in use: 0x67620001 to 0x67620004 (relational, graph, evals, training), 0x67620007 (training
// audits), 0x67620008 (evals).
const postgresSchemaLock = 0x67620009

// Open applies the schema of the research agent tables (gb_agent_research_runs and gb_agent_research_states) to d, the relational
// database opened for the backend (config.DBBackendSQLite or config.DBBackendPostgres; empty is sqlite). The statements are
// idempotent.
func Open(backend string, d *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch backend {
	case "", config.DBBackendSQLite:
		if _, err := d.ExecContext(ctx, sqlite.Schema); err != nil {
			return fmt.Errorf("agent research: init schema: %w", err)
		}
		return nil
	case config.DBBackendPostgres:
		conn, err := d.Conn(ctx)
		if err != nil {
			return fmt.Errorf("agent research: init schema: %w", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(postgresSchemaLock)); err != nil {
			return fmt.Errorf("agent research: init schema: lock: %w", err)
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(postgresSchemaLock))
		if _, err := conn.ExecContext(ctx, postgres.Schema); err != nil {
			return fmt.Errorf("agent research: init schema: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown agent research db backend %q", backend)
}
