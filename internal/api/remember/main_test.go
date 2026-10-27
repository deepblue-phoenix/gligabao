package remember

import (
	"database/sql"
	"net/http"
	"os"
	"testing"

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

// testHandler is Handler with a remember manager of its own, which stops with the test.
func testHandler(t testing.TB, d *sql.DB, llm memory_core.LLM, mo ...memory_core.Option) http.HandlerFunc {
	return Handler(memory_remember.NewManager(t.Context(), d, llm), d, llm, mo...)
}

// testEvalHandler is EvalHandler with a remember manager of its own, which stops with the test.
func testEvalHandler(t testing.TB, d *sql.DB, llm memory_core.LLM, mo ...memory_core.Option) http.HandlerFunc {
	return EvalHandler(memory_remember.NewManager(t.Context(), d, llm), d, llm, mo...)
}
