# Artifacts

An artifact is a piece of data ingested to be remembered: the raw text that `POST /v1/gb/remember` receives, stored as it came. It is the provenance of the graph: every entity and edge is anchored to the artifact(s) it was inferred from, so a fact can always be traced back to the original text.

Code: `internal/memory/memory_remember/remember.go` (`Remember`, `Store`, `ResolveKind`), `remember_chunks.go` (chunked texts), `remember_websearch.go` (web searches), `internal/db/graph/model/model.go` (types), `internal/db/graph/{sqlite,postgres}/db_schema.go` (tables).

Images are not artifacts in this sense: `remember` with an `image` stores an image artifact (a row in `gb_image_artifacts` tracking a file on disk) through a separate pipeline, and they are not linked to entities or edges yet (see [images.md](images.md)).

## Kinds

Every artifact has a `kind`, set by the optional `kind` field of the remember request:

| Kind | Meaning |
| ---- | ------- |
| `knowledge` (default) | Knowledge that is not strictly episodic: a reference text, a note, a description of how things are. Cognee-style memorization. |
| `episode` | A Graphiti-style episode: something written or observed at a point in time, such as a message or a news item. |

`kind` is trimmed and case-insensitive; an empty one means `knowledge`; any other value returns `400` and stores nothing.

Two more kinds exist but cannot be asked for: `web_search`, the result of a [web search](web-search.md) that `remember` made (with `web_search`) for an entity of another artifact, and `series_observation`, a text derived from a [timeseries](timeseries.md#observations) (with `timeseries.observations`). Both have a `query` and a parent (below).

The kind is stored and returned, but it does not change how the text is extracted or reconciled yet. Both kinds get the same bi-temporal treatment (see [temporal.md](temporal.md)). It exists so that behaviour can differ per kind later (for example, a `knowledge` artifact need not supersede older facts).

## Fields

Table `gb_artifacts`:

| Column | Meaning |
| ------ | ------- |
| `id` | The `artifact_id` returned by `remember` |
| `dataset_id` | The [dataset](datasets.md) it belongs to. Artifacts are never shared between datasets. |
| `kind` | `knowledge` or `episode` (`web_search` and `series_observation` are created only by the pipeline) |
| `text` | The raw input, unchanged |
| `session_id` | The [session](sessions.md) that remembered it: the request's `session_id`, NULL if it had none |
| `valid_at` | Event time: when the text was written or observed. This is the request's `reference_time` (default: now). Relative dates in the text are resolved against it, and facts without a date of their own inherit it. |
| `created_at` | System time: when it was ingested |

| `parent_id` | The artifact this one hangs off; NULL for an artifact that is nobody's child. Either the artifact it is a chunk of (see [Chunks](#chunks)), or, for a `web_search` artifact, the artifact the searched entity came from (see [Web searches](#web-searches)), or, for a `series_observation`, the artifact that anchors the series (see [Series observations](#series-observations)) |
| `chunk_index`, `start_offset`, `end_offset`, `tokens` | Only for a chunk: its position among its siblings (from 0), the byte range of its text in the parent's text, and its size in tokens |
| `query` | Only for a `web_search` artifact: the web search query that was issued for it; for a `series_observation`, the series and window it is of |
| `series_id` | Only for a `series_observation` artifact: the series it describes (`gb_series.id`; NULL if the series was deleted) |

Every `remember` call adds one artifact (plus, for a long text, its chunks), so sending the same text twice stores two artifacts. They add no duplicate entities or edges, but both are recorded as sources (below).

## Chunks

Artifacts can be the children of other artifacts. A text larger than one chunk (1024 tokens by default) is stored as an artifact holding the whole text, the **parent**, and is split into overlapping chunks that are artifacts too, its **children** (`parent_id` set; same `kind`, `session_id` and times; `text` is the chunk). Each chunk is extracted, stored and embedded individually, and the entities and edges link to the chunk they came from. A text that fits one chunk has no children. See [chunking.md](chunking.md).

## Web searches

With `web_search`, `remember` also searches the web for the latest world events about the key entities of the text, and stores each result as an artifact of its own, of kind `web_search`. It saves the **query** that was issued (`query`) and its **parent** (`parent_id`) is the artifact **from which the entity came**: the artifact the entity was extracted from, which for a long text is the chunk. A web search is therefore the second kind of child artifact; it is told apart from a chunk by its kind and by having no `chunk_index`. The entities and edges extracted from the result are linked to it like to any artifact (see below), so a fact learned on the web traces back to the query that found it and to the text whose entity was searched for. Everything is in [web-search.md](web-search.md).

## Series observations

With `timeseries.observations`, `remember` derives a text from the stats of each timeseries it stored (generated in Go, not by an LLM) and stores it as an artifact of its own, of kind `series_observation`: it records the **series** it describes (`series_id`) and a `query` saying which series and window, and its **parent** (`parent_id`) is the artifact that anchors the series. It is the third kind of child artifact and, like a web search, is not a chunk (no `chunk_index`). The entities and edges extracted from its text link to it like to any artifact, so a fact derived from a series traces back to the observation, the series, and the text it was anchored to. Everything is in [timeseries.md](timeseries.md#observations).

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

## Sessions

An artifact carries the `session_id` of the `remember` call that created it, which connects the artifacts of one user/agent run (and, through `recall`'s `session_id`, the recalls). The session is also recorded against the entities and edges the artifact yields, in `gb_entity_sessions` and `gb_edge_sessions`, including entities and facts that already existed, so a session that only repeats known facts still leaves a trace. Image artifacts carry it too (`gb_image_artifacts.session_id`). See [sessions.md](sessions.md).

## Where artifacts show up

- `POST /v1/gb/remember` returns the new `artifact_id`, its `kind`, `session_id` (if any), `valid_at` and `created_at` ([remember.md](remember.md)).
- Edges in the [graph export](graph.md) and in the recall `evidence` ([recall.md](recall.md)) carry `artifact_id` (the creating artifact).

## Limitations

- There is no endpoint to read, list or delete artifacts yet. The raw text can only be read from the database.
- `recall` does not use artifact text: it answers from the graph only.
- The `gb_edge_artifacts` and `gb_entity_artifacts` links, and the session links (`gb_entity_sessions`, `gb_edge_sessions`), are stored but not exposed by the API yet.
- Artifacts are not embedded, except chunks: `remember` embeds the entities and edges extracted from an artifact, and the text of each chunk of a long text (see [chunking.md](chunking.md) and [vectors.md](vectors.md)); the text of an artifact that is not chunked is not embedded.
