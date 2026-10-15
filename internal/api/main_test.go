package api

import (
	"os"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gligabao-atif-*")
	if err != nil {
		panic(err)
	}
	memory_core.TrajectoryDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// mgrFor makes the remember manager for Register, which stops with the test.
func mgrFor(t testing.TB, database *db.DB, llm memory_core.LLM) *memory_remember.Manager {
	return memory_remember.NewManager(t.Context(), database.Relational, llm)
}
