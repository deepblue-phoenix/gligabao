package investigate_core

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// The pure core imports neither the database, the model nor the file system.
func TestPureCoreImports(t *testing.T) {
	pure := []string{"ids.go", "limits.go", "frontier.go", "notes.go", "claims.go", "challenges.go", "diff.go", "tools.go",
		"loop_state.go", "prompt.go", "evict.go", "gates.go", "breakers.go", "finish_tools.go"}
	forbidden := []string{"database/sql", "os", "io/fs", "io", "net/http", "context", "path/filepath", "github.com/deepblue-phoenix/gligabao"}
	for _, file := range pure {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbidden {
				if path == bad || strings.HasPrefix(path, bad+"/") {
					t.Errorf("%s imports %s", file, path)
				}
			}
		}
	}
}
