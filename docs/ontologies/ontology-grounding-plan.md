# Plan: ontology graph grounding with a YAML/JSON ontology DSL

## Description
Design plan (not implemented yet, reviewed and decided on 2026-11-19) for grounding `remember` graphs against user-editable ontologies. The ontology is a declarative YAML/JSON DSL (typed property graph: classes, relations, properties), not OWL. It is compiled at runtime into (1) a JSON Schema for the LLM's structured extraction output and (2) an in-memory Go registry with validators and lookup maps, swapped atomically when the user saves a change.

Companion todo (checklist of what to build): `g_todo/gligabao/ontology_graph_grounding.md`.

## Decisions
- OWL is removed entirely (parser `memory_ontology` RDF/XML, tables `gb_onto_classes` / `gb_onto_edges`, OWL docs, `build/ontology/market.owl`; the market ontology is converted once to the new YAML format). No JSON-LD, no individuals.
- Default mode is `annotate`; `strict` drops non-conforming nodes/edges.
- `remember` may name several ontologies; they are merged into one registry for the call.
- Query-time subclass expansion (`type IN descendants`) and the background re-validation worker are out of scope for now (a later phase).
- Grounding is off by default: it only runs when `ontology_grounding` is `true`.

## Where things stand (existing code)
- `internal/memory/memory_ontology`: RDF/XML parser, classes + edges only; `internal/db/graph/{postgres,sqlite}/db_ontology.go`; `/v1/gb/ontologies` API (POST/PUT/GET); `docs/ontologies/ontologies.md` ("remember and query do not use them yet"). All of it is OWL-shaped (no properties, temporal flag, versions) and gets replaced.
- Extraction: `memory_remember.Extract` (stage `StageExtractGraph`) gets entities/edges from the LLM with free-form types. Pipeline stages in `memory_remember/remember_pipeline.go` (extract, reconcile, load candidates, resolve edges, store, ...).
- Closest precedent for a request flag wired through the pipeline: `classify_graph` (`memory_core.WithClassifyGraph`, table `gb_artifact_classes`).
- `go.mod` has no YAML lib and no `santhosh-tekuri/jsonschema/v6`: both must be added.
- No `response_format` / JSON schema use in the LLM client was found: check whether it supports structured output before step 2; otherwise fall back to describing the ontology in the prompt and rely on validation.

## Plan

### 1. Package `internal/memory/memory_ontology` (rewritten around the Spec)
- Structs at the top of the file: `PropertyDef` (type, description, enum, pattern, required), `ClassDef` (subClassOf, description, properties, aliases), `RelationDef` (description, domain, range, temporal, properties, aliases), `Spec` (id, version, classes, relations).
- `Parse(format, bytes) (Spec, error)`: YAML or JSON.
- `Validate(Spec) []Problem` checks the ontology itself: `subClassOf` cycles, unknown parents, unknown domain/range classes, unknown property types, enum/pattern validity (regex compiles), alias collisions, conflicting property redefinition in a subclass.
- `Compile(Spec) (*CompiledRegistry, error)`, a pure function: `Ancestors`, `Descendants`, class and edge property validators, `ExtractionJSON`.
- Methods: `IsSubClassOf`, `Resolve` (alias to canonical name), `Expand` (class to class plus descendants).
- Merging several ontologies: union of classes/relations; a name defined in two ontologies with different definitions is an error (400) naming both.

### 2. Extraction schema (`ExtractionJSON`)
- JSON Schema Draft 2020-12, `anyOf` branch per class (`type` const, merged ancestor properties, `additionalProperties: false`) and per relation (`predicate` const, edge properties, `valid_from` / `valid_to` when `temporal`).
- Used to constrain the LLM output in `strict` mode (see 3).

### 3. Remember pipeline integration
- Request fields:
  - `ontology_grounding` (bool, default `false`).
  - `ontologies` (list of names; if omitted, all of the user's ontologies).
  - `ontology_grounding_mode` (`annotate` default, `strict`).
  - 400 if `ontologies` or the mode is set without `ontology_grounding: true`, for an unknown ontology name or mode, for conflicting definitions across the selected ontologies, and for an image remember.
- A new grounding stage between extract and reconcile/store (nothing runs after store).
- `annotate`: extraction is not schema-constrained. Nodes/edges whose type/relation resolves in the registry (directly or via alias) are marked ontology valid and tagged with `ontology_version`. Violations are kept, flagged and recorded as events. Nothing is dropped.
- `strict`: extraction is schema-constrained; any remaining violation (unknown type or relation, wrong domain/range, property failing validation, missing required) is dropped before the store stage.
- Domain/range check: `IsSubClassOf(source.Type, rel.Domain)` and `IsSubClassOf(target.Type, rel.Range)`; property check with the compiled validators.

### 4. Events table `gb_ontology_events` (Postgres and SQLite schemas)
- Purpose: debugging ontologies; shows which nodes/edges of new graphs matched.
- Columns: id, dataset_id, artifact_id (cascade), run_id, ontology_id, ontology_version, mode, subject_kind (node|edge), name, type/relation, outcome (`matched`, `matched_via_alias`, `unmatched`, `violation`, `dropped`), reason code, detail JSON (failing property, validator message), entity_id / edge_id (nullable), created_at.
- Reason codes: `unknown_class`, `unknown_relation`, `domain_violation`, `range_violation`, `property_invalid`, `missing_required`.
- Indexes on (artifact_id), (ontology_id), (dataset_id, created_at).
- Read endpoint `GET /v1/gb/ontologies/{name}/events?artifact_id=&outcome=&limit=`.

### 5. Explicit summary structs
```go
type GroundingSummary struct{ Mode string; Ontologies []OntologyRef; Nodes, Edges Counts }
type Counts struct{ Total, Matched, ViaAlias, Violations, Dropped int }
type AnnotateSummary struct{ GroundingSummary; MatchedPercent float64; MatchedClasses []ClassCount; Violations []ReasonCount }
type StrictSummary struct{ GroundingSummary; DroppedRatio float64; Violations []ReasonCount }
```
- `remember` response carries `ontology_grounding: {...}` with the struct for the mode.
- Document the strict `DroppedRatio` as a signal that the ontology does not cover the corpus vocabulary.

### 6. Storage and runtime
- `gb_ontologies` stores the spec (JSONB in Postgres, TEXT in SQLite), version and metadata; a save bumps the version, history kept in `gb_ontology_versions`.
- `Store` with `atomic.Pointer` per (user, ontology) holds the compiled registry; swapped on save, loaded lazily from the DB.
- `gb_entities` and `gb_edges` get nullable `ontology_version`, `ontology_class` (relation for edges) and an ontology-valid flag.

### 7. API
- `POST/PUT/GET /v1/gb/ontologies` take a Spec as YAML or JSON; `PUT` bumps the version; invalid specs return a 400 with the list of problems; `format: rdfxml` is gone.

### 8. Housekeeping (gligabao/AGENTS.md)
- Functional style, structs at the top of files, split arguments, `t_*_test.go` test names, `err`-named errors.
- Minor version bump, `CHANGELOG.md` entry (breaking: OWL removed, ontologies API/schema changed), `./run_tests.sh`.
- Docs: rewrite `docs/ontologies/ontologies.md`; update `docs/remember/remember.md`, `docs/graph.md`; then the `update gligabao docs` flow for `gligabao_docs` and the site.
- Python client param `ontology_grounding=` (and `ontologies=`, mode).
- Tests: spec validation (cycles, bad refs), compile (ancestor merge, extraction schema), both modes on fixed extractions, event rows, API validation, alias resolution, multi-ontology merge conflicts, hot-swap concurrency under `-race`, DB tests on both backends.

## Phase 2 (out of scope for now)
- Query-time subclass expansion with `Descendants`.
- Background re-validation worker on a major version bump.
