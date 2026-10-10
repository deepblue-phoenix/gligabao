# gligabao
<img width="800" height="447" alt="image" src="https://github.com/user-attachments/assets/a4cf6b9f-2bdb-4c16-8ed4-558eca33e4a7" />

Minimal memory system for AI.

gligabao is a small, self-hosted HTTP service that gives an AI application long-term memory. You send it free-form text, and it turns that text into a knowledge graph. Later you ask it questions in natural language and it answers from what it stored, citing the facts it used.

## How it works

- **Remember**: an LLM reads the text and extracts *entities* (people, organizations, places, ...) and *edges* (relations such as `works_at` between two entities). Entities are deduplicated, and each edge is linked back to the original text it came from. Following [Graphiti](https://github.com/getzep/graphiti), each text is stored as a timestamped *episode*, and every edge is a bi-temporal fact that records when it was true.
- **Recall**: the query is used to find relevant entities in the graph, the surrounding subgraph is expanded by one hop, and an LLM answers using only that context. The response includes the evidence (entities and edges) behind the answer.
- **Storage**: SQLite (a single file, the default) or PostgreSQL; see [Database](#database).

## API

| Endpoint | Purpose | Docs |
| -------- | ------- | ---- |
| `POST /v1/gb/remember` | Extract a temporal knowledge graph from text and store it | [docs/remember.md](docs/remember.md), [docs/temporal.md](docs/temporal.md) |
| `POST /v1/gb/recall` | Answer a question from the graph, with evidence | [docs/recall.md](docs/recall.md) |
| `POST /v1/gb/datasets`, `GET /v1/gb/datasets?user_id=`, `DELETE /v1/gb/datasets/{name}?user_id=` | Create, list and (soft) delete datasets | [docs/datasets.md](docs/datasets.md) |
| `GET /health` | Health check | |

## Datasets

Memory is organized in datasets: a hard container for everything stored (episodes, entities, edges). Every dataset has an owner, a `user_id`, which covers everything in it; all endpoints require `user_id` (in the JSON body, or the query string for `GET`/`DELETE`), and dataset names are unique per user. `remember` and `recall` take an optional `"dataset"` name, default `main`. Datasets are isolated: the same entity in two datasets is two separate nodes, and a recall only sees its own dataset's graph. Deleting a dataset is soft (`deleted = true`). Details: [docs/datasets.md](docs/datasets.md), [docs/identity.md](docs/identity.md).

## Remember

`POST /v1/gb/remember` with `{"user_id": "u1", "text": "Alice works at Acme and lives in Paris."}` stores the text as an episode and adds the facts it states to the graph, with the time they became true. An optional `reference_time` (RFC 3339, default now) says when the text was written, so relative dates in it can be resolved.

1. **Extract**: the text is sent to an LLM (Anthropic, default `claude-haiku-5-5`), which returns JSON listing entities (`name`, `type`, `description`) and edges (`source`, `target`, `relation`, `description`, `valid_at`, `invalid_at`).
2. **Normalize**: entity types are lowercased (empty becomes `other`), and relations are lowercased with spaces replaced by `_`.
3. **Reconcile**: new edges are compared with stored live edges that touch the same entities (see [Temporal facts](#temporal-facts)).
4. **Store**: in one transaction, the raw text goes into `gb_memories` (the episode), entities are upserted into `gb_entities`, and edges are inserted into `gb_edges` (each pointing back to its episode).

Deduplication:
- Entities are unique on (normalized name, type). Re-submitting one only updates its description if the new one is non-empty.
- A new edge that repeats a stored fact that is still true (same source, target, relation) is merged instead of inserted.
- An edge that mentions an entity the LLM didn't list creates that entity with type `other`.
- Sending the same text twice adds a second episode but no duplicate entities or edges.

The response returns the `memory_id`, the entities, the `edges` (with their temporal fields) and `invalidated_edges`. If any LLM call fails or its output is unusable, the request returns 502 and nothing is stored. Details: [docs/remember.md](docs/remember.md).

## Temporal facts

Graphiti's bi-temporal model. Every input is an **episode** (`gb_memories`): the raw text, `valid_at` (when it was written or observed, the `reference_time`) and `created_at` (ingestion time). The facts extracted from it are **edges** (`gb_edges`), each with:

- **Event time** (`valid_at`, `invalid_at`): when the fact became true and stopped being true in the world. `valid_at` inherits the episode's time unless the text says otherwise; `invalid_at` is null while the fact is still true.
- **System time** (`created_at`, `expired_at`): when gligabao learned the fact, and when newer knowledge superseded it.

When a new fact contradicts a stored one, the older fact is closed (`invalid_at` = start of the new fact, `expired_at` = now) instead of being deleted, so history is preserved. If the news is about an earlier time than what is already known, the new fact is the one that gets closed. Example: "Alice works at Acme" (2023) followed by "Alice started working at Globex" (2024-03-01) leaves the Acme edge with `invalid_at` = 2024-03-01 and a new live edge for Globex.

`remember` makes 1 LLM call, or 2 when there are stored facts to compare against. `recall` shows each edge's validity to the LLM, so it can distinguish current from past facts. Details: [docs/temporal.md](docs/temporal.md).

## Recall

`POST /v1/gb/recall` with `{"user_id": "u1", "query": "Where does Alice work?"}` answers from the graph and returns the evidence it used.

1. **Retrieve**: the query is split into terms (stopwords and very short words are dropped). Entities are scored by keyword match against names (including whole-name matches such as "New York") and descriptions. The top 10 become *seeds*; if nothing matches, the 25 most recently updated entities are used so vague questions still work.
2. **Expand**: every edge touching a seed is added (newest first, up to 100) along with the entity at its other end, giving a one-hop subgraph.
3. **Answer**: the subgraph is rendered as text with IDs and sent to the LLM with the question. The model is told to answer only from the graph and to return JSON with the `answer` plus the IDs of the entities and edges it used.
4. **Verify evidence**: cited IDs are checked against the subgraph that was actually sent, so hallucinated IDs are dropped. The endpoints of every cited edge are included, and each edge carries its `memory_id` so a fact can be traced to the original text.

If the graph is empty, a fixed "I don't have any information" answer is returned without calling the LLM. If the graph lacks the needed information, the answer says so. Details: [docs/recall.md](docs/recall.md).

## Database

The main database is SQLite (default, one file) or PostgreSQL, chosen with `db_backend` in the config:

```toml
db_backend = "postgres"
postgres_dsn = "postgres://user:password@localhost:5432/gligabao?sslmode=disable"   # or the POSTGRES_DSN env var
```

The schema is created on startup. SQLite takes its file from `serve --db` (default `bin/gligabao.db`). All SQL lives in the backend packages (`internal/db/sqlite`, `internal/db/postgres`), each with its own schema and dialect; the rest of the code calls the routing functions in `internal/db`, which dispatch to the backend in use. Tests run against SQLite, and also against PostgreSQL when `POSTGRES_TEST_DSN` is set (each test gets a temporary schema):

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

Configuration is read from `bin/gligabao.toml` (override with `--config`). It sets `port`, and optionally `anthropic_api_key`, `openai_api_key` and `pinecone_api_key`, which default to the `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` and `PINECONE_API_KEY` environment variables. `db_backend` picks the main database, `"sqlite"` (default) or `"postgres"` with `postgres_dsn` (see [Database](#database)). `vector_store` picks where embeddings are stored (`internal/vectordb`, one namespace per dataset): `"sqlite"` (default; [sqlite-vec](https://github.com/asg017/sqlite-vec) in a separate file next to the main DB, override with `serve --vector-db <path>`) or `"pinecone"`. `embedding_dimensions` (default 1536) is the vector size; the sqlite store fixes it when its file is created and a Pinecone index must match it. For Pinecone, set `pinecone_index_host` (default: `PINECONE_INDEX_HOST`), the data-plane host of the index. A `[prices."<model-prefix>"]` table (`input`, `output`, optional `cache_write`/`cache_read`, USD per 1M tokens) overrides or extends the built-in model prices used to compute the cost of each LLM call; the longest matching prefix wins. Each call's tokens and cost are stored in the `gb_llm_calls` table.

## License

See [LICENSE](LICENSE).

## Building

The sqlite-vec vector store needs CGO (a C compiler such as gcc/clang) and `mattn/go-sqlite3`. sqlite-vec's Go bindings include `sqlite3.h`, so builds need `CGO_ENABLED=1` and that header on the include path; `source ./cgo_env.sh` sets both. `run_build.sh`, `run_tests.sh`, `run_evals.sh` and the Dockerfile already do this. For a plain `go build`/`go test`, run `source ./cgo_env.sh` first.
