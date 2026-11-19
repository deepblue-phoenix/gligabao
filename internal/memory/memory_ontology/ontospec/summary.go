package ontospec

import (
	"sort"
)

// Counts is how many nodes or edges there were, and what became of them.
type Counts struct {
	Total      int `json:"total"`
	Matched    int `json:"matched"`    // matched by name
	ViaAlias   int `json:"via_alias"`  // matched by an alias
	Violations int `json:"violations"` // flagged (annotate)
	Dropped    int `json:"dropped"`    // removed (strict)
}

// ClassCount is how many items resolved to a class or a relation.
type ClassCount struct {
	Class string `json:"class"`
	Count int    `json:"count"`
}

// ReasonCount is how many items have a reason code.
type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// GroundingSummary is what both modes report.
type GroundingSummary struct {
	Mode       string `json:"mode"`
	Ontologies []Ref  `json:"ontologies"`
	Nodes      Counts `json:"nodes"`
	Edges      Counts `json:"edges"`
}

// AnnotateSummary is the summary of a call in annotate mode.
type AnnotateSummary struct {
	GroundingSummary
	// MatchedPercent is the share, of all nodes and edges, that matched (by name or alias), 0 to 100.
	MatchedPercent float64       `json:"matched_percent"`
	MatchedClasses []ClassCount  `json:"matched_classes"`
	Violations     []ReasonCount `json:"violations"`
}

// StrictSummary is the summary of a call in strict mode.
type StrictSummary struct {
	GroundingSummary
	// DroppedRatio is the share, of all nodes and edges the LLM extracted, that was dropped, 0 to 1. A high ratio is a signal that the
	// ontology does not cover the vocabulary of the corpus (or that the LLM does not follow it): see docs/ontologies/ontologies.md.
	DroppedRatio float64       `json:"dropped_ratio"`
	Violations   []ReasonCount `json:"violations"`
}

// Summarize builds the summary of the items of a call: an AnnotateSummary or a StrictSummary, by mode.
func Summarize(mode string,
	refs []Ref,
	items []Item) any {
	nodes, edges := filterKind(items, KindNode), filterKind(items, KindEdge)
	base := GroundingSummary{
		Mode:       mode,
		Ontologies: refs,
		Nodes:      count(nodes),
		Edges:      count(edges),
	}
	reasons := reasonCounts(items)
	total := base.Nodes.Total + base.Edges.Total
	if mode == ModeStrict {
		return StrictSummary{
			GroundingSummary: base,
			DroppedRatio:     ratio(base.Nodes.Dropped+base.Edges.Dropped, total),
			Violations:       reasons,
		}
	}
	return AnnotateSummary{
		GroundingSummary: base,
		MatchedPercent:   100 * ratio(base.Nodes.Matched+base.Nodes.ViaAlias+base.Edges.Matched+base.Edges.ViaAlias, total),
		MatchedClasses:   classCounts(items),
		Violations:       reasons,
	}
}

func filterKind(items []Item,
	kind string) []Item {
	var out []Item
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

func count(items []Item) Counts {
	c := Counts{Total: len(items)}
	for _, it := range items {
		switch it.Outcome {
		case OutcomeMatched:
			c.Matched++
		case OutcomeMatchedViaAlias:
			c.ViaAlias++
		case OutcomeDropped:
			c.Dropped++
		default:
			c.Violations++
		}
	}
	return c
}

func ratio(n,
	total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}

func reasonCounts(items []Item) []ReasonCount {
	counts := map[string]int{}
	for _, it := range items {
		if it.Reason != "" {
			counts[it.Reason]++
		}
	}
	out := make([]ReasonCount, 0, len(counts))
	for r, n := range counts {
		out = append(out, ReasonCount{
			Reason: r,
			Count:  n,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Count > out[j].Count || out[i].Count == out[j].Count && out[i].Reason < out[j].Reason
	})
	return out
}

func classCounts(items []Item) []ClassCount {
	counts := map[string]int{}
	for _, it := range items {
		if it.Valid() {
			counts[it.Class]++
		}
	}
	out := make([]ClassCount, 0, len(counts))
	for c, n := range counts {
		out = append(out, ClassCount{
			Class: c,
			Count: n,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Count > out[j].Count || out[i].Count == out[j].Count && out[i].Class < out[j].Class
	})
	return out
}
