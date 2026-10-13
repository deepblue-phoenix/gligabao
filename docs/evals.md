# Evals and ATIF trajectories

Evals are end-to-end checks that run `remember` and `recall` against the real LLM and verify the stored temporal facts and the answer. To let an eval look inside a run, remember and recall can record themselves as an [ATIF](https://github.com/laude-institute/harbor) (Agent Trajectory Interchange Format) trajectory: the ordered list of everything the call did, with the LLM's tokens and cost per step.

Trajectories are generated **only for eval calls**. The regular endpoints and regular Go calls never build, store or return them.

For the suite format and the runner's usage see [`evals/README.md`](../evals/README.md).

## Eval mode

The eval endpoints are not registered by default. They exist only when the server runs in eval mode:

- `gligabao serve --eval`, or
- `eval = true` in `gligabao.toml` (default `false`).

The flag wins when given. In eval mode the server logs a warning at startup. Do not enable it on a server you expose to untrusted clients; the responses are larger and each call writes a file.

### `POST /v1/gb/eval/remember`

Same request body and behavior as [`POST /v1/gb/remember`](remember.md), including `models` (the LLM model of each stage, see [Models](#models)). The response is the usual remember result plus:

- `trajectory_id`: the ID of the trajectory (also the file name, see [Storage](#storage)).
- `trajectory`: the full ATIF trajectory.

### `POST /v1/gb/eval/recall`

Same request body and behavior as [`POST /v1/gb/recall`](recall.md). The response is the usual recall result (`answer`, `evidence`) plus `trajectory_id` and `trajectory`.

Errors are the same as for the regular endpoints, and a failed call returns no trajectory (it is still stored, see below). Without eval mode both routes answer `404`/`405`.

## From Go

`memory_remember.Remember` and `memory_recall.Recall` take options. `memory.WithEval()` is the only way to generate a trajectory; the eval endpoints are the only callers that pass it.

```go
res, err := memory.Remember(ctx, db, llm, "u1", "main", text, time.Time{}, memory.WithEval())
res.TrajectoryID // same as res.Trajectory.TrajectoryID
res.Trajectory   // *atif.Trajectory

ans, err := memory_recall.Recall(ctx, db, llm, "u1", "main", query, memory.WithEval(), memory.WithTrajectoryDir(dir))
```

Without `WithEval()`, `TrajectoryID` and `Trajectory` are empty and nothing is written. `memory.WithTrajectoryDir(dir)` overrides the storage directory for one call.

## Storage

Each trajectory is written as pretty-printed JSON to `<dir>/<trajectory_id>.json` when the call finishes. The directory is, in order of precedence:

1. `memory.WithTrajectoryDir` (per call);
2. `ServeOptions.TrajectoryDir` / `server.Options.TrajectoryDir`;
3. an `atif/` directory next to the SQLite file (`bin/atif/` with the default `bin/gligabao.db`);
4. `memory.TrajectoryDir`, default `bin/atif`, when there is no SQLite file.

A call that fails is stored too, with `notes` set to `failed: <error>`, but the caller only gets the trajectory of a successful call. A failure to write the file is logged and never fails the call.

The eval runner (`go run ./evals`) writes everything to the eval database, `bin/gligabao-eval.db` (`-db`; the admin UI can show it, `serve --eval-db`), which isolates the evaluation data from the main database. It holds the datasets the cases build (artifacts, entities and edges), the experiments and their runs; a run references its datasets and artifacts instead of copying them. It is **never reset**: each run of an experiment creates its own datasets (`<case>_run<run id>`), so executions do not interfere. The experiment runs reference the trajectories by ID, so the trajectories are stored in an `atif/` directory next to the eval database (`-atif <dir>` changes it), see [experiments.md](experiments.md).

## Trajectory format

Package `internal/ai/atif` defines the types and a `Builder`; the schema version is `ATIF-v1.6`. Top level:

| field | meaning |
|---|---|
| `schema_version` | `ATIF-v1.6` |
| `trajectory_id`, `session_id` | `trajectory_id` is a random UUID. `session_id` is the request's [`session_id`](sessions.md) if it gave one (so the trajectories of one session share it), else the same UUID |
| `agent` | `name` (`gligabao-remember` / `gligabao-recall`), `version` |
| `steps` | the steps, below |
| `notes` | set to `failed: <error>` if the call failed |
| `final_metrics` | totals over all steps: prompt/completion tokens, `total_cost_usd`, `total_steps` |
| `extra.input` | the call's `user_id`, `dataset`, `session_id` (empty if none) (and `reference_time` for remember) |

A step has `step_id` (from 1), `timestamp`, `source` (`system`, `user` or `agent`) and `message`. Agent steps may also have `model_name`, `metrics` (`prompt_tokens` including cache tokens, `completion_tokens`, `cost_usd`), `tool_calls` and an `observation` whose `source_call_id` points at the tool call.

Every LLM call becomes up to three steps: a `system` step (the prompt; omitted if empty), a `user` step (the input message) and an `agent` step (the model's raw output, with `model_name`, `metrics` and `extra.provider` / `extra.stop_reason`). Work that is not an LLM call is an agent step with one tool call:

| call | tool | what it records |
|---|---|---|
| remember | `extract_graph` | the entities and edges (new facts) the LLM extracted from the text, before any stored fact is considered |
| remember | `find_candidates_by_name` | the live stored facts touching an entity named like an endpoint of a new fact (exact normalized name), found by this lookup alone |
| remember | `search_similar_entities` | (only with vectors, on an existing dataset) the endpoints of the new facts that were embedded, the stored entities they matched by similarity (aliases) with their scores, and their live facts (the candidates found by similarity alone); `model_name` and `metrics` for the embedding. An error here is recorded but not fatal |
| remember | `load_candidates` | the two lookups merged by edge ID: the stored facts that could duplicate or contradict the new ones, each with how it was found (`found_by`: `name`, `similar`, `name+similar`) |
| remember | `embed` | the embedding request (only with a vector store): the texts of the entities and edges, with `model_name` and `metrics` (tokens, cost), so the embedding cost counts in `final_metrics` |
| remember | `store_artifact` | `artifact_id`, entities, edges and `invalidated_edges` that were stored |
| recall | `retrieve_graph` | the subgraph handed to the LLM, as rendered in its prompt |

### Remember

1. `user`: the text.
2. LLM call: entity/edge extraction (`system`, `user`, `agent`).
3. tool `extract_graph`, then the candidates: tool `find_candidates_by_name`, tool `search_similar_entities` (only when vectors are enabled and the dataset exists), tool `load_candidates` (the merge).
4. LLM call: duplicate/contradiction resolution (only when there are stored facts to compare against; see [temporal.md](temporal.md)).
5. tool `embed` (only when vectors are enabled).
6. tool `store_artifact`.

### Recall

1. `user`: the query.
2. tool `retrieve_graph`.
3. LLM call: the answer (`system`, `user`, `agent`). If nothing was retrieved there is no LLM call; the last step is a plain `agent` step with the fixed "I don't have any information" answer.

### Example (recall, shortened)

```json
{
  "schema_version": "ATIF-v1.6",
  "trajectory_id": "5c1e0d52-7b1f-4a7e-9d3a-0f6a2f3b9c11",
  "session_id": "5c1e0d52-7b1f-4a7e-9d3a-0f6a2f3b9c11",
  "agent": {"name": "gligabao-recall", "version": "0.1"},
  "steps": [
    {"step_id": 1, "timestamp": "2025-01-31T09:00:01Z", "source": "user", "message": "Where does Alice work?"},
    {"step_id": 2, "timestamp": "2025-01-31T09:00:01Z", "source": "agent",
     "message": "Retrieved the subgraph around the entities matching the query.",
     "tool_calls": [{"tool_call_id": "call_2", "function_name": "retrieve_graph",
                     "arguments": {"user_id": "u1", "dataset": "main", "query": "Where does Alice work?"}}],
     "observation": {"results": [{"source_call_id": "call_2", "content": "[entity 1] Alice (person)\n..."}]}},
    {"step_id": 3, "timestamp": "2025-01-31T09:00:01Z", "source": "system", "message": "..."},
    {"step_id": 4, "timestamp": "2025-01-31T09:00:01Z", "source": "user", "message": "<graph>...</graph>"},
    {"step_id": 5, "timestamp": "2025-01-31T09:00:03Z", "source": "agent", "model_name": "claude-haiku-5-5",
     "message": "{\"answer\": \"Alice works at Acme.\", ...}",
     "metrics": {"prompt_tokens": 412, "completion_tokens": 38, "cost_usd": 0.00006},
     "extra": {"provider": "anthropic", "stop_reason": "end_turn"}}
  ],
  "final_metrics": {"total_prompt_tokens": 412, "total_completion_tokens": 38, "total_cost_usd": 0.00006, "total_steps": 5},
  "extra": {"input": {"user_id": "u1", "dataset": "main"}}
}
```

## How the evals use it

`evals/main.go` starts an in-process server with eval mode on and runs each case through `/v1/gb/eval/remember` (once per memory, in order) and `/v1/gb/eval/recall`. It decodes the `trajectory` from every response and fails the case if one is missing. A case's outcome holds the answer, the failed temporal-fact checks and the trajectories (one per remember, then the recall's), so checks can use the steps and costs directly. For now the runner reports the trajectory count, step count and total cost with `-v`.

### Models

An eval can vary the model of each LLM stage of `remember` (`extract_graph`, `resolve_edges`, `extract_image`; see [remember.md](remember.md#choosing-the-model-of-each-stage)), to compare models stage by stage. The runner passes them as `models` in every `remember` request of a case, from two places that are combined stage by stage:

1. `-models extract_graph=<model>,resolve_edges=<model>` on the command line: the defaults of the whole run (and recorded in the experiment run's `params.models`);
2. `"models": {"extract_graph": "<model>"}` in a case of a suite: that case only; a stage named here wins over `-models`.

A stage named by neither uses the default model. The recall's LLM call is not affected. The model of each LLM step is in the trajectory (`model_name`), the requested `models` are in `extra.input.models`, and the entities and edges a case stores carry the `extract_graph` model as their `model_id`, so the graph of a run shows which model produced it. A runner invocation is a run of an experiment, so comparing models means running the suite once per setting under the same experiment (for instance `-experiment extract-models -models extract_graph=claude-sonnet-4-5`).

#### Using it: precision per stage, and how models behave

Because each stage has its own model, an eval can answer two different questions.

**How precise is a model at one stage?** Change one stage and hold the rest, so any difference in the results comes from that stage alone. Each setting is a run of the same experiment:

```sh
./run_evals.sh -experiment extract-models                                                     # baseline: every stage on the default
./run_evals.sh -experiment extract-models -models extract_graph=claude-sonnet-4-5             # only the extraction changes
./run_evals.sh -experiment extract-models -models resolve_edges=claude-sonnet-4-5             # only the resolution changes
./run_evals.sh -experiment extract-models -models extract_graph=claude-sonnet-4-5,resolve_edges=claude-haiku-5-5   # a mix
```

What the cases check is what measures precision, so pick the case for the stage:

- `extract_graph` is checked by `expect_edges` (is each expected fact stored between the right entities, with the right `valid_at` / `invalid_at`) and, through the graph it builds, by the recall's `expect_contains`. A model that misses a fact, attaches the wrong dates or names an entity differently fails these. For a long text, `chunking_register` checks that facts from the start, middle and end all came out.
- `resolve_edges` only runs when the new facts have stored candidates to be compared with, so use a case that remembers more than one text about the same entities, like `job_change` (the Acme fact must be closed by the Globex one, and only that one). A model that misses a contradiction leaves the old fact live (`ended` fails); one that flags a contradiction that is not one closes a fact that should stay true; one that misses a duplicate stores it twice.
- `extract_image` is only used by an image remember, which the runner does not issue yet.

Each case is pass or fail, and an LLM is not deterministic, so one run says little: repeat each setting a few times (each run is a run of the experiment, with its own datasets) and compare how often the cases pass. Add cases with `expect_edges` for the facts you care about; the more checks, the finer the comparison, and the checks of each run are kept (`passed_checks`, `problems`) so you can see *which* fact a model got wrong. Since the recall always uses the default model, a change in the answer comes from what the `remember` stages stored, not from a different answerer.

A case can also pin its own models (`"models"` in the suite), for example to run one case with a stronger model than the rest of the run, or to keep a regression case on the model that used to fail it.

**How do models behave?** Beyond pass or fail, a run keeps what each model did:

- the **trajectories** hold the raw output of every LLM step with its `model_name`, tokens and cost, so you can read how a model extracted (the entities and edges it listed, how it typed and named them, what dates it gave) and what `resolve_edges` decided, side by side with another model's on the same text;
- the **graph** of every case of every run is kept in its own dataset (`<case>_run<run id>`), and its entities and edges carry the `extract_graph` model as `model_id`, so the graphs two models built from the same text can be opened in the admin UI and compared (more or fewer entities, finer or coarser relations, different validity);
- the **cost** of each case and of the whole run is recorded, so a model can be judged on what it costs for the quality it gives (a cheaper model for `resolve_edges` and a stronger one for `extract_graph`, say), and the steps and tokens show how verbose it is;
- the **experiment run** records the `models` of the invocation in its `params` (see [experiments.md](experiments.md)), so a result can always be traced back to the setting that produced it. Runs of one experiment can then be compared by their `params`, results, and cost.

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
