package memory_remember

// Ontology grounding (see docs/ontologies/ontologies.md): with the ontology_grounding flag the entities and edges the LLM extracts are
// checked against the user's ontologies, merged into one compiled ontology for the call (ontospec.CompiledOntology).
//
//   - The extraction prompt describes the ontology. In annotate mode it is a guide (the LLM may use other types when none fits); in strict
//     mode the LLM is told the output must conform to the JSON Schema of the ontology (CompiledOntology.ExtractionJSON). The LLM client has no
//     structured output (response_format), so the schema goes into the prompt and the result is validated afterwards, which is the
//     guarantee in both modes.
//   - The ground_graph stage (it makes no run log output: gb_ontology_events has what it decided) runs between the extraction of a text (or chunk) and its reconcile and store stages. It tags every
//     entity and edge with what it matched, and in strict mode drops those that do not conform.
//   - The ground_graph stage also adds the immediate ontology neighborhood of what matched (addNeighborhood): entities for the matched
//     classes and their ancestors, is_a and subclass_of edges, and the ontology relations between those classes.
//   - The store stage writes the tags on the rows and one event per grounded item in gb_ontology_events, in the transaction that stores
//     the artifact (the artifact does not exist before, and nothing runs after the store).
//   - The call returns a summary (ontospec.AnnotateSummary or ontospec.StrictSummary) in Result.OntologyGrounding.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology/ontospec"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_temporal"
)

// groundCall is the grounding of one Remember call: the compiled ontology and the mode.
type groundCall struct {
	reg  *ontospec.CompiledOntology
	mode string
}

// grounding is what the ground_graph stage made of one extraction; store writes it as events.
type grounding struct {
	mode  string
	refs  []ontospec.Ref
	runID int64
	items []ontospec.Item
	links []ontospec.Link // the edges that were grounded, as the extraction made them (an edge item's Index is in it)
}

func (g *groundCall) strict() bool { return g.mode == ontospec.ModeStrict }

// ontologyPrompt is what is added to the extraction prompt under an ontology.
func (g *groundCall) ontologyPrompt() string {
	var b strings.Builder
	b.WriteString("\n\nThe graph is grounded in this ontology:\n<ontology>\n")
	b.WriteString(g.reg.Prompt())
	b.WriteString("</ontology>\n\nOntology rules:\n")
	b.WriteString("- Give every entity a \"type\" that is one of the classes above, written exactly as in the ontology (the most specific class that fits), and every edge a \"relation\" that is one of the relations above, written exactly as in the ontology.\n")
	b.WriteString("- An edge's source and target must be entities whose classes are the domain and the range of its relation, or subclasses of them.\n")
	b.WriteString("- An entity or an edge may have a \"properties\" object with the properties its class or relation declares, with values of the declared types. Leave out a property the text does not state; give every required one.\n")
	if g.strict() {
		schema, _ := json.Marshal(g.reg.ExtractionJSON())
		b.WriteString("- Strict: leave out every entity and edge that fits no class or relation of the ontology. The answer MUST validate against this JSON Schema; the type, relation and properties of everything in it are checked, and what does not conform is dropped.\n<json_schema>\n")
		b.Write(schema)
		b.WriteString("\n</json_schema>")
	} else {
		b.WriteString("- Use these types and relations whenever one fits. If none fits an entity or a fact you still want to extract, use your own type or relation.")
	}
	return b.String()
}

//---------------------------------------------------------------------------------------------
// The ground_graph stage

func (j *job) hasGrounding() bool { return j.ground != nil }

// groundStage is the ground_graph stage, for the current item: it grounds the entities and edges of the extraction against the
// ontologies, tags them and, in strict mode, drops those that do not conform. It never fails the call.
func (j *job) groundStage(_ context.Context) error {
	ex := j.exs[j.i]
	if ex == nil {
		return nil
	}
	links := groundLinks(ex.Edges)
	items := j.ground.reg.Ground(j.ground.mode,
		groundNodes(ex.Entities),
		links)
	g := &grounding{
		mode:  j.ground.mode,
		refs:  j.ground.reg.Refs,
		runID: j.run.ID(),
		items: items,
		links: links,
	}

	//------------------
	// GROUNDING

	ex.Entities, ex.Edges = applyGrounding(ex,
		items,
		g.refs)

	// The neighborhood in the ontology of what matched is added to the graph.
	ex.Entities, ex.Edges = addNeighborhood(ex.Entities,
		ex.Edges,
		items,
		j.ground.reg,
		j.ref)

	//------------------

	ex.grounding = g
	j.groundItems = append(j.groundItems, items...)
	summary := ontospec.Summarize(j.ground.mode,
		g.refs,
		items)

	//------------------
	// TRACE
	j.tr.Tool("The entities and edges were grounded against the ontologies; each was tagged with what it matched"+
		map[bool]string{true: ", and the ones that do not conform were dropped.", false: ", and the ones that do not conform were flagged."}[j.ground.strict()],
		"ground_graph",
		map[string]any{"mode": j.ground.mode, "ontologies": g.refs},
		map[string]any{"summary": summary, "items": items})
	
	//------------------
	
	return nil
}

func groundNodes(es []Entity) []ontospec.Node {
	out := make([]ontospec.Node, len(es))
	for i, e := range es {
		out[i] = ontospec.Node{
			Name:       e.Name,
			Type:       e.Type,
			Properties: e.Properties,
		}
	}
	return out
}

func groundLinks(es []Edge) []ontospec.Link {
	out := make([]ontospec.Link, len(es))
	for i, e := range es {
		out[i] = ontospec.Link{
			Source:     e.Source,
			Target:     e.Target,
			Relation:   e.Relation,
			Properties: e.Properties,
		}
	}
	return out
}

//---------------------------------------------------------------------------------------------

// applyGrounding returns the entities and edges of ex tagged with the items (in strict mode only those that were kept).
func applyGrounding(ex *Extraction,
	items []ontospec.Item,
	refs []ontospec.Ref) ([]Entity, []Edge) {
	nodeItems := items[:len(ex.Entities)]
	edgeItems := items[len(ex.Entities):]
	entities := make([]Entity, 0, len(ex.Entities))
	for i, e := range ex.Entities {
		if !nodeItems[i].Kept() {
			continue
		}
		e.Ontology = tag(nodeItems[i], refs)
		entities = append(entities, e)
	}
	edges := make([]Edge, 0, len(ex.Edges))
	for i, e := range ex.Edges {
		if !edgeItems[i].Kept() {
			continue
		}
		e.Ontology = tag(edgeItems[i], refs)
		edges = append(edges, e)
	}
	return entities, edges
}

//---------------------------------------------------------------------------------------------
// The neighborhood

// Names of what the neighborhood adds to the graph.
const (
	// TypeOntologyClass is the type of the entities that stand for a class of the ontology.
	TypeOntologyClass = "ontology_class"
	// RelationIsA links an entity that matched a class to the entity of that class.
	RelationIsA = "is_a"
)

// addNeighborhood returns the entities and edges with the immediate ontology neighborhood of the nodes that matched added: an entity for
// each matched class and each of its ancestors (type TypeOntologyClass, tagged valid), an is_a edge from each matched entity to its
// class, subclass_of edges up the hierarchy, and the relations of the ontology that run between two of those classes. Only classes
// that were matched are brought in, never the rest of the ontology. Every added entity and edge is tagged valid
// with origin OriginOntology (the ones the LLM extracted get OriginExtracted); what is already in the extraction (same entity name, same edge) is not added again. The items are not changed, so the summary and
// the events keep describing the extraction alone.
func addNeighborhood(entities []Entity,
	edges []Edge,
	items []ontospec.Item,
	reg *ontospec.CompiledOntology,
	ref time.Time) ([]Entity, []Edge) {
	matched := filterItems(items, func(it ontospec.Item) bool { return it.Kind == ontospec.KindNode && it.Valid() })
	classes := mapItems(matched, func(it ontospec.Item) string { return it.Class })
	hood := reg.Neighborhood(classes)

	have := map[string]bool{}
	for _, e := range entities {
		have[memory_core.Key(e.Name)] = true
	}
	haveEdge := map[string]bool{}
	for _, e := range edges {
		haveEdge[edgeKey(e.Source, e.Relation, e.Target)] = true
	}
	// The entities in the extraction that matched, by class: they get an is_a edge to the class.
	isA := mapItems(matched, func(it ontospec.Item) ClassLink {
		return ClassLink{
			Entity: it.Name,
			Class:  it.Class,
		}
	})

	outEntities := append([]Entity(nil), entities...)
	for _, c := range hood.Classes {
		if have[memory_core.Key(c.Name)] {
			continue
		}
		have[memory_core.Key(c.Name)] = true
		outEntities = append(outEntities, Entity{
			Name: c.Name,
			Type: TypeOntologyClass,
			Ontology: &graph.Grounding{
				Class:   c.Name,
				Version: c.Ref.Version,
				Valid:   true,
				OriginType:  graph.OriginOntology,
			},
		})
	}
	outEdges := append([]Edge(nil), edges...)
	add := func(source,
		relation,
		target string,
		version int) {
		relation = normRelation(relation)
		if k := edgeKey(source, relation, target); !haveEdge[k] {
			haveEdge[k] = true
			e := Edge{
				Source:      source,
				Relation:    relation,
				Target:      target,
				Description: memory_core.SynthStatement(source, relation, target),
				Ontology: &graph.Grounding{
					Class:   relation,
					Version: version,
					Valid:   true,
					OriginType:  graph.OriginOntology,
				},
			}
			memory_temporal.ApplyReference(&e, ref)
			outEdges = append(outEdges, e)
		}
	}
	for _, l := range isA {
		add(l.Entity,
			RelationIsA,
			l.Class,
			reg.ClassRef(l.Class).Version)
	}
	for _, e := range hood.Edges {
		add(e.Source,
			e.Relation,
			e.Target,
			e.Ref.Version)
	}
	return outEntities, outEdges
}

// ClassLink says an entity of the extraction matched a class.
type ClassLink struct {
	Entity string
	Class  string
}

func filterItems(items []ontospec.Item,
	keep func(ontospec.Item) bool) []ontospec.Item {
	var out []ontospec.Item
	for _, it := range items {
		if keep(it) {
			out = append(out, it)
		}
	}
	return out
}

func mapItems[R any](items []ontospec.Item,
	f func(ontospec.Item) R) []R {
	out := make([]R, len(items))
	for i, it := range items {
		out[i] = f(it)
	}
	return out
}

//---------------------------------------------------------------------------------------------

// tag is the grounding stored on the row of an item.
func tag(it ontospec.Item,
	refs []ontospec.Ref) *graph.Grounding {
	version := it.Ref.Version
	if it.Ref.ID == 0 && len(refs) == 1 {
		version = refs[0].Version // an item that did not resolve: the version it was grounded against
	}
	return &graph.Grounding{
		Class:   it.Class,
		Version: version,
		Valid:   it.Valid(),
		OriginType:  graph.OriginExtracted,
	}
}

//---------------------------------------------------------------------------------------------
// The events

// events returns the rows of gb_ontology_events of the grounding of an artifact: one per item, or, for an item that resolved to nothing
// (so that no ontology is the one that defines it), one per ontology of the call. entityIDs are the stored entities by normalized name
// and edges the edges the artifact stated, in their stored state.
func (g *grounding) events(dsID,
	artifactID int64,
	entityIDs map[string]int64,
	edges []Edge) []graph.OntologyEvent {
	edgeIDs := map[string]int64{}
	for _, e := range edges {
		edgeIDs[edgeKey(e.Source, e.Relation, e.Target)] = e.ID
	}
	var out []graph.OntologyEvent
	for _, it := range g.items {
		var entityID, edgeID int64
		if it.Kind == ontospec.KindNode {
			entityID = entityIDs[memory_core.Key(it.Name)]
		} else if it.Kept() {
			l := g.links[it.Index]
			edgeID = edgeIDs[edgeKey(l.Source, l.Relation, l.Target)]
		}
		detail, _ := json.Marshal(it.Detail)
		if it.Detail == nil {
			detail = []byte("{}")
		}
		refs := []ontospec.Ref{it.Ref}
		if it.Ref.ID == 0 {
			refs = g.refs
		}
		for _, ref := range refs {
			out = append(out, graph.OntologyEvent{
				DatasetID:       dsID,
				ArtifactID:      artifactID,
				RunID:           g.runID,
				OntologyID:      ref.ID,
				OntologyVersion: ref.Version,
				Mode:            g.mode,
				SubjectKind:     it.Kind,
				Name:            it.Name,
				Type:            it.Type,
				Class:           it.Class,
				Outcome:         it.Outcome,
				Reason:          it.Reason,
				Detail:          detail,
				EntityID:        entityID,
				EdgeID:          edgeID,
			})
		}
	}
	return out
}

func edgeKey(source,
	relation,
	target string) string {
	return memory_core.Key(source) + "\x00" + relation + "\x00" + memory_core.Key(target)
}
