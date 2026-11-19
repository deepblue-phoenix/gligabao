package ontospec

import (
	"fmt"
	"math"
	"regexp"
	"time"
)

// Grounding modes.
const (
	// ModeAnnotate keeps everything: nodes and edges that match the ontology are tagged, the others are flagged. Nothing is dropped.
	ModeAnnotate = "annotate"
	// ModeStrict drops the nodes and edges that do not conform.
	ModeStrict = "strict"
)

// Outcomes of an item.
const (
	OutcomeMatched         = "matched"
	OutcomeMatchedViaAlias = "matched_via_alias"
	OutcomeUnmatched       = "unmatched" // annotate: the type or relation is not in the ontology
	OutcomeViolation       = "violation" // annotate: it matched but breaks a rule
	OutcomeDropped         = "dropped"   // strict: removed
)

// Reason codes of an item that is not a clean match.
const (
	ReasonUnknownClass     = "unknown_class"
	ReasonUnknownRelation  = "unknown_relation"
	ReasonDomainViolation  = "domain_violation"
	ReasonRangeViolation   = "range_violation"
	ReasonPropertyInvalid  = "property_invalid"
	ReasonMissingRequired  = "missing_required"
	ReasonEndpointDropped  = "endpoint_dropped" // strict: an end of the edge was dropped
	ReasonEndpointUnlisted = "endpoint_unlisted"
)

// Item kinds.
const (
	KindNode = "node"
	KindEdge = "edge"
)

// Node is an entity of an extraction, as grounding sees it.
type Node struct {
	Name       string
	Type       string
	Properties map[string]any
}

// Link is an edge of an extraction, as grounding sees it.
type Link struct {
	Source, Target string
	Relation       string
	Properties     map[string]any
}

// Item is the grounding of one node or edge.
type Item struct {
	Kind  string `json:"subject_kind"` // KindNode or KindEdge
	Index int    `json:"index"`        // position in the nodes or in the links that were grounded
	Name  string `json:"name"`         // the node's name, or "source relation target"
	Type  string `json:"type"`         // the type or relation as the extraction wrote it
	// Class is the name of the class or relation it resolved to; "" if it did not resolve.
	Class   string         `json:"class,omitempty"`
	Ref     Ref            `json:"ontology"`
	Outcome string         `json:"outcome"`
	Reason  string         `json:"reason,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// Valid says the item matched the ontology with no violation.
func (i Item) Valid() bool { return i.Outcome == OutcomeMatched || i.Outcome == OutcomeMatchedViaAlias }

// Kept says the item stays in the graph.
func (i Item) Kept() bool { return i.Outcome != OutcomeDropped }

// Issue is one thing wrong with the properties of an item.
type Issue struct {
	Property string `json:"property"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
}

//---------------------------------------------------------------------------------------------

// Ground checks nodes and links, as an extraction made them, against the compiled ontology. It returns one Item per node, then one per link.
//
// A node is a match if its type resolves to a class (by name or alias) and its properties are those of the class, valid. A link is a
// match if its relation resolves, the class of its source is the domain or a subclass of it, the class of its target is the range or a
// subclass, and its properties are those of the relation, valid.
//
// In ModeAnnotate a node or link that is not a match is kept, with the outcome "unmatched" (unknown type or relation) or "violation".
// In ModeStrict it is "dropped": and so is a link with an end that was dropped or that the extraction does not list (the domain and
// range of such an end cannot be judged; in ModeAnnotate they are skipped).
func (r *CompiledOntology) Ground(mode string,
	nodes []Node,
	links []Link) []Item {
	
	strict := mode == ModeStrict
	nodeItems := make([]Item, len(nodes))
	byKey := map[string]int{} // Key(name) -> index of the first node of that name
	for i, n := range nodes {
		nodeItems[i] = r.groundNode(strict,
			i,
			n)
		if _, ok := byKey[Key(n.Name)]; !ok {
			byKey[Key(n.Name)] = i
		}
	}
	linkItems := make([]Item, len(links))
	for i, l := range links {
		linkItems[i] = r.groundLink(strict,
			i,
			l,
			nodes,
			nodeItems,
			byKey)
	}
	return append(nodeItems, linkItems...)
}

//---------------------------------------------------------------------------------------------

func (r *CompiledOntology) groundNode(strict bool,
	i int,
	n Node) Item {
	item := Item{
		Kind:  KindNode,
		Index: i,
		Name:  n.Name,
		Type:  n.Type,
	}
	class, viaAlias, ok := r.Resolve(n.Type)
	if !ok {
		return flagged(strict,
			item,
			true,
			ReasonUnknownClass,
			map[string]any{"type": n.Type})
	}
	item.Class, item.Ref = class, r.ClassRef(class)
	if issues := checkProps(r.classes[class].props, n.Properties); len(issues) > 0 {
		return flagged(strict,
			item,
			false,
			issues[0].Reason,
			map[string]any{"issues": issues})
	}
	return matched(item, viaAlias)
}

func (r *CompiledOntology) groundLink(strict bool,
	i int,
	l Link,
	nodes []Node,
	nodeItems []Item,
	byKey map[string]int) Item {
	item := Item{
		Kind:  KindEdge,
		Index: i,
		Name:  l.Source + " " + l.Relation + " " + l.Target,
		Type:  l.Relation,
	}
	rel, viaAlias, ok := r.ResolveRelation(l.Relation)
	if !ok {
		return flagged(strict,
			item,
			true,
			ReasonUnknownRelation,
			map[string]any{"relation": l.Relation})
	}
	info := r.relations[rel]
	item.Class, item.Ref = rel, r.RelationRef(rel)

	src, srcListed := byKey[Key(l.Source)]
	dst, dstListed := byKey[Key(l.Target)]
	switch {
	case strict && (!srcListed || !dstListed):
		return flagged(strict,
			item,
			false,
			ReasonEndpointUnlisted,
			map[string]any{"source_listed": srcListed, "target_listed": dstListed})
	case strict && (srcListed && !nodeItems[src].Kept() || dstListed && !nodeItems[dst].Kept()):
		return flagged(strict,
			item,
			false,
			ReasonEndpointDropped,
			nil)
	}
	if srcListed && !r.within(nodes[src].Type, info.domain) {
		return flagged(strict,
			item,
			false,
			ReasonDomainViolation,
			map[string]any{"source_type": nodes[src].Type, "domain": info.domain})
	}
	if dstListed && !r.within(nodes[dst].Type, info.rng) {
		return flagged(strict,
			item,
			false,
			ReasonRangeViolation,
			map[string]any{"target_type": nodes[dst].Type, "range": info.rng})
	}
	if issues := checkProps(info.props, l.Properties); len(issues) > 0 {
		return flagged(strict,
			item,
			false,
			issues[0].Reason,
			map[string]any{"issues": issues})
	}
	return matched(item, viaAlias)
}

// within says whether a type is one of the classes (or a subclass of one); no classes means any.
func (r *CompiledOntology) within(typ string,
	classes []string) bool {
	if len(classes) == 0 {
		return true
	}
	for _, c := range classes {
		if r.IsSubClassOf(typ, c) {
			return true
		}
	}
	return false
}

func matched(item Item,
	viaAlias bool) Item {
	item.Outcome = OutcomeMatched
	if viaAlias {
		item.Outcome = OutcomeMatchedViaAlias
	}
	return item
}

// flagged marks an item that is not a match: dropped in strict mode, else unmatched (the type or relation is unknown) or a violation.
func flagged(strict bool,
	item Item,
	unknown bool,
	reason string,
	detail map[string]any) Item {
	item.Reason, item.Detail = reason, detail
	switch {
	case strict:
		item.Outcome = OutcomeDropped
	case unknown:
		item.Outcome = OutcomeUnmatched
	default:
		item.Outcome = OutcomeViolation
	}
	return item
}

//---------------------------------------------------------------------------------------------

// checkProps checks the properties of a node or an edge against the definitions: every required one is there, no other is, and each
// has its type and satisfies its enum and pattern. Values are as encoding/json decodes them (string, float64, bool).
func checkProps(defs map[string]propInfo,
	values map[string]any) []Issue {
	var out []Issue
	for _, name := range sortedKeys(defs) {
		if v, ok := values[name]; (!ok || v == nil) && defs[name].def.Required {
			out = append(out, Issue{
				Property: name,
				Reason:   ReasonMissingRequired,
				Message:  "required property is missing",
			})
		}
	}
	for _, name := range sortedKeys(values) {
		info, ok := defs[name]
		switch {
		case !ok:
			out = append(out, Issue{
				Property: name,
				Reason:   ReasonPropertyInvalid,
				Message:  "not a property of this class or relation",
			})
		case values[name] == nil:
		default:
			if msg := checkValue(info, values[name]); msg != "" {
				out = append(out, Issue{
					Property: name,
					Reason:   ReasonPropertyInvalid,
					Message:  msg,
				})
			}
		}
	}
	return out
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// checkValue returns what is wrong with a value, or "" if it is valid.
func checkValue(info propInfo,
	v any) string {
	d := info.def
	switch d.Type {
	case TypeString:
		s, ok := v.(string)
		if !ok {
			return "must be a string"
		}
		if len(d.Enum) > 0 && !contains(d.Enum, s) {
			return fmt.Sprintf("%q is not one of the allowed values", s)
		}
		if info.re != nil && !info.re.MatchString(s) {
			return fmt.Sprintf("%q does not match the pattern %s", s, d.Pattern)
		}
	case TypeNumber:
		if _, ok := toFloat(v); !ok {
			return "must be a number"
		}
	case TypeInteger:
		f, ok := toFloat(v)
		if !ok || f != math.Trunc(f) {
			return "must be an integer"
		}
	case TypeBoolean:
		if _, ok := v.(bool); !ok {
			return "must be a boolean"
		}
	case TypeDate:
		s, ok := v.(string)
		if !ok || !isDate(s) {
			return "must be an ISO 8601 date or date-time"
		}
	}
	return ""
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func isDate(s string) bool {
	if dateRe.MatchString(s) {
		_, err := time.Parse("2006-01-02", s)
		return err == nil
	}
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}
