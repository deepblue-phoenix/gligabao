# Evals of trained models (LLM as a judge)

An **eval** of a trained model asks the model the questions of a dataset and has an external LLM judge how closely its answers agree with the dataset's. For now the dataset is the model's own **training dataset**, so the score says how well the model learned what it was trained on. The code is the eval subsystem [`evals/training`](../../evals/training/README.md) (the logic) and `internal/api/training/evals.go` (the HTTP endpoints and the background runner). It is separate from the knowledge-extraction evals ([evals.md](../evals.md), `evals/knowledge`).

## What happens

`POST /v1/gb/training/models/{id}/evals` (the **Eval** button in the admin UI):

1. The server finds the model's dataset by the name recorded when it was trained (`dataset`, without `.jsonl`) in the training datasets directory ([DATASETS.md](DATASETS.md)). It must still be there: a model trained on the sidecar's default dataset (jobs from before datasets were required) or a dataset that was deleted answers `400`. The sha256 of the file read is stored with the eval; if it differs from the model's `dataset_sha256`, the UI shows "dataset changed since training".
2. A row of `gb_trained_model_evals` is made (status `running`) and the request answers `202` with it. The eval goes on in the background.
3. The examples are processed in **batches** of `batch_size` (default 10). For each batch the model answers every question (one sidecar `POST /infer/query` per question, with the training prompt: the `instruction`, and the `input` after a blank line; `max_tokens` 1024 and `temperature` 0.1 unless set), then the batch is judged by **one** LLM call. The judge LLM is the one the server uses for knowledge ingestion (the Anthropic client; `judge_model` picks another of its models). Its calls are recorded in `gb_inferences` (type `eval_judge`, stage `judge`) like the other LLM calls, and totalled on the eval.
4. After each batch its items are stored in `gb_trained_model_eval_items` and the counters of the eval row are updated, so the progress can be read at any time. The eval ends `completed`, `cancelled` (by the user; what was done is kept) or `failed` (with an `error`: the model or the judge kept failing, see the [subsystem README](../../evals/training/README.md#how-it-works)). Evals that were running when the server stopped are marked `failed` at the next start.

Only one eval per trained model runs at a time (`409` otherwise). Inference requests of an eval are not stored in the model's request history (`gb_trained_model_inferences`), they are the eval's items.

The trained model is asked **one question at a time**, in dataset order; only the judge works in batches. (Parallel questions are not offered: a Modal inference function that gets parallel calls starts more GPU containers, each of which loads the model.) A cold model makes the first question slow (the model load, see [../modal.md](../modal.md#cold-and-warm-starts)); the rest run warm. A full eval of a 500-example dataset is 500 inferences, so it takes a while: use `limit` to try it on the first few.

## The score

The judge gives each answer 0 to 5 against the dataset's answer: 5 same, 4 correct with minor differences, 3 partly correct, 2 mostly wrong, 1 wrong, 0 empty/nonsense/refusal. 4 or 5 is **correct**, 2 or 3 **partial**, 0 or 1 **incorrect**. An eval reports the mean score of the judged items (0 to 5), the share of correct ones, the three counts, and the number of failed items (not counted in the mean). The full rubric and the prompt are in `evals/training/judge.go`.

The judge is an LLM: its scores are not exact, and repeating an eval can differ a little. The reference answer is the dataset's `output`, so what is measured is agreement with it, not truth.

## API

All under `/v1/gb/training/`, with `?db=<name>` to pick the database like the other training endpoints.

| Endpoint | |
|---|---|
| `POST /models/{id}/evals` | Starts an eval. Optional JSON body: `limit` (only the first n questions; default all), `batch_size` (pairs per judge call, 1 to 25, default 10), `judge_model`, `max_tokens` (default 1024), `temperature` (default 0.1). `202` with the eval; `400` bad body or dataset not usable, `404` unknown model, `409` one running, `503` no sidecar or no LLM. |
| `GET /models/{id}/evals[?limit=n]` | `{"evals": [...]}`, newest first (without items). |
| `GET /evals/{id}` | `{"eval": {...}, "items": [...]}` with the items stored so far, in dataset order. |
| `POST /evals/{id}/cancel` | Stops a running eval (`202`); `409` if it is not running. |

An eval has `status`, `dataset`, `dataset_sha256`, `params` (batch size, max tokens, temperature, limit), `judge_model`, `num_examples`, the counters `num_answered`, `num_judged`, `num_failed`, `num_correct`, `num_partial`, `num_incorrect`, `score_sum` (mean = `score_sum / num_judged`), `num_batches`, `judge_calls`, `judge_input_tokens`, `judge_output_tokens`, `judge_cost_usd`, `error`, `created_at` and `finished_at`. An item has `idx`, `batch`, `instruction`, `input`, `reference` (the dataset's answer), `response` (the model's), `status` (`judged` or `failed`), `error`, `score`, `reason`, `input_tokens`, `output_tokens` and `duration_ms`.

## Tables

`gb_trained_model_evals` (one row per eval) and `gb_trained_model_eval_items` (one per question, cascading), in SQLite and PostgreSQL; the columns are documented in `internal/db/relational/sqlite/db_schema.go`. Go: `relational.RecordTrainedModelEval`, `SaveTrainedModelEvalBatch`, `FinishTrainedModelEval`, `GetTrainedModelEval`, `ListTrainedModelEvals`, `ListTrainedModelEvalItems`.
