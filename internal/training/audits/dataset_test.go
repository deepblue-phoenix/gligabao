package audits_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/training/audits"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

func TestLoadDataset(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "mine")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	data := "{\"instruction\":\"q0\",\"output\":\"a0\"}\n{\"instruction\":\"q1\",\"output\":\"a1\"}\n"
	if err := os.WriteFile(filepath.Join(sub, "data.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "dataset.toml"), []byte("name = \"mine\"\nfile = \"data.jsonl\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	name, sum, examples, err := audits.LoadDataset(dir, &trainingdb.TrainedModel{Dataset: "mine.jsonl"})
	if err != nil || name != "mine" || len(sum) != 64 || len(examples) != 2 {
		t.Fatalf("%q %q %d %v", name, sum, len(examples), err)
	}
	for _, tc := range []struct{ dataset, want string }{
		{"", "default dataset"},
		{"deleted.jsonl", "not in the training datasets directory"},
	} {
		if _, _, _, err := audits.LoadDataset(dir, &trainingdb.TrainedModel{Dataset: tc.dataset}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("dataset %q: %v, want %q", tc.dataset, err, tc.want)
		}
	}
}
