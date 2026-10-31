# Evals and ATIF trajectories

Evals are end-to-end checks that run `remember` and `query` against the real LLM and verify the stored temporal facts and the answer. Each individual check is an **eval case** (an input, here some texts to remember and a question, plus what is expected and metadata), and the cases are grouped into **evals**. To let a case look inside a run, remember and query can record themselves as an [ATIF](https://github.com/laude-institute/harbor) (Agent Trajectory Interchange Format) trajectory: the ordered list of everything the call did, with the LLM's tokens and cost per step.

Trajectories are generated **only for eval calls** (the calls the eval cases make). The regular endpoints and regular Go calls never build, store or return them.

For the eval format and the runner's usage see [`internal/evals/README.md`](../../internal/evals/README.md). This page is about the knowledge-extraction evals; the other eval system, which judges trained models with an LLM, is in [training/EVALS.md](training.md) (the two are described together in [`internal/evals/README.md`](../../internal/evals/README.md)).

## Eval mode

The eval endpoints are not registered by default. They exist only when the server runs in eval mode:

- `gligabao serve --eval`, or
- `eval = true` in `gligabao.toml` (default `false`).

The flag wins when given. In eval mode the server logs a warning at startup. Do not enable it on a server you expose to untrusted clients; the responses are larger and each call writes a file.

### `POST /v1/gb/eval/remember`

Same request body and behavior as [`POST /v1/gb/remember`](../remember.md), including `models` (the LLM model of each stage, see [Models](#models)). The response is the usual remember result plus:

- `trajectory_id`: the ID of the trajectory (also the file name, see [Storage](#storage)).
- `trajectory`: the full ATIF trajectory.

### `POST /v1/gb/eval/query`

Same request body and behavior as [`POST /v1/gb/query`](../query.md). The response is the usual query result (`answer`, `evidence`) plus `trajectory_id` and `trajectory`.

Errors are the same as for the regular endpoints, and a failed call returns no trajectory (it is still stored, see below). Without eval mode both routes answer `404`/`405`.

## From Go

`memory_remember.Remember` and `memory_query.Query` take options. `memory.WithEval()` is the only way to generate a trajectory; the eval endpoints are the only callers that pass it.

```go
res, err := memory.Remember(ctx, db, llm, "u1", "main", text, time.Time{}, memory.WithEval())
res.TrajectoryID // same as res.Trajectory.TrajectoryID
res.Trajectory   // *atif.Trajectory

ans, err := memory_query.Query(ctx, db, llm, "u1", "main", query, memory.WithEval(), memory.WithTrajectoryDir(dir))
```

Without `WithEval()`, `TrajectoryID` and `Trajectory` are empty and nothing is written. `memory.WithTrajectoryDir(dir)` overrides the storage directory for one call.

## Storage

Each trajectory is written as pretty-printed JSON to `<dir>/<trajectory_id>.json` when the call finishes. The directory is, in order of precedence:

1. `memory.WithTrajectoryDir` (per call);
2. `ServeOptions.TrajectoryDir` / `server.Options.TrajectoryDir`;
3. an `atif/` directory next to the SQLite file (`build/atif/` with the default `build/gligabao.db`);
4. `memory.TrajectoryDir`, default `build/atif`, when there is no SQLite file.

A call that fails is stored too, with `notes` set to `failed: <error>`, but the caller only gets the trajectory of a successful call. A failure to write the file is logged and never fails the call.

The evals are read from the directories listed in `evals_knowledge_dirs` of the config file (`-config`, default `build/gligabao.toml`; relative paths are relative to that file; `./run_evals.sh` passes it). More than one directory can be listed: an eval of an earlier directory hides one of the same name in a later one, and a case's `text_file` is looked up in the directories in order, so a program that embeds gligabao can keep its own evals (and corpus) in a directory of its own next to gligabao's. `-evals dir` (repeatable) overrides the list; with neither, the directory is `build/evals_knowledge`.

The `text` of an entry of `remember` is a string, or a list of strings that are joined with a single space, so a long text can be written on several lines in the file and stay readable (`"text": ["First part,", "second part."]` is `"First part, second part."`). Use `text_file` for a text too long even for that.

The eval runner (`go run ./internal/evals/knowledge/cmd`) writes everything to the eval database, `build/gligabao-eval.db` (`-db`; the admin UI can show it, `serve --eval-db`), which isolates the evaluation data from the main database. It holds the datasets the cases build (artifacts, entities and edges), the experiments and their runs; a run references its datasets and artifacts instead of copying them. It is **never reset**: each run of an experiment creates its own datasets (`<case>_run<eval run id>`), so executions do not interfere. The experiment runs reference the trajectories by ID, so the trajectories are stored in an `atif/` directory next to the eval database (`-atif <dir>` changes it), see [experiments.md](experiments.md).

## Trajectory format

Package `internal/ai/atif` defines the types and a `Builder`; the schema version is `ATIF-v1.6`. Top level:

| field | meaning |
|---|---|
| `schema_version` | `ATIF-v1.6` |
| `trajectory_id`, `session_id` | `trajectory_id` is a random UUID. `session_id` is the request's [`session_id`](../sessions.md) if it gave one (so the trajectories of one session share it), else the same UUID |
| `agent` | `name` (`gligabao-remember` / `gligabao-query`), `version` |
| `steps` | the steps, below |
| `notes` | set to `failed: <error>` if the call failed |
| `final_metrics` | totals over all steps: prompt/completion tokens, `total_cost_usd`, `total_steps` |
| `extra.input` | the call's `user_id`, `dataset`, `session_id` (empty if none) (and `reference_time` for remember) |

A step has `step_id` (from 1), `timestamp`, `source` (`system`, `user` or `agent`) and `message`. Agent steps may also have `model_name`, `metrics` (`prompt_tokens` including cache tokens, `completion_tokens`, `cost_usd`), `tool_calls` and an `observation` whose `source_call_id` points at the tool call.

Every LLM call becomes up to three steps: a `system` step (the prompt; omitted if empty), a `user` step (the input message) and an `agent` step (the model's raw output, with `model_name`, `metrics` and `extra.provider` / `extra.stop_reason`). Work that is not an LLM call is an agent step with one tool call:

| call | tool | what it records |
|---|---|---|
| remember | `extract_graph` | the entities and edges (new facts) the LLM extracted from the text, before any stored fact is considered |
| remember | `summarize_chunk` | (only for a chunk of a [chunked](../chunking.md#chunk-summaries) text) the summary the LLM wrote for the chunk, the model that wrote it and the reasoning level it ran with, and its `inference_id` (the LLM call itself is the usual `system`, `user`, `agent` steps) |
| remember | `find_candidates_by_name` | the live stored facts touching an entity named like an endpoint of a new fact (exact normalized name), found by this lookup alone |
| remember | `search_similar_entities` | (only with vectors, on an existing dataset) the endpoints of the new facts that were embedded, the stored entities they matched by similarity (aliases) with their scores, and their live facts (the candidates found by similarity alone); `model_name` and `metrics` for the embedding. An error here is recorded but not fatal |
| remember | `load_candidates` | the two lookups merged by edge ID: the stored facts that could duplicate or contradict the new ones, each with how it was found (`found_by`: `name`, `similar`, `name+similar`) |
| remember | `embed` | the embedding request (only with a vector store): the texts of the entities and edges (and, for a chunk, the chunk and its summary), with `model_name` and `metrics` (tokens, cost), so the embedding cost counts in `final_metrics` |
| remember | `store_artifact` | `artifact_id`, entities, edges and `invalidated_edges` that were stored |
| remember | `select_geo_entities` | (only when the server has a [geo resolver](../geo.md)) the entities that may be places (not a person, concept, product or event, and without those that already have geo metadata) |
| remember | `detect_geo` | (only with a geo resolver, when there are candidates) which of them the LLM judged to be places, with their `kind` and the `query` for the resolver (the LLM call itself is the usual `system`, `user`, `agent` steps) |
| remember | `resolve_geo` | (only with a geo resolver, one per place) the geo resolver's answer: whether it found the place, and its coordinates, display name and country; `latency_ms` in the extra. An error here is recorded but not fatal |
| remember | `store_geo` | (only when a place was found) the number of places stored in `gb_entity_geo` |
| remember | `select_web_search_entities` | (only with [`web_search`](../web-search.md)) the key entities that were picked to search the web for |
| remember | `web_search` | (only with `web_search`, one per entity) the web search: the entity, the query issued, the briefing, the queries the model sent to the search engine, the sources and `search_errors`; `model_name`, `metrics` (tokens and cost, search fee included) and `latency_ms` in the extra. An error here is recorded but not fatal |
| query | `detect_geo` | (only with a geo resolver, and when the graph is not empty) the places the LLM found in the question, each with `text`, `kind` and `query`; or the `error` if it failed (the LLM call itself is the usual `system`, `user`, `agent` steps). It comes after `retrieve_graph` |
| query | `search_vectors` | the embedding request of the question and what the four vector indexes returned for it (`entities`, `edges`, `chunks`, `summaries`: the row `id` and the cosine `score` of each match, best first), with `model_name` and `metrics` (tokens, cost), so the embedding cost counts in `final_metrics`. It comes first: the matches become seeds, facts and passages of the next step (see [query.md](../query.md#2-retrieve-by-similarity-and-passages)) |
| query | `retrieve_graph` | the context handed to the LLM, as rendered in its prompt: the subgraph (entities and live facts) followed by the passages, if any |

### Remember

1. `user`: the text.
2. LLM call: entity/edge extraction (`system`, `user`, `agent`).
3. tool `extract_graph`, then the candidates: tool `find_candidates_by_name`, tool `search_similar_entities` (only when vectors are enabled and the dataset exists), tool `load_candidates` (the merge).
   (For each chunk of a [chunked](../chunking.md) text, an LLM call and a `summarize_chunk` tool step follow the extraction.)
4. LLM call: duplicate/contradiction resolution (only when there are stored facts to compare against; see [temporal.md](../temporal.md)).
5. tool `embed` (only when vectors are enabled).
6. tool `store_artifact`.
6b. only when the server has a [geo resolver](../geo.md): tool `select_geo_entities`, then the LLM call of `detect_geo` and the tool `detect_geo`, then for each place tool `resolve_geo`, and tool `store_geo`.
7. only with [`web_search`](../web-search.md): tool `select_web_search_entities`, then for each entity tool `web_search`, followed by the steps 2 to 6 for its briefing (`extract_graph` ... `store_artifact`, each labelled ` (web search for "<entity>")`; the artifact is of kind `web_search` with the `query` and `parent_id`).

### Query

1. `user`: the query.
2. tool `search_vectors`.
3. tool `retrieve_graph`.
4. LLM call: the answer (`system`, `user`, `agent`). If nothing was retrieved (no entity, fact or passage) there is no LLM call; the last step is a plain `agent` step with the fixed "I don't have any information" answer.

### Example (query, shortened)

```json
{
  "schema_version": "ATIF-v1.6",
  "trajectory_id": "5c1e0d52-7b1f-4a7e-9d3a-0f6a2f3b9c11",
  "session_id": "5c1e0d52-7b1f-4a7e-9d3a-0f6a2f3b9c11",
  "agent": {"name": "gligabao-query", "version": "0.1"},
  "steps": [
    {"step_id": 1, "timestamp": "2025-01-31T09:00:01Z", "source": "user", "message": "Where does Alice work?"},
    {"step_id": 2, "timestamp": "2025-01-31T09:00:01Z", "source": "agent", "model_name": "text-embedding-3-small",
     "message": "Embedded the question and searched the entity, edge, chunk and summary indexes with it.",
     "tool_calls": [{"tool_call_id": "call_2", "function_name": "search_vectors",
                     "arguments": {"question": "Where does Alice work?"}}],
     "observation": {"results": [{"source_call_id": "call_2", "content": "{\"entities\":[{\"id\":1,\"score\":0.78}],\"edges\":[{\"id\":1,\"score\":0.83}],\"chunks\":null,\"summaries\":null,\"input_tokens\":5}"}]},
     "metrics": {"prompt_tokens": 5, "cost_usd": 0.0000001}},
    {"step_id": 3, "timestamp": "2025-01-31T09:00:01Z", "source": "agent",
     "message": "Retrieved the subgraph around the entities matching the query, and the passages close to it.",
     "tool_calls": [{"tool_call_id": "call_3", "function_name": "retrieve_graph",
                     "arguments": {"user_id": "u1", "dataset": "main", "query": "Where does Alice work?"}}],
     "observation": {"results": [{"source_call_id": "call_3", "content": "[entity 1] Alice (person)\n..."}]}},
    {"step_id": 4, "timestamp": "2025-01-31T09:00:01Z", "source": "system", "message": "..."},
    {"step_id": 5, "timestamp": "2025-01-31T09:00:01Z", "source": "user", "message": "<graph>...</graph>"},
    {"step_id": 6, "timestamp": "2025-01-31T09:00:03Z", "source": "agent", "model_name": "claude-haiku-5-5",
     "message": "{\"answer\": \"Alice works at Acme.\", ...}",
     "metrics": {"prompt_tokens": 412, "completion_tokens": 38, "cost_usd": 0.00006},
     "extra": {"provider": "anthropic", "stop_reason": "end_turn"}}
  ],
  "final_metrics": {"total_prompt_tokens": 417, "total_completion_tokens": 38, "total_cost_usd": 0.0000601, "total_steps": 6},
  "extra": {"input": {"user_id": "u1", "dataset": "main"}}
}
```

## How the evals use it

The in-process server has geo on: the runner gives it a geo resolver, Nominatim of OpenStreetMap (cached) by default, so `remember` stores the coordinates of the places it finds and `query` finds those of the question (one more LLM call each, and a lookup per place; it needs internet). `-geo=false` turns it off; a program that runs the evals sets `knowledge.Executor.Geo` (nil: off). A failed lookup never fails a case (see [geo.md](../geo.md)).

`internal/evals/knowledge` starts an in-process server with eval mode on and runs each case through `/v1/gb/eval/remember` (once per memory, in order) and `/v1/gb/eval/query`. It decodes the `trajectory` from every response and fails the case if one is missing. A case's outcome holds the answer, the failed temporal-fact checks and the trajectories (one per remember, then the query's), so checks can use the steps and costs directly. For now the runner reports the trajectory count, step count and total cost with `-v`.

### The `geo` eval and `expect_geo`

`build/evals_knowledge/geo.json` has five cases about places: a city (`geo_city`), a city and its country (`geo_city_country`), a landmark (`geo_landmark`), two cities (`geo_two_cities`) and a mountain (`geo_mountain`). Each remembers a sentence, asks a question about it (`expect_contains`, `expect_edges`) and has an `expect_geo` check on what the geo stage stored. `expect_geo` is a list of `{"entity": "Paris", "kind": "city", "country_code": "fr", "latitude": 48.86, "longitude": 2.35, "radius_km": 30}`: the entity (name, case-insensitive) must have a row in `gb_entity_geo`, and each part that is given must hold: its `kind` (what the LLM judged it to be), its `country_code` (case-insensitive) and its coordinates within `radius_km` (default 100) of the given ones (great-circle distance). The LLM decides what is a place and the geo resolver where it is, so the check measures both; the coordinates and the country come from Nominatim and are generous on purpose. A case with `expect_geo` needs geo on in the eval server: with `-geo=false` it is errored, not failed. `-v` prints the places that were stored.

### The `temporal` eval

`build/evals_knowledge/temporal.json` has twenty cases about temporal reasoning and state changes. The entities are abstract (`Entity A`, `Entity B`, ...) and the relations are everyday ones (a partnership, a lease, a suspension, a baton, an access badge, a sublicense, a permit, a mandate, a council, a guild or a board), written as ordinary prose, so a failing case is easy to read and reason about when debugging. The rules of a relation (for instance that a partnership is exclusive, or that a sublicense is valid only while its issuer holds a master license) are stated in the text itself, as a sentence of the prose, so the extractor and the answerer can rely on what the text says and the dates rather than on world knowledge.

Next to `temporal.json` is a manifest, `build/evals_knowledge/temporal.md` (the loaders only read `*.json`): the creation date, a summary and the eval dataset generation skill, that is, how the dataset was generated, the rules every case follows, how to add or regenerate cases and the known limits. Every knowledge eval file is to have a manifest like it (so far only `temporal` has one).

The cases cover facts that start, end on a stated date, expire after a duration, end when an event happens, are superseded by a later exclusive fact, are renewed or extended, are restated, corrected or only announced, are reported late about an earlier time, or are given in relative dates ("two weeks ago"). Each case remembers one to four texts (each with its `reference_time`) and asks one question "as of" an explicit date, so the answer does not depend on today's date. Because the answer is checked with `expect_contains` (a case-insensitive substring), a question asks for a label (`Entity C`), for a one-word state that no other state contains (`FREE` or `SUSPENDED`, `ACTIVE` or `LAPSED`), or for the labels followed by `COUNT=<number>`, so a wrong set fails on the count. `expect_edges` checks the dates the extraction stored (`valid_at`, `invalid_at`, `ended`, and `superseded` where a later text closes the fact without saying so).

Since superseded facts are not retrieved by `query` ([temporal-edge-cases.md](../temporal-edge-cases.md#5-what-query-sees)), a question about a past date is only asked where the fact at that date ended in its own text; questions about a fact that a later text replaced ask about the state after the change. Two cases (`temporal_lease_extended`, `temporal_badge_regranted`) have several facts between the same two entities and so check only the answer.

| case | what it tests |
|---|---|
| `temporal_partnership_swap` | An exclusive partnership is taken over by a new partner in a later text: the old partnership must be superseded without the text ever saying that it ended. |
| `temporal_lease_expiry` | Two leases whose terms are given as durations: the end dates must be computed, and one has run out at the question date while the other has not. |
| `temporal_baton_relay` | The baton is handed over several times across three texts, one hand-over being part of the first text: the last event wins. |
| `temporal_suspension_lifted_reimposed` | A suspension is imposed, lifted and imposed again, all in one text; the question date falls in the gap between the lifting and the second suspension. |
| `temporal_chair_succession` | A one-seat role changes hands twice across three texts; each handover ends the previous holder at its date. |
| `temporal_backfilled_history` | A later text reports an older partnership: it must be stored as ended history and must not close the partnership that is current. |
| `temporal_restated_then_ended` | A fact is restated by a later text (which must not move its start) and a third text then says that it ended earlier than that text. |
| `temporal_derived_expiry` | A sublicense ends not by date but because its issuer's master license was revoked, which the reader has to infer from the rule in the text. |
| `temporal_permit_windows` | Three permits with windows: one not yet open, one already closed and one open at the question date; the start of a fact may lie after the text. |
| `temporal_lease_extended` | A lease with a stated end is extended by a later text: at the question date the original term would have run out but the extension has not. |
| `temporal_correction` | A later text corrects the earlier one: the earlier fact was never true, and the corrected one holds from the original date. |
| `temporal_partner_crosswise_swap` | Two partnerships are dissolved and re-formed crosswise at one instant: four facts change in a single text. |
| `temporal_mandate_until_event` | A mandate has no date of its own: it ends when an event happens, and the event is told in the same text. |
| `temporal_announced_change` | A change that is only announced for the future must not end the present fact; the question date is before the announced date. |
| `temporal_badge_regranted` | An access badge is granted, revoked and granted anew in three texts about the same two entities; the question date falls between the revocation and the new grant. |
| `temporal_relative_dates` | Dates given only relative to the date of the report; the reader must resolve them and then compare which partnership is newest. |
| `temporal_guild_membership` | Members join and leave a guild over six months in one text, told in the order of the entities rather than of time; the question is a headcount at a date. |
| `temporal_board_replacements` | A board with two seats is renewed over three texts, each seat replaced by a different entity, so that nobody from the first text is still seated. |
| `temporal_lease_overlap_boundary` | Three consecutive leases, one starting on the day the previous one ends: the question is about which overlapped in time (touching at an end date is not overlapping). |
| `temporal_mixed_state_chain` | Four texts that mix an exclusive partnership broken and replaced, a suspension that is lifted and a lease that is renewed; the question counts what is still in force. |

### Models

An eval case can vary the model of each LLM stage of `remember` (`extract_graph`, `summarize_chunk`, `resolve_edges`, `web_search`, `extract_image`; see [remember.md](../remember.md#choosing-the-model-of-each-stage)) and of the query (`query`, the call that answers the question), to compare models stage by stage. `detect_geo` is a stage of both pipelines (the entities of a text, the places of a question) and one setting applies to both. The runner passes the stages of `remember` as `models` in every `remember` request of a case and `query` and `detect_geo` as `models` of its query request. The settings come from two places that are combined stage by stage:

1. `-models extract_graph=<model>,query=<model>` on the command line: the defaults of the whole run (and recorded in the experiment run's `params.models`);
2. `"models": {"extract_graph": "<model>", "query": "<model>"}` in a case of an eval: that case only; a stage named here wins over `-models`.

A stage named by neither uses the default model. The model of each LLM step is in the trajectory (`model_name`), the requested `models` are in `extra.input.models`, and the entities and edges a case stores carry the `extract_graph` model as their `model_id`, so the graph of a run shows which model produced it. A runner invocation is a run of an experiment, so comparing models means running the eval once per setting under the same experiment (for instance `-experiment extract-models -models extract_graph=claude-sonnet-4-5`, or `-experiment answer-models -models query=claude-sonnet-4-5` to vary the model that answers). The in-process server of the evals has a [geo resolver](../geo.md) unless the runner is given `-geo=false`, so `detect_geo` applies to every run by default.

### Reasoning levels

An eval case can also vary the reasoning (thinking) level of each LLM stage, to see how it affects the results (see [remember.md](../remember.md#reasoning)). The stages are `extract_graph`, `summarize_chunk`, `resolve_edges`, `resolve_series`, `detect_geo` and `extract_image` for `remember`, and `query` for the LLM call that answers the question (`detect_geo` is a stage of the query too, and its level applies to both). The levels come from two places that are combined stage by stage:

1. `-reasoning extract_graph=low,query=medium` on the command line: the defaults of the whole run (recorded in the experiment run's `params.reasoning`);
2. `"reasoning": {"extract_graph": "low", "query": "medium"}` in a case of an eval: that case only; a stage named here wins over `-reasoning`, as for models (a case controls its own parameters).

The runner sends the `remember` stages as `reasoning` in every `remember` request of the case, and `query` and `detect_geo` as the per-stage `reasoning` of its query request. A stage named by neither runs with the server's default, which is `none` (no thinking). **Every case of the evals sets all of them explicitly to `none`**, so a run never depends on that default and is the baseline to compare against (a test checks it). So with the shipped evals `-reasoning` changes nothing until the stage is removed from the cases (or the cases are edited, e.g. a copy of the evals passed with `-evals`): the level a comparison varies is the one the case sets.

Each LLM call is a [inference](../inferences.md) with the level it ran with, its tokens (thinking tokens count as output) and its cost, so a comparison can look at both the pass rate and what the thinking cost, per stage.

#### Using it: precision per stage, and how models behave

Because each stage has its own model, an eval run can answer two different questions.

**How precise is a model at one stage?** Change one stage and hold the rest, so any difference in the results comes from that stage alone. Each setting is a run of the same experiment:

```sh
./run_evals.sh -experiment extract-models                                                     # baseline: every stage on the default
./run_evals.sh -experiment extract-models -models extract_graph=claude-sonnet-4-5             # only the extraction changes
./run_evals.sh -experiment extract-models -models resolve_edges=claude-sonnet-4-5             # only the resolution changes
./run_evals.sh -experiment extract-models -models extract_graph=claude-sonnet-4-5,resolve_edges=claude-haiku-5-5   # a mix
```

What the cases check is what measures precision, so pick the case for the stage:

- `extract_graph` is checked by `expect_edges` (is each expected fact stored between the right entities, with the right `valid_at` / `invalid_at`). `"ended": true` requires an `invalid_at`; `"superseded": true` also requires an `expired_at`, which is set only when a later text contradicts the fact (see [temporal-edge-cases.md](../temporal-edge-cases.md)) and, through the graph it builds, by the query's `expect_contains`. A model that misses a fact, attaches the wrong dates or names an entity differently fails these. For a long text, `chunking_register` checks that facts from the start, middle and end all came out.
- `resolve_edges` only runs when the new facts have stored candidates to be compared with, so use a case that remembers more than one text about the same entities, like `job_change` (the Acme fact must be closed by the Globex one, and only that one). A model that misses a contradiction leaves the old fact live (`ended` and `superseded` fail); one that flags a contradiction that is not one closes a fact that should stay true; one that misses a duplicate stores it twice.
- `web_search` is only used by a remember entry with `"web_search": true` (see [web-search.md](../web-search.md)).
- `extract_image` is only used by an image remember, which the runner does not issue yet.

Each case is pass or fail, and an LLM is not deterministic, so one run says little: repeat each setting a few times (each run is a run of the experiment, with its own datasets) and compare how often the cases pass. Add cases with `expect_edges` for the facts you care about; the more checks, the finer the comparison, and the checks of each run are kept (`passed_checks`, `problems`) so you can see *which* fact a model got wrong. Since the query always uses the default model, a change in the answer comes from what the `remember` stages stored, not from a different answerer.

A case can also pin its own models (`"models"` in the eval), for example to run one case with a stronger model than the rest of the run, or to keep a regression case on the model that used to fail it.

**How do models behave?** Beyond pass or fail, a run keeps what each model did:

- the **trajectories** hold the raw output of every LLM step with its `model_name`, tokens and cost, so you can read how a model extracted (the entities and edges it listed, how it typed and named them, what dates it gave) and what `resolve_edges` decided, side by side with another model's on the same text;
- the **graph** of every case of every run is kept in its own dataset (`<case>_run<eval run id>`), and its entities and edges carry the `extract_graph` model as `model_id`, so the graphs two models built from the same text can be opened in the admin UI and compared (more or fewer entities, finer or coarser relations, different validity);
- the **cost** of each case and of the whole run is recorded, so a model can be judged on what it costs for the quality it gives (a cheaper model for `resolve_edges` and a stronger one for `extract_graph`, say), and the steps and tokens show how verbose it is;
- the **experiment run** records the `models` of the invocation in its `params` (see [experiments.md](experiments.md)), so a result can always be traced back to the setting that produced it. Runs of one experiment can then be compared by their `params`, results, and cost.

## Eval commentary (debugging a failed case)

For a person who runs the evals at a terminal and wants to know *why* a case failed, the runner can have an LLM read the failure together with the case's ATIF trajectories. It is behind a flag, so automated runs are not affected:

```sh
./run_evals.sh -commentary [-commentary-model claude-sonnet-5-5] [-e eval] [case ...]
```

When a case fails (a check did not hold) or errors, and `-commentary` is on:

1. The failure log is printed as usual, followed by `Run commentary on this failure? [y/N]`. `y` or `yes` runs the commentary; `n`, `no` or Enter (the default) skips it and the run goes on.
2. The LLM gets: the case (its input as it ran and what it expects, its `models` / `reasoning` and the run's), the failed checks, the error if there was one, the answer, the checks that held, and the ATIF trajectories of the case (every remember, then the query). It is asked for the most likely cause, the stage and step ids where the behaviour diverges, the evidence and a suggestion. The trajectories are sent as compact JSON; if they are longer than about 400 KB the longest strings in them are cut (marked `...[N bytes cut]`) until they fit.
3. The commentary is printed under the failure log, and then `Press Enter to continue ...`: the run is paused until the person confirms, so there is time to read it. A failed LLM call is printed instead of the commentary (and also waits for Enter); it never changes the result of the case.

The model is `-commentary-model`, default `claude-sonnet-5-5` (not the default model of the rest of gligabao). Under each commentary the runner prints its tokens and cost and the running total of the commentaries of the run (`tokens: 1200 in, 300 out; cost $0.0054; commentary total this run: $0.0108 (2)`), and the final summary of the run repeats the total (`commentary: 2, cost $0.0108`). The cost comes from the price table (`ai.DefaultPrices`, which has `claude-sonnet-5-5`, `claude-opus-5-5` and `claude-fable-5-1`; a model without a price shows `cost unknown`) and is not part of the case's or the run's cost. Cases that were cancelled (Ctrl-C) are never offered a commentary, and Ctrl-C at the prompt stops the run as usual. The commentary does not apply to the training evals.

Because the runner blocks on the answer, `-commentary` needs a terminal on stdin: with stdin piped or redirected the runner stops at startup with an error instead of waiting forever. From Go, set `knowledge.Executor.Commentary` (a `*knowledge.Commentary` with the `LLM`, `Model` and the `In` reader the answers are read from); nil (the default) never asks anything. The code is `internal/evals/knowledge/commentary.go`.

## Generating traces in code

`internal/ai/atif` is independent of gligabao's memory code:

```go
b := atif.NewBuilder(atif.Agent{Name: "my-agent", Version: "1"})
b.User("hello")
b.Agent(atif.Step{ModelName: "m", Message: "hi", Metrics: &atif.Metrics{PromptTokens: 10, CompletionTokens: 3}})
b.Tool("looking it up", "search", map[string]any{"q": "x"}, "result text")
traj := b.Build()                  // steps numbered, final_metrics totalled
path, err := atif.WriteFile(dir, traj) // <dir>/<trajectory_id>.json, pretty-printed
```

## Viewing the pipeline's data in the admin UI

In the trajectory graph of the admin UI, the tool nodes `extract_graph`, `find_candidates_by_name`, `search_similar_entities` and `load_candidates` have an icon button (graph, equals sign, magnifier, merge arrow). It opens a view over the graph with the data of that stage: the extracted entities and edges, the candidates each lookup found on its own (and, for the similarity search, the matched stored entities with their scores), and the merged candidates with how each was found (name, similarity, both). It is built from the tool result stored in the trajectory.
