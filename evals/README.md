# Evals

End-to-end checks that run against a real gligabao server (and the real LLM), so `ANTHROPIC_API_KEY` is required. `OPENAI_API_KEY` is also needed for `remember` to vector-index the entities and edges (into `bin/gligabao-eval.vectors`, shown by "View vec db" in the admin UI); without it the runner warns and no vectors are stored.

Run from the repo root: `./run_evals.sh [-s|--suite name] [-v] [-models stage=model,...] [case-name ...]`, e.g. `./run_evals.sh -v remember_recall` or `./run_evals.sh --suite chunking`. `./run_evals.sh --help` lists the suites (the runner's `-list-suites`). `-v` prints the input and output of every LLM call. `-models` picks the LLM model of each `remember` stage for the whole run (see [Models](#models)), `-reasoning` the thinking level of each stage and of the recall (see [Reasoning levels](#reasoning-levels)).

The runner is pure Go (`evals/main.go`; `run_evals.sh` just invokes `go run ./evals`). Everything it writes goes to the **eval database**, the SQLite DB `bin/gligabao-eval.db` (`-db <file>` to change it), which keeps the evaluation data apart from the main DB: the datasets of the cases, the experiments and their runs. It is **never reset**. The cases run behind an in-process HTTP server using the real handlers, in eval mode. `./run_server_local.sh` shows this DB in the admin UI next to the standard one (`EVAL_DB=...` overrides the path; each case of each run is listed as its own dataset with a "View graph" button, and the Evals block reads the experiments from it). The runner calls the eval endpoints, which return the ATIF trajectory of every call along with its result; see [docs/evals.md](../docs/evals.md). `-atif <dir>` changes where the trajectories are stored as JSON files (default: `atif/` next to the eval database, since the experiments reference them).

Each run of an experiment creates its own datasets, which isolates the runs from each other without wiping anything: a case remembers into the dataset `<case>_run<run id>` (characters a dataset name does not allow become `_`; the case part is cut so the name fits 64 bytes), under the user `eval`. The stored-edge checks only look at that dataset, and the graph and artifacts of every case of every run stay in the eval DB. The experiment records only reference them (the dataset and the artifact IDs), and the admin UI reads the graph and artifacts back from that dataset.

## Cases

- `remember_recall`: one text, stored with its temporal validity (`valid_at` = the artifact's time, still true), then a question about it.
- `job_change`: "Alice works at Acme" is remembered, then later "Alice started working at Globex". The Acme fact must be closed (`invalid_at` = time of the second text), the Globex fact must be live, and "Where does Alice work now?" must be answered with Globex.

- `chunking_register` (suite `chunking.json`): a ~3,700-word membership register of 60 people (`suites/corpus/harbor_cooperative.txt`), remembered with `chunk_size` 512 so it is split into ~20 chunks, each extracted, stored and embedded on its own (see [docs/chunking.md](../docs/chunking.md)). Besides the usual edge and answer checks (facts from the first, middle and last chunks must all be in the graph, and the question mixes the start and the end of the text), `expect_min_chunks` turns on the chunking checks: enough chunks, none over the chunk size, the chunk artifacts stored as children of the text and matching the response, the chunks consecutive/overlapping/covering the whole text, and the facts linked to the chunks (not the parent) and to at least half of them. It makes ~20 extraction calls (and some resolution ones), so it takes a few minutes.

- `web_search_companies` (suite `web_search.json`): a text about three real big companies (Apple, Microsoft and Nvidia) remembered with `"web_search": true`, so the key entities are also searched for on the web (see [docs/web-search.md](../docs/web-search.md)). Besides the usual edge and answer checks, `expect_web_searches` turns on the web search checks: at least that many `web_search` artifacts were stored, each with the query that was issued, its text, a parent artifact in the dataset (the artifact the searched entity came from) and entities of its own. It calls the real web search tool (needs web search enabled for the Anthropic organization of `ANTHROPIC_API_KEY`) and costs a few cents.

- `timeseries_ingest_aapl` (suite `timeseries.json`): ingestion of made-up Apple (AAPL) prices. One text with `timeseries` in its `remember` entry: 10 daily closes (anchored to the entity Apple) and 3 volume points (no anchors, so anchored to the artifact). `expect_series` checks what was stored: each series exists (`key`, `field`), has the expected number of points and first/last values, and the expected anchors (`anchor_entity`, `anchor_artifact`). The case has no `query`, so there is no recall (recall does not use timeseries yet) and no `expect_contains`.

## Suite format

Each file in `evals/suites/` is a suite: a JSON list of cases (embedded into the binary). Case names must be unique across all suites. `basic.json` holds the first two cases above, `chunking.json` the third, `web_search.json` the fourth, `timeseries.json` the fifth. A `remember` entry may carry `timeseries` (the request's `timeseries`, see [docs/timeseries.md](../docs/timeseries.md)) and a case `expect_series` (see above). A case with no `query` skips the recall.

A `remember` entry has either `text` or `text_file`, a path relative to the suites directory (e.g. `corpus/harbor_cooperative.txt`; the `.txt` files under `suites/corpus/` are embedded as well) whose content is the text, for corpora too large for JSON. A `remember` entry may set `"web_search": true` to make that call search the web for its key entities, and a case `expect_web_searches` (see above). A case may also set `chunk_size` (tokens; 32 to 8000, default 1024) to run with a smaller chunk size, and `expect_min_chunks` (see above).

```json
[
{
  "name": "job_change",
  "remember": [
    {"kind": "episode", "text": "Alice works at Acme and lives in Paris.", "reference_time": "2023-01-01T00:00:00Z"},
    {"kind": "episode", "text": "Alice started working at Globex.", "reference_time": "2024-03-01T00:00:00Z"}
  ],
  "query": "Where does Alice work now?",
  "expect_contains": ["Globex"],
  "expect_edges": [
    {"source": "Alice", "target": "Acme", "valid_at": "2023-01-01T00:00:00Z", "invalid_at": "2024-03-01T00:00:00Z", "ended": true},
    {"source": "Alice", "target": "Globex", "valid_at": "2024-03-01T00:00:00Z", "ended": false}
  ]
}
]
```

1. `POST /v1/gb/eval/remember` (with `"dataset": "<case name>"`) for each `remember` entry, in order, with its `text`, `kind` (the suites use `episode`) and `reference_time`.
2. The stored edges (`gb_edges`) are checked against `expect_edges`: an edge between `source` and `target` (any relation) must exist; `valid_at` / `invalid_at` are compared exactly when given; `ended` says whether it must have an `invalid_at` (and then also an `expired_at`).
3. `POST /v1/gb/eval/recall` (the case's dataset) with `query`.
4. Passes if the temporal checks pass and the answer contains every `expect_contains` string (case-insensitive). `-v` also prints the stored edges and the trajectory count, step count and cost.

## Reasoning levels

The thinking level of each LLM stage can be varied too: `extract_graph`, `summarize_chunk`, `resolve_edges`, `resolve_series`, `extract_image` and `recall` (the answer of the recall), each `none`, `low`, `medium` or `high`. Two places, combined stage by stage like the models: `-reasoning extract_graph=low,recall=medium` on the command line (recorded in the experiment run's `params.reasoning`), and `"reasoning": {"extract_graph": "low"}` in a case, which wins (an eval controls its own parameters). **Every case of the suites sets every stage explicitly to `none`** (the server's default too, so that no thinking tokens are paid for), which is the baseline: to compare levels, set the stage in the cases (a copy of the suites passed with `-suites` is enough) and run each setting as a run of the same experiment. The inferences of the eval database record the level of each call with its tokens and cost. Details: [docs/evals.md](../docs/evals.md#reasoning-levels).

## Models

The model of each LLM stage of `remember` can be varied, to compare models stage by stage. The stages are `extract_graph` (entities and edges), `summarize_chunk` (the summary of each chunk of a long text), `resolve_edges` (duplicates and contradictions), `web_search` (the model that runs the web search of an entry with `web_search`) and `extract_image`; a stage with no model uses the default model, so you only name the ones you want to change. Two places, combined stage by stage:

- `-models extract_graph=claude-sonnet-4-5,resolve_edges=claude-haiku-5-5` on the command line sets the models for every case of the run (recorded in the experiment run's `params`);
- `"models": {"extract_graph": "claude-sonnet-4-5"}` in a case sets them for that case, and wins over `-models` for the stages it names.

The cases' `remember` requests carry them as `models`, so the stored entities and edges have the `extract_graph` model as their `model_id`, and the trajectories show the model of every LLM step. The recall is not affected. This is how to evaluate the precision of a model at one stage (change one stage per run and keep the rest on the default, e.g. `-experiment extract-models -models extract_graph=claude-sonnet-4-5`, then compare the pass/fail of the cases and the facts that failed across runs; `extract_graph` is checked by `expect_edges` and the answer, `resolve_edges` by a case with several texts about the same entities, like `job_change`), and to see how different models behave (the trajectories hold each model's raw output, each run keeps its own graph with the `extract_graph` model as `model_id`, and the cost of every case is recorded). An LLM is not deterministic, so repeat a setting a few times. More in [docs/evals.md](../docs/evals.md#using-it-precision-per-stage-and-how-models-behave). Details: [docs/evals.md](../docs/evals.md#models), [docs/remember.md](../docs/remember.md#choosing-the-model-of-each-stage).

## Experiments

Every runner invocation is a run of an experiment (`-experiment <name>`, default `default`; `-experiment-desc` sets the description of a new one). It is persisted in the eval database (`-db`): the suites and cases it executed, each case's result, and the IDs of the ATIF trajectories. The trajectory files are kept by default in an `atif/` directory next to the eval database (`bin/atif/`), so the IDs can be resolved later. See [docs/experiments.md](../docs/experiments.md).
