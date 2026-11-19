# Extracted graph enrichment with the ontology

## Description
With `ontology_grounding: true`, `remember` does two things with the graph the LLM extracts. It **tags** every entity and edge with what it matched (see [ontologies.md](ontologies.md#grounding-in-remember)), and it **enriches** the graph: the immediate ontology neighborhood of what matched is written into it, as entities and edges. This page is about the second part. The tags are never replaced by it, and the two work in both modes (`annotate` and `strict`).

The point: a graph of facts about Rex and Pip ends up connected to the concepts (`Dog`, `Animal`) the ontology defines, so that the hierarchy and the relations of the ontology can be followed in the graph itself.

## What is added
Let M be the classes that the entities of the extraction matched, with no violation (a match through an alias counts). The neighborhood is:

| What | Added as |
| --- | --- |
| each class of M, and each ancestor of those classes | an entity named as the class, type `ontology_class`, tagged valid, with `origin_type` `ontology` (`ontology_class` = the class, `ontology_version`, `ontology_valid` = true, `origin_type` = `ontology`) |
| each entity that matched a class | an edge `entity --is_a--> class` |
| each class of the subgraph and each of its direct parents | an edge `class --subclass_of--> parent` |
| each relation whose domain and range both have a class in the subgraph | an edge `domain class --relation--> range class`, for every pair (domain class, range class) in the subgraph |

Example, with an ontology where `Dog` and `Puppy` are kinds of `Animal`/`Dog`, and `Keeper keeps Animal`. The text "Sam keeps Rex" extracts `Sam (Keeper)`, `Rex (Dog)` and the edge `Sam keeps Rex`. The graph stored is that, plus:

```
entities: Keeper, Dog, Animal                       (type ontology_class)
Sam    --is_a-->        Keeper
Rex    --is_a-->        Dog
Dog    --subclass_of--> Animal
Keeper --keeps-->       Animal
```

## What is not added
- **The rest of the ontology.** Only the classes that were matched in the call, and their ancestors, are brought in. Classes nobody mentioned, and relations with an end outside the subgraph, are not.
- **Relations without fixed ends.** A relation with no `domain` or no `range` is skipped.
- **Inferred facts.** There is no reasoning: no transitive closure (an entity of `Puppy` gets `is_a Puppy`, not also `is_a Dog` and `is_a Animal`; follow `subclass_of` for that), and no inverse or inferred relations.
- **Write-backs.** Nothing is written to the stored ontology; the call only writes to the graph.
- **Entities that did not match.** An entity with an unknown class or a violation is kept as in the grounding (tagged, or dropped in strict mode), but brings no neighborhood.

## How the added rows behave
- **Nodes** are tagged like the matched ones. They are ordinary entities (embedded, found by `query`), named after the class. An extracted entity with the same name as a class is the same entity (entities are unique by normalized name in a dataset).
- **Edges** are tagged too: `origin_type` = `ontology`, `ontology_valid` = true, `ontology_class` = the relation (`is_a`, `subclass_of`, or the ontology relation) and `ontology_version` of the ontology that defines it. Their description is the synthesized statement (`Rex is a Dog.`), and `valid_at` is the reference time of the artifact, like any fact the text does not date.
- **No duplicates.** An entity or an edge that the extraction already has (same name; same source, relation and target) is not added again. The edges then go through the usual reconciliation, so the same neighborhood from a later `remember` is a confirmation of the stored edges, not a new row.
- **Strict mode.** The neighborhood is built from what was kept, so it never points to a dropped node.
- **Origin.** `origin_type` on `gb_entities` and `gb_edges` says where a row comes from: `extracted` (the LLM made it; the tags of the grounding) or `ontology` (added by the enrichment); `NULL` for rows never grounded. `WHERE origin_type = 'ontology'` isolates the enrichment, which can be filtered out of a query or deleted without touching the extracted facts. For an entity shared between artifacts, the origin of the latest grounded mention is kept.
- **Not counted as grounding.** The summary in the response and `gb_ontology_events` describe what the extraction made; the added entities and edges are not items of either. They show in `res.entities` and `res.edges`, and in the graph.

## Where it is in the code
- `internal/memory/memory_ontology/ontospec/neighborhood.go`: `CompiledOntology.Neighborhood(classes)`, a pure function that returns the classes (`ClassNode`) and the edges (`ClassEdge`) of the subgraph, sorted. Tests: `t_neighborhood_test.go`.
- `internal/memory/memory_remember/remember_ontology.go`: `addNeighborhood` turns it into entities and edges. It runs in the `ground_graph` stage, right after the grounding is applied, so that the reconcile and store stages treat the new rows as part of the extraction. The constants `TypeOntologyClass` (`ontology_class`) and `RelationIsA` (`is_a`) are there too; the hierarchy relation is `ontospec.RelationSubClassOf` (`subclass_of`).
