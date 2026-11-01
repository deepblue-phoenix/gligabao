package admin

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
)

// useCount is how many times an entity or an edge went into the context of an LLM call (see graph.InContextCount), per pipeline.
type useCount struct {
	Query    int `json:"query"`
	Remember int `json:"remember"`
}

// inContext holds the use counts of the entities and edges of a graph, by ID. An item that never went into a context is absent
// (its counts are zero).
type inContext struct {
	Entities map[int64]useCount `json:"entities"`
	Edges    map[int64]useCount `json:"edges"`
}

// graphView is the JSON of the admin graph endpoints: the graph (see memory.GraphExport) and how often its entities and edges were in
// an LLM context, which the admin UI shows on the nodes and edges and in its in-context histograms.
type graphView struct {
	*memory.GraphExport
	InContext inContext `json:"in_context"`
}

// withInContext returns g with the in-context counts of its dataset (gb_graph_in_ctx_counters).
func withInContext(ctx context.Context, d *sql.DB, g *memory.GraphExport) (*graphView, error) {
	counts, err := graph.InContextCounts(ctx, d, g.DatasetID)
	if err != nil {
		return nil, err
	}
	v := &graphView{GraphExport: g, InContext: inContext{Entities: map[int64]useCount{}, Edges: map[int64]useCount{}}}
	for _, c := range counts {
		switch c.Kind {
		case graph.CounterEntity:
			v.InContext.Entities[c.ItemID] = useCount{c.Query, c.Remember}
		case graph.CounterEdge:
			v.InContext.Edges[c.ItemID] = useCount{c.Query, c.Remember}
		}
	}
	return v, nil
}
