package postgres_test

import (
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/evals/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db/readstest"
)

// TestReads writes defs and runs of both types through the Store and reads them back.
func TestReads(t *testing.T) { readstest.Run(t, open(t), postgres.Store) }
