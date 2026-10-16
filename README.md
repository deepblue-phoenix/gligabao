# gligabao
<img width="800" height="447" alt="image" src="https://github.com/user-attachments/assets/a4cf6b9f-2bdb-4c16-8ed4-558eca33e4a7" />

Minimal memory system for AI.

gligabao is a small, self-hosted HTTP service that gives an AI application long-term memory. You send it free-form text, and it turns that text into a knowledge graph. Later you ask it questions in natural language and it answers from what it stored, citing the facts it used.

## How it works

- **Remember**: an LLM reads the text and extracts *entities* (people, organizations, places, ...) and *edges* (relations such as `works_at` between two entities). Entities are deduplicated, and each edge and entity is anchored to the artifact (the original text) it was inferred from. Following [Graphiti](https://github.com/getzep/graphiti), each text is stored as a timestamped *artifact*, of kind *episode* (Graphiti-style, anchored in time) or *knowledge* (the default, not strictly episodic), and every edge is a bi-temporal fact that records when it was true.
- **Recall**: the query is used to find relevant entities in the graph, the surrounding subgraph is expanded by one hop, and an LLM answers using only that context. The response includes the evidence (entities and edges) behind the answer.
- **Storage**: SQLite (a single file, the default) or PostgreSQL; see [Database](#database).

## API

| Endpoint | Purpose | Docs |
| -------- | ------- | ---- |
| `POST /v1/gb/remember` | Extract a temporal knowledge graph from text and store it; or, with `image`, read an image (OCR and theme) and store the extraction | [docs/remember.md](docs/remember.md), [docs/artifacts.md](docs/artifacts.md), [docs/temporal.md](docs/temporal.md), [docs/images.md](docs/images.md), [docs/chunking.md](docs/chunking.md), [docs/web-search.md](docs/web-search.md) |
| `POST /v1/gb/timeseries`, `GET /v1/gb/timeseries`, `GET /v1/gb/timeseries/{id}` | Store timeseries in a dataset and anchor them to artifacts, entities and facts; list and read them. A `remember` request can also supply them, fetch them from the providers, or derive `series_observation` artifacts from them (`timeseries`) | [docs/timeseries.md](docs/timeseries.md) |
| `POST /v1/gb/recall` | Answer a question from the graph, with evidence | [docs/recall.md](docs/recall.md) |
| `POST /v1/gb/datasets`, `GET /v1/gb/datasets?user_id=`, `DELETE /v1/gb/datasets/{name}?user_id=` | Create, list and (soft) delete datasets | [docs/datasets.md](docs/datasets.md) |
| `GET /v1/gb/datasets/{name}/graph?user_id=` | Export a dataset's whole graph as JSON | [docs/graph.md](docs/graph.md) |
| `POST /v1/gb/ontologies`, `PUT /v1/gb/ontologies/{name}` | Load an OWL ontology (RDF/XML) under a name, or update the one stored under it (merge or replace) | [docs/ontologies.md](docs/ontologies.md) |
| `GET /health` | Health check | |
| `GET /version` | Version (and git commit) of the running binary | [Versioning](#versioning) |

## Datasets

Memory is organized in datasets: a hard container for everything stored (artifacts, entities, edges). Every dataset has an owner, a `user_id`, which covers everything in it; all endpoints require `user_id` (in the JSON body, or the query string for `GET`/`DELETE`), and dataset names are unique per user. `remember` and `recall` take an optional `"dataset"` name, default `main`. Datasets are isolated: the same entity in two datasets is two separate nodes, and a recall only sees its own dataset's graph. Deleting a dataset is soft (`deleted = true`) and also removes its embeddings: `remember` embeds the entities and edges it stores into vector namespaces of the dataset, one vector dataset per purpose (`dataset-<id>-entities`, `dataset-<id>-edges`, `dataset-<id>-chunks`); a vector dataset is a chromem collection or a Pinecone namespace. Details: [docs/datasets.md](docs/datasets.md), [docs/identity.md](docs/identity.md), [docs/vectors.md](docs/vectors.md).

## Ontologies

An ontology describes the concepts of a domain and how they relate. `POST /v1/gb/ontologies` with `{"user_id": "u1", "name": "market", "content": "<rdf:RDF ...>"}` loads an OWL ontology written in RDF/XML under a name (unique per user). Its metadata (name, owner, IRI, SHA-256 of the document, counts) is tracked in the relational database (`gb_ontologies`), and the ontology itself is loaded into the graph database as a graph of its own: classes become nodes, and `rdfs:subClassOf` and object properties (domain to range) become edges. `PUT /v1/gb/ontologies/{name}` updates the one stored under that name: by default the supplied classes and properties are merged into it (new ones are added, ones that exist already are overwritten, nothing is deleted), and with `"replace": true` the supplied ontology replaces it entirely. It is separate from the memory graph, and `remember` and `recall` do not use it yet. [`ontology/market.owl`](ontology/market.owl) is a basic ontology of markets with the 20 most important concepts and their relationships. Details: [docs/ontologies.md](docs/ontologies.md).

## Remember

`POST /v1/gb/remember` with `{"user_id": "u1", "text": "Alice works at Acme and lives in Paris."}` stores the text as an artifact and adds the facts it states to the graph, with the time they became true. An optional `kind` (`knowledge`, the default, or `episode`) says what sort of artifact it is. An optional `reference_time` (RFC 3339, default now) says when the text was written, so relative dates in it can be resolved. An optional `session_id` (any string you choose) ties the artifact, and the entities and facts extracted from it, to the user/agent session it came from (see [Sessions](#sessions)).

1. **Extract**: the text is sent to an LLM (Anthropic, default `claude-haiku-5-5`; see [Models per stage](#models-per-stage)), which returns JSON listing entities (`name`, `type`, `description`) and edges (`source`, `target`, `relation`, `description`, `valid_at`, `invalid_at`).
2. **Normalize**: entity types are lowercased (empty becomes `other`), and relations are lowercased with spaces replaced by `_`.
3. **Reconcile**: new edges are compared with stored live edges that touch the same entities (see [Temporal facts](#temporal-facts)).
4. **Store**: in one transaction, the raw text goes into `gb_artifacts`, entities are upserted into `gb_entities` and linked to the artifact in `gb_entity_artifacts` (an entity is shared by every artifact that mentions it), and edges are inserted into `gb_edges` (each with the `artifact_id` that created it, and linked in `gb_edge_artifacts`; a repeated fact adds the new artifact to the stored edge's links instead of being inserted again).

Images: `POST /v1/gb/remember` with `{"user_id": "u1", "image": "<base64 JPEG, PNG, GIF or WebP>"}` runs a separate image pipeline instead: a vision LLM (default `claude-haiku-5-5`) reads the text in the image (its title and text blocks) and classifies the whole image by a theme (`plot`, `graph`, `chart`, `table`, `document`, ...). The image file is kept in an image directory next to the database (`serve --image-dir`), tracked by `gb_image_artifacts` (metadata and path, never the bytes), and what was read goes into `gb_image_extractions` and `gb_image_text_blocks`; nothing is added to the graph yet. An image the dataset already holds (same SHA-256) is not read again: its stored result is returned with `"cached": true`. Details: [docs/images.md](docs/images.md).

Deduplication:
- Entities are unique on (normalized name, type). Re-submitting one only updates its description if the new one is non-empty.
- A new edge that repeats a stored fact that is still true (same source, target, relation) is merged instead of inserted.
- An edge that mentions an entity the LLM didn't list creates that entity with type `other`.
- Sending the same text twice adds a second artifact but no duplicate entities or edges.

A text of more than one chunk (1024 tokens by default) is a document: it is stored as an artifact, split into overlapping chunks at paragraph boundaries that are child artifacts of it, and each chunk is extracted, stored and embedded (into a dedicated chunk vector collection) individually. Details: [docs/chunking.md](docs/chunking.md).

The response returns the `artifact_id` and `kind`, the entities, the `edges` (with their temporal fields) and `invalidated_edges`. If any LLM call fails or its output is unusable, the request returns 502 and nothing is stored. Details: [docs/remember.md](docs/remember.md), [docs/artifacts.md](docs/artifacts.md).

### Web search

`POST /v1/gb/remember` with `"web_search": true` also searches the web for the latest world events about the key entities of the text (the ones in the most facts, at most 3), with Anthropic's cloud-based web search tool, so it is only available when an Anthropic API key is supplied (else `503`). Each result is stored as an artifact of its own, of kind `web_search`, that saves the query that was issued and whose parent is the artifact the entity came from; the entities and facts found in it are extracted and stored in the graph like any other. It is opt-in per call, runs after the text is stored and never fails the request, and every search costs a fee on top of the tokens. Details: [docs/web-search.md](docs/web-search.md).

### Models per stage

Timeseries: `POST /v1/gb/remember` takes an optional `timeseries` (`{"series": [{"key": "AAPL.close", "points": [["2025-01-31T00:00:00Z", 222.0]], "anchors": [{"entity": {"name": "Apple", "type": "organization"}}]}]}`) and stores the series next to the text, anchored to the artifact it creates and to the entities and facts its anchors select. The entities do not exist when the request is made, so an anchor names the entity it expects and is looked up once the text has been extracted and stored; a series that cannot be anchored stores nothing. `POST /v1/gb/timeseries` anchors series to what is already stored (by ID or selector). Points are kept apart from the graph (`gb_series`, `gb_series_points`, `gb_series_anchors`) and every series has stats computed in Go (min, max, change, trend, largest move). The caller supplies the series, or the server fetches them: a program embedding gligabao registers timeseries **providers** (a natural-language description of when each applies, plus a Go callback that fetches), and `timeseries.fetch` in a `remember` request lets an LLM decide which provider is used for which entity (a stock price series for a public company, say). With `timeseries.observations`, `remember` also derives a `series_observation` artifact from each series it stored: a text generated in Go from the series' stats, extracted into the graph like any text, so what a series says becomes facts (fact → observation artifact → series → anchoring artifact). `recall` does not use series yet. Details: [docs/timeseries.md](docs/timeseries.md), [docs/timeseries-design.md](docs/timeseries-design.md).

Each LLM-invoking stage of `remember` can run on its own model. `POST /v1/gb/remember` takes an optional `models` object with the stages `extract_graph` (the entities and edges), `resolve_edges` (the duplicate/contradiction check), `web_search` (the [web search](docs/web-search.md)), `resolve_series` (the choice of [timeseries providers](docs/timeseries.md#providers)) and `extract_image` (the image pipeline): `{"user_id": "u1", "text": "...", "models": {"extract_graph": "claude-sonnet-4-5", "resolve_edges": "claude-haiku-5-5"}}`. It can be given with every call. A stage you leave out uses the default model, so you can change one stage and keep the others. The model that runs `extract_graph` is what is recorded as the `model_id` of the entities and edges it yields (the extracted knowledge). The evals take the same choice: `models` in a case of a suite, or `go run ./evals -models extract_graph=...,resolve_edges=...`. Details: [docs/remember.md](docs/remember.md#choosing-the-model-of-each-stage), [docs/evals.md](docs/evals.md#models).

## Sessions

`remember` and `recall` take an optional `session_id`, a label for one user/agent run. `remember` stores it on the artifact (`gb_artifacts.session_id`) and records it against every entity and edge the artifact yields in `gb_entity_sessions` and `gb_edge_sessions`, **also when the entity or fact already existed** and no new row was written for it, so the sessions an entity or fact was discovered in are all kept. `recall` echoes it and puts it in its trajectory and monitoring event, so a recall can be connected to the artifacts of its session; it does not change the answer. A session has no state of its own (no cache, no expiry). Details: [docs/sessions.md](docs/sessions.md); what a Cognee-style session memory would add, and why it is not included: [docs/not-included.md](docs/not-included.md).

## Temporal facts

Graphiti's bi-temporal model. Every input is an **artifact** (`gb_artifacts`, with a `kind`): the raw text, `valid_at` (when it was written or observed, the `reference_time`) and `created_at` (ingestion time). The facts extracted from it are **edges** (`gb_edges`), each with:

- **Event time** (`valid_at`, `invalid_at`): when the fact became true and stopped being true in the world. `valid_at` inherits the artifact's time unless the text says otherwise; `invalid_at` is null while the fact is still true.
- **System time** (`created_at`, `expired_at`): when gligabao learned the fact, and when newer knowledge superseded it.

When a new fact contradicts a stored one, the older fact is closed (`invalid_at` = start of the new fact, `expired_at` = now) instead of being deleted, so history is preserved. If the news is about an earlier time than what is already known, the new fact is the one that gets closed. Example: "Alice works at Acme" (2023) followed by "Alice started working at Globex" (2024-03-01) leaves the Acme edge with `invalid_at` = 2024-03-01 and a new live edge for Globex.

`remember` makes 1 LLM call, or 2 when there are stored facts to compare against (each on its [own model](#models-per-stage), if the call chose them). `recall` shows each edge's validity to the LLM, so it can distinguish current from past facts. Details: [docs/temporal.md](docs/temporal.md).

## Recall

`POST /v1/gb/recall` with `{"user_id": "u1", "query": "Where does Alice work?"}` answers from the graph and returns the evidence it used.

1. **Retrieve**: the query is split into terms (stopwords and very short words are dropped). Entities are scored by keyword match against names (including whole-name matches such as "New York") and descriptions. The top 10 become *seeds*; if nothing matches, the 25 most recently updated entities are used so vague questions still work.
2. **Expand**: every edge touching a seed is added (newest first, up to 100) along with the entity at its other end, giving a one-hop subgraph.
3. **Answer**: the subgraph is rendered as text with IDs and sent to the LLM with the question. The model is told to answer only from the graph and to return JSON with the `answer` plus the IDs of the entities and edges it used.
4. **Verify evidence**: cited IDs are checked against the subgraph that was actually sent, so hallucinated IDs are dropped. The endpoints of every cited edge are included, and each edge carries its `artifact_id` so a fact can be traced to the original text.

If the graph is empty, a fixed "I don't have any information" answer is returned without calling the LLM. If the graph lacks the needed information, the answer says so. Details: [docs/recall.md](docs/recall.md).

## Graph export

`GET /v1/gb/datasets/{name}/graph?user_id=u1` returns the whole graph of a dataset as JSON: `dataset`, `entities` (`id`, `name`, `type`, `description`) and `edges` (`id`, `source_id`, `target_id`, `source`, `target`, `relation`, `description`, `artifact_id` and the bi-temporal fields). Edges reference entities by `source_id`/`target_id`. Superseded edges are included, with `invalid_at`/`expired_at` set, so the output is the full history, not just current facts. It makes no LLM call. An empty dataset returns empty lists; an unknown or deleted dataset returns 404. Details: [docs/graph.md](docs/graph.md).

## Database

The main database is SQLite (default, one file) or PostgreSQL, chosen with `db_backend` in the config:

```toml
db_backend = "postgres"
postgres_dsn = "postgres://user:password@localhost:5432/gligabao?sslmode=disable"   # or the POSTGRES_DSN env var
```

The schema is created on startup. SQLite takes its file from `serve --db` (default `bin/gligabao.db`). All SQL lives in the backend packages, each with its own schema and dialect; the rest of the code calls the routing functions, which dispatch to the backend in use. Datasets and the LLM call log are in `internal/db/relational` (`sqlite`, `postgres`); the knowledge graph (artifacts, entities, edges) is in `internal/db/graph` (currently `sqlite` and `postgres` tables in the same database; dedicated graph databases are meant to be added there). Tests run against SQLite, and also against PostgreSQL when `POSTGRES_TEST_DSN` is set (each test gets a temporary schema):

```sh
POSTGRES_TEST_DSN='postgres://user:pw@localhost:5432/gb?sslmode=disable' ./run_tests.sh
```

## Getting started

```sh
./run_build.sh                      # builds bin/gligabao
export ANTHROPIC_API_KEY=...        # required for remember/recall
./bin/gligabao serve                # listens on :7878 by default
./run_tests.sh                      # runs all tests
```

The Python sidecar (`side_car/`, a FastAPI service the server will talk to; today a skeleton with a health check) is built into its own image with `./run_build_sidecar.sh`. Details: [docs/sidecar.md](docs/sidecar.md).

Configuration is read from `bin/gligabao.toml` (override with `--config`). It sets `port`, and optionally `anthropic_api_key`, `openai_api_key` and `pinecone_api_key`, which default to the `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` and `PINECONE_API_KEY` environment variables. `db_backend` picks the main database, `"sqlite"` (default) or `"postgres"` with `postgres_dsn` (see [Database](#database)). `vector_store` picks where embeddings are stored (`internal/db/vector`, isolated by dataset, one per index: entities, edges, chunks): `"chromem"` (default; [chromem-go](https://github.com/philippgille/chromem-go), a pure-Go embedded vector database stored in a directory next to the main DB, override with `serve --vector-dir <path>`) or `"pinecone"` (hosted, independent of chromem). `embedding_dimensions` (default 1536) is the vector size, and `openai_api_key` is what `remember` uses to compute the embeddings (without it no vectors are stored); the chromem store fixes it when its directory is created and a Pinecone index must match it. Details: [docs/vectors.md](docs/vectors.md). For Pinecone, set `pinecone_index_host` (default: `PINECONE_INDEX_HOST`), the data-plane host of the index. A `[prices."<model-prefix>"]` table (`input`, `output`, optional `cache_write`/`cache_read`, USD per 1M tokens) overrides or extends the built-in model prices used to compute the cost of each LLM call; the longest matching prefix wins. Every model invocation (LLM call, embedding request, web search, Jev classification) is a row of the `gb_model_runs` table, with the model, the thinking level, the tokens and the cost (see [docs/model-runs.md](docs/model-runs.md)). `chunk_size` (default 1024 tokens) and `chunk_overlap_percent` (default 20) control the [chunking](docs/chunking.md) of long texts. `jev_api_key` (default: `JEV_API_KEY`) enables Jev, TypeSafe's system 1 model (`jev_base_url` and `jev_model` are optional overrides); when a key is set, `remember` first classifies the text as simple/medium/complex in the number of entities it will yield and logs it with Jev's confidence and the call's latency (see [docs/remember.md](docs/remember.md)).

### Eval mode

With `serve --eval` (or `eval = true` in the config; default `false`) the server also registers `POST /v1/gb/eval/remember` and `POST /v1/gb/eval/recall`. They take the same bodies as the regular endpoints, record the call as an [ATIF](https://github.com/laude-institute/harbor) trajectory, store it as pretty-printed JSON in an `atif/` directory next to the SQLite file, and return the full `trajectory` (and its `trajectory_id`) in the response. They are not registered otherwise, and the regular endpoints never generate trajectories. Details: [docs/evals.md](docs/evals.md). `go run ./evals` runs through these endpoints (`-atif <dir>` also keeps the trajectories as files).

### Admin UI

`admin_ui/` holds a small admin panel (TypeScript, SCSS, HTML). `./run_build.sh` builds it (needs node/npm) and embeds `admin_ui/dist` into the binary with `go:embed`; pass `--no-admin-ui` (or `BUILD_ADMIN_UI=false`) to skip it. With `admin_ui = true` in the config (or `serve --admin-ui`; default `false`) the server serves it at `/v1/gb/admin/`. If the assets were not embedded, the server logs a warning and does not register the handlers.

## Embedding in Go programs

gligabao is also a Go library. All server code is under `internal/` (not importable); the public API is the root package `github.com/deepblue-phoenix/gligabao`, which the CLI (`cmd/gligabao`, `internal/cli`) itself uses, so embedded and standalone behave the same.

```go
import "github.com/deepblue-phoenix/gligabao"

cfg, err := gligabao.LoadConfig("gligabao.toml") // or gligabao.DefaultConfig()
if err != nil { /* ... */ }
err = gligabao.Serve(ctx, cfg, gligabao.ServeOptions{DBPath: "data/gligabao.db"})
```

- `Config` / `Price`: the configuration described above; `DefaultConfig()` takes API keys from the environment, and fields can be set in code instead of a TOML file.
- `ServeOptions`: `DBPath` (default `bin/gligabao.db`), `VectorDir` (the chromem directory; default: next to `DBPath`, as `<name>.vectors`) and `ImageDir` (the image files; default: next to `DBPath`, as `<name>.images`), the equivalents of `serve --db` / `--vector-dir` / `--image-dir`.
- `ServeOptions.TimeseriesProviders`: the [timeseries providers](docs/timeseries.md#providers) `remember` can fetch from. Each is a `gligabao.TimeseriesProvider{Name, Description, QueryFormat, Fetch}`: `Description` says in natural language when the provider should be used (an LLM reads it to pick providers for entities), `Fetch` is your callback that returns the series.
- `ServeOptions.Hooks`: callbacks gligabao invokes, today the system-event hook (called after every `remember`, `recall` and timeseries request, with counts and timings, for logging and metrics). All the hooks available to embedding programs are documented in [docs/hooks.md](docs/hooks.md).
- `Serve(ctx, cfg, opts)` blocks until `ctx` is cancelled or SIGINT/SIGTERM arrives, then shuts down gracefully. A nil `cfg` means `DefaultConfig()`.

gligabao needs no CGO, so the embedding program can be built with `CGO_ENABLED=0` (see [Building](#building)). A working example with a cobra `serve` command is in the sibling `gligabao_int` project, which uses `replace github.com/deepblue-phoenix/gligabao => ../gligabao` during local development.

## License

See [LICENSE](LICENSE).

## Building

gligabao is pure Go and builds with `CGO_ENABLED=0`: SQLite uses the `modernc.org/sqlite` driver and the default vector store is [chromem-go](https://github.com/philippgille/chromem-go), so no C compiler is needed. `./run_build.sh` builds `bin/gligabao`, `./run_tests.sh` runs the tests, and `docker build .` (or `./run_build_container.sh`) builds a static binary in a container; the Dockerfile builds with `CGO_ENABLED=0`. Plain `go build ./cmd/gligabao` / `go test ./...` work as they are.

## Versioning

The version is in [`VERSION`](VERSION) (semantic version) and the history in [`CHANGELOG.md`](CHANGELOG.md) ([Keep a Changelog](https://keepachangelog.com/) format). The version is embedded into the binary at build time (`go:embed`, see `version.go`), so a deployed binary always knows it: `gligabao --version`, `gligabao version` and `GET /version` (JSON, includes the git commit stamped by `go build`). Programs embedding gligabao get `gligabao.Version()` and can pass their own version in `ServeOptions.App`. Every change bumps `VERSION` and adds a changelog entry; `go test` fails if the two disagree. The rules for people and coding agents are in [AGENTS.md](AGENTS.md).
