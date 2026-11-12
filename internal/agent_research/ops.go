package agent_research

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db/sqlite"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"
)

// Ops is what the Manager needs of the world around it that depends on the database: the functions that store and read the runs and
// their states (the backend).
type Ops struct {
	Store db.Store
	// Investigate makes the store of the investigative loop (frontier, notes, claims, challenges) on the database.
	Investigate func(*sql.DB) investigate_core.Store
	// ErrSink makes the sink that stores the GBerrors of the agents in the database (nil: they are not stored).
	ErrSink func(*sql.DB) gberrors.Sink
}

// The Ops of each backend.
var (
	SQLiteOps = Ops{
		Store:       sqlite.Store,
		Investigate: sqlite.NewInvestigateStore,
		ErrSink:     errSink(config.DBBackendSQLite),
	}
	PostgresOps = Ops{
		Store:       postgres.Store,
		Investigate: postgres.NewInvestigateStore,
		ErrSink:     errSink(config.DBBackendPostgres),
	}
)

// errSink returns the function that makes the GBerror sink of the backend on a database.
func errSink(backend string) func(*sql.DB) gberrors.Sink {
	return func(d *sql.DB) gberrors.Sink {
		sink, err := gberrors.SinkFor(backend, d)
		if err != nil {
			return nil
		}
		return sink
	}
}

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
// audits), 0x67620008 (evals), 0x6762000a (errors).
const postgresSchemaLock = 0x67620009

// Open applies the schema of the research agent tables (gb_agent_research_runs and gb_agent_research_states) to d, the relational
// database opened for the backend (config.DBBackendSQLite or config.DBBackendPostgres; empty is sqlite). The statements are
// idempotent.
func Open(backend string,
	d *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch backend {
	case "", config.DBBackendSQLite:
		if _, err := d.ExecContext(ctx, sqlite.Schema); err != nil {
			return fmt.Errorf("agent research: init schema: %w", err)
		}
		for _, u := range sqlite.Upgrade {
			var n int
			if err := d.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM pragma_table_info('gb_agent_research_runs') WHERE name = ?`,
				u.Column).Scan(&n); err != nil {
				return fmt.Errorf("agent research: upgrade schema: %w", err)
			}
			if n > 0 {
				continue
			}
			if _, err := d.ExecContext(ctx, u.Statement); err != nil {
				return fmt.Errorf("agent research: upgrade schema: %w", err)
			}
		}
		migrated, err := migrateSQLiteStatus(ctx, d)
		if err != nil {
			return fmt.Errorf("agent research: upgrade schema: %w", err)
		}
		if migrated { // the rebuilt table has lost its indexes
			if _, err := d.ExecContext(ctx, sqlite.Schema); err != nil {
				return fmt.Errorf("agent research: init schema: %w", err)
			}
		}
		if _, err := d.ExecContext(ctx, sqlite.SubagentIndexes); err != nil {
			return fmt.Errorf("agent research: init schema: %w", err)
		}
		return nil
	case config.DBBackendPostgres:
		conn, err := d.Conn(ctx)
		if err != nil {
			return fmt.Errorf("agent research: init schema: %w", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx,
			`SELECT pg_advisory_lock($1)`,
			int64(postgresSchemaLock)); err != nil {
			return fmt.Errorf("agent research: init schema: lock: %w", err)
		}
		defer conn.ExecContext(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`,
			int64(postgresSchemaLock))
		for _, stmts := range []string{postgres.Schema, postgres.Upgrade, postgres.StatusMigration, postgres.SubagentIndexes} {
			if _, err := conn.ExecContext(ctx, stmts); err != nil {
				return fmt.Errorf("agent research: init schema: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown agent research db backend %q", backend)
}

// migrateSQLiteStatus rebuilds gb_agent_research_runs if its CHECK on status does not allow 'stopped' yet (a table made before 0.145.0),
// and reports whether it did. The rebuild is run on one connection with foreign keys off, as SQLite requires (dropping the table
// would otherwise delete its states), in a transaction, and the connection goes back to the pool with them on.
func migrateSQLiteStatus(ctx context.Context,
	d *sql.DB) (bool, error) {
	var ddl string
	if err := d.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'gb_agent_research_runs'`).Scan(&ddl); err != nil {
		return false, err
	}
	if strings.Contains(ddl, "'stopped'") {
		return false, nil
	}
	conn, err := d.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return false, err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, sqlite.StatusMigration); err != nil {
		tx.Rollback()
		return false, err
	}
	return true, tx.Commit()
}
