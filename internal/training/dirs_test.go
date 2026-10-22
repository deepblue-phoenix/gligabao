package training

import (
	"path/filepath"
	"testing"
)

func TestDirsWithDefaults(t *testing.T) {
	got := Dirs{Evals: "/x/evals"}.WithDefaults("data/g.db")
	want := Dirs{
		Datasets:    filepath.Join("data", "datasets_training"),
		Evals:       "/x/evals",
		ModelExport: filepath.Join("data", "trained_models"),
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if def := DefaultDirs("bin/gligabao.db"); def.ModelExport != filepath.Join("bin", "trained_models") {
		t.Fatalf("default: %+v", def)
	}
}
