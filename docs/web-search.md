# Web search

`POST /v1/gb/remember` can enrich what it remembers with the **latest world events** about the key entities of the text. With `"web_search": true`, after the text is stored as usual, the pipeline searches the web for the entities the text is about, and remembers what it finds: each result is an [artifact](artifacts.md) of its own, of kind `web_search`, and the entities and facts it mentions are stored in the graph like any other. The request, the response and the rest of the pipeline are in [remember.md](remember.md).

Code: `internal/memory/memory_remember/remember_websearch.go` (the stage), `internal/ai/websearch.go` (the Anthropic call), `internal/db/graph/` (the artifact).

## When it is available

Web search uses Anthropic's **cloud-based web search tool** (`web_search_20250305`, a *server tool*: Anthropic runs the searches and reads the pages, gligabao only sends one Messages API request per search and gets the answer back). It needs nothing but the Anthropic API key that `remember` needs anyway, so it is available **only when an Anthropic API key is supplied** (`ANTHROPIC_API_KEY`, or `anthropic_api_key` in `gligabao.toml`): the server then logs `web search is available` at startup. Without the key `remember` itself returns `503`, and a Go caller that builds the pipeline without a searcher gets `memory_core.ErrWebSearchUnavailable` if it asks for a web search.

Your Anthropic organization must have web search enabled (a setting in the Anthropic Console), and the model that runs the search must support the tool.

It is **off unless asked for**, per call: nothing is searched for a request that does not set `web_search`.

## Request

```json
{"user_id": "u1", "text": "Apple, Microsoft and Nvidia are among the largest companies in the world.", "web_search": true}
```

- `web_search` (optional, default `false`): search the web for the key entities of this text. `503` if web search is not available (no Anthropic API key). `400` together with `image` (the image pipeline extracts no entities yet).
- `models.web_search` (optional): the model that runs the searches (see [Choosing the model of each stage](remember.md#choosing-the-model-of-each-stage)); a stage left out uses the default model. It must support Anthropic's web search tool. The graph is extracted from the results by the `extract_graph` model, like any text.

## What it does

It is the last stage of `remember`, and runs after the text (or, for a long text, every chunk) has been extracted, reconciled and stored:

1. **Pick the key entities** (trajectory step `select_web_search_entities`). The candidates are the entities the extraction listed, of the text itself (or of all its chunks). *Concepts* are skipped (`type` `concept`: they have no world events). The rest are ranked by how many facts (edges) of the call they are an endpoint of, most first, and the first **3** are searched for (`maxWebSearchEntities`; an entity that is only mentioned and in no fact ranks last, and ties keep the order of the extraction). Every search costs money and time, hence the cap. This is a heuristic, kept simple on purpose.
2. **Search** (step `web_search`, one per entity, all at the same time). For each key entity the model is asked, with a fixed prompt, to look for the most recent significant news about it (announcements, deals, leadership changes, legal or regulatory actions, results, launches, conflicts, ...) and to answer with a short **dated briefing** (at most 300 words, full names, no guessing). It may make up to 3 searches per entity. The **query** issued for the entity is `latest world events and news about <name> (<type>)`; the model also sends its own, refined queries to the search engine, which are reported (`searches`) but are not the artifact's query. If the model finds nothing relevant it says so and nothing is stored for the entity.
3. **Extract**. The briefing is a text like any other: the graph (entities and edges) is extracted from it (step `extract_graph`, on the `extract_graph` model), with **now** as the reference time, so facts without a date of their own are valid from the moment they were read.
4. **Reconcile and store**. The new facts are compared with the stored live facts of the dataset and stored exactly as those of any text (see [temporal.md](temporal.md)): a duplicate is merged, and a fact that the news contradicts closes the older one (for example a new CEO closes the fact that the previous one was the CEO). The entities of the briefing are **nodes like all others**: they are deduplicated with the dataset's entities by (normalized name, type), linked to the web search artifact, recorded against the call's [session](sessions.md), and embedded when vectors are configured. The search does not recurse: entities found on the web are not searched for in turn.

## The `web_search` artifact

Each briefing is stored as **an artifact of its own** (`gb_artifacts`), with `kind = 'web_search'`. A `remember` request cannot ask for that kind (`kind` stays `knowledge` or `episode`); only this stage creates it. Besides the usual columns it has:

| Column | Value |
| ------ | ----- |
| `query` | The web search **query that was issued** for the artifact: `latest world events and news about Apple (organization)`. NULL for any other kind of artifact. |
| `parent_id` | The **artifact from which the searched entity came**: the artifact in which the entity was extracted (for a long text, the [chunk](chunking.md) of its first appearance, not the parent text). It is the same column that links a chunk to the text it is a part of, but the two are told apart: a web search has `kind = 'web_search'` and no `chunk_index`. |
| `text` | The briefing, then a `Sources:` list of the pages it is based on (`- <title> <url> (page age)`). Only the briefing is extracted; the sources are kept for provenance. |
| `valid_at`, `created_at` | Both the moment of the search (not the reference time of the text that mentioned the entity). |
| `session_id` | That of the `remember` call. |

So the provenance chain of a fact discovered on the web reads: the fact (`gb_edges.artifact_id`, `gb_edge_artifacts`) → the `web_search` artifact (with the query that produced it) → its `parent_id`, the artifact whose entity was searched for. Likewise an entity found on the web is linked to the `web_search` artifact (`gb_entity_artifacts`), and the entity that was searched for is linked to its parent. Deleting an artifact deletes the web searches that hang off it (`ON DELETE CASCADE`), like its chunks.

Example. "Apple designs the iPhone" is remembered with `web_search` as artifact 1; Apple is a key entity, so:

| Artifact | `kind` | `parent_id` | `query` |
| -------- | ------ | ----------- | ------- |
| 1 | `knowledge` | | |
| 2 | `web_search` | 1 | `latest world events and news about Apple (organization)` |

The datasets' counts (`DatasetCounts`) count a web search as an artifact but not as a chunk (nor as a chunk summary).

## Response

The response of `remember` gets `web_searches` (omitted without `web_search`, or when there was nothing to search for), one item per entity searched for. `entities` and `edges` of the response stay those of the text itself; what the web yielded is in the items:

```json
{
  "artifact_id": 1,
  "entities": [{"name": "Apple", "type": "organization"}],
  "web_searches": [
    {
      "entity": "Apple", "entity_type": "organization",
      "query": "latest world events and news about Apple (organization)",
      "parent_artifact_id": 1,
      "artifact_id": 2,
      "searches": ["Apple news this week", "Apple announcement"],
      "sources": [{"url": "https://news.example/1", "title": "Apple unveils ...", "page_age": "May 1, 2026"}],
      "entities": [{"name": "Tim Cook", "type": "person"}],
      "edges": [{"id": 7, "source": "Apple", "target": "...", "relation": "...", "description": "..."}],
      "invalidated_edges": [],
      "cost_usd": 0.034
    }
  ]
}
```

`artifact_id` is 0 (omitted) when nothing was stored for the entity, which is when the search found nothing, or failed (then `error` says why).

## Failures never fail `remember`

The text has been stored before the first search starts, and the web search is an enrichment. A failure in it (the search call, the extraction of the briefing, its storage) is logged (`remember: web search failed ...`), recorded in the [trajectory](evals/knowledge.md), and reported in the item's `error`; the response is still `200` and what was stored stays. A search that finds nothing stores nothing and is not an error. The one thing that is checked up front is availability (`503`).

## Cost

Each search is billed by Anthropic: **$10 per 1,000 searches** (`ai.WebSearchRequestUSD`, $0.01 each) on top of the tokens of the model (which include the pages the tool read). A call is recorded in `gb_inferences` as any other (type `remember`, stage `web_search`, reasoning `none`: the web search does not use extended thinking) with its tokens and the cost including the search fee, and a trajectory's totals include it. With the key entity cap of 3 and up to 3 searches each, a `remember` call can make up to 9 searches, about $0.09 plus tokens, plus the usual extraction (and, when there are stored facts to compare, resolution) call for each briefing. That is why the feature is opt-in per call.

## In the trajectory

An [eval trajectory](evals/knowledge.md) of a call with `web_search` has, after the steps of the text itself, a `select_web_search_entities` step, then one `web_search` step per entity (the briefing, the searches the model made, the sources, `latency_ms`, tokens and cost), each followed by the usual steps of a text (`extract_graph`, `find_candidates_by_name`, `load_candidates`, `store_artifact`, ... labelled ` (web search for "<entity>")`).

## From Go

```go
res, err := memory_remember.Remember(ctx, db, llm, "u1", "main", "knowledge", text, time.Time{},
	memory_core.WithWebSearcher(aiClient), // makes it available; *ai.Client implements memory_core.WebSearcher
	memory_core.WithWebSearch(true))       // asks for it, for this call
res.WebSearches
```

`ai.Client.WebSearch(ctx, ai.WebSearchRequest{...})` is the Anthropic call itself. The server wires the client as the searcher exactly when it has an Anthropic API key (`server.NewHandler`'s `ws` argument; nil leaves web search unavailable).

## Eval

The eval `bin/evals_knowledge/web_search.json` has one case, `web_search_companies`: it remembers a text about three big companies (Apple, Microsoft and Nvidia) with `"web_search": true`, then checks that the facts of the text are stored, that `recall` still answers from it, and (`expect_web_searches`) that at least one `web_search` artifact was stored, each with its query, text, a parent artifact in the dataset and entities of its own. It calls the real web search, so it needs `ANTHROPIC_API_KEY` with web search enabled and costs a few cents: `./run_evals.sh web_search_companies`. See [`internal/evals/knowledge/README.md`](../internal/evals/knowledge/README.md).

## Admin UI

With the [admin UI](evals/experiments.md#admin-ui) enabled, web searches are shown in three places:

- The **Web search** card of the dashboard: per database, what the web searches left in it (the `web_search` artifacts, the datasets that hold them, the pages they cite, the distinct entities linked to a result and how many of those no text of the user mentions, the facts they created or restated), and, for the eval database, the totals recorded in the trajectories of all runs: entity searches (found, empty, failed), searches made by the search tool, entities and facts extracted, facts closed, latency (average, median, maximum) and cost. A **View** button per dataset lists its web searches.
- The **run** of an experiment has a *Web searches* column with the number of searches of each case, and a case that made some shows their totals and a **View web searches** button.
- The list of web searches (a modal): one entry per search with its status, latency, cost and model, the query issued, the queries the model sent to the search engine, the result (the briefing), its sources, the entities and the facts that were extracted from it, the facts it closed, and the artifact the searched entity came from.

The database keeps only the searches that found something, and not their latency, cost or the queries sent to the search engine, so the list of a dataset lacks those; the list of a case is read from its [trajectory](evals/knowledge.md) and has them. The entities and facts are those of the whole result (the briefing), which cites the sources: they are not attributed to individual pages.

The JSON behind it (under `/v1/gb/admin/api`): `GET /web-searches?db=<name>[&include_deleted=true]` (the totals above), `GET /web-searches/dataset?db=<name>&dataset_id=<id>` (the searches stored in a dataset) and `GET /evals/runs/{id}/web-searches` (the searches of each eval of a run, from the trajectories).

## Limitations

- The selection of key entities is a heuristic (facts per entity, no concepts, at most 3). It does not know which entities have news.
- The query is built from the entity's name and type only; an ambiguous name ("Apple") relies on the entity's description in the prompt to be disambiguated.
- The web is not trusted more than the text: a briefing is extracted and reconciled like any text, and a wrong page can close a right fact. The artifact keeps its query and sources so it can be traced.
- A briefing is never chunked (it is limited to about 300 words).
- The same entity is searched again on every call that asks for it; there is no cache and no check for a recent search.
- The entities a web search discovers are not offered to the [timeseries providers](timeseries.md#providers): `timeseries.fetch` only considers the entities of the text itself.
- The search results are not embedded as text (as a chunk is); only the entities and edges extracted from them are.
- Existing database files are not migrated: the schema gained `gb_artifacts.query`, so delete the file and the server recreates it.
