package remember

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
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

// testHandler is Handler with a remember manager of its own, which stops with the test. Graph classification is on by default in the
// real handler; here a request that does not say otherwise gets "classify_graph": false, so that the scripted LLMs of the tests are not
// asked for classes. testRawHandler is the handler as it is.
func testHandler(t testing.TB,
	d *sql.DB,
	llm memory_core.LLM,
	mo ...memory_core.Option) http.HandlerFunc {
	h := testRawHandler(t,
		d,
		llm,
		mo...)
	return func(w http.ResponseWriter,
		r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(body, &m) == nil {
			if _, ok := m["classify_graph"]; !ok && m["image"] == nil && m["graph_classes"] == nil {
				m["classify_graph"] = json.RawMessage("false")
				body, _ = json.Marshal(m)
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h(w, r)
	}
}

// testRawHandler is Handler with a remember manager of its own, which stops with the test.
func testRawHandler(t testing.TB,
	d *sql.DB,
	llm memory_core.LLM,
	mo ...memory_core.Option) http.HandlerFunc {
	return Handler(memory_remember.NewManager(t.Context(),
		d,
		llm),
		d,
		llm,
		mo...)
}

// testEvalHandler is EvalHandler with a remember manager of its own, which stops with the test.
func testEvalHandler(t testing.TB,
	d *sql.DB,
	llm memory_core.LLM,
	mo ...memory_core.Option) http.HandlerFunc {
	return EvalHandler(memory_remember.NewManager(t.Context(),
		d,
		llm),
		d,
		llm,
		mo...)
}
