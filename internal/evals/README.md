# Evals

gligabao has two eval systems. They are separate: each lives in its own subdirectory, has its own code, README and tests, and checks something different.

**Terms.** An **eval case** (*case*) is one input/output combination, plus metadata, to be evaluated. Cases are grouped into an **eval** (a set of cases). A *run* executes cases (or a whole eval) once and records a *result* for each case; an *experiment* is a named series of runs. Both eval systems below use these words.

| | What it checks | How it runs | Judge |
|---|---|---|---|
| [`knowledge/`](knowledge/README.md) | **Knowledge extraction**: `remember` then `recall` against the real LLM; the stored temporal facts and the recalled answer | `./run_evals.sh` (`go run ./internal/evals/knowledge`), an end-to-end runner with JSON evals | Programmatic checks (stored edges, `expect_contains`, ...) |
| [`training/`](training/README.md) | **Trained models**: an LLM judges a trained model's answers against reference answers. Used for an **eval** (cases the model never saw, in `bin/evals_training/*.json`: does it generalize? `/evals`). The training data audits (did the training take?) are in `internal/training_audits` | The **Eval** button of a trained model in the admin UI (`POST /v1/gb/training/models/{id}/evals`), run in the background by the server; and the command-line runner of the evals, `./run_evals.sh -t training -model ID`, which does what the Eval button does and stores the run the same way | An external LLM (the one used for knowledge ingestion), grading question/answer pairs in batches |

The eval database code (experiments, runs, case results) is in [`db/`](db), the `internal/evals/db` package.

`./run_evals.sh` runs either kind: `-t knowledge` (default) or `-t training`. `knowledge/` is a `package main` command. `training/` is a library (`package training`) that `internal/training` calls (it needs the server's sidecar connection, LLM and database), plus the command `training/cmd` that runs the evals of `bin/evals_training/` against a trained model.

**Evals of trained models.** An eval is a file of `bin/evals_training/` (`{name, instruction, input?, output}` cases, read from disk), named after the training dataset its cases ask about (`world-knowledge-base`); the cases ask about knowledge in that dataset but are phrased differently from its examples, so they are ones the model never saw. Running an eval against a trained model is an *eval run* (`evalsdb.TrainedModelRun`, kind `eval`). Both the server (the Eval button, the API) and the command-line runner run the same cases the same way and store the same rows: an eval run in the universal eval tables, with one case result per case, whose `case_name` is the case's name, as for a knowledge case. Asking a model the questions of its own training dataset is a *training data audit*, which is not an eval: its cases are things the model was trained on, so it checks the training (memorization, fit, bad training data) and says nothing about generalization. It is in `internal/training_audits`.

The audits share no code, types or tables with the evals. The design: [docs/training/EVALS.md](../../docs/evals/training.md) and [docs/training/audit.md](../../docs/training/audit.md).
