package vector

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestChromem(t *testing.T) *Chromem {
	t.Helper()
	s, err := NewChromem(filepath.Join(t.TempDir(), "vectors"), 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ids(ms []Match) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestChromemUpsertQuery(t *testing.T) {
	ctx := context.Background()
	s := newTestChromem(t)
	err := s.Upsert(ctx, "main", []Vector{
		{ID: "x", Values: []float32{1, 0, 0}, Metadata: map[string]any{"type": "person"}},
		{ID: "y", Values: []float32{0, 1, 0}, Metadata: map[string]any{"type": "org"}},
		{ID: "xy", Values: []float32{1, 1, 0}, Metadata: map[string]any{"type": "person"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, "other", []Vector{{ID: "x", Values: []float32{0, 0, 1}}}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Query(ctx, "main", []float32{1, 0.1, 0}, QueryOptions{TopK: 2, IncludeValues: true})
	if err != nil {
		t.Fatal(err)
	}
	if g := ids(got); len(g) != 2 || g[0] != "x" || g[1] != "xy" {
		t.Fatalf("got %v", g)
	}
	if got[0].Score < 0.99 || got[0].Metadata["type"] != "person" || len(got[0].Values) != 3 || got[0].Values[0] != 1 {
		t.Fatalf("got %+v", got[0])
	}

	// Datasets are isolated.
	got, _ = s.Query(ctx, "other", []float32{1, 0, 0}, QueryOptions{})
	if len(got) != 1 || got[0].ID != "x" || got[0].Score > 0.01 {
		t.Fatalf("got %+v", got)
	}

	// Filter.
	got, err = s.Query(ctx, "main", []float32{0, 1, 0}, QueryOptions{TopK: 5, Filter: map[string]any{"type": map[string]any{"$eq": "person"}}})
	if err != nil {
		t.Fatal(err)
	}
	if g := ids(got); len(g) != 2 || g[0] != "xy" || g[1] != "x" {
		t.Fatalf("got %v", g)
	}
	if _, err := s.Query(ctx, "main", []float32{0, 1, 0}, QueryOptions{Filter: map[string]any{"n": map[string]any{"$gt": 1}}}); err == nil {
		t.Error("expected error for unsupported operator")
	}
}

func TestChromemUpsertReplaces(t *testing.T) {
	ctx := context.Background()
	s := newTestChromem(t)
	s.Upsert(ctx, "n", []Vector{{ID: "a", Values: []float32{1, 0, 0}, Metadata: map[string]any{"v": 1}}})
	if err := s.Upsert(ctx, "n", []Vector{{ID: "a", Values: []float32{0, 1, 0}, Metadata: map[string]any{"v": 2}}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Query(ctx, "n", []float32{0, 1, 0}, QueryOptions{})
	if len(got) != 1 || got[0].Score < 0.99 || got[0].Metadata["v"] != float64(2) {
		t.Fatalf("got %+v", got)
	}
}

func TestChromemDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestChromem(t)
	s.Upsert(ctx, "n", []Vector{{ID: "a", Values: []float32{1, 0, 0}}, {ID: "b", Values: []float32{0, 1, 0}}})
	s.Upsert(ctx, "m", []Vector{{ID: "a", Values: []float32{1, 0, 0}}})
	if err := s.Delete(ctx, "n", []string{"a", "nope"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Query(ctx, "n", []float32{1, 0, 0}, QueryOptions{})
	if g := ids(got); len(g) != 1 || g[0] != "b" {
		t.Fatalf("got %v", g)
	}
	if err := s.DeleteAll(ctx, "n"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Query(ctx, "n", []float32{1, 0, 0}, QueryOptions{}); len(got) != 0 {
		t.Fatalf("got %v", ids(got))
	}
	if got, _ = s.Query(ctx, "m", []float32{1, 0, 0}, QueryOptions{}); len(got) != 1 {
		t.Fatalf("other dataset affected: %v", ids(got))
	}
}

func TestChromemErrors(t *testing.T) {
	ctx := context.Background()
	s := newTestChromem(t)
	if err := s.Upsert(ctx, "n", []Vector{{ID: "a", Values: []float32{1}}}); err == nil {
		t.Error("expected dimension error")
	}
	if err := s.Upsert(ctx, "n", []Vector{{Values: []float32{1, 0, 0}}}); err == nil {
		t.Error("expected empty ID error")
	}
	if _, err := s.Query(ctx, "n", []float32{1}, QueryOptions{}); err == nil {
		t.Error("expected dimension error")
	}
}

func TestChromemPersistsAndChecksDimensions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vectors")
	s, err := NewChromem(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	s.Upsert(ctx, "n", []Vector{{ID: "a", Values: []float32{1, 0, 0}}})
	s.Close()

	if _, err := NewChromem(path, 4); err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	s, err = NewChromem(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, _ := s.Query(ctx, "n", []float32{1, 0, 0}, QueryOptions{}); len(got) != 1 {
		t.Fatalf("got %v", ids(got))
	}
}

func TestChromemStats(t *testing.T) {
	ctx := context.Background()
	s := newTestChromem(t)
	st, err := s.Stats(ctx, "main")
	if err != nil || st.Count != 0 || st.Dimensions != 3 || st.Backend != "chromem" {
		t.Fatalf("empty: %+v %v", st, err)
	}
	if err := s.Upsert(ctx, "main", []Vector{{ID: "a", Values: []float32{1, 0, 0}}, {ID: "b", Values: []float32{0, 1, 0}}}); err != nil {
		t.Fatal(err)
	}
	if st, err = s.Stats(ctx, "main"); err != nil || st.Count != 2 || st.Bytes != 24 {
		t.Fatalf("stats: %+v %v", st, err)
	}
	if st, _ = s.Stats(ctx, "other"); st.Count != 0 {
		t.Fatalf("other: %+v", st)
	}
}
