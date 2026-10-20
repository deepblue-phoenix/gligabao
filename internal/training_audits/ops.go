package training_audits

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db/sqlite"
)

const (
	// CallJudge is the type of the runs (gb_inferences) that the judge's LLM calls of an audit are recorded as; StageJudge is
	// their stage.
	CallJudge  = "training_audit_judge"
	StageJudge = "judge"
)

// Ops is what the Manager needs of the world around it that depends on the database: the functions that store and read the
// audits (the backend) and the type its judge calls are recorded as.
type Ops struct {
	Store    db.Store
	CallType string
}

// The Ops of each backend.
var (
	SQLiteOps   = Ops{Store: sqlite.Store, CallType: CallJudge}
	PostgresOps = Ops{Store: postgres.Store, CallType: CallJudge}
)

// OpsFor returns the Ops of a database backend (config.DBBackendSQLite or config.DBBackendPostgres; empty is sqlite).
func OpsFor(backend string) (Ops, error) {
	switch backend {
	case "", config.DBBackendSQLite:
		return SQLiteOps, nil
	case config.DBBackendPostgres:
		return PostgresOps, nil
	}
	return Ops{}, fmt.Errorf("unknown training audits db backend %q", backend)
}

// postgresSchemaLock is the advisory lock key that serializes the starters that apply the schema (CREATE ... IF NOT EXISTS can
// still race in PostgreSQL).
const postgresSchemaLock = 0x67620007

// Open applies the schema of the training audits tables to d, the relational database opened for the backend (config.DBBackendSQLite
// or config.DBBackendPostgres; empty is sqlite). The statements are idempotent. The training tables (the trained models) are made
// by internal/training/db's Open, which is called before.
func Open(backend string, d *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch backend {
	case "", config.DBBackendSQLite:
		if _, err := d.ExecContext(ctx, sqlite.Schema); err != nil {
			return fmt.Errorf("training audits: init schema: %w", err)
		}
		return nil
	case config.DBBackendPostgres:
		conn, err := d.Conn(ctx)
		if err != nil {
			return fmt.Errorf("training audits: init schema: %w", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(postgresSchemaLock)); err != nil {
			return fmt.Errorf("training audits: init schema: lock: %w", err)
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(postgresSchemaLock))
		if _, err := conn.ExecContext(ctx, postgres.Schema); err != nil {
			return fmt.Errorf("training audits: init schema: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown training audits db backend %q", backend)
}
