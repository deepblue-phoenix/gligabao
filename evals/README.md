# Evals

gligabao has two eval systems. They are separate: each lives in its own subdirectory, has its own code, README and tests, and checks something different.

**Terms.** An **eval case** (*case*) is one input/output combination, plus metadata, to be evaluated. Cases are part of an **eval suite** (*suite*). A *run* executes cases (or a whole suite) once and records a *result* for each case; an *experiment* is a named series of runs. Both eval systems below use these words.

| | What it checks | How it runs | Judge |
|---|---|---|---|
| [`knowledge/`](knowledge/README.md) | **Knowledge extraction**: `remember` then `recall` against the real LLM; the stored temporal facts and the recalled answer | `./run_evals.sh` (`go run ./evals/knowledge`), an end-to-end runner with JSON suites | Programmatic checks (stored edges, `expect_contains`, ...) |
| [`training/`](training/README.md) | **Trained models**: how closely a freshly fine-tuned model's answers match its training dataset. The suite is the model's fine-tuning dataset and each example is a case (scheme `finetune_dataset`) | The **Eval** button of a trained model in the admin UI (`POST /v1/gb/training/models/{id}/evals`), run in the background by the server | An external LLM (the one used for knowledge ingestion), grading question/answer pairs in batches |

`knowledge/` is a `package main` command. `training/` is a library (`package training`) that `internal/api/training` calls: it needs the server's sidecar connection, LLM and database, so it has no command of its own. Running a suite against a trained model is a *suite run* in the code (`TrainedModelSuiteRun`), the API and the UI call it an eval. The design of the second one, its API and its tables: [docs/training/EVALS.md](../docs/training/EVALS.md).
