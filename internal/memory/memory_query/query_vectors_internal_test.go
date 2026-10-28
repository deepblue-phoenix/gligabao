package memory_query

import (
	"reflect"
	"testing"
)

func TestVectorRowID(t *testing.T) {
	for _, c := range []struct {
		id, kind string
		want     int64
		ok       bool
	}{
		{"entity-12", "entity", 12, true},
		{"edge-3", "edge", 3, true},
		{"chunk-7", "chunk", 7, true},
		{"summary-9", "summary", 9, true},
		{"edge-3", "entity", 0, false}, // another index's vector
		{"entity-x", "entity", 0, false},
		{"entity-", "entity", 0, false},
		{"", "entity", 0, false},
	} {
		if got, ok := vectorRowID(c.id, c.kind); got != c.want || ok != c.ok {
			t.Errorf("vectorRowID(%q, %q) = %d, %v; want %d, %v", c.id, c.kind, got, ok, c.want, c.ok)
		}
	}
}

func TestUnion(t *testing.T) {
	if got := union([]int64{3, 1}, []int64{1, 2, 3, 4}); !reflect.DeepEqual(got, []int64{3, 1, 2, 4}) {
		t.Errorf("union = %v", got)
	}
	if got := union(nil, nil); len(got) != 0 {
		t.Errorf("union of nothing = %v", got)
	}
}
