package training

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestDirsWithDefaults(t *testing.T) {
	got := Dirs{Evals: []string{"/x/evals", "/y/evals"}}.WithDefaults("data/g.db")
	want := Dirs{
		Datasets:    []string{filepath.Join("data", "datasets_training")},
		Evals:       []string{"/x/evals", "/y/evals"},
		ModelExport: filepath.Join("data", "trained_models"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if def := DefaultDirs("bin/gligabao.db"); def.ModelExport != filepath.Join("bin", "trained_models") {
		t.Fatalf("default: %+v", def)
	}
}
