package training_audits_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/training_audits"
)

func TestLoadDataset(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "mine")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	data := "{\"instruction\":\"q0\",\"output\":\"a0\"}\n\n{\"instruction\":\"q1\",\"input\":\"ctx\",\"output\":\"a1\"}\n"
	if err := os.WriteFile(filepath.Join(sub, "data.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "dataset.toml"), []byte("name = \"mine\"\nfile = \"data.jsonl\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ds, err := training_audits.LoadDataset(dir, "mine.jsonl")
	if err != nil || ds.Name != "mine" || len(ds.SHA256) != 64 || len(ds.Examples) != 2 {
		t.Fatalf("%+v %v", ds, err)
	}
	if got := ds.Examples[1].Prompt(); got != "q1\n\nctx" {
		t.Errorf("prompt %q", got)
	}
	for _, tc := range []struct{ dataset, want string }{
		{"", "default dataset"},
		{"deleted.jsonl", "not in the training datasets directory"},
	} {
		if _, err := training_audits.LoadDataset(dir, tc.dataset); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("dataset %q: %v, want %q", tc.dataset, err, tc.want)
		}
	}
}

func TestParseExamples(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "no records"},
		{"not json\n", "line 1 is not a dataset record"},
		{"{\"instruction\":\"q\",\"output\":\"a\"}\n{\"instruction\":\" \"}\n", "line 2 has no instruction"},
		{"{\"instruction\":\"\xff\"}", "UTF-8"},
	} {
		if _, err := training_audits.ParseExamples([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.in, err, tc.want)
		}
	}
}
