package sqlite_test

import (
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/evals/db/readstest"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db/sqlite"
)

// TestReads writes defs and runs of both types through the Store and reads them back.
func TestReads(t *testing.T) {
	readstest.Run(t,
		open(t),
		sqlite.Store)
}

// TestIsolation is the isolation suite of the owners.
func TestIsolation(t *testing.T) {
	readstest.RunIsolation(t,
		open(t),
		sqlite.Store)
}
