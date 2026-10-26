# Chunking

A text that is larger than one chunk is a document: `POST /v1/gb/remember` does not extract it in one piece, it cuts it into overlapping chunks and processes each chunk on its own. This page describes how the text is cut, what is stored, and how it is embedded. The request and response are in [remember.md](remember.md). Each chunk is also [summarized](#chunk-summaries), and the summary is stored and embedded as an artifact of its own.

Code: `internal/chunk` (the chunker), `internal/memory/memory_remember/remember_chunks.go` (the pipeline).

## When a text is chunked

`remember` counts the tokens of `text`. If the count is **more than the chunk size** (1024 tokens by default) the text is chunked; otherwise it is not, and nothing changes: one artifact, one extraction, no chunks, no chunk vectors (see [remember.md](remember.md)). A text is never chunked "just because": a short text has no chunk of its own.

| Setting (`gligabao.toml`) | Default | Meaning |
| ------------------------- | ------- | ------- |
| `chunk_size` | `1024` | The largest chunk **in tokens**, overlap included; also the threshold above. Between 32 and 8000 (every chunk is embedded as a whole, and the OpenAI embedding models take at most 8191 tokens). |
| `chunk_overlap_percent` | `20` | How much of a chunk repeats the end of the previous one, as a percentage of `chunk_size`. `0` turns the overlap off; at most `50`. |

Out-of-range values fail config validation at startup. In Go the same settings are `memory_core.WithChunking(chunk.Config{Size, OverlapPercent, Tokenizer})` per call, and `gligabao.Config.ChunkSize` / `ChunkOverlapPercent` for the server.

## Token-based sizing

Sizes are token counts, not characters. gligabao has no tokenizer vocabulary, so the default `chunk.Estimator` estimates the tokens of a BPE tokenizer (such as the one of the `text-embedding-3` models) with a few rules: a run of ASCII letters costs 1 token per 4 letters, a run of digits 1 per 3, a CJK character 1, any other symbol 1, whitespace nothing. It is on purpose a little conservative (on English prose it over-counts by 10-20%), so a chunk sized by it stays below the real limit of the embedding model. To size chunks by a model's exact tokens, implement `chunk.Tokenizer` (a single `Count(text) int`, roughly additive over whitespace-separated words) and set `Config.Tokenizer`; the server uses the estimator.

## How the text is cut (`chunk.Split`)

1. **Paragraphs.** The text is split on blank lines. A paragraph is the unit of meaning the chunker tries to keep whole.
2. **Oversized paragraphs.** A paragraph that is larger than a chunk is split into sentences (after `.`, `!`, `?`, `…`, or a CJK full stop, and at line breaks, so lists split by line). A sentence that is still larger is packed word by word, and a single word that is larger (a blob of base64, say) is cut into pieces. After this step every unit fits one chunk and starts and ends at the best boundary available.
3. **Packing.** Units are put into chunks greedily, in order: a chunk takes units until the next one does not fit. So a chunk ends at a paragraph boundary whenever it can, and a paragraph is only cut when it alone is larger than a chunk.
4. **Overlap.** Every chunk but the first starts with the tail of the previous one, at most `chunk_size * chunk_overlap_percent / 100` tokens (204 by default): the last *whole units* that fit that budget, so the overlap also starts at a paragraph or sentence boundary; if not even the last unit fits, the last *words* of it. If the overlap would leave no room for the chunk's first new unit it is shortened, or dropped. Units cut out of a sentence are kept at `chunk_size` less the overlap, so they always leave room for it.

Each chunk is the slice `text[start:end]` of the original, so the separators between paragraphs are kept as they were. The last chunk reaches the end of the text. The chunker is deterministic: the same text and settings give the same chunks.

Known limit: sentence splitting is by punctuation only, so an abbreviation like "e.g. " inside a paragraph larger than a chunk can end a unit early. It only affects where an oversized paragraph is cut.

## Chunks are artifacts

A chunk is an [artifact](artifacts.md) of its own, and **artifacts can be the children of other artifacts**:

- The artifact for what the caller sent, the whole text, is the **parent**. It is stored first, exactly as a short text is (`kind`, `session_id`, `valid_at` as given, `parent_id` NULL). Its id is the `artifact_id` of the response.
- Each chunk is a **child** of it: a row of `gb_artifacts` with `parent_id` set to the parent, the same `kind`, `session_id`, `valid_at` and `created_at`, `text` = the chunk's text, and `chunk_index` (from 0), `start_offset` / `end_offset` (the byte range in the parent's text, overlap included) and `tokens`.
- Deleting the parent deletes its chunks (`ON DELETE CASCADE`).

A chunk has one child of its own: its [summary](#chunk-summaries), an artifact of kind `chunk_summary`.

Provenance goes through the chunks: the entities and edges of a document are linked (`gb_entity_artifacts`, `gb_edge_artifacts`, `gb_edges.artifact_id`, and the session tables) to the **chunk** they were extracted from, not to the parent. A fact stated in two overlapping chunks is one edge linked to both. The parent is reached through `parent_id`.

## Each chunk is extracted individually

1. **Split** the text into chunks.
2. **Extract** the graph (entities and edges) from each chunk with its own LLM call, the same [extraction](remember.md#how-it-works) as for a short text, with the document's `reference_time`. Nothing is written yet, so a failure here (an LLM error, truncated output) stores nothing.
3. **Summarize** each chunk with its own LLM call (see [Chunk summaries](#chunk-summaries)). Nothing is written yet either.
4. **Store the parent** artifact.
5. **Per chunk, in order:** load the [candidate facts](remember.md#candidate-facts-load_candidates), resolve duplicates and contradictions, then store the chunk artifact with its entities and edges and its summary artifact, and embed everything, in one transaction per chunk. Because the chunks go in one after the other, a chunk is compared against the facts that the chunks before it stored, so the same fact stated in several chunks (or by the overlap) is one edge, and a later chunk can close an earlier fact (see [temporal.md](temporal.md)).

The cost of a document is therefore at least two LLM calls per chunk (the extraction and the summary, plus a resolution call where the candidates need comparing) and one embeddings request per chunk.

Failure after step 4: the transaction of a chunk cannot span the whole document (a chunk's candidates are read from the database, which must contain the previous chunks), so if chunk *k* fails the request returns `502` and the parent plus the *k − 1* chunks stored so far remain, each of them complete (artifact, summary, entities, edges, links, vectors). The error says which chunk failed. Sending the text again stores a new parent and re-extracts every chunk; the entities and edges are deduplicated as always.

## Chunk summaries

Every chunk is summarized by an LLM call of its own (the `summarize_chunk` stage, `summarizeChunk` in `internal/memory/memory_remember/remember_summaries.go`). The model is asked for a faithful two-to-four sentence summary of the chunk, in plain prose, that names the people, organizations, places and dates that matter, keeps figures exact and adds nothing from outside the chunk (at most 512 output tokens). It sees the chunk only, not the rest of the document. The calls are made with the extractions, before anything is written, so a failure (an LLM error, an empty or truncated summary) stores nothing; and each is recorded as an inference in `gb_inferences` like the other calls.

**A summary is an artifact of its own.** It is a row of `gb_artifacts`, stored in the same transaction as the chunk:

- `kind` = `chunk_summary` (only the pipeline creates it; a caller cannot ask for it);
- `parent_id` = the **chunk** artifact it summarizes (not the document: the chain is document, chunk, summary); `text` = the summary; the `session_id`, `valid_at` and `created_at` of the chunk;
- `inference_id` = the [inference](inferences.md) (`gb_inferences`) of the call that wrote it. The run holds the model, as the API reports it (a reference to `gb_models`; a new model is registered), and the reasoning level it ran with: `none`, `low`, `medium` or `high` (see [remember.md](remember.md#reasoning)), plus the tokens and cost of the call. It is `NULL` for any other artifact (and for a summary whose run could not be recorded);
- no `chunk_index`, `query` or `series_id`. It is derived, not a chunk (`Artifact.IsDerived()`, not `IsChunk()`), and, like a web search, it is not counted by `Counts.Chunks` (`Counts.Summaries` counts it);
- no entity or edge links to it: the graph is extracted from the chunk, not from its summary;
- deleting the chunk (or the document) deletes the summary.

**Model and reasoning.** The stage has its own model (`models.summarize_chunk`, default model if not given) and a reasoning level (`reasoning.summarize_chunk`, `none` if not given); both are per request, see [remember.md](remember.md#choosing-the-model-of-each-stage). What was used is what is recorded, in the summary's inference: the model the API answered with and the level the call ran with, so every summary says how it was written, even in a dataset whose chunks were summarized by different settings.

**It is embedded.** The summary's text goes to a vector index of its own (below), so a summary can be searched apart from the chunk it condenses.

## Chunks are embedded

Every chunk's text is embedded as a whole and written to a **dedicated vector collection**, next to the entity and edge ones (see [vectors.md](vectors.md#datasets)):

| Index | Vector dataset | Vector ID | Metadata | Embedded text |
| ----- | -------------- | --------- | -------- | ------------- |
| chunks | `dataset-<id>-chunks` (`memory_core.ChunkDataset`) | `chunk-<chunk artifact id>` | `kind` = `chunk`, `artifact_id`, `parent_id`, `index` | the chunk's text |

The chunk's **summary** is embedded into a further index, in the same request:

| Index | Vector dataset | Vector ID | Metadata | Embedded text |
| ----- | -------------- | --------- | -------- | ------------- |
| summaries | `dataset-<id>-summaries` (`memory_core.SummaryDataset`) | `summary-<summary artifact id>` | `kind` = `summary`, `artifact_id` (the summary's), `chunk_id` (the chunk's), `parent_id` (the document's), `index` (the chunk's) | the summary's text |

The chunk's embedding and its summary's go in the same embeddings request as the entities and edges that the chunk yields, so a chunk still costs one request. It uses the same model and dimensions as the other indexes, so all vectors share one space. Like the others it needs an OpenAI key (without one chunks are still made, extracted and stored, but not embedded), is computed before the chunk's transaction commits and written right after it, and is removed with the dataset. `VectorStats` (the admin UI's **View vec db**) lists the chunk and summary indexes with the number of chunk and summary artifacts each should hold. The vectors do not affect `query` yet.

## Response

For a chunked text the response is that of the document (see [remember.md](remember.md#response)):

- `artifact_id` is the parent's; `entities`, `edges` and `invalidated_edges` are those of all the chunks together (an entity once per name and type, an edge once);
- `chunks` lists the chunks: `artifact_id`, `index`, `tokens`, `overlap_tokens`, `start_offset`, `end_offset`, and how many `entities` and `edges` its extraction listed (before merging with the other chunks);
- each chunk also has its `summary`: `artifact_id` (the summary artifact), `text`, `model`, `reasoning` and `inference_id`;
- `embedded_entities`, `embedded_edges`, `embedded_chunks` and `embedded_summaries` are the sums over the chunks.

`chunks` is absent for a text that was not chunked. With the eval endpoints the trajectory has a `chunk_text` step, then an `extract_graph` step and a `summarize_chunk` step (with the summary, its model and reasoning level) per chunk, a `store_parent_artifact` step, and for each chunk its candidate steps, an `embed` and a `store_artifact` step (their messages end with `(chunk i of n)`).

## Limitations

- A text that fits in one chunk is not chunked and so has no chunk vector, and a text above the limit has chunk vectors but no vector for the whole.
- The chunks are extracted one after another (an LLM call each), so a very long text takes long: the request time grows with the number of chunks, and the HTTP server has no streaming or job queue for it. `remember` accepts at most 1 MiB of text.
- The extractor sees one chunk at a time: a fact that needs two distant paragraphs, or a pronoun whose antecedent is in an earlier chunk beyond the overlap, is not resolved.
- The token count is an estimate (above); a custom `Tokenizer` is only available from Go.
- A summary sees one chunk, so it cannot use context from elsewhere in the document; a failed summary fails the request. Chunk summaries are not extracted into the graph.
- `query` does not read chunks, summaries or their vectors yet.
