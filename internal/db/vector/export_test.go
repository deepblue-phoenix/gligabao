package vector

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func exported(t *testing.T, e Exporter, dataset string) []Vector {
	t.Helper()
	var out []Vector
	if err := e.Export(context.Background(), dataset, func(v Vector) error { out = append(out, v); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestChromemExport(t *testing.T) {
	ctx := context.Background()
	s := newTestChromem(t)
	if err := s.Upsert(ctx, "main", []Vector{
		{ID: "y", Values: []float32{0, 1, 0}, Metadata: map[string]any{"n": float64(2), "kind": "edge"}},
		{ID: "x", Values: []float32{1, 0, 0}},
		{ID: "z", Values: []float32{0, 0, 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, "", []Vector{{ID: "d", Values: []float32{1, 1, 0}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, "gone", []Vector{{ID: "g", Values: []float32{1, 1, 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "gone", []string{"g"}); err != nil {
		t.Fatal(err)
	}

	names, err := s.Datasets(ctx)
	if err != nil || !reflect.DeepEqual(names, []string{"", "main"}) { // the empty one is left out, the default is ""
		t.Fatalf("datasets %q, %v", names, err)
	}
	got := exported(t, s, "main")
	if len(got) != 3 || got[0].ID != "x" || got[1].ID != "y" || got[2].ID != "z" {
		t.Fatalf("export (ID order): %+v", got)
	}
	if !reflect.DeepEqual(got[1].Values, []float32{0, 1, 0}) || got[1].Metadata["kind"] != "edge" || got[1].Metadata["n"] != float64(2) || got[0].Metadata != nil {
		t.Errorf("values or metadata lost: %+v", got)
	}
	if def := exported(t, s, ""); len(def) != 1 || def[0].ID != "d" {
		t.Errorf("default dataset: %+v", def)
	}
	if none := exported(t, s, "nope"); len(none) != 0 {
		t.Errorf("unknown dataset: %+v", none)
	}

	stop := errors.New("stop")
	if err := s.Export(ctx, "main", func(Vector) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("the error of fn must be returned, got %v", err)
	}
}
