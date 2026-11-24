// Package gbruntime defines Runtime, the clients that gligabao assembles when the server starts (the databases, ...) and hands to
// every hook of the embedding program, so hook code reuses them instead of opening its own.
//
// This package is outside internal/ on purpose: the embedding programs import it (as gligabao.Runtime), and the monitoring package
// uses it in the signatures of the hooks.
package gbruntime

import (
	"database/sql"
	"sync/atomic"
)

// Database backends of Runtime.DBBackend (the values of the db_backend setting).
const (
	BackendSQLite   = "sqlite"
	BackendPostgres = "postgres"
)

// Runtime is what gligabao has set up by the time the server starts. It is immutable and safe for concurrent use; hooks must not
// close the clients it holds (gligabao owns them and closes them when the server stops).
type Runtime struct {
	// Relational is the relational database of gligabao (the same one the gb_* tables are in). A hook may keep tables of its own in
	// it (create them with CREATE TABLE IF NOT EXISTS from OnStart). Never nil.
	Relational *sql.DB
	// DBBackend is the kind of Relational: BackendSQLite or BackendPostgres. SQL differs between them (placeholders `?` or `$1`,
	// column types), so a hook that writes SQL switches on it.
	DBBackend string
}

// Ref is the place the server puts the Runtime in once it is built. The hooks that are called per request read it from there
// (Get); the server sets it, once, before it accepts requests.
type Ref struct {
	p atomic.Pointer[Runtime]
}

// Set stores rt (nil clears it). A nil Ref ignores it.
func (r *Ref) Set(rt *Runtime) {
	if r != nil {
		r.p.Store(rt)
	}
}

// Get returns the stored Runtime, nil before the server has built it (or after it stopped). A nil Ref returns nil.
func (r *Ref) Get() *Runtime {
	if r == nil {
		return nil
	}
	return r.p.Load()
}
