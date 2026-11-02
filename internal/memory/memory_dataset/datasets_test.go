package memory_dataset

import "testing"

func TestParseVectorDataset(t *testing.T) {
	for _, idx := range VectorIndexes {
		for _, id := range []int64{0, 7, 1234567890123} {
			gotID, gotIdx, ok := ParseVectorDataset(VectorDataset(id, idx))
			if !ok || gotID != id || gotIdx != idx {
				t.Errorf("%s: got %d %q %v", VectorDataset(id, idx), gotID, gotIdx, ok)
			}
		}
	}
	for _, name := range []string{"", "main", "dataset-", "dataset-7", "dataset-7-", "dataset-x-entities", "dataset--1-entities", "dataset-07-entities", "dataset-7-nope", "dataset-7-entities-2", "Dataset-7-entities", "dataset-+7-edges"} {
		if id, idx, ok := ParseVectorDataset(name); ok {
			t.Errorf("%q parsed as %d %q", name, id, idx)
		}
	}
}
