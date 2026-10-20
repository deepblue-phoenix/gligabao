# Evals (`internal/evals_neo`)

The evals system: the data model, its storage and the runners. It replaces `internal/evals`, which is still in the tree only until it is removed; the two share no code, types or tables (the tables here are `gb_neo_*`).

**Terms.** An **eval** is a named set of **cases** of one **type** (`knowledge`, `training`). Evals and cases are *definitions* (`EvalDef`, `CaseDef`: stored in the database, reusable); running them makes *runs* (`EvalRun`, `CaseRun`: append-only history). An **experiment** groups evals that are run together (`ExperimentDef`, run as an `ExperimentRun`). The whole vocabulary and the rules of the model are in [`core/doc.go`](core/doc.go).

| Package | What it is |
|---|---|
| [`core`](core) | the data model: plain structs |
| [`db`](db) | the schema and the functions that write and read the records, for SQLite (`db/sqlite`) and PostgreSQL (`db/postgres`); `db.Store` holds the functions of a backend as a value |
| [`evalfile`](evalfile) | reading the evals from their JSON files |
| [`runner`](runner) | what every type shares in running: `SyncEval` (puts an eval of the files in the database as defs), `EnsureExperiment`, and the recorders of the runs (`RunEval`, `EvalRecorder`, `ExperimentRecorder`) |
| [`knowledge`](knowledge) | the executor of the knowledge evals, and its command (`knowledge/cmd`) |
| [`training`](training) | the executor of the training evals (the engine and the judge), and its command (`training/cmd`) |

The root package has `Open(backend, *sql.DB)`, which applies the schema (`internal/db.Open` calls it, so every database of the server and of the runners has the tables), and `StoreFor(backend)`.

## How an eval runs

1. The evals are read from their files (`bin/evals_knowledge/*.json`, `bin/evals_training/*.json`; `{name, description?, cases}`; case names are unique over all the evals).
2. `runner.SyncEval` puts each one in the database: the eval def is created (or updated), and a case def with the metadata of its type for each case (created, rewritten if the file changed, ordered as the file). Cases that were dropped from a file stay in the database (they may have runs), after the others.
3. `runner.RunEval` records an eval run (`running`) and gives an `EvalRecorder` to the executor of the type. The executor runs the cases and records each as a case run with its metadata; the eval run (its `Counts`, cost and metadata) is rewritten as the cases finish, so a run that is going shows its progress. The run ends `completed`, `errored` (the executor could not go on: the model or the judge is down) or `cancelled` (Ctrl-C). A case that fails is an `errored` case run and the run goes on.
4. An experiment run (knowledge: `-experiment`, default `default`) sums the counts and the cost of its eval runs.

A case run's `Status` says whether it ran to the end and its `Verdict` (`pass` or `fail`) how it went: knowledge passes when every check holds; training when the judge's score is at least `core.TrainingPassScore` (4 of 5).

## Running

`./run_evals.sh` runs either kind (it starts the sidecar for the training one if needed):

```
./run_evals.sh [-e eval] [-v] [case ...]              # knowledge: go run ./internal/evals_neo/knowledge/cmd
./run_evals.sh -t training -model ID [-e eval] [-v]   # training:  go run ./internal/evals_neo/training/cmd
```

- **knowledge** needs `ANTHROPIC_API_KEY` (and `OPENAI_API_KEY` for the embeddings; without it nothing is vector indexed). It runs each case behind an in-process server in eval mode, in the eval database (`-db`, default `bin/gligabao-eval.db`, never reset; every run of a case has a dataset of its own, `<case>_run<eval run id>`). Flags: `-evals`, `-eval`, `-experiment`, `-experiment-desc`, `-models`, `-reasoning`, `-atif`, `-db`, `-v`, `-list-evals`. The exit status is 1 if any case did not pass.
- **training** asks a trained model (through the sidecar) the questions of an eval and has the LLM judge grade the answers in batches; the run is stored in the database that holds the trained models (`-db`, default `bin/gligabao.db`). Flags: `-model`, `-list-models`, `-eval`, `-evals`, `-batch-size`, `-judge-model`, `-max-tokens`, `-temperature`, `-sidecar`, `-db`, `-v`, `-list-evals`. The exit status is 1 if any case is not correct or errored.

Tests (`go test ./internal/evals_neo/...`) use fake LLMs, models and judges, and a temporary SQLite database; the PostgreSQL tests of `db/postgres` run when `POSTGRES_TEST_DSN` is set.

## Not done yet

The server's **Eval** button of the trained models, the evals views of the admin UI and the API behind them still read and write the old tables (`internal/evals/db`); the runs of the commands above are in the `gb_neo_*` tables, which nothing in the server reads yet.
