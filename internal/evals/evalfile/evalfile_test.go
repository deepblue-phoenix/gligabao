package evalfile_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

type tcase struct {
	Name string `json:"name"`
}

func nameOf(c *tcase) string { return c.Name }

func writeEval(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadDirs(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeEval(t, a, "one.json", `{"name":"one","cases":[{"name":"a1"}]}`)
	writeEval(t, b, "one.json", `{"name":"one","cases":[{"name":"a1"},{"name":"b1"}]}`) // hidden by a's, its cases too
	writeEval(t, b, "two.json", `{"name":"two","cases":[{"name":"b2"}]}`)

	evals, err := evalfile.ReadDirs([]string{a, b}, nameOf, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := evalfile.Names(evals); !slices.Equal(got, []string{"one", "two"}) {
		t.Fatalf("names = %v", got)
	}
	if evals[0].Dir != a || len(evals[0].Cases) != 1 || evals[1].Dir != b {
		t.Fatalf("evals = %+v", evals)
	}

	// A case name used by two evals of different directories is an error; so is a repeated eval name in one directory.
	writeEval(t, b, "three.json", `{"name":"three","cases":[{"name":"a1"}]}`)
	if _, err := evalfile.ReadDirs([]string{a, b}, nameOf, nil); err == nil {
		t.Error("duplicate case name over directories: expected an error")
	}
	writeEval(t, a, "dup.json", `{"name":"one","cases":[{"name":"z"}]}`)
	if _, err := evalfile.ReadDirs([]string{a}, nameOf, nil); err == nil {
		t.Error("duplicate eval name in a directory: expected an error")
	}
	if _, err := evalfile.ReadDirs([]string{filepath.Join(a, "nope")}, nameOf, nil); err == nil {
		t.Error("a missing directory: expected an error")
	}
}

func TestResolveDirs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "g.toml")
	if err := os.WriteFile(cfgPath, []byte(`evals_knowledge_dirs = ["k1", "/abs/k2"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	pick := func(c *config.Config) []string { return c.EvalsKnowledgeDirs }

	got, err := evalfile.ResolveDirs(nil, cfgPath, true, pick, "def")
	if err != nil || !slices.Equal(got, []string{filepath.Join(dir, "k1"), "/abs/k2"}) {
		t.Fatalf("config: %v, %v", got, err)
	}
	if got, err = evalfile.ResolveDirs([]string{"x"}, cfgPath, true, pick, "def"); err != nil || !slices.Equal(got, []string{"x"}) {
		t.Fatalf("flag wins: %v, %v", got, err)
	}
	missing := filepath.Join(dir, "none.toml")
	if got, err = evalfile.ResolveDirs(nil, missing, false, pick, "def"); err != nil || !slices.Equal(got, []string{"def"}) {
		t.Fatalf("default config missing: %v, %v", got, err)
	}
	if _, err = evalfile.ResolveDirs(nil, missing, true, pick, "def"); err == nil {
		t.Fatal("an explicit missing config: expected an error")
	}
}
