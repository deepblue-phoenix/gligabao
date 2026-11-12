package memory_temporal

import (
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

func TestMergeCandidates(t *testing.T) {
	byName := []graph.Edge{{
		ID: 9,
	}, {
		ID: 5,
	}, {
		ID: 2,
	}}
	bySimilar := []graph.Edge{{
		ID: 7,
	}, {
		ID: 5,
	}, {
		ID: 1,
	}}
	via := map[int64]string{}
	got := MergeCandidates(byName,
		bySimilar,
		5,
		via)
	var ids []int64
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	want := []int64{9, 7, 5, 2, 1} // newest first, edge 5 once
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v",
			ids,
			want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v",
				ids,
				want)
		}
	}
	if via[9] != ViaName || via[7] != ViaSimilar || via[5] != ViaBoth {
		t.Errorf("via = %v", via)
	}
	if got := MergeCandidates(byName,
		bySimilar,
		2,
		nil); len(got) != 2 || got[0].ID != 9 || got[1].ID != 7 {
		t.Errorf("capped = %v", got)
	}
}
