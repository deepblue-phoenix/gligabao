package evalfile_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

	// A case name used by two evals (here of different directories) is fine, and a bare name that both have is ambiguous for Select.
	writeEval(t, b, "three.json", `{"name":"three","cases":[{"name":"a1"}]}`)
	evals, err = evalfile.ReadDirs([]string{a, b}, nameOf, nil)
	if err != nil {
		t.Fatalf("the same case name in two evals: %v", err)
	}
	if _, err := evalfile.Select(evals, nameOf, "", []string{"a1"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous bare name: %v", err)
	}
	sel, err := evalfile.Select(evals, nameOf, "three", []string{"a1"})
	if err != nil || len(sel) != 1 || sel[0].Name != "three" || len(sel[0].Cases) != 1 {
		t.Errorf("name with its eval: %+v %v", sel, err)
	}
	if sel, err = evalfile.Select(evals, nameOf, "", []string{"b2"}); err != nil || len(sel) != 1 || sel[0].Name != "two" {
		t.Errorf("a name only one eval has: %+v %v", sel, err)
	}
	// A repeated case name in one eval and a repeated eval name in one directory are errors.
	writeEval(t, b, "four.json", `{"name":"four","cases":[{"name":"x"},{"name":"x"}]}`)
	if _, err := evalfile.ReadDirs([]string{b}, nameOf, nil); err == nil {
		t.Error("duplicate case name in an eval: expected an error")
	}
	if err := os.Remove(filepath.Join(b, "four.json")); err != nil {
		t.Fatal(err)
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
