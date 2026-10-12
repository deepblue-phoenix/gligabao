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

Same request body and behavior as [`POST /v1/gb/remember`](remember.md). The response is the usual remember result plus:

- `trajectory_id`: the ID of the trajectory (also the file name, see [Storage](#storage)).
- `trajectory`: the full ATIF trajectory.

### `POST /v1/gb/eval/recall`

Same request body and behavior as [`POST /v1/gb/recall`](recall.md). The response is the usual recall result (`answer`, `evidence`) plus `trajectory_id` and `trajectory`.

Errors are the same as for the regular endpoints, and a failed call returns no trajectory (it is still stored, see below). Without eval mode both routes answer `404`/`405`.

## From Go

`memory.Remember` and `memory.Recall` take options. `memory.WithEval()` is the only way to generate a trajectory; the eval endpoints are the only callers that pass it.

```go
res, err := memory.Remember(ctx, db, llm, "u1", "main", text, time.Time{}, memory.WithEval())
res.TrajectoryID // same as res.Trajectory.TrajectoryID
res.Trajectory   // *atif.Trajectory

ans, err := memory.Recall(ctx, db, llm, "u1", "main", query, memory.WithEval(), memory.WithTrajectoryDir(dir))
```

Without `WithEval()`, `TrajectoryID` and `Trajectory` are empty and nothing is written. `memory.WithTrajectoryDir(dir)` overrides the storage directory for one call.

## Storage

Each trajectory is written as pretty-printed JSON to `<dir>/<trajectory_id>.json` when the call finishes. The directory is, in order of precedence:

1. `memory.WithTrajectoryDir` (per call);
2. `ServeOptions.TrajectoryDir` / `server.Options.TrajectoryDir`;
3. an `atif/` directory next to the SQLite file (`bin/atif/` with the default `bin/gligabao.db`);
4. `memory.TrajectoryDir`, default `bin/atif`, when there is no SQLite file.

A call that fails is stored too, with `notes` set to `failed: <error>`, but the caller only gets the trajectory of a successful call. A failure to write the file is logged and never fails the call.

The eval runner (`go run ./evals`) writes its database to `bin/gligabao-eval.db` (`-db`; the admin UI can show it, `serve --eval-db`) and uses a throwaway trajectory directory per case, since it reads the trajectories from the responses; `-atif <dir>` keeps them as files.

## Trajectory format

Package `internal/ai/atif` defines the types and a `Builder`; the schema version is `ATIF-v1.6`. Top level:

| field | meaning |
|---|---|
| `schema_version` | `ATIF-v1.6` |
| `trajectory_id`, `session_id` | a random UUID (both the same for now) |
| `agent` | `name` (`gligabao-remember` / `gligabao-recall`), `version` |
| `steps` | the steps, below |
| `notes` | set to `failed: <error>` if the call failed |
| `final_metrics` | totals over all steps: prompt/completion tokens, `total_cost_usd`, `total_steps` |
| `extra.input` | the call's `user_id`, `dataset` (and `reference_time` for remember) |

A step has `step_id` (from 1), `timestamp`, `source` (`system`, `user` or `agent`) and `message`. Agent steps may also have `model_name`, `metrics` (`prompt_tokens` including cache tokens, `completion_tokens`, `cost_usd`), `tool_calls` and an `observation` whose `source_call_id` points at the tool call.

Every LLM call becomes up to three steps: a `system` step (the prompt; omitted if empty), a `user` step (the input message) and an `agent` step (the model's raw output, with `model_name`, `metrics` and `extra.provider` / `extra.stop_reason`). Work that is not an LLM call is an agent step with one tool call:

| call | tool | what it records |
|---|---|---|
| remember | `load_candidates` | how many stored facts could duplicate or contradict the new ones |
| remember | `store_artifact` | `artifact_id`, entities, edges and `invalidated_edges` that were stored |
| recall | `retrieve_graph` | the subgraph handed to the LLM, as rendered in its prompt |

### Remember

1. `user`: the text.
2. LLM call: entity/edge extraction (`system`, `user`, `agent`).
3. tool `load_candidates`.
4. LLM call: duplicate/contradiction resolution (only when there are stored facts to compare against; see [temporal.md](temporal.md)).
5. tool `store_artifact`.

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
