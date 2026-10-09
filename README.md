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

## Remember

`POST /v1/gb/remember` with `{"text": "Alice works at Acme and lives in Paris."}` stores the text as a memory and adds what it says to the graph.

1. **Extract**: the text is sent to an LLM (Anthropic, default `claude-haiku-4-5`, temperature 0), which returns JSON listing entities (`name`, `type`, `description`) and edges (`source`, `target`, `relation`, `description`).
2. **Normalize**: entity types are lowercased (empty becomes `other`), and relations are lowercased with spaces replaced by `_`.
3. **Store**: in one transaction, the raw text goes into `gb_memories`, entities are upserted into `gb_entities`, and edges are upserted into `gb_edges`, each pointing back to its source memory.

Deduplication:
- Entities are unique on (normalized name, type). Re-submitting one only updates its description if the new one is non-empty.
- Edges are unique on (source, target, relation).
- An edge that mentions an entity the LLM didn't list creates that entity with type `other`.
- Sending the same text twice adds a second memory row but no duplicate entities or edges.

The response returns the `memory_id` and the entities and edges that were extracted. If the LLM fails or its output is unusable, the request returns 502 and nothing is stored. Details: [docs/remember.md](docs/remember.md).

## Recall

`POST /v1/gb/recall` with `{"query": "Where does Alice work?"}` answers from the graph and returns the evidence it used.

1. **Retrieve**: the query is split into terms (stopwords and very short words are dropped). Entities are scored by keyword match against names (including whole-name matches such as "New York") and descriptions. The top 10 become *seeds*; if nothing matches, the 25 most recently updated entities are used so vague questions still work.
2. **Expand**: every edge touching a seed is added (newest first, up to 100) along with the entity at its other end, giving a one-hop subgraph.
3. **Answer**: the subgraph is rendered as text with IDs and sent to the LLM with the question. The model is told to answer only from the graph and to return JSON with the `answer` plus the IDs of the entities and edges it used.
4. **Verify evidence**: cited IDs are checked against the subgraph that was actually sent, so hallucinated IDs are dropped. The endpoints of every cited edge are included, and each edge carries its `memory_id` so a fact can be traced to the original text.

If the graph is empty, a fixed "I don't have any information" answer is returned without calling the LLM. If the graph lacks the needed information, the answer says so. Details: [docs/recall.md](docs/recall.md).

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
