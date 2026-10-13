# Evals

End-to-end checks that run against a real gligabao server (and the real LLM), so `ANTHROPIC_API_KEY` is required. `OPENAI_API_KEY` is also needed for `remember` to vector-index the entities and edges (into `bin/gligabao-eval.vectors`, shown by "View vec db" in the admin UI); without it the runner warns and no vectors are stored.

Run from the repo root: `./run_evals.sh [-v] [case-name ...]`, e.g. `./run_evals.sh -v remember_recall`. `-v` prints the input and output of every LLM call.

The runner is pure Go (`evals/main.go`; `run_evals.sh` just invokes `go run ./evals`). Everything it writes goes to the **eval database**, the SQLite DB `bin/gligabao-eval.db` (`-db <file>` to change it), which keeps the evaluation data apart from the main DB: the datasets of the cases, the experiments and their runs. It is **never reset**. The cases run behind an in-process HTTP server using the real handlers, in eval mode. `./run_server_local.sh` shows this DB in the admin UI next to the standard one (`EVAL_DB=...` overrides the path; each case of each run is listed as its own dataset with a "View graph" button, and the Evals block reads the experiments from it). The runner calls the eval endpoints, which return the ATIF trajectory of every call along with its result; see [docs/evals.md](../docs/evals.md). `-atif <dir>` changes where the trajectories are stored as JSON files (default: `atif/` next to the eval database, since the experiments reference them).

Each run of an experiment creates its own datasets, which isolates the runs from each other without wiping anything: a case remembers into the dataset `<case>_run<run id>` (characters a dataset name does not allow become `_`; the case part is cut so the name fits 64 bytes), under the user `eval`. The stored-edge checks only look at that dataset, and the graph and artifacts of every case of every run stay in the eval DB. The experiment records only reference them (the dataset and the artifact IDs), and the admin UI reads the graph and artifacts back from that dataset.

## Cases

- `remember_recall`: one text, stored with its temporal validity (`valid_at` = the artifact's time, still true), then a question about it.
- `job_change`: "Alice works at Acme" is remembered, then later "Alice started working at Globex". The Acme fact must be closed (`invalid_at` = time of the second text), the Globex fact must be live, and "Where does Alice work now?" must be answered with Globex.

- `chunking_register` (suite `chunking.json`): a ~3,700-word membership register of 60 people (`suites/corpus/harbor_cooperative.txt`), remembered with `chunk_size` 512 so it is split into ~20 chunks, each extracted, stored and embedded on its own (see [docs/chunking.md](../docs/chunking.md)). Besides the usual edge and answer checks (facts from the first, middle and last chunks must all be in the graph, and the question mixes the start and the end of the text), `expect_min_chunks` turns on the chunking checks: enough chunks, none over the chunk size, the chunk artifacts stored as children of the text and matching the response, the chunks consecutive/overlapping/covering the whole text, and the facts linked to the chunks (not the parent) and to at least half of them. It makes ~20 extraction calls (and some resolution ones), so it takes a few minutes.

## Suite format

Each file in `evals/suites/` is a suite: a JSON list of cases (embedded into the binary). Case names must be unique across all suites. `basic.json` holds the first two cases above, `chunking.json` the third.

A `remember` entry has either `text` or `text_file`, a path relative to the suites directory (e.g. `corpus/harbor_cooperative.txt`; the `.txt` files under `suites/corpus/` are embedded as well) whose content is the text, for corpora too large for JSON. A case may also set `chunk_size` (tokens; 32 to 8000, default 1024) to run with a smaller chunk size, and `expect_min_chunks` (see above).

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

## Experiments

Every runner invocation is a run of an experiment (`-experiment <name>`, default `default`; `-experiment-desc` sets the description of a new one). It is persisted in the eval database (`-db`): the suites and cases it executed, each case's result, and the IDs of the ATIF trajectories. The trajectory files are kept by default in an `atif/` directory next to the eval database (`bin/atif/`), so the IDs can be resolved later. See [docs/experiments.md](../docs/experiments.md).
