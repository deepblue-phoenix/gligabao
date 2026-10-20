package sqlite_test

import (
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/db/readstest"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/db/sqlite"
)

// TestReads writes defs and runs of both types through the Store and reads them back.
func TestReads(t *testing.T) { readstest.Run(t, open(t), sqlite.Store) }
