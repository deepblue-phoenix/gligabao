# Evals

gligabao has two eval systems. They are separate: each lives in its own subdirectory, has its own code, README and tests, and checks something different.

**Terms.** An **eval case** (*case*) is one input/output combination, plus metadata, to be evaluated. Cases are grouped into an **eval** (a set of cases). A *run* executes cases (or a whole eval) once and records a *result* for each case; an *experiment* is a named series of runs. Both eval systems below use these words.

| | What it checks | How it runs | Judge |
|---|---|---|---|
| [`knowledge/`](knowledge/README.md) | **Knowledge extraction**: `remember` then `recall` against the real LLM; the stored temporal facts and the recalled answer | `./run_evals.sh` (`go run ./evals/knowledge`), an end-to-end runner with JSON evals | Programmatic checks (stored edges, `expect_contains`, ...) |
| [`training/`](training/README.md) | **Trained models**: an LLM judges a trained model's answers against reference answers. Used for a **training data audit** (the model's own fine-tuning dataset: did the training take? `/audits`) and for **evals** (cases the model never saw: does it generalize? `/evals`) | The **Audit** and **Eval** buttons of a trained model in the admin UI (`POST /v1/gb/training/models/{id}/audits` and `.../evals`), run in the background by the server | An external LLM (the one used for knowledge ingestion), grading question/answer pairs in batches |

`knowledge/` is a `package main` command. `training/` is a library (`package training`) that `internal/api/training` calls: it needs the server's sidecar connection, LLM and database, so it has no command of its own. Running an eval against a trained model is an *eval run* in the code (`TrainedModelEvalRun`); asking it its own training dataset is an *audit run* (`TrainedModelAuditRun`), which is not an eval: its cases are things the model was trained on, so it checks the training (memorization, fit, bad training data) and says nothing about generalization. They share the engine and the judge and have separate API endpoints, tables and Go types. The design of the second system, its API and its tables: [docs/training/AUDIT.md](../docs/training/AUDIT.md) and [docs/training/EVALS.md](../docs/training/EVALS.md).
