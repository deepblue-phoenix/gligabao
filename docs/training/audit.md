# Training data audit of trained models

A **training data audit** (an *audit*) asks a trained model the questions of its **own fine-tuning dataset** and has an external LLM judge how closely its answers reproduce the dataset's. Every case is something the model was trained on, so the score is a sanity check of the training, not a measure of how well the model works on new inputs. It is what practitioners call a *training set sanity check* or *training data audit*:

- **Did the training take?** (memorization / capacity check) A model that cannot reproduce the demonstrations it was explicitly trained on points to too low a learning rate, too few steps, too little capacity, or a prompt/format or masking bug.
- **Is the training data clean?** (data auditing) The examples the model keeps failing are often the bad ones: label noise, ambiguous prompts, contradictory completions, broken formatting.
- **Does it replicate or generalize?** (fidelity check) Near-verbatim answers on every example, with nothing learned in between, is what overfitting looks like.

An audit is **not an eval**. An [eval of a trained model](../evals/training.md) is for cases the model never saw, which is the only way to measure generalization. The two share no code, types or tables (see [Audit or eval](#audit-or-eval)). The code is the package [`internal/training_audits`](../../internal/training_audits/doc.go): `core` (the data model), `db` (the storage, sqlite and postgres), `api` (the HTTP handlers) and the engine, judge and manager in the package itself.

## What happens

`POST /v1/gb/training/models/{id}/audits` (the **Audit** button in the admin UI):

1. The server finds the model's dataset by the name recorded when it was trained (`dataset`, without `.jsonl`) in the training datasets directory ([datasets.md](datasets.md)). It must still be there: a model trained on the sidecar's default dataset or a dataset that was deleted answers `400`. The sha256 of the file read is stored with the audit; if it differs from the model's `dataset_sha256`, the UI shows "dataset changed since training".
2. A row of `gb_neo_training_audit_runs` is made (status `running`) and the request answers `202` with it. The audit goes on in the background.
3. The cases (the dataset's examples) are processed in **batches** of `batch_size` (default 10). For each batch the model answers every question (one sidecar `POST /infer/query` per question, with the training prompt: the `instruction`, and the `input` after a blank line; `max_tokens` 1024 and `temperature` 0.1 unless set), then the batch is judged by **one** LLM call. The judge is the LLM the server uses for knowledge ingestion (the Anthropic client; `judge_model` picks another of its models). Its calls are recorded in `gb_inferences` with type `training_audit_judge` (the evals' are `training_eval_judge`), stage `judge`, and totalled on the audit.
4. After each batch its cases are stored (one `gb_neo_training_audit_case_runs` row each, written once, final, in the same transaction as the run's counters) and the counters of the audit's row are updated, so the progress can be read at any time. The audit ends `completed`, `cancelled` (by the user; what was done is kept) or `errored` (with an `error` text: the model or the judge kept failing). Audits that were running when the server stopped are marked `errored` at the next start. A cancel keeps the batches done; the cases of the batch it interrupted that were reached are stored as `cancelled`.

Only one audit per trained model runs at a time (`409` otherwise). The model is asked **one question per prompt, one at a time** (see [Why one question per prompt](../evals/training.md#why-one-question-per-prompt)); only the judge works in batches. The score, the judge's rubric and how it fails are the those of the evals: see [The score](../evals/training.md#the-score) and [How the judge works](../evals/training.md#how-the-judge-works). Inference requests of an audit are not stored in the model's request history (`gb_trained_model_inferences`), they are the audit's cases.

Reading the result: a mean score near 5 means the model reproduces its training data. A low score is the thing to look at: sort the cases by score in the admin UI (lowest first) and see whether the failures are a pattern (a whole kind of example, a format) or a few odd examples (bad data). A high score says nothing about new inputs.

## Audit or eval

| | Training data audit | Eval of a trained model |
|---|---|---|
| Cases | the model's own fine-tuning dataset: things it was trained on | the cases of an [eval](../evals/training.md) (`bin/evals_training/*.json`), meant to be ones the model never saw |
| Measures | memorization and fit; data quality | generalization |
| Code | `internal/training_audits` (types `core.AuditRun`, `core.AuditCaseRun`) | `internal/evals/training` and `internal/training` (types `evalsdb.TrainedModelRun`, `TrainedModelCase`) |
| Endpoints | `internal/training_audits/api`, `/v1/gb/training/models/{id}/audits`, `/audits/{id}[/events\|/cancel]` | `internal/api/training`, the same under `/evals` |
| Tables | `gb_neo_training_audit_runs`, `gb_neo_training_audit_case_runs` | the `training` rows of the universal eval tables, `gb_training_eval_run_meta`, `gb_training_case_meta` |
| Experiments | none | training experiments |
| Judge calls in `gb_inferences` | `training_audit_judge` | `training_eval_judge` |
| Statuses | a run is `pending`, `running`, then `completed`, `errored` or `cancelled`; a case is the same, and its verdict is the `judge_score` | a run is `running`, then `completed`, `error` or `cancelled`; a case is `judged` or `error` |

The admin UI shows both with the same components (the live view, the answers table); `auditRun` and `auditCase` in `admin_ui/src/eval_live.ts` turn the JSON of an audit into the shape it shows an eval's.

## API

All under `/v1/gb/training/`.

| Endpoint | |
|---|---|
| `POST /models/{id}/audits` | Starts an audit. Optional JSON body: `limit` (only the first n cases; default all), `batch_size` (pairs per judge call, 1 to 25, default 10), `judge_model`, `max_tokens` (default 1024), `temperature` (default 0.1). `202` with the audit; `400` bad body or dataset not usable, `404` unknown model, `409` one running, `503` no sidecar or no LLM. |
| `GET /models/{id}/audits[?limit=n]` | `{"audits": [...]}`, newest first (without case runs). |
| `GET /audits/{id}` | The audit with its `case_runs` stored so far, in dataset order. |
| `GET /audits/{id}/events` | Live feed (Server-Sent Events) of an audit, used by the admin UI's live view. Every event's data is `{type, run, case_runs}`. The first is a `snapshot` (stored case runs plus answers the judge has not scored yet, status `running`); then an `answer` for each question as the model answers it (before it is judged), a `batch` (`run` with the progress, and the batch's final `case_runs`) when a batch is judged, and an `end` (the stream then closes). For an audit that is over: the snapshot and the `end`. Case runs are applied by `idx`, so a reconnect is safe. `404` unknown audit. |
| `POST /audits/{id}/cancel` | Stops a running audit (`202`); `409` if it is not running. |

An audit run has `id`, `trained_model_id`, `dataset`, `dataset_sha256`, `status`, `error`, `params` (batch size, max tokens, temperature, limit), `counts` (`total`, `pending`, `running`, `completed`, `errored`, `cancelled`), `cost_usd`, the statistics `judge_model`, `num_answered`, `num_judged`, `num_correct`, `num_partial`, `num_incorrect`, `score_sum` (mean = `score_sum / num_judged`), `num_batches`, `judge_calls`, `judge_input_tokens`, `judge_output_tokens`, and `created_at`, `started_at`, `finished_at`. A case run has `id`, `run_id`, `idx`, `status`, `error`, `instruction`, `input`, `reference` (the dataset's answer), `response` (the model's), `batch`, `judge_score` (0 to 5), `judge_reason`, `input_tokens`, `output_tokens`, `duration_ms` and `created_at`.

## Tables

An audit has two tables of its own, created on start-up (SQLite and PostgreSQL) by `training_audits.Open`; the full description is in `internal/training_audits/db/sqlite/schema.go`.

| table | what it stores |
|---|---|
| `gb_neo_training_audit_runs` | one row per audit: `trained_model_id`, `dataset`, `dataset_sha256`, `status`, `error`, `params`, `counts_*`, `cost_usd`, the statistics (`judge_model`, `num_*`, `score_sum`, `num_batches`, `judge_*`), `created_at`, `started_at`, `finished_at` (updated after every batch) |
| `gb_neo_training_audit_case_runs` | one row per example, written when its judge batch is done: `run_id`, `idx` (the example's index in the dataset, unique within a run), `status`, `error`, `instruction`, `input`, `reference`, `response`, `batch`, `judge_score`, `judge_reason`, tokens and `duration_ms`; deleting an audit deletes its case runs |

Audits made before version 0.79.0 are in the tables `gb_training_audit_runs` and `gb_training_audit_cases` of the old code, which the server no longer creates, reads or shows (an existing database keeps them; they can be dropped).

Go: `training_audits.Manager` (`Start`, `ListRuns`, `GetRun`, `GetRunWithCaseRuns`, `CancelRun`, `Watch`) over `training_audits.Ops` (`SQLiteOps`, `PostgresOps`), whose `Store` is the functions of `training_audits/db/sqlite` or `postgres`.
