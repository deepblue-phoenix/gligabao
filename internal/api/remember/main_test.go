package remember

import (
	"os"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
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
