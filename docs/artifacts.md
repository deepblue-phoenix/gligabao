# Artifacts

An artifact is a piece of data ingested to be remembered: the raw text that `POST /v1/gb/remember` receives, stored as it came. It is the provenance of the graph: every entity and edge is anchored to the artifact(s) it was inferred from, so a fact can always be traced back to the original text.

Code: `internal/memory/remember.go` (`Remember`, `Store`, `ResolveKind`), `internal/db/graph/model/model.go` (types), `internal/db/graph/{sqlite,postgres}/db_schema.go` (tables).

Images are not artifacts in this sense: `remember` with an `image` stores an image artifact (a row in `gb_image_artifacts` tracking a file on disk) through a separate pipeline, and they are not linked to entities or edges yet (see [images.md](images.md)).

## Kinds

Every artifact has a `kind`, set by the optional `kind` field of the remember request:

| Kind | Meaning |
| ---- | ------- |
| `knowledge` (default) | Knowledge that is not strictly episodic: a reference text, a note, a description of how things are. Cognee-style memorization. |
| `episode` | A Graphiti-style episode: something written or observed at a point in time, such as a message or a news item. |

`kind` is trimmed and case-insensitive; an empty one means `knowledge`; any other value returns `400` and stores nothing.

The kind is stored and returned, but it does not change how the text is extracted or reconciled yet. Both kinds get the same bi-temporal treatment (see [temporal.md](temporal.md)). It exists so that behaviour can differ per kind later (for example, a `knowledge` artifact need not supersede older facts).

## Fields

Table `gb_artifacts`:

| Column | Meaning |
| ------ | ------- |
| `id` | The `artifact_id` returned by `remember` |
| `dataset_id` | The [dataset](datasets.md) it belongs to. Artifacts are never shared between datasets. |
| `kind` | `knowledge` or `episode` |
| `text` | The raw input, unchanged |
| `valid_at` | Event time: when the text was written or observed. This is the request's `reference_time` (default: now). Relative dates in the text are resolved against it, and facts without a date of their own inherit it. |
| `created_at` | System time: when it was ingested |

Every `remember` call adds one artifact, so sending the same text twice stores two artifacts. They add no duplicate entities or edges, but both are recorded as sources (below).

## Provenance links

Entities and edges point back to their artifacts in three ways:

| Link | Cardinality | Records |
| ---- | ----------- | ------- |
| `gb_edges.artifact_id` | an edge has one | The artifact that **created** the edge. Set to `NULL` if that artifact is deleted. |
| `gb_edge_artifacts` (`edge_id`, `artifact_id`) | many-to-many | The creating artifact **and every artifact whose duplicate of the fact was merged** into the edge. |
| `gb_entity_artifacts` (`entity_id`, `artifact_id`) | many-to-many | Every artifact that mentions the entity. |

Why link tables:

- **Entities** are deduplicated per dataset on (normalized name, type), so "Alice" mentioned in artifacts 1, 5 and 9 is one row. A single `artifact_id` column could only say where she first appeared. The link table also answers "which artifacts ground this entity?" and "which entities does this artifact contribute to?", and lets an artifact be removed without losing entities that other artifacts still support.
- **Edges** are created by one artifact, hence `artifact_id`. But when a later artifact states the same fact, the new edge is recognized as a duplicate (exactly, or by the LLM) and merged into the stored edge instead of being inserted. Without `gb_edge_artifacts` the fact that this artifact also supports it would be lost. The stored edge keeps its own `artifact_id`, `created_at` and `valid_at`.
- Contradicted edges are not linked to the artifact that contradicted them: a contradiction is not a restatement.

An entity is linked to an artifact whether it was created by it or already existed, and also when an edge names a stored entity the extraction did not list. Links are idempotent (primary key on the pair), and deleting either side removes only the link: deleting an entity cascades to its edges, and deleting an artifact sets `artifact_id` to `NULL` on its edges and removes its link rows.

Example. "Alice works at Acme" is remembered twice as artifacts 1 and 2:

| Table | Rows |
| ----- | ---- |
| `gb_edges` | one edge, `artifact_id = 1` |
| `gb_edge_artifacts` | (edge, 1), (edge, 2) |
| `gb_entity_artifacts` | (Alice, 1), (Alice, 2), (Acme, 1), (Acme, 2) |

## Where artifacts show up

- `POST /v1/gb/remember` returns the new `artifact_id`, its `kind`, `valid_at` and `created_at` ([remember.md](remember.md)).
- Edges in the [graph export](graph.md) and in the recall `evidence` ([recall.md](recall.md)) carry `artifact_id` (the creating artifact).

## Limitations

- There is no endpoint to read, list or delete artifacts yet. The raw text can only be read from the database.
- `recall` does not use artifact text: it answers from the graph only.
- The `gb_edge_artifacts` and `gb_entity_artifacts` links are stored but not exposed by the API yet.
- Artifacts are not embedded: `remember` embeds the entities and edges extracted from them (see [vectors.md](vectors.md)), not the artifact text.
- A text that is too long for one extraction call fails the whole request (`502`); artifacts are not chunked.
