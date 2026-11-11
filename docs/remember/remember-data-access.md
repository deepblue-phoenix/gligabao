# Remember: reads and writes

What the [remember](remember.md) pipeline reads from and writes to the relational/graph database and the [vector store](../vectors.md), stage by stage. The stages are those of `rememberPipeline` (`internal/memory/memory_remember/remember_pipeline.go`, see [pipeline.md](../pipeline.md)).

**In short:** remember is mostly a writer, but it is not insert-only. Before it stores anything it **reads** the facts already stored (by name in the graph, and by similarity in the vector store) so that the LLM can decide which new facts repeat or contradict old ones. Its store step also looks up existing entities, so it upserts instead of duplicating. The query side of the pipeline is only used by `load_candidates`; every other read is a lookup that keeps the writes idempotent or avoids repeated work.

All reads and writes are scoped to one `(user, dataset)`: nothing is shared across datasets.

## Summary

| Stage | Reads | Writes |
|---|---|---|
| (setup, `Submit`) | none (names are validated in code) | `gb_remember_runs` row (status `started`) |
| `classify_complexity` | none | `gb_inferences` |
| `chunk_text` | none | run log output only |
| `extract_graph` | none | `gb_inferences` |
| `summarize_chunk` | none | `gb_inferences` |
| `store` (parent of a document) | none | `gb_datasets` (created if missing), `gb_artifacts` |
| `load_candidates` | **`gb_datasets`, `gb_edges`/`gb_entities` (live edges by name), vector store (entity index, similarity)** | `gb_inferences` (the embedding request) |
| `resolve_edges` | none (works on the candidates in memory) | `gb_inferences`, `gb_graph_in_ctx_counters` |
| `store` | **`gb_entities` (`FindEntityID`, `GetEntity`), `gb_edges` (`GetEdge`)** | relational: see [the store stage](#the-store-stage); vector store: upsert; `gb_inferences` |
| `store_series` | none | `gb_datasets` (created if missing), `gb_series`, `gb_series_anchors`, `gb_series_points` |
| `classify_graph` | `gb_artifact_classes` (known class names) | `gb_artifact_classes`, `gb_inferences` |
| `resolve_geo` | `gb_entities`, `gb_entity_geo` (skip places already resolved) | `gb_entity_geo` |
| `web_search` | none (the search is a web call) | `gb_inferences`, `gb_graph_in_ctx_counters`, then the reconcile stages below for its text |
| `fetch_series` | none from the database (providers are called over the network) | series tables as in `store_series`, `gb_inferences` |
| `observe_series` | `gb_series_points` (the points of the window) | then the reconcile stages below for its text |

Every stage also writes its status, times and inference IDs to the run log (`gb_remember_run_stages`) and, for debugging, outputs to `gb_remember_run_stage_outputs`; see [Run log](#run-log-writes).

`web_search` and `observe_series` make a text of their own and send it through the same `load_candidates`, `resolve_edges` and `store` stages as the text of the call (`job.reconcile`), so every read and write of those three applies to them too, with the artifact kinds `web_search` and `series_observation`.

## The reads

### `load_candidates`: the stored facts (the main read)

`loadCandidates` (`remember_vectors.go`) calls `memory_temporal.LoadCandidates`, which:

1. Resolves the dataset (`relational.DatasetID`). An unknown dataset gives no candidates, not an error.
2. **By name:** reads the live edges (`expired_at IS NULL`) of the dataset that touch an entity whose normalized name is one of the endpoints of the new edges (`graph.LoadCandidateEdges`), newest first, at most `memory_temporal.MaxCandidates` (50).
3. **By similarity** (only with vectors and an embedder configured, `findSimilarEntities`): embeds the endpoint names (type and description from the extraction, one batch request), queries the **vector store** with each embedding (`Vectors.Query` on `memory_dataset.EntityDataset(dsID)`, `similarEntityTopK` matches each), and reads the live edges of the entities found, with the same filter and cap. This finds aliases (`Alicia` for a stored `Alice Smith`).
4. Merges both lists, an edge found twice once, newest first, at most 50.

A failed similarity search (embedding or vector store) is not fatal: it is logged and the candidates come from the exact names only. A failed relational read fails the stage.

For a document, `load_candidates` runs for each chunk after the previous chunk was stored, so a chunk is compared with the facts of the chunks before it.

### `store`: lookups inside the write transaction

Inside the transaction of the `store` stage (`store`, `remember.go`):

- `tx.FindEntityID`: an edge endpoint that the extraction did not list is looked up among the stored entities of the dataset (any type); if found it is linked to the artifact, if not a new entity of type `other` is created.
- `tx.UpsertEntity` (a read-then-write on `(normalized name, type)`): an entity that exists is reused, not duplicated.
- `tx.GetEdge` (in `memory_temporal/store.go`): the stored edge that a new fact duplicates, or that it contradicts, is read before it is merged or invalidated.
- `tx.GetEntity` (`prepareVectors`): every entity to embed is read back as stored (name, type, description), so the vector is made from the stored text, not from the extraction.

### Other reads

- `classify_graph`: `graph.ClassNames` reads the class names the dataset already uses (only when the call has no fixed class list), so the LLM reuses them. A failure is logged and the LLM is simply not told of them.
- `resolve_geo`: `tx.FindEntity` and `tx.GetEntityGeo`, so a place that already has coordinates is not looked up again.
- `observe_series`: `graph.SeriesPoints` reads the points of a series in the window to write the observation text.
- The remember manager reads the run row (`relational.GetRememberRun`) when it starts a queued run and for `Manager.Status`.

## The writes

### The store stage

One transaction per text (the text of the call, one chunk, a web search text or an observation text), `memory_remember.store`:

1. Before the transaction: `relational.EnsureDataset` (creates the dataset if missing) and `relational.EnsureModel` (adds the model snapshot if it is not in `gb_models`). They are outside the graph transaction so the write lock is not held while they run.
2. In the transaction: `gb_artifacts` (the artifact: the text, or a chunk with its offsets, or a `web_search` / `series_observation` artifact with its query / series, child of its parent); the chunk summary as an artifact of its own, child of the chunk; `gb_entities` (upsert); `gb_entity_artifacts` (the entity is linked to the artifact, new or not); `gb_entity_sessions` and `gb_edge_sessions` (with a session ID); `gb_edges` and `gb_edge_artifacts` (new edges, and the link of a duplicate to this artifact); `gb_edge_events` (created, duplicate, invalidated, ...); contradicted edges are expired in `gb_edges` (the old fact is kept, with its end time); and, for a text of one chunk, the series of the call (`gb_series`, `gb_series_anchors`, `gb_series_points`).
3. The embeddings are computed before the commit (an embedder failure stores nothing).
4. After the commit: the vectors are **upserted** into the vector store, one dataset per kind (entities, edges, chunks, summaries; `memory_dataset.*Dataset(dsID)`), keyed by entity / edge / artifact ID, so writing them again overwrites. A vector store failure after the commit returns an error with the relational data already saved; the vectors are written again the next time the entity or fact is mentioned.
5. After the transaction: the cost of the embedding request is recorded as an inference (`StageEmbed`). It is recorded outside the transaction to avoid `SQLITE_BUSY`.

For a document, the parent artifact is stored first (`storeParent`, its own transaction, `gb_artifacts` only), then each chunk is stored in its own transaction. A failure of a chunk leaves the parent and the chunks before it stored (each complete); the error names the chunk. A failure while extracting any chunk stores nothing, since extraction runs for all the chunks before the first write.

### Inferences and counters

- Every LLM, embedding and web search call is recorded in `gb_inferences` (`memory_core.RecordRun`), detached from the call's context so a cancelled call still records what it spent. The IDs go to the `inference_ids` of the stage that made them.
- `resolve_edges` and `web_search` call `memory_core.CountInContext`: one row per entity or edge shown to an LLM in `gb_graph_in_ctx_counters` (source: pipeline `remember`, the stage and the run ID). A failure is logged and ignored.

### Run log writes

- `Submit` of the [remember manager](remember-manager.md) creates the `gb_remember_runs` row (status `started`, every stage `not_started`) before the run begins; the run overwrites it with its parameters and start time.
- The pipeline runner saves the run row at every stage start and end (`gb_remember_run_stages`: status, error, times, inference IDs); the stages do not touch the log themselves (see [pipeline.md](../pipeline.md)).
- `runLog.Output` adds rows to `gb_remember_run_stage_outputs` (extraction, candidates and similarity matches, the edge plan, what was stored, raw LLM answers).
- The final status (`succeeded` / `failed` with the error) is saved when the run ends.

## What does not touch the database

- `classify_complexity`, `chunk_text`, `extract_graph`, `summarize_chunk` and the `resolve_edges` decision are LLM calls or pure code on the state of the call; they only record their inferences. Nothing is written to the graph before `store`, so an extraction failure leaves no trace in it.
- `resolve_geo`, `web_search`, `fetch_series`: the outside lookups (geocoder, web, series providers) are network calls; only their results are written, by the stages above.

## Failure behaviour (what is committed when)

| Stage | Policy | If it fails |
|---|---|---|
| `classify_complexity`, `classify_graph`, `resolve_geo`, `web_search`, `fetch_series`, `observe_series` | advisory | the call succeeds; what the stage already stored stays |
| `load_candidates`: the similarity search | partial | falls back to candidates by name |
| `load_candidates`: the relational read | required | the call fails; no write of that text was made |
| `store` | required | the transaction rolls back; the vectors are not written |
| vector `Upsert` after commit | required | the call errors, the relational data stays |

## Code

| Path | What |
|---|---|
| `internal/memory/memory_remember/remember_pipeline.go` | the stages |
| `internal/memory/memory_remember/remember_vectors.go` | `loadCandidates`, `findSimilarEntities`, `prepareVectors`, `pendingVectors.write` |
| `internal/memory/memory_remember/remember.go` | `store` (the transaction) |
| `internal/memory/memory_temporal/{candidates,store}.go` | `LoadCandidates`, `StoreEdges` |
| `internal/memory/memory_core/in_context.go` | `CountInContext` |
| `internal/db/graph/` | `LoadCandidateEdges`, `Tx` (`FindEntityID`, `UpsertEntity`, `InsertEdge`, ...) |
