package errors

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/sys/errors/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/sys/errors/sqlite"
)

// postgresSchemaLock is the advisory lock key that serializes the starters that apply the schema. Others in use: see
// internal/agent_research/ops.go.
const postgresSchemaLock = 0x6762000a

// Init sets the errors up: it creates the table gb_errors in d, the relational database opened for the backend
// (config.DBBackendSQLite or config.DBBackendPostgres; empty is sqlite). The statements are idempotent. db.Open calls it, so every
// program that opens the databases has it.
func Init(backend string,
	d *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch backend {
	case "", config.DBBackendSQLite:
		if _, err := d.ExecContext(ctx, sqlite.Schema); err != nil {
			return fmt.Errorf("errors: init schema: %w", err)
		}
		return nil
	case config.DBBackendPostgres:
		conn, err := d.Conn(ctx)
		if err != nil {
			return fmt.Errorf("errors: init schema: %w", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx,
			`SELECT pg_advisory_lock($1)`,
			int64(postgresSchemaLock)); err != nil {
			return fmt.Errorf("errors: init schema: lock: %w", err)
		}
		defer conn.ExecContext(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`,
			int64(postgresSchemaLock))
		if _, err := conn.ExecContext(ctx, postgres.Schema); err != nil {
			return fmt.Errorf("errors: init schema: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown errors db backend %q", backend)
}

// SinkFor returns the Sink that inserts into gb_errors of d, for the backend (as in Init).
func SinkFor(backend string,
	d Execer) (Sink, error) {
	var insert func(context.Context,
		sqlite.Row) (int64, error)
	switch backend {
	case "", config.DBBackendSQLite:
		insert = func(ctx context.Context,
			r sqlite.Row) (int64, error) {
			return sqlite.Insert(ctx,
				d,
				r)
		}
	case config.DBBackendPostgres:
		insert = func(ctx context.Context,
			r sqlite.Row) (int64, error) {
			return postgres.Insert(ctx,
				d,
				postgres.Row(r))
		}
	default:
		return nil, fmt.Errorf("unknown errors db backend %q", backend)
	}
	return func(ctx context.Context,
		e *GBerror) error {
		meta, err := json.Marshal(e.Meta)
		switch {
		case err != nil:
			meta = []byte(`{"meta_error":"not encodable"}`)
		case string(meta) == "null":
			meta = []byte("{}")
		}
		id, err := insert(ctx, sqlite.Row{
			Op:        e.Op,
			Package:   e.Package,
			Function:  e.Function,
			Message:   e.Message,
			Stack:     e.Stack,
			Meta:      string(meta),
			CreatedAt: e.CreatedAt,
		})
		if err == nil {
			e.ID = id
		}
		return err
	}, nil
}
