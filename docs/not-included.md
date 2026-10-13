# Not included

Things gligabao deliberately does not do (or does not do yet), with the reasoning. This is a living list: add an entry when a feature is considered and left out.

## Session memory

> **What is included, and what is not.** gligabao does have a `session_id`: an optional label on `remember` and `recall` that ties artifacts, and the entities and edges extracted from them, to the user/agent session they came from (see [sessions.md](sessions.md)). That is *provenance*: it records where things came from and changes no behaviour. What is left out is session *memory* as described below: a per-session cache of recent turns, consulted before graph recall, with a TTL and a flush into the graph.

**The concept.** Session memory (as in [Cognee](https://github.com/topoteretes/cognee)) is a short-term, per-conversation layer that sits next to the long-term knowledge graph. Each turn of a chat (question, retrieved context, answer) is written to a fast, ephemeral cache keyed by a `session_id`. On the next `recall` in the same session, the system first looks in that cache, and only if nothing relevant is there does it fall through to the full pipeline of vector seeding and graph traversal. The cache is what lets a follow-up like "what did I just ask?" or "run that again" be answered without touching the graph, and what lets a standalone chatbot keep its recent context without running a separate store such as Redis. Cognee can later promote session content into the permanent graph (a "memify"/consolidation step), so the session acts as a staging area between raw dialogue and structured knowledge.

**What it would look like here.** Building on the `session_id` that `remember` and `recall` already accept: a bounded store of the last N turns per `(user_id, dataset, session_id)` with a TTL; a cheap lookup over those turns before graph recall (exact hash, BM25 or keyword overlap); and an explicit way to flush a session into the graph as `episode` artifacts.

**Why it is not included.** gligabao is a long-term memory service, not a chat backend. The `remember` / `recall` API only labels a call with a session (`session_id`, see [sessions.md](sessions.md)); it has no notion of a conversation's state, and `recall` is stateless: the same query against the same graph gives the same answer, whichever call came before it, with or without a `session_id`. Adding a session cache would add mutable, expiring, per-conversation state to a service whose storage is otherwise a single, auditable, bi-temporal graph. The cases session memory serves are already covered elsewhere:

- The caller already holds the live conversation. An agent harness (Pi, LangChain, Cursor, a hand-written chatbot) keeps the active window (`messages = [...]`) in its own memory. A second copy of the recent turns inside the memory service can only drift from what the model actually sees, creating sync problems (which turns were trimmed, summarised or edited?) without adding information.
- Conversation turns can go into the graph directly. Following Graphiti, a turn is an `episode` artifact (`kind: "episode"`, with `reference_time`). It is anchored in time, its facts are bi-temporal (`valid_at` / `invalid_at`), and it supersedes older facts when it contradicts them. There is no separate "session cache" versus "long-term graph" split: the graph is the one place dialogue lives, and temporal reasoning works on it turn by turn. See [artifacts.md](artifacts.md) and [temporal.md](temporal.md).
- It is not the core of what gligabao offers. Cognee positions session memory as a convenience layer bolted on for quick standalone bots; its real differentiator is the ontology-grounded knowledge pipeline and graph retrieval. gligabao is in the same position, so effort goes into extraction, deduplication, temporal reconciliation and recall quality instead.

### Rationale: is session memory a "lesser" feature?

It depends on where the architectural boundaries are drawn.

- **In Cognee's philosophy:** it is an auxiliary convenience layer. Cognee primarily views itself as an ontology-grounded knowledge pipeline and GraphRAG engine. Session memory was added so developers building quick standalone chatbots would not need a separate Redis setup to remember the last turn. It is not Cognee's core technical differentiator.
- **Why Graphiti has no session cache:** Graphiti does not separate a session cache from the long-term graph. Every conversational turn is an Episode node inside the graph. With bi-temporal tracking (`valid_at`, `invalid_at`), it can reason about real-time, turn-by-turn dialogue in the graph without an ephemeral side cache.
- **In agent harness design (Pi, LangChain, Cursor):** session memory in the database is often redundant. The harness already holds the active conversation window in local memory or an execution context. Offloading conversation history to a knowledge engine can desynchronise what the model sees from what the database has cached.

### Rationale: is exact / keyword matching useful?

In plain natural-language dialogue, exact string matching rarely hits, because users seldom repeat the same phrase:

- Turn 1: "Set the server port to 8080."
- Turn 2: "What port did we configure earlier?"

A naive exact match of turn 2 against the turn history has a 0% hit rate. Lexical checks in session caches still exist for practical reasons:

1. **Repetitive tool calls and idempotent commands.** Agent workflows (coding, shell execution) often repeat identical commands or queries (`git status`, `read_file config.json`, an error code, a function name). A hash or exact-token lookup answers these in about 0.1 ms at no API cost.
2. **Deterministic fast-fail.** An exact or BM25 check over the last 5 turns costs almost nothing. If the user says "repeat that", "run test", or names an exact symbol, the cache catches it; otherwise the system falls through to vector seeding and graph traversal.
3. **BM25 / keyword overlap rather than verbatim match.** Production keyword caches usually use tokenized BM25 or an inverted index, not `query == past_query`. If a prompt mentions a unique noun or ID from three messages ago (e.g. `Order_98412`), lexical matching hits accurately without paying for embedding or graph-traversal latency.

These benefits are real but small, and they belong to a cache in front of the model, which is the harness's job. gligabao's `recall` already finds entities by vector similarity over the graph, and a specific name or ID that was remembered as an entity is found that way.

### Practical verdict

- Let the **harness** (the application's own `messages` array) hold the immediate 5 to 10 conversation turns.
- Pass the same `session_id` to `remember` and `recall` when you want to know afterwards which artifacts, entities and facts (and recalls) belong to one run. The label is stored with the artifacts and, for entities and edges, in `gb_entity_sessions` and `gb_edge_sessions`, even when a repeated entity or fact was not written again. It is bookkeeping, not memory: it never makes `recall` return something different.
- Use **gligabao** for what it is for: `remember` to extract entities and temporal relations into the knowledge graph (sending turns as `episode` artifacts when dialogue should be remembered), and `recall` to seed on and traverse that graph for deeper background context.

### If this is revisited

The least invasive option is to keep the session cache outside the graph's core tables: the existing `session_id` would scope a small, TTL-bound turn buffer used only as a pre-check before graph recall, with an explicit flush into `episode` artifacts (the flushed artifacts would then carry the `session_id` like any other). Anything that makes `recall` results depend on hidden per-session state should be weighed against the stateless, reproducible behaviour that exists today.
