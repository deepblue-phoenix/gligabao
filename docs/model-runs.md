# Model runs

`gb_models` is the **static** list of the models gligabao knows (see [graph.md](graph.md#models)). `gb_model_runs` is its **dynamic** counterpart: one row per invocation of a model, written as the call happens (so what was spent is kept even if the operation fails later). It is how each run of a model is tracked, and how runs with different settings are compared.

| Column | Meaning |
| --- | --- |
| `id` | The run |
| `type` | The operation that made the call: `remember`, `remember_image` or `recall` |
| `stage` | The pipeline stage: `extract_graph`, `summarize_chunk`, `resolve_edges`, `resolve_series`, `extract_image`, `web_search`, `classify_complexity` (Jev) and `embed` for `remember`; `recall` for `recall`. Empty if unknown |
| `model_id` | The model that was run, as the API reported it (`gb_models.id`); NULL if it did not say (e.g. a Jev call that failed) |
| `reasoning` | The thinking level the call ran with: `none`, `low`, `medium` or `high`. It is the level that was *applied*: `none` for embeddings, web searches, Jev, and models without extended thinking |
| `input_tokens`, `output_tokens` | The usage (the thinking tokens are in `output_tokens`) |
| `cost_usd` | The cost, from the price table (0 if the model's price is unknown) |
| `created_at` | When it was recorded |

**Every** model call of the system is a run: each LLM call (`Complete`), each embedding request (one run per request, even for a batch), each web search and each Jev classification. Nothing else writes cost.

## Thinking level

The thinking level is chosen per stage; the support for `low`, `medium` and `high` is complete, only the default is off:

- `none` (no thinking) for every LLM stage of **`remember`** (and `remember_image`), see [remember.md](remember.md#reasoning), and for **`recall`**, see [recall.md](recall.md): thinking costs output tokens, so it is off unless a request (or an [eval](evals.md#reasoning-levels)) turns it on.

A request can pick another one per stage. A [chunk summary](chunking.md#chunk-summaries) artifact stores only a reference to its run (`gb_artifacts.model_run_id`), which holds the model and the level (and the tokens and cost) of the call that wrote it.

## Admin UI

With the [admin UI](../README.md) enabled, the **model runs** view shows, for each database, the totals of all runs (runs, input and output tokens, cost), their breakdown by type, stage, model and thinking level, and the list of the runs, newest first (100 per page). Clicking a row of a breakdown filters the list by it (click again, or **Clear filter**, to remove it); the totals and breakdowns are never filtered.

The JSON behind it is `GET /v1/gb/admin/api/model-runs?db=<name>[&type=][&stage=][&model_id=][&reasoning=][&limit=][&offset=]` (`limit` defaults to 100, at most 500): `totals`, `by_type`, `by_stage`, `by_model`, `by_reasoning` (each `key`, `label`, `runs`, `input_tokens`, `output_tokens`, `cost_usd`), and `runs` (with `total`, `limit`, `offset`) for the filters given. From Go: `relational.ModelRunGroups` and `relational.ListModelRuns`.

## Example

```sql
-- what each stage costs, by thinking level
SELECT stage, reasoning, COUNT(*), SUM(input_tokens), SUM(output_tokens), SUM(cost_usd)
FROM gb_model_runs
GROUP BY stage, reasoning;
```

From Go: `relational.RecordModelRun`, `relational.GetModelRun` and `relational.ModelRunStats` (sums by type); `memory_core.WithRecording` records every call of an LLM, and puts the run's ID in `ai.Response.ModelRunID`.

This table replaces `gb_llm_calls` (tokens and cost per call, without the model's ID, the stage or the thinking level).
