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
| `POST /v1/gb/remember` | Extract a temporal knowledge graph from text and store it | [docs/remember.md](docs/remember.md), [docs/artifacts.md](docs/artifacts.md), [docs/temporal.md](docs/temporal.md) |
| `POST /v1/gb/recall` | Answer a question from the graph, with evidence | [docs/recall.md](docs/recall.md) |
| `POST /v1/gb/datasets`, `GET /v1/gb/datasets?user_id=`, `DELETE /v1/gb/datasets/{name}?user_id=` | Create, list and (soft) delete datasets | [docs/datasets.md](docs/datasets.md) |
| `GET /v1/gb/datasets/{name}/graph?user_id=` | Export a dataset's whole graph as JSON | [docs/graph.md](docs/graph.md) |
| `POST /v1/gb/ontologies`, `PUT /v1/gb/ontologies/{name}` | Load an OWL ontology (RDF/XML) under a name, or update the one stored under it (merge or replace) | [docs/ontologies.md](docs/ontologies.md) |
| `GET /health` | Health check | |

## Datasets

Memory is organized in datasets: a hard container for everything stored (artifacts, entities, edges). Every dataset has an owner, a `user_id`, which covers everything in it; all endpoints require `user_id` (in the JSON body, or the query string for `GET`/`DELETE`), and dataset names are unique per user. `remember` and `recall` take an optional `"dataset"` name, default `main`. Datasets are isolated: the same entity in two datasets is two separate nodes, and a recall only sees its own dataset's graph. Deleting a dataset is soft (`deleted = true`) and also removes its embeddings, which are stored in a per-dataset vector namespace. Details: [docs/datasets.md](docs/datasets.md), [docs/identity.md](docs/identity.md), [docs/vectors.md](docs/vectors.md).

## Ontologies

An ontology describes the concepts of a domain and how they relate. `POST /v1/gb/ontologies` with `{"user_id": "u1", "name": "market", "content": "<rdf:RDF ...>"}` loads an OWL ontology written in RDF/XML under a name (unique per user). Its metadata (name, owner, IRI, SHA-256 of the document, counts) is tracked in the relational database (`gb_ontologies`), and the ontology itself is loaded into the graph database as a graph of its own: classes become nodes, and `rdfs:subClassOf` and object properties (domain to range) become edges. `PUT /v1/gb/ontologies/{name}` updates the one stored under that name: by default the supplied classes and properties are merged into it (new ones are added, ones that exist already are overwritten, nothing is deleted), and with `"replace": true` the supplied ontology replaces it entirely. It is separate from the memory graph, and `remember` and `recall` do not use it yet. [`ontology/market.owl`](ontology/market.owl) is a basic ontology of markets with the 20 most important concepts and their relationships. Details: [docs/ontologies.md](docs/ontologies.md).

## Remember

`POST /v1/gb/remember` with `{"user_id": "u1", "text": "Alice works at Acme and lives in Paris."}` stores the text as an artifact and adds the facts it states to the graph, with the time they became true. An optional `kind` (`knowledge`, the default, or `episode`) says what sort of artifact it is. An optional `reference_time` (RFC 3339, default now) says when the text was written, so relative dates in it can be resolved.

1. **Extract**: the text is sent to an LLM (Anthropic, default `claude-haiku-5-5`), which returns JSON listing entities (`name`, `type`, `description`) and edges (`source`, `target`, `relation`, `description`, `valid_at`, `invalid_at`).
2. **Normalize**: entity types are lowercased (empty becomes `other`), and relations are lowercased with spaces replaced by `_`.
3. **Reconcile**: new edges are compared with stored live edges that touch the same entities (see [Temporal facts](#temporal-facts)).
4. **Store**: in one transaction, the raw text goes into `gb_artifacts`, entities are upserted into `gb_entities` and linked to the artifact in `gb_entity_artifacts` (an entity is shared by every artifact that mentions it), and edges are inserted into `gb_edges` (each with the `artifact_id` that created it, and linked in `gb_edge_artifacts`; a repeated fact adds the new artifact to the stored edge's links instead of being inserted again).

Deduplication:
- Entities are unique on (normalized name, type). Re-submitting one only updates its description if the new one is non-empty.
- A new edge that repeats a stored fact that is still true (same source, target, relation) is merged instead of inserted.
- An edge that mentions an entity the LLM didn't list creates that entity with type `other`.
- Sending the same text twice adds a second artifact but no duplicate entities or edges.

The response returns the `artifact_id` and `kind`, the entities, the `edges` (with their temporal fields) and `invalidated_edges`. If any LLM call fails or its output is unusable, the request returns 502 and nothing is stored. Details: [docs/remember.md](docs/remember.md), [docs/artifacts.md](docs/artifacts.md).

## Temporal facts

Graphiti's bi-temporal model. Every input is an **artifact** (`gb_artifacts`, with a `kind`): the raw text, `valid_at` (when it was written or observed, the `reference_time`) and `created_at` (ingestion time). The facts extracted from it are **edges** (`gb_edges`), each with:

- **Event time** (`valid_at`, `invalid_at`): when the fact became true and stopped being true in the world. `valid_at` inherits the artifact's time unless the text says otherwise; `invalid_at` is null while the fact is still true.
- **System time** (`created_at`, `expired_at`): when gligabao learned the fact, and when newer knowledge superseded it.

When a new fact contradicts a stored one, the older fact is closed (`invalid_at` = start of the new fact, `expired_at` = now) instead of being deleted, so history is preserved. If the news is about an earlier time than what is already known, the new fact is the one that gets closed. Example: "Alice works at Acme" (2023) followed by "Alice started working at Globex" (2024-03-01) leaves the Acme edge with `invalid_at` = 2024-03-01 and a new live edge for Globex.

`remember` makes 1 LLM call, or 2 when there are stored facts to compare against. `recall` shows each edge's validity to the LLM, so it can distinguish current from past facts. Details: [docs/temporal.md](docs/temporal.md).

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
./bin/gligabao serve                # listens on :8080 by default
./run_tests.sh                      # runs all tests
```

Configuration is read from `bin/gligabao.toml` (override with `--config`). It sets `port`, and optionally `anthropic_api_key`, `openai_api_key` and `pinecone_api_key`, which default to the `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` and `PINECONE_API_KEY` environment variables. `db_backend` picks the main database, `"sqlite"` (default) or `"postgres"` with `postgres_dsn` (see [Database](#database)). `vector_store` picks where embeddings are stored (`internal/db/vector`, one namespace per dataset): `"chromem"` (default; [chromem-go](https://github.com/philippgille/chromem-go), a pure-Go embedded vector database stored in a directory next to the main DB, override with `serve --vector-dir <path>`) or `"pinecone"` (hosted, independent of chromem). `embedding_dimensions` (default 1536) is the vector size; the chromem store fixes it when its directory is created and a Pinecone index must match it. Details: [docs/vectors.md](docs/vectors.md). For Pinecone, set `pinecone_index_host` (default: `PINECONE_INDEX_HOST`), the data-plane host of the index. A `[prices."<model-prefix>"]` table (`input`, `output`, optional `cache_write`/`cache_read`, USD per 1M tokens) overrides or extends the built-in model prices used to compute the cost of each LLM call; the longest matching prefix wins. Each call's tokens and cost are stored in the `gb_llm_calls` table.

### Eval mode

With `serve --eval` (or `eval = true` in the config; default `false`) the server also registers `POST /v1/gb/eval/remember` and `POST /v1/gb/eval/recall`. They take the same bodies as the regular endpoints, record the call as an [ATIF](https://github.com/laude-institute/harbor) trajectory, store it as pretty-printed JSON in an `atif/` directory next to the SQLite file, and return the full `trajectory` (and its `trajectory_id`) in the response. They are not registered otherwise, and the regular endpoints never generate trajectories. Details: [docs/evals.md](docs/evals.md). `go run ./evals` runs through these endpoints (`-atif <dir>` also keeps the trajectories as files).

## Embedding in Go programs

gligabao is also a Go library. All server code is under `internal/` (not importable); the public API is the root package `github.com/deepblue-phoenix/gligabao`, which the CLI (`cmd/gligabao`, `internal/cli`) itself uses, so embedded and standalone behave the same.

```go
import "github.com/deepblue-phoenix/gligabao"

cfg, err := gligabao.LoadConfig("gligabao.toml") // or gligabao.DefaultConfig()
if err != nil { /* ... */ }
err = gligabao.Serve(ctx, cfg, gligabao.ServeOptions{DBPath: "data/gligabao.db"})
```

- `Config` / `Price`: the configuration described above; `DefaultConfig()` takes API keys from the environment, and fields can be set in code instead of a TOML file.
- `ServeOptions`: `DBPath` (default `bin/gligabao.db`) and `VectorDir` (the chromem directory; default: next to `DBPath`, as `<name>.vectors`), the equivalents of `serve --db` / `--vector-dir`.
- `Serve(ctx, cfg, opts)` blocks until `ctx` is cancelled or SIGINT/SIGTERM arrives, then shuts down gracefully. A nil `cfg` means `DefaultConfig()`.

gligabao needs no CGO, so the embedding program can be built with `CGO_ENABLED=0` (see [Building](#building)). A working example with a cobra `serve` command is in the sibling `gligabao_int` project, which uses `replace github.com/deepblue-phoenix/gligabao => ../gligabao` during local development.

## License

See [LICENSE](LICENSE).

## Building

gligabao is pure Go and builds with `CGO_ENABLED=0`: SQLite uses the `modernc.org/sqlite` driver and the default vector store is [chromem-go](https://github.com/philippgille/chromem-go), so no C compiler is needed. `./run_build.sh` builds `bin/gligabao`, `./run_tests.sh` runs the tests, and `docker build .` (or `./run_build_container.sh`) builds a static binary in a container; the Dockerfile builds with `CGO_ENABLED=0`. Plain `go build ./cmd/gligabao` / `go test ./...` work as they are.
