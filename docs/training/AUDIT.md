# Training data audit of trained models

A **training data audit** (an *audit*, a `TrainedModelRun` of kind `audit` in the code) asks a trained model the questions of its **own fine-tuning dataset** and has an external LLM judge how closely its answers reproduce the dataset's. Every case is something the model was trained on, so the score is a sanity check of the training, not a measure of how well the model works on new inputs. It is what practitioners call a *training set sanity check* or *training data audit*:

- **Did the training take?** (memorization / capacity check) A model that cannot reproduce the demonstrations it was explicitly trained on points to too low a learning rate, too few steps, too little capacity, or a prompt/format or masking bug.
- **Is the training data clean?** (data auditing) The examples the model keeps failing are often the bad ones: label noise, ambiguous prompts, contradictory completions, broken formatting.
- **Does it replicate or generalize?** (fidelity check) Near-verbatim answers on every example, with nothing learned in between, is what overfitting looks like.

An audit is **not an eval**. An [eval of a trained model](EVALS.md) is for cases the model never saw, which is the only way to measure generalization. The two have their own tables and their own endpoint to start one, and share the rest (see [Audit or eval](#audit-or-eval)). The code is the eval subsystem [`internal/evals/training`](../../internal/evals/training/README.md) (the logic, shared with the evals), `internal/api/training/audits.go` (the endpoint that starts an audit) and `runs.go` (the manager and the background runner, shared with the evals).

## What happens

`POST /v1/gb/training/models/{id}/audits` (the **Audit** button in the admin UI):

1. The server finds the model's dataset by the name recorded when it was trained (`dataset`, without `.jsonl`) in the training datasets directory ([DATASETS.md](DATASETS.md)). It must still be there: a model trained on the sidecar's default dataset or a dataset that was deleted answers `400`. The sha256 of the file read is stored with the audit; if it differs from the model's `dataset_sha256`, the UI shows "dataset changed since training".
2. A row of `gb_training_audit_runs` is made (status `running`) and the request answers `202` with it. The audit goes on in the background.
3. The cases (the dataset's examples) are processed in **batches** of `batch_size` (default 10). For each batch the model answers every question (one sidecar `POST /infer/query` per question, with the training prompt: the `instruction`, and the `input` after a blank line; `max_tokens` 1024 and `temperature` 0.1 unless set), then the batch is judged by **one** LLM call. The judge is the LLM the server uses for knowledge ingestion (the Anthropic client; `judge_model` picks another of its models). Its calls are recorded in `gb_inferences` with type `training_audit_judge` (the evals' are `training_eval_judge`), stage `judge`, and totalled on the audit.
4. After each batch its cases are stored (one `gb_training_audit_cases` row each) and the counters of the audit's row are updated, so the progress can be read at any time. The audit ends `completed`, `cancelled` (by the user; what was done is kept) or `error` (with an `error` text: the model or the judge kept failing). Audits that were running when the server stopped are marked `error` at the next start.

Only one audit per trained model runs at a time (`409` otherwise). The model is asked **one question per prompt, one at a time** (see [Why one question per prompt](EVALS.md#why-one-question-per-prompt)); only the judge works in batches. The score, the judge's rubric and how it fails are the same as for the evals: see [The score](EVALS.md#the-score) and [How the judge works](EVALS.md#how-the-judge-works). Inference requests of an audit are not stored in the model's request history (`gb_trained_model_inferences`), they are the audit's cases.

Reading the result: a mean score near 5 means the model reproduces its training data. A low score is the thing to look at: sort the cases by score in the admin UI (lowest first) and see whether the failures are a pattern (a whole kind of example, a format) or a few odd examples (bad data). A high score says nothing about new inputs.

## Audit or eval

| | Training data audit | Eval of a trained model |
|---|---|---|
| Cases | the model's own fine-tuning dataset: things it was trained on | the cases of an [eval](EVALS.md) (`bin/evals_training/*.json`), meant to be ones the model never saw |
| Measures | memorization and fit; data quality | generalization |
| Run (Go) | `relational.TrainedModelRun`, kind `audit`, with `Dataset` and `DatasetSHA256` | the same type, kind `eval`, with `Eval` |
| Case (Go) | `relational.TrainedModelCase`, no `Name` (a dataset example has none) | the same type, with the case's `Name` |
| Start | `internal/api/training/audits.go`, `POST /v1/gb/training/models/{id}/audits` | `internal/api/training/evals.go`, `POST /v1/gb/training/models/{id}/evals` |
| Follow, cancel, list | `internal/api/training/runs.go` and `/v1/gb/training/audits/{id}[/events\|/cancel]`, `/models/{id}/audits` | the same code, at `/v1/gb/training/evals/{id}[/events\|/cancel]`, `/models/{id}/evals`; the same JSON shapes |
| Tables | `gb_training_audit_runs`, `gb_training_audit_cases` | the `training` rows of the universal eval tables, `gb_training_eval_run_meta`, `gb_training_case_meta` |
| Experiments | none | training experiments |
| Judge calls in `gb_inferences` | `training_audit_judge` | `training_eval_judge` |
| Statuses | a run is `running`, then `completed`, `error` or `cancelled`; a case is `judged` or `error` | the same words (one set of constants) |

Both reuse the same engine (`trainingeval.Run`: ask the model one question at a time, judge in batches), the same manager and the same admin UI components (the live view, the answers table). An audit has tables of its own because it has no experiments and no named cases to share with the universal eval tables.

## API

All under `/v1/gb/training/`, with `?db=<name>` to pick the database like the other training endpoints.

| Endpoint | |
|---|---|
| `POST /models/{id}/audits` | Starts an audit. Optional JSON body: `limit` (only the first n cases; default all), `batch_size` (pairs per judge call, 1 to 25, default 10), `judge_model`, `max_tokens` (default 1024), `temperature` (default 0.1). `202` with the audit; `400` bad body or dataset not usable, `404` unknown model, `409` one running, `503` no sidecar or no LLM. |
| `GET /models/{id}/audits[?limit=n]` | `{"audits": [...]}`, newest first (without cases). |
| `GET /audits/{id}` | `{"run": {...}, "cases": [...]}` with the cases stored so far, in dataset order. |
| `GET /audits/{id}/events` | Live feed (Server-Sent Events) of an audit, used by the admin UI's live view. The first event is a `snapshot` (`{run, cases}`: stored cases plus answers the judge has not scored yet, status `answered`); then an `answer` for each question as the model answers it (before it is judged), a `batch` (`run` with the progress, and the batch's judged `cases`) when a batch is judged, and an `end` (the stream then closes). For an audit that is over: the snapshot and the `end`. Cases are applied by `idx`, so a reconnect is safe. `404` unknown audit. |
| `POST /audits/{id}/cancel` | Stops a running audit (`202`); `409` if it is not running. |

An audit run has `id`, `trained_model_id`, `kind` (`audit`), `status`, `dataset`, `dataset_sha256`, `params` (batch size, max tokens, temperature, limit), `judge_model`, `num_examples`, the counters `num_answered`, `num_judged`, `num_errored`, `num_correct`, `num_partial`, `num_incorrect`, `score_sum` (mean = `score_sum / num_judged`), `num_batches`, `judge_calls`, `judge_input_tokens`, `judge_output_tokens`, `judge_cost_usd`, `error`, `created_at` and `finished_at`. A case has `id`, `run_id`, `idx`, `batch`, `instruction`, `input`, `reference` (the dataset's answer), `response` (the model's), `status` (`judged` or `error`), `error`, `score`, `reason`, `input_tokens`, `output_tokens` and `duration_ms`.

## Tables

An audit has two tables of its own, not rows of the universal eval tables: it has no experiments and no knowledge-style case definitions to share with them. Both are created on start-up (SQLite and PostgreSQL); deleting a trained model deletes its audits. The full description is in `internal/db/relational/sqlite/db_schema.go`.

| table | what it stores |
|---|---|
| `gb_training_audit_runs` | one row per audit: `trained_model_id`, `status` (`running`, `completed`, `error`, `cancelled`), `dataset`, `dataset_sha256`, `params`, `judge_model`, the counters (`num_examples` ... `score_sum`, `num_batches`), the judge's `judge_calls`, tokens and `judge_cost_usd` (updated after every batch), `error`, `started_at`, `finished_at` |
| `gb_training_audit_cases` | one row per example, written when its judge batch is done: `run_id`, `idx` (the example's index in the dataset, unique within a run), `batch`, `instruction`, `input`, `reference` (the dataset's answer), `response` (the model's), `status` (`judged` or `error`), `error`, `score`, `reason`, tokens and `duration_ms` |

Go (the stored type of both kinds is `relational.TrainedModelRun` and `TrainedModelCase`): `relational.RecordTrainedModelAuditRun`, `SaveTrainedModelAuditRunBatch`, `FinishTrainedModelAuditRun`, `FailRunningTrainedModelAuditRuns`, `GetTrainedModelAuditRun`, `ListTrainedModelAuditRuns`, `ListTrainedModelAuditCases`.
