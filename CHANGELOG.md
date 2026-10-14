# Changelog

All notable changes to gligabao are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/) (while below 1.0.0: a minor bump may break things).

Newest entry first; its version must equal the contents of `VERSION` (checked by `go test`). See [AGENTS.md](AGENTS.md).

## [0.13.0] - 2026-10-16

### Added
- `series_observation` artifacts. `POST /v1/gb/remember` takes `timeseries.observations: true` (with `series`, `fetch` or both; off by default). After the text, the web search and the fetch, `remember` derives a new kind of artifact from each series it stored (supplied or fetched, at most 10 per request): a text generated in Go, not by an LLM, from the series' stats (change, trend, high, low, average, largest move) over the period the request wrote, naming the entity the series is about. That text is extracted, reconciled and stored like any other, so what a series says reaches the graph as bi-temporal facts, deduplicated and embedded like all others. A failure never fails `remember`: it is reported per observation. The response gets `series_observations`. See [docs/timeseries.md](docs/timeseries.md#observations).
- An observation artifact has `kind` `series_observation`, the series it describes (`series_id`), a `query` saying which series and window, and as its parent the artifact that anchors the series (the call's artifact, or for a fetched series the artifact the entity came from). Its `valid_at` is the last point of the window. The provenance chain reads: fact, observation artifact, series, anchoring artifact. Like a web search it is not a chunk.
- Trajectories get the `timeseries_observations` input and an `observe_series` step per series.
- The Python client's `remember` takes `observe_timeseries` (client version 0.5.0).
- Go: `memory_core.WithSeriesObservations`, `Result.SeriesObservations`, `graph.ArtifactSeriesObservation`, `graph.Artifact.SeriesID` and `IsDerived`, `Tx.InsertSeriesObservationArtifact`, `memory_timeseries.ObservationText`.

### Changed
- Database schema: `gb_artifacts` gets a `series_id` column (→ `gb_series`, `ON DELETE SET NULL`). Existing SQLite database files are not migrated: delete the file (and the eval database) and the server recreates it. PostgreSQL adds the column by itself.
- `recall` is not changed: it does not follow observations to their series (on hold, see [docs/timeseries-design.md](docs/timeseries-design.md)).

## [0.12.0] - 2026-10-15

### Added
- Timeseries providers. A program embedding gligabao registers a list of providers when it starts the server (`gligabao.ServeOptions.TimeseriesProviders`). A provider (`gligabao.TimeseriesProvider`) has a `Name`, a natural-language `Description` of what it offers and **in which cases it should be used**, an optional `QueryFormat` (what identifies an entity to it, e.g. a ticker symbol) and a `Fetch` callback with the logic of fetching. The providers are validated at startup.
- `POST /v1/gb/remember` takes `timeseries.fetch` (`providers`, `before`, `after`; all optional; alone or with `series`). After the text is stored, an LLM (new stage `resolve_series`, choosable in `models`) reads the providers' descriptions and decides which provider fetches series for which entity of the text (for example a stock price provider for an entity that is a public company), and what to ask it. The callbacks then run in parallel for the period around the text's reference time (by default 30 days before it and nothing after it, so a memory never holds the future of its event), and the series are stored and anchored to the entity and to the artifact it came from, with the period as the window. Fetching again adds points to the same series. A failure in a provider, the LLM or the storage never fails `remember`: it is reported per fetch. The response gets `series_fetches`. `400` for an unknown provider or a malformed window, `503` if the server has no provider.
- Fetched series have `source` `fetched`, their `provider` and a `definition` (entity and query).
- Trajectories get the `timeseries_fetch` input and the steps `select_series_entities`, `resolve_series_providers` and `fetch_timeseries`.
- The Python client's `remember` takes `fetch_timeseries` (client version 0.4.0).
- Go: `memory_core.SeriesProvider`, `ResolveSeriesProviders`, `WithSeriesProviders`, `WithSeriesFetch`, `ResolveSeriesFetch`, `ParseFetchWindow`; `Result.SeriesFetches`.

### Changed
- `timeseries.fetch` is no longer a `400`; `timeseries` needs `series`, `fetch` or both.
- The stages that take a model (`models`) now include `resolve_series`.
- Go: `api.Register`, `api.RegisterEval` and `server.NewHandler` take the registered providers (nil for none).

## [0.11.0] - 2026-10-14

### Added
- Timeseries. `POST /v1/gb/remember` (and `/v1/gb/eval/remember`) takes an optional `timeseries` with `series`: a series has a `key` (and optional `field`, `name`, `unit`, `frequency`), `points` as `[time, value]` pairs and optional `anchors`. The series are stored next to the text, in the same transaction, and anchored to the artifact the request creates (the default) and to the entities and facts that their anchors select. The entities do not exist when the request is made, so an anchor is a selector (`entity: {name, type}`, `edge: {source, relation, target}`) that is looked up after the text has been extracted and stored; IDs of things already stored (`artifact_id`, `entity_id`, `edge_id`) work too. An anchor can have a `role`, a `window` and an `on_unmatched` (`create`, `ignore`, `error`). A series that cannot be anchored stores nothing (`422`). For a chunked text the series are stored after the chunks, anchored to the parent artifact. The response gets `series`. Not allowed with `image` (`400`); `timeseries.fetch` is reserved and is a `400`. See [docs/timeseries.md](docs/timeseries.md).
- `POST /v1/gb/timeseries` stores series in an existing dataset and anchors them to what it holds, for series that are known only after `remember` returned, that change later, or that are too big for a `remember` request (body up to 16 MiB). Sending a key again adds points to the stored series. `GET /v1/gb/timeseries` lists a dataset's series (stats and anchors, no points) and `GET /v1/gb/timeseries/{id}` reads one with its points between `from` and `to` (and the stats of that window).
- Every series has stats computed in Go whenever points are written: count, first, last, min, max, mean, change, percent change, trend and the largest move between two points.
- New tables `gb_series`, `gb_series_points` and `gb_series_anchors` (SQLite and PostgreSQL). Anchors are real foreign keys, so deleting an artifact, entity or edge removes only its anchors.
- Trajectories get the `timeseries` input and a `store_timeseries` step.
- The Python client's `remember` takes `timeseries`, and has `attach_timeseries` (client version 0.3.0).
- Go: `memory_core.WithSeries`, `ResolveSeries`, `SeriesSpec`; package `memory_timeseries` (`Store`, `Attach`, `Summarize`); `graph.ListSeries`, `GetSeries`, `SeriesPoints`, `ListSeriesAnchors`; new `Tx` methods (`UpsertSeries`, `UpsertSeriesPoints`, `ListSeriesPoints`, `UpdateSeriesSummary`, `AnchorSeries`, `FindEntity`, `FindEdgeID`, `CheckArtifact`); `Result.Series`.
- [docs/timeseries-design.md](docs/timeseries-design.md): the design, the changes made while building the first version, and what is left (fetching series from a data provider, derived observation artifacts, embedding, `recall`).

### Changed
- Database schema: three new tables (see above). Existing database files are not migrated: delete the file (and the eval database) and the server recreates it.
- `remember` returns `422` when a timeseries anchor that must match finds nothing.

## [0.10.0] - 2026-10-14

### Added
- Admin UI: web search. A new **Web search** card shows, per database, the totals of the web searches `remember` made: `web_search` artifacts, the datasets that hold them, sources, entities linked to a result (and those only known from the web), facts created or supported, and, for the eval database, the totals recorded in the eval trajectories (entity searches found / empty / failed, searches made by the search tool, entities and facts extracted, latency, cost). Each dataset with web searches has a **View** button.
- Admin UI: the runs of an experiment show how many web searches each eval made, and an eval that made some gets a **View web searches** button. The list shows, per search, its result, sources, the queries sent to the search engine, the entities and facts extracted from the result, its latency, model, tokens and cost (searches read from a dataset, not from a trajectory, lack the latency, cost and search-engine queries).
- Admin API: `GET /v1/gb/admin/api/web-searches`, `GET /v1/gb/admin/api/web-searches/dataset` and `GET /v1/gb/admin/api/evals/runs/{id}/web-searches`. See [docs/web-search.md](docs/web-search.md#admin-ui).

## [0.9.0] - 2026-10-14

### Added
- Web search for `remember`. `POST /v1/gb/remember` (and `/v1/gb/eval/remember`) takes an optional `web_search` flag (default `false`). When set, after the text is stored the key entities of the text (the extracted entities in the most facts, concepts excluded, at most 3) are searched for on the web, to get the latest world events about each. The search is Anthropic's cloud-based web search tool (`web_search_20250305`), so it is only available when an Anthropic API key is supplied: a request with `web_search` and no key returns `503` (and `400` together with an `image`). The server logs at startup whether web search is available. See [docs/web-search.md](docs/web-search.md).
- Each web search result is an artifact of its own, of the new kind `web_search` (a caller cannot ask for that kind). It saves the query that was issued (new column `gb_artifacts.query`) and its parent (`gb_artifacts.parent_id`) is the artifact from which the searched entity came (for a long text, the chunk). The graph is extracted from the result like from any text (reference time: now), reconciled with the stored facts, and stored: the entities discovered on the web are ordinary nodes, linked to the `web_search` artifact (and to the call's session), embedded when vectors are configured. The searched entities are not searched for in turn.
- The `remember` response gets `web_searches`: per entity searched, its query, the parent artifact, the `web_search` artifact, the sources and what the result yielded (`entities`, `edges`, `invalidated_edges`).
- New model stage `web_search` for `models` (the model that runs the web search tool; the results are extracted on `extract_graph`). The Python client's `remember` takes `web_search`.
- The web search is an enrichment that runs after the text is stored: a failure in it is logged, recorded in the trajectory and reported in the result, and never fails the request. It is billed by Anthropic ($0.01 per search, `ai.WebSearchRequestUSD`) on top of the tokens; that is included in the recorded cost (`gb_llm_calls`, trajectory totals). Trajectories get the steps `select_web_search_entities` and `web_search`.
- Eval: the suite `web_search.json` with one case, `web_search_companies`, that remembers a text about Apple, Microsoft and Nvidia with `web_search`; remember entries of a suite can set `"web_search": true`, and a case `expect_web_searches` (checks on the stored `web_search` artifacts).
- Go: `ai.Client.WebSearch`, `memory_core.WebSearcher`, `WithWebSearcher` (makes it available), `WithWebSearch` (asks for it per call), `ErrWebSearchUnavailable`, `StageWebSearch`, `graph.ArtifactWebSearch`, `Tx.InsertWebSearchArtifact`, `Artifact.Query` and `Artifact.IsChunk`, `Result.WebSearches`.

### Changed
- Database schema: `gb_artifacts` has a new column `query`. Existing database files are not migrated: delete the file (and the eval database) and the server recreates it.
- A child artifact is no longer always a chunk: `Counts.Chunks` counts the artifacts with a `chunk_index`, not those with a `parent_id`.
- Go: `server.NewHandler`, `api.Register` and `api.RegisterEval` take one more argument, the web searcher (nil for none; the server passes the Anthropic client).

## [0.8.0] - 2026-10-14

### Added
- Jev, TypeSafe's "system 1" model, as a small package (`internal/jev`) that wraps calls to its evaluation endpoint (`POST https://api.typesafe.ai/v1/systemone`, model `jev-latest`). It is only used when its API key is supplied: `jev_api_key` (default: the `JEV_API_KEY` environment variable). `jev_base_url` (`JEV_BASE_URL`) and `jev_model` are optional overrides. Without a key the server logs that Jev is off and nothing below runs. A call is one request and is not retried, so the measured latency is that of one request.
- First use, a new first stage of `remember`, `classify_complexity`: Jev answers one `choice` question that classifies the incoming text as `simple` (up to 5 entities), `medium` (6 to 20) or `complex` (more than 20) in the number of entities it is expected to yield, as a prediction of the compute needed per entity and edge. The `confidence` Jev returns with its answer, and the probability of each class, are tracked. For now the result is only logged (`jev: remember text complexity`, with `complexity`, `confidence`, `latency_ms`, text size). The stage is advisory: a Jev failure is logged and never fails `remember`. It always runs on Jev, so it is not part of `models`. At most the first 32 KiB of a text are sent to Jev.
- The stage is a step of the ATIF trajectory of the eval endpoints (`classify_complexity`) with its result (complexity, confidence, probabilities), the Jev model version and tokens, and `latency_ms` / `started_at` in the step's extra (also for a failed call), so Jev latency can be measured and compared across runs.
- Go: `jev.Client` (`Evaluate`, `Classify`), `memory_core.WithJev`, `memory_core.Classifier`, `Trace.TimedTool`.

### Changed
- Go: `server.NewHandler`, `api.Register` and `api.RegisterEval` take one more argument, the Jev classifier (nil for none).

## [0.7.0] - 2026-10-14

### Added
- Per-stage LLM models for `remember`. `POST /v1/gb/remember` (and `/v1/gb/eval/remember`) takes an optional `models` object that picks the model of each LLM-invoking stage: `extract_graph` (entities and edges), `resolve_edges` (duplicate/contradiction check) and `extract_image` (the image pipeline). It can be given with every call. A stage that is left out, or given as an empty string, uses the default model, so a call can change one stage and keep the others; without `models` nothing changes. An unknown stage or a malformed model name returns `400`.
- The model that runs the `extract_graph` stage is the one recorded as the `model_id` of the entities and edges it yields (the model name as the provider reports it), so the stored knowledge reflects the model chosen for that stage.
- Evals can vary the models too: `go run ./evals -models extract_graph=<model>,resolve_edges=<model>` sets them for a whole run (recorded in the experiment run's `params`), and a case of a suite can set `"models"` of its own, which wins stage by stage.
- Go: `memory_core.WithModels`, `memory_core.Models` (`ResolveModels`, `ParseModels`, `Merge`), the `memory_core.Stage*` constants and `CallOptions.ModelLLM`. Eval trajectories record the requested `models` in `extra.input`.
- The Python client's `remember` takes `models`.

## [0.6.1] - 2026-10-14

### Added
- New eval case `chunking_register` (suite `evals/suites/chunking.json`): remembers a ~3,700-word, 60-person register (`evals/suites/corpus/harbor_cooperative.txt`) that needs about 20 chunks, then checks that facts from the start, middle and end were extracted and recalled, and that the chunks are stored correctly (count, size, child artifacts, coverage and overlap of the text, provenance of the facts).
- Eval suites: a `remember` entry can take `text_file` (a file under the suites directory) instead of `text`; a case can set `chunk_size` and `expect_min_chunks` to turn on the chunking checks. See `evals/README.md`.

### Changed
- The eval runner waits up to 30 minutes (was 2) for a `remember` call, since a chunked text is extracted chunk by chunk in one request.

## [0.6.0] - 2026-10-14

### Added
- Chunking of long texts. `POST /v1/gb/remember` with a text of more than one chunk (`chunk_size`, 1024 tokens by default) no longer extracts it in one LLM call: the text is stored as an artifact (the parent), split into overlapping chunks, and every chunk is an artifact too, a child of the parent. See the new `docs/chunking.md`.
- The chunker (`internal/chunk`) sizes chunks in tokens (a pure-Go estimate of a BPE tokenizer by default; `chunk.Tokenizer` can be replaced from Go), cuts at paragraph boundaries (oversized paragraphs fall back to sentences, then words), and repeats the tail of each chunk at the start of the next one (`chunk_overlap_percent` of the chunk size, 20 by default, at most 50).
- Each chunk is processed individually: its own entity/edge extraction, candidate comparison against the facts the earlier chunks stored, storage, and embedding. Entities and edges link to the chunk they came from.
- Chunks are embedded into a dedicated vector dataset, `dataset-<id>-chunks` (vector ID `chunk-<chunk artifact id>`, metadata `kind`, `artifact_id`, `parent_id`, `index`), next to the entity and edge ones, using the same embeddings request as the chunk's entities and edges. It is listed by `VectorStats` (admin UI "View vec db") and removed with the dataset.
- Config: `chunk_size` (default 1024; 32 to 8000) and `chunk_overlap_percent` (default 20; 0 to 50), validated at startup. Go: `memory_core.WithChunking`, `gligabao.Config.ChunkSize` / `ChunkOverlapPercent`.
- The remember response gains `chunks` (artifact ID, index, tokens, overlap, byte range, number of entities and edges extracted per chunk) and `embedded_chunks`, only for a chunked text; `artifact_id` is then the parent's. Eval trajectories gain `chunk_text` and `store_parent_artifact` steps.
- Go: `graph.Tx.InsertChunkArtifact`, `graph.NewChunkArtifact`, `Artifact.ParentID` / `ChunkIndex` / `StartOffset` / `EndOffset` / `Tokens`, `Counts.Chunks`, `memory.ChunkVectorID`, `memory_core.ChunkDataset`, `memory_core.IndexChunks`.

### Changed
- DB schema: `gb_artifacts` gains `parent_id` (self-reference, `ON DELETE CASCADE`), `chunk_index`, `start_offset`, `end_offset` and `tokens`. There is no migration: existing databases (and vector directories) must be recreated.
- A text that is not larger than a chunk is processed exactly as before: no chunks and no chunk vector.
- A chunked text is not one transaction: the chunks go in one after the other (each chunk's candidates must see the previous ones), so a failure while storing chunk *k* leaves the parent and the chunks before it. An extraction failure still stores nothing, because every chunk is extracted before anything is written.
- `graph.Counts.Artifacts` counts chunk artifacts as well; `Counts.Chunks` says how many of them are chunks.
- Go: `graph.Tx` gained `InsertChunkArtifact` (implement it in custom backends); `memory_remember.Result` gained `Chunks` and `EmbeddedChunks`.

## [0.5.0] - 2026-10-14

### Added
- Optional `session_id` on `POST /v1/gb/remember` (text and image) and `POST /v1/gb/recall`: a string the caller chooses (at most 128 bytes, no control characters, else `400`) that labels one user/agent session. In Go it is the option `memory_core.WithSessionID`.
- `remember` attaches the session to the artifact (`gb_artifacts.session_id`, and `gb_image_artifacts.session_id` for images) and echoes it as `session_id` in the response.
- New tables `gb_entity_sessions` and `gb_edge_sessions` record which sessions each entity and each edge (fact) was discovered in. They are written for every entity and edge an artifact of a session mentions or states, including ones that already existed and were therefore not written again, so a session that only repeats known facts is still recorded. A call without a session adds no rows.
- `recall` echoes `session_id`, and sets it as the `session_id` of its eval trajectory (previously always the trajectory's own ID; `remember` trajectories do the same), so the calls of one session can be grouped. It does not change retrieval or the answer, and nothing about the recall is stored. The session ID is also in the `remember.*` / `recall.*` monitoring events and log lines.
- `docs/sessions.md`, and the session fields documented in `remember.md`, `recall.md`, `artifacts.md`, `images.md`, `evals.md` and the README.
- Go: `graph.ListEntitySessions`, `graph.ListEdgeSessions`, `Artifact.SessionID`, `ImageArtifact.SessionID`, `atif.Builder.SetSessionID`.

### Changed
- DB schema: new `session_id` columns and the two link tables. There is no migration: existing databases must be recreated.
- `graph.Tx.InsertArtifact` takes a `sessionID` argument; `Tx` gained `LinkEntitySession` and `LinkEdgeSession`.
- `docs/not-included.md`: the session memory entry now says that the `session_id` label is included and only the per-session cache of turns is left out.

## [0.4.5] - 2026-10-14

### Added
- `docs/not-included.md`: a list of things gligabao deliberately leaves out, starting with session memory (the Cognee-style per-conversation cache), why it is excluded, and the rationale around exact/keyword matching.

## [0.4.4] - 2026-10-14

### Changed
- Refactor, no behaviour change: the text pipeline moved out of `internal/memory` into two new subpackages. `internal/memory/memory_remember` has `Remember`, `Extract`, `Store`, `Result`, `ResolveKind`, `CallRemember` and the fact reconciliation, and `internal/memory/memory_recall` has `Recall`, `RecallResult`, the evidence types and `CallRecall`. What both share moved to `memory_core`: `Fact` (with `FactFromEdge`, `FactFromEvidence`), `Key`, `SynthStatement`, `DatasetID` and `ErrDatasetNotFound` (`memory.ErrDatasetNotFound` is still the same error). Go code that embeds gligabao must use the new import paths (e.g. `memory_remember.Remember`, `memory_recall.Recall`).

## [0.4.3] - 2026-10-14

### Changed
- Refactor, no behaviour change: the image pipeline (`RememberImage`, `LoadImage`, `ExtractImage`, `ImageDir`, `WithImageDir`, ...) moved from `internal/memory` to the new subpackage `internal/memory/memory_image`. What it shares with the text pipeline (the `LLM` interface and call recording, per-call options and the ATIF trace, user and dataset name validation, JSON extraction from LLM output) moved to the new subpackage `internal/memory/memory_core`. Go code that embeds gligabao must use the new import paths (e.g. `memory_core.LLM`, `memory_core.WithEval`, `memory_core.TrajectoryDir`, `memory_image.RememberImage`).

## [0.4.2] - 2026-10-14

### Changed
- Admin UI: the header is narrower and has no background or border; only the title and the health indicator are visible.

## [0.4.1] - 2026-10-14

### Added
- Admin UI: the lower right corner shows, in small text, the running gligabao version and a "github url" link to the commit the binary was built from (only when the binary was built inside a git checkout).
- `GET /version`: the `gligabao` object has a new `commit_url` field (GitHub URL of the full commit).

## [0.4.0] - 2026-10-13

### Added
- Entities and edges now record the model that inferred them: a new `model_id` column of `gb_entities` and `gb_edges`, a reference to the new `gb_models` table (NULL if unknown). It is the model the LLM API reported for the extraction call. An entity shared by several artifacts keeps the model that wrote its stored state: the one that created it, then the one that last replaced its description. The value is set by the pipeline, never taken from the LLM's output.
- New table `gb_models` (`id`, `name`, `provider`, `kind` = `llm` or `embedding`, `supported`, `created_at`). The ID is derived from the name alone (first 8 bytes of the SHA-256 of the trimmed, lowercased name, in hex), so a model has the same ID in every database.
- On every startup gligabao inserts the models it supports into `gb_models` and marks them `supported`: the default chat and embedding models and every key of the price table (built-in prices plus `[prices]` in the config). Models that are no longer supported keep their row with `supported` cleared. A model that is only seen in an API response (e.g. a dated snapshot like `claude-haiku-4-5-20251001`) is inserted, not supported, when something it inferred is stored.
- `model_id` appears in the graph JSON (`GET /v1/gb/admin/api/graph`, the eval graph endpoint), which also lists the referenced models (`models`: id, name, provider, kind, supported), in recall evidence and in the remember result.
- Admin UI (eval graph view): the model name is shown in the tooltips, in the details of the clicked entity and of each of its facts, and the status line lists the models that inferred the graph.

### Changed
- DB schema: new `gb_models` table and `model_id` columns. There is no migration: existing databases must be recreated.

## [0.3.0] - 2026-10-13

### Added
- Admin UI: paste an image (Ctrl+V) anywhere on the page to remember it. The image opens in an overlay over a tinted background; enter the user and dataset and press Remember to call `POST /v1/gb/remember` with the image. A spinning circle shows while the server works, and the extracted metadata (theme, title, text blocks, hashes, model, tokens, cost, cached or new) appears next to the image. Esc, the Close button or a click on the background closes it.
- Admin UI: an "Image store" card on the dashboard with the number and total size of the stored images, their extractions, LLM tokens and cost, counts by media type and by theme, and a comparison of the image directory with the database (files on disk, missing files, untracked files). It refreshes after an image is remembered from the UI.
- `GET /v1/gb/admin/api/images[?db=<name>]`: the statistics behind the card.

## [0.2.0] - 2026-10-13

### Added
- Image knowledge extraction: `POST /v1/gb/remember` (and the eval endpoint) accepts a base64 `image` (JPEG, PNG, GIF or WebP, up to 5 MiB) instead of `text`. It runs a separate image pipeline: one vision-LLM call (default model `claude-haiku-5-5`) reads the title and text blocks in the image (OCR) and classifies the whole image by a theme (`plot`, `chart`, `graph`, `diagram`, `table`, `document`, `screenshot`, `slide`, `map`, `photo`, `illustration`, `other`). See [docs/images.md](docs/images.md).
- Image files are stored on the file system, never in the database: in an image directory next to the SQLite file (`<name>.images`, e.g. `bin/gligabao.images`) as `<dataset id>/<sha256>.<ext>`. Set it with `serve --image-dir` or `ServeOptions.ImageDir`.
- New tables, created on startup in existing databases too: `gb_image_artifacts` (metadata only: media type, SHA-256, size and the file's path relative to the image directory), `gb_image_extractions` (theme, title, text, the model's raw output, tokens and cost) and `gb_image_text_blocks` (the text regions in reading order). Nothing is added to the graph, the vector store or `gb_artifacts` yet; grounding comes later.
- An image already in the dataset (same SHA-256) is not processed again: `remember` returns the stored extraction with `"cached": true`, without an LLM call or new rows. Only the same user's dataset is searched.
- Image calls are logged in `gb_llm_calls` with type `remember_image`, and emit the `remember_image.completed` / `remember_image.failed` system events.
- The LLM client can send images (`ai.Message.Images`).
- Python client: `Client.remember(user_id, image=<bytes or path>)`; `text` is now optional (exactly one of `text` and `image` is required).

### Changed
- A text larger than 1 MiB is still rejected with `400`, but the request body limit is now about 7 MiB, to fit images.

## [0.1.0] - 2026-10-13

First tracked version; summarizes everything built so far.

### Added
- Temporal knowledge graph memory: `POST /v1/gb/remember` extracts entities and facts (with validity intervals and artifact provenance) from text; `POST /v1/gb/recall` answers questions with evidence.
- Datasets owned by users, OWL ontologies, graph export (`GET /v1/gb/datasets/{name}/graph`).
- SQLite and PostgreSQL relational backends; chromem-go (default) and Pinecone vector stores; entities and edges embedded into per-dataset vector stores.
- Public Go API at the repo root for embedding gligabao, and the `gligabao` CLI (`serve`).
- Eval mode: endpoints returning ATIF trajectories, eval suites, experiments and runs kept in an eval database.
- Embedded admin UI at `/v1/gb/admin/`: datasets, graphs, vectors, evals and remember traces.
- System event hooks (`monitoring`), structured logging with logrus.
- Version tracking: `VERSION` embedded in the binary, this changelog, `gligabao version`, `--version`, `GET /version`.
