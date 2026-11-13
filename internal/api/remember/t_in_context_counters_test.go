package remember

import (
	"context"
	"database/sql"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// rememberEntityCounts returns the in-context events of the entities of the dataset for the remember pipeline, by entity name:
// how many LLM prompts of Remember listed each (entities with no count are not in the map).
func rememberEntityCounts(t *testing.T,
	d *sql.DB,
	dsID int64) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	ents, err := graph.ListEntities(ctx,
		d,
		dsID)
	if err != nil {
		t.Fatal(err)
	}
	names := map[int64]string{}
	for _, e := range ents {
		names[e.ID] = e.Name
	}
	list, err := graph.ListInContextEvents(ctx,
		d,
		dsID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, c := range list {
		if c.Kind == graph.CounterEntity && c.Pipeline == graph.PipelineRemember {
			out[names[c.ItemID]]++
		}
	}
	return out
}
