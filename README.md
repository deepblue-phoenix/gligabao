# gligabao

Minimal memory system for AI.

gligabao is a small, self-hosted HTTP service that gives an AI application long-term memory. You send it free-form text, and it turns that text into a knowledge graph. Later you ask it questions in natural language and it answers from what it stored, citing the facts it used.

## How it works

- **Remember**: an LLM reads the text and extracts *entities* (people, organizations, places, ...) and *edges* (relations such as `works_at` between two entities). Entities are deduplicated, and each edge is linked back to the original text it came from. A separate pipeline extracts dated *events* and tracks when they were true.
- **Recall**: the query is used to find relevant entities in the graph, the surrounding subgraph is expanded by one hop, and an LLM answers using only that context. The response includes the evidence (entities and edges) behind the answer.
- **Storage**: everything lives in a single SQLite file.

## API

| Endpoint | Purpose | Docs |
| -------- | ------- | ---- |
| `POST /v1/gb/remember` | Extract a knowledge graph and temporal events from text and store them | [docs/remember.md](docs/remember.md), [docs/events.md](docs/events.md) |
| `POST /v1/gb/recall` | Answer a question from the graph, with evidence | [docs/recall.md](docs/recall.md) |
| `GET /health` | Health check | |

## Remember

`POST /v1/gb/remember` with `{"text": "Alice works at Acme and lives in Paris."}` stores the text as a memory and adds what it says to the graph and to the event timeline. An optional `reference_time` (RFC 3339, default now) says when the text was written, so relative dates in it can be resolved.

1. **Extract**: the text is sent to an LLM (Anthropic, default `claude-haiku-4-5`, temperature 0), which returns JSON listing entities (`name`, `type`, `description`) and edges (`source`, `target`, `relation`, `description`).
2. **Normalize**: entity types are lowercased (empty becomes `other`), and relations are lowercased with spaces replaced by `_`.
3. **Events**: a second pipeline extracts and reconciles temporal events (see [Events](#events)).
4. **Store**: in one transaction, the raw text goes into `gb_memories`, entities are upserted into `gb_entities`, edges are upserted into `gb_edges` (each pointing back to its source memory), and events are written to `gb_events`.

Deduplication:
- Entities are unique on (normalized name, type). Re-submitting one only updates its description if the new one is non-empty.
- Edges are unique on (source, target, relation).
- An edge that mentions an entity the LLM didn't list creates that entity with type `other`.
- Sending the same text twice adds a second memory row but no duplicate entities or edges.

The response returns the `memory_id`, the entities and edges that were extracted, and the `events` (plus `invalidated_events`). If any LLM call fails or its output is unusable, the request returns 502 and nothing is stored. Details: [docs/remember.md](docs/remember.md).

## Events

Each `remember` call also extracts *events*: things that happened, started, ended, changed or were planned ("Alice joined Acme in March 2023"). They live in their own tables (`gb_events`, `gb_event_participants`) and follow the bi-temporal model used by [Graphiti](https://github.com/getzep/graphiti):

- **Event time** (`valid_at`, `invalid_at`): when the event started and stopped being true in the world. Null means unknown, or still true.
- **System time** (`created_at`, `expired_at`): when gligabao learned about it, and when newer knowledge superseded it.

1. **Extract**: an LLM call receives the text, the reference time and the entities found in the same text. It returns events with a name, description, participants and dates. Relative dates ("last June") are resolved against the reference time, and dates are never guessed.
2. **Find candidates**: stored events that are still live and share a participant with the new ones (at most 50).
3. **Resolve**: exact repeats are detected in code; for the rest an LLM call flags duplicates and contradictions. This call is skipped when there is nothing to compare.
4. **Store**: duplicates are merged (filling in missing dates). When a new event contradicts an older one, the one that started earlier is closed (`invalid_at` set to the start of the later one, `expired_at` set to now) instead of being deleted, so history is preserved. If the news is about an earlier time than what is already known, the new event is the one that gets closed.

Example: after "Alice joined Acme in 2020" and later "Alice joined Globex in June 2023", the Acme event stays in the database with `invalid_at` = 2023-06-01 and is reported in `invalidated_events`.

Events add one LLM call to every `remember`, or two when there are stored events to compare against. `recall` does not use events yet. Details: [docs/events.md](docs/events.md).

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
