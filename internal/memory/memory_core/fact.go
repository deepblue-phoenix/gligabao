package memory_core

import (
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

// Fact is the memory layer's view of an edge: one statement about the world ("Alice works at Acme.") together
// with the time it held and where it came from.
//
// A Fact is NOT a separate stored thing. The graph is still the source of truth: a fact is a row of gb_edges,
// graph.Edge / graph.EvidenceEdge stay the types the graph and the API use, and a Fact is built from them
// (FactFromEdge, FactFromEvidence) and can be turned back (Edge). It exists so the fact-level behaviour (its text,
// its validity, whether two facts are the same) lives in one place instead of being re-derived from edge fields.
type Fact struct {
	// EdgeID is the gb_edges row; 0 for a fact that is not stored yet (just extracted).
	EdgeID int64
	// Source, Relation, Target are the edge: Source --Relation--> Target. SourceID / TargetID are the entity rows,
	// known only when the fact was read as an EvidenceEdge (0 otherwise).
	Source, Relation, Target string
	SourceID, TargetID       int64
	// Statement is the fact as a sentence (the edge's description). It can be empty; see Text.
	Statement string
	// Event time: when the fact held in the real world. InvalidAt nil means it is still true.
	ValidAt, InvalidAt *time.Time
	// System time: when we learned the fact, and when newer knowledge superseded it.
	CreatedAt, ExpiredAt *time.Time
	// ArtifactID is the artifact that created the edge, if known (see gb_edge_artifacts for every source).
	ArtifactID *int64
}

// FactFromEdge views an edge (as extracted or loaded by name) as a fact.
func FactFromEdge(e graph.Edge) Fact {
	return Fact{
		EdgeID: e.ID, Source: e.Source, Relation: e.Relation, Target: e.Target, Statement: e.Description,
		ValidAt: e.ValidAt, InvalidAt: e.InvalidAt, CreatedAt: e.CreatedAt, ExpiredAt: e.ExpiredAt,
	}
}

// FactFromEvidence views an edge read for the graph context / evidence as a fact.
func FactFromEvidence(e graph.EvidenceEdge) Fact {
	return Fact{
		EdgeID: e.ID, Source: e.Source, Relation: e.Relation, Target: e.Target, SourceID: e.SourceID, TargetID: e.TargetID,
		Statement: e.Description, ValidAt: e.ValidAt, InvalidAt: e.InvalidAt, CreatedAt: e.CreatedAt, ExpiredAt: e.ExpiredAt,
		ArtifactID: e.ArtifactID,
	}
}

// FactsFromEdges views each edge as a fact, in order.
func FactsFromEdges(edges []graph.Edge) []Fact {
	out := make([]Fact, len(edges))
	for i, e := range edges {
		out[i] = FactFromEdge(e)
	}
	return out
}

// Edge is the fact as the graph edge it is stored as (without the entity IDs and the artifact).
func (f Fact) Edge() graph.Edge {
	return graph.Edge{
		ID: f.EdgeID, Source: f.Source, Target: f.Target, Relation: f.Relation, Description: f.Statement,
		ValidAt: f.ValidAt, InvalidAt: f.InvalidAt, CreatedAt: f.CreatedAt, ExpiredAt: f.ExpiredAt,
	}
}

// Text is the fact as a sentence: its statement, or one made from the edge when there is none.
func (f Fact) Text() string {
	if f.Statement != "" {
		return f.Statement
	}
	return SynthStatement(f.Source, f.Relation, f.Target)
}

// SynthStatement makes a sentence out of an edge: "Alice works_at Acme" -> "Alice works at Acme."
func SynthStatement(source, relation, target string) string {
	return source + " " + strings.ReplaceAll(relation, "_", " ") + " " + target + "."
}

// SameAs reports whether f and o state the same edge: same endpoints (by normalized name) and relation. Time and
// wording are not compared.
func (f Fact) SameAs(o Fact) bool {
	return Key(f.Source) == Key(o.Source) && Key(f.Target) == Key(o.Target) && f.Relation == o.Relation
}

// Ended reports whether the fact stopped being true in the real world (it has an InvalidAt).
func (f Fact) Ended() bool { return f.InvalidAt != nil }

// Superseded reports whether newer knowledge replaced the fact (it has an ExpiredAt).
func (f Fact) Superseded() bool { return f.ExpiredAt != nil }

// Span is the event-time validity as "(valid_at -> invalid_at)", with "?" for an unknown end.
func (f Fact) Span() string { return span(f.ValidAt, f.InvalidAt) }

// ContextLine is the fact as one line of the graph context given to the recall LLM.
func (f Fact) ContextLine() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[edge %d] %s(%d) --%s--> %s(%d)", f.EdgeID, f.Source, f.SourceID, f.Relation, f.Target, f.TargetID)
	if f.Statement != "" {
		sb.WriteString(": " + f.Statement)
	}
	sb.WriteString(" [valid_at=")
	if f.ValidAt != nil {
		sb.WriteString(relational.FormatTime(*f.ValidAt))
	} else {
		sb.WriteString("?")
	}
	if f.InvalidAt != nil {
		sb.WriteString(", invalid_at=" + relational.FormatTime(*f.InvalidAt))
	}
	if f.Superseded() {
		sb.WriteString(", superseded")
	}
	sb.WriteString("]")
	return sb.String()
}

// Summary is the fact as a map for trajectories (the keys are what the admin UI reads); via, if not empty, says
// how the fact was found.
func (f Fact) Summary(via string) map[string]any {
	m := map[string]any{"source": f.Source, "relation": f.Relation, "target": f.Target, "fact": f.Statement, "valid": f.Span()}
	if f.EdgeID != 0 {
		m["id"] = f.EdgeID
	}
	if via != "" {
		m["found_by"] = via
	}
	return m
}

func span(valid, invalid *time.Time) string {
	f := func(t *time.Time) string {
		if t == nil {
			return "?"
		}
		return relational.FormatTime(*t)
	}
	return "(" + f(valid) + " -> " + f(invalid) + ")"
}
