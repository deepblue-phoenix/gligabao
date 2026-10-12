# Evals

End-to-end checks that run against a real gligabao server (and the real LLM), so `ANTHROPIC_API_KEY` is required.

Run from the repo root: `./run_evals.sh [-v] [case-name ...]`, e.g. `./run_evals.sh -v remember_recall`. `-v` prints the input and output of every LLM call.

The runner is pure Go (`evals/main.go`; `run_evals.sh` just invokes `go run ./evals`). All cases run in the SQLite DB `bin/gligabao-eval.db` (`-db <file>` to change it; `-db ""` = a throwaway DB per case) behind an in-process HTTP server using the real handlers, running in eval mode. Cases stay independent because each one soft-deletes the dataset `test` first and `remember` recreates it empty. `./run_server_local.sh` shows this DB in the admin UI next to the standard one (`EVAL_DB=...` overrides the path; the dashboard's "include deleted datasets" shows the earlier cases). The runner calls the eval endpoints, which return the ATIF trajectory of every call along with its result; see [docs/evals.md](../docs/evals.md). `-atif <dir>` changes where the trajectories are stored as JSON files (default: `atif/` next to the database).

All cases use the dataset `test`. At the start of each case it is deleted (`DELETE /v1/gb/datasets/test`, a soft delete; a missing dataset is fine), so `remember` recreates it fresh, and the stored-edge checks only look at the live `test` dataset.

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

1. `POST /v1/gb/eval/remember` (with `"dataset": "test"`) for each `remember` entry, in order, with its `text`, `kind` (the suites use `episode`) and `reference_time`.
2. The stored edges (`gb_edges`) are checked against `expect_edges`: an edge between `source` and `target` (any relation) must exist; `valid_at` / `invalid_at` are compared exactly when given; `ended` says whether it must have an `invalid_at` (and then also an `expired_at`).
3. `POST /v1/gb/eval/recall` (dataset `test`) with `query`.
4. Passes if the temporal checks pass and the answer contains every `expect_contains` string (case-insensitive). `-v` also prints the stored edges and the trajectory count, step count and cost.

## Experiments

Every runner invocation is a run of an experiment (`-experiment <name>`, default `default`; `-experiment-desc` sets the description of a new one). It is persisted in the eval database: the suites and cases it executed, each case's result, and the IDs of the ATIF trajectories. The trajectory files are now kept by default in an `atif/` directory next to the eval database (`bin/atif/`), so the IDs can be resolved later. With `-db ""` nothing is persisted. See [docs/experiments.md](../docs/experiments.md).
