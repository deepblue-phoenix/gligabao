package training_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/training"
)

// write creates dir/rel with content (and the directories above it).
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addDataset adds a dataset to dir by hand: a subdirectory called name with a manifest and a valid data file.
func addDataset(t *testing.T, dir, name string) {
	t.Helper()
	write(t, dir, name+"/dataset.toml", "file = \"data.jsonl\"\n")
	write(t, dir, name+"/data.jsonl", goodDataset)
}

func TestDiscoverDatasets(t *testing.T) {
	dir := t.TempDir()
	// The manifest says where the data file is; it can be in a subdirectory, and the name defaults to the directory's.
	write(t, dir, "b-set/dataset.toml", "file = \"data/rows.jsonl\"\ndescription = \"d\"\nversion = \"2\"\ngenerator = \"gen.py\"\n")
	write(t, dir, "b-set/data/rows.jsonl", goodDataset)
	write(t, dir, "a-dir/dataset.toml", "name = \"alpha\"\nformat = \"jsonl\"\nfile = \"x.jsonl\"\n")
	write(t, dir, "a-dir/x.jsonl", goodDataset)
	write(t, dir, "a-dir/other.jsonl", "not a dataset file: the manifest decides\n")
	// Not datasets: no manifest, a loose file, a hidden directory.
	write(t, dir, "no-manifest/rows.jsonl", goodDataset)
	write(t, dir, "loose.jsonl", goodDataset)
	write(t, dir, ".hidden/dataset.toml", "file = \"x.jsonl\"\n")
	// Problems.
	write(t, dir, "bad-toml/dataset.toml", "file = \n")
	write(t, dir, "no-file/dataset.toml", "name = \"nf\"\n")
	write(t, dir, "escape/dataset.toml", "file = \"../a-dir/x.jsonl\"\n")
	write(t, dir, "absolute/dataset.toml", "file = \"/etc/passwd\"\n")
	write(t, dir, "wrong-ext/dataset.toml", "file = \"x.json\"\n")
	write(t, dir, "bad-format/dataset.toml", "file = \"x.jsonl\"\nformat = \"csv\"\n")
	write(t, dir, "bad name!/dataset.toml", "file = \"x.jsonl\"\n")
	write(t, dir, "missing/dataset.toml", "file = \"x.jsonl\"\ngenerator = \"gen.py\"\n")
	write(t, dir, "dup/dataset.toml", "name = \"alpha\"\nfile = \"x.jsonl\"\n")
	write(t, dir, "dup/x.jsonl", goodDataset)

	found, problems := training.DiscoverDatasets(dir)
	var names []string
	for _, f := range found {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "alpha,b-set" {
		t.Fatalf("found %v", names)
	}
	b := found[1]
	if b.Description != "d" || b.Version != "2" || b.Generator != "gen.py" || b.Format != "jsonl" || b.Filename() != "b-set.jsonl" ||
		b.Path != filepath.Join(dir, "b-set", "data", "rows.jsonl") || b.Dir != filepath.Join(dir, "b-set") {
		t.Fatalf("b-set: %+v", b)
	}
	want := map[string]string{
		"bad-toml": "invalid manifest", "no-file": "no `file`", "escape": "relative path", "absolute": "relative path",
		"wrong-ext": "ending in .jsonl", "bad-format": "unsupported format", "bad name!": "invalid dataset name",
		"missing": "run gen.py", "dup": "already taken",
	}
	got := map[string]string{}
	for _, p := range problems {
		got[p.Dir] = p.Error
	}
	if len(got) != len(want) {
		t.Fatalf("problems %v", got)
	}
	for dirName, sub := range want {
		if !strings.Contains(got[dirName], sub) {
			t.Errorf("problem of %s: %q, want %q", dirName, got[dirName], sub)
		}
	}

	// A missing or unset directory has no datasets.
	for _, d := range []string{"", filepath.Join(dir, "nope")} {
		if f, p := training.DiscoverDatasets(d); len(f) != 0 || len(p) != 0 {
			t.Errorf("%q: %v %v", d, f, p)
		}
	}
}
