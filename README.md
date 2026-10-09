# gligabao

Minimal memory system for AI.

gligabao is a small, self-hosted HTTP service that gives an AI application long-term memory. You send it free-form text, and it turns that text into a knowledge graph. Later you ask it questions in natural language and it answers from what it stored, citing the facts it used.

## How it works

- **Remember**: an LLM reads the text and extracts *entities* (people, organizations, places, ...) and *edges* (relations such as `works_at` between two entities). Entities are deduplicated, and each edge is linked back to the original text it came from.
- **Recall**: the query is used to find relevant entities in the graph, the surrounding subgraph is expanded by one hop, and an LLM answers using only that context. The response includes the evidence (entities and edges) behind the answer.
- **Storage**: everything lives in a single SQLite file.

## API

| Endpoint | Purpose | Docs |
| -------- | ------- | ---- |
| `POST /v1/gb/remember` | Extract a knowledge graph from text and store it | [docs/remember.md](docs/remember.md) |
| `POST /v1/gb/recall` | Answer a question from the graph, with evidence | [docs/recall.md](docs/recall.md) |
| `GET /health` | Health check | |

## Project layout

- `main.go`, `cmd/`: CLI entry point (cobra); `gligabao serve` starts the server.
- `internal/server`: HTTP server setup.
- `internal/api`: HTTP handlers.
- `internal/memory`: remember/recall logic.
- `internal/ai`: LLM (Anthropic) and embeddings (OpenAI) clients.
- `internal/db`: SQLite connection and schema.
- `internal/config`: TOML config loading.

## Getting started

```sh
./run_build.sh                      # builds bin/gligabao
export ANTHROPIC_API_KEY=...        # required for remember/recall
./bin/gligabao serve                # listens on :8080 by default
./run_tests.sh                      # runs all tests
```

Configuration is read from `bin/gligabao.toml` (override with `--config`). It sets `port`, and optionally `anthropic_api_key` and `openai_api_key`, which default to the `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` environment variables. The SQLite file defaults to `bin/gligabao.db` (override with `serve --db <path>`).

## License

See [LICENSE](LICENSE).
