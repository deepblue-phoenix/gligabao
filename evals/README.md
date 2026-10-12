# Evals

End-to-end checks that run against a real gligabao server (and the real LLM), so `ANTHROPIC_API_KEY` is required.

Run from the repo root: `./run_evals.sh [-v] [case-name ...]`, e.g. `./run_evals.sh -v remember_recall`. `-v` prints the input and output of every LLM call.

The runner is pure Go (`evals/main.go`; `run_evals.sh` just invokes `go run ./evals`). All cases run in the SQLite DB `bin/gligabao-eval.db` (`-db <file>` to change it; `-db ""` = a throwaway DB per case) behind an in-process HTTP server using the real handlers, running in eval mode. **The eval DB (and its `.vectors` directory) is reset at the start of every run**, so the artifacts, entities and edges the cases create do not pile up; `-keep-db` skips the reset. The experiments are not in it: they are recorded in the main DB (`-main-db`, default `bin/gligabao.db`), which is never reset. Within a run, cases stay independent because each one has its own dataset, named after the case. The graph of every case therefore stays in the eval DB after the run. `./run_server_local.sh` shows this DB in the admin UI next to the standard one (`EVAL_DB=...` overrides the path; each case is listed as its own dataset with a "View graph" button). The runner calls the eval endpoints, which return the ATIF trajectory of every call along with its result; see [docs/evals.md](../docs/evals.md). `-atif <dir>` changes where the trajectories are stored as JSON files (default: `atif/` next to the main database, since the experiments reference them).

Each case uses its own dataset, named after the case (characters a dataset name does not allow become `_`, max 64 bytes), under the user `eval`. At the start of the case that dataset is deleted (`DELETE /v1/gb/datasets/<case>`, a soft delete; a missing dataset is fine, and it only exists with `-keep-db`), so `remember` recreates it fresh, and the stored-edge checks only look at it. Nothing is overwritten between cases: the built-up graph of each case stays in the eval DB.

## Cases

- `remember_recall`: one text, stored with its temporal validity (`valid_at` = the artifact's time, still true), then a question about it.
- `job_change`: "Alice works at Acme" is remembered, then later "Alice started working at Globex". The Acme fact must be closed (`invalid_at` = time of the second text), the Globex fact must be live, and "Where does Alice work now?" must be answered with Globex.

## Suite format

Each file in `evals/suites/` is a suite: a JSON list of cases (embedded into the binary). Case names must be unique across all suites. `basic.json` holds the two cases above.

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

Every runner invocation is a run of an experiment (`-experiment <name>`, default `default`; `-experiment-desc` sets the description of a new one). It is persisted in the **main** database (`-main-db`, never reset, so the history survives the eval DB resets): the suites and cases it executed, each case's result, and the IDs of the ATIF trajectories. The trajectory files are kept by default in an `atif/` directory next to the main database (`bin/atif/`), so the IDs can be resolved later. With `-main-db ""` nothing is persisted. The runner refuses to run if `-db` and `-main-db` are the same file. See [docs/experiments.md](../docs/experiments.md).
