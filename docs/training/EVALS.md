# Evals of trained models (LLM as a judge)

An **eval** of a trained model asks the model the questions of a dataset and has an external LLM judge how closely its answers agree with the dataset's. For now the dataset is the model's own **training dataset**, so the score says how well the model learned what it was trained on. The code is the eval subsystem [`evals/training`](../../evals/training/README.md) (the logic) and `internal/api/training/evals.go` (the HTTP endpoints and the background runner). It is separate from the knowledge-extraction evals ([evals.md](../evals.md), `evals/knowledge`).

## What happens

`POST /v1/gb/training/models/{id}/evals` (the **Eval** button in the admin UI):

1. The server finds the model's dataset by the name recorded when it was trained (`dataset`, without `.jsonl`) in the training datasets directory ([DATASETS.md](DATASETS.md)). It must still be there: a model trained on the sidecar's default dataset (jobs from before datasets were required) or a dataset that was deleted answers `400`. The sha256 of the file read is stored with the eval; if it differs from the model's `dataset_sha256`, the UI shows "dataset changed since training".
2. A run of type `training` is made with one eval in it (a row of `gb_experiment_results` and its `gb_training_eval_meta`, status `running`) and the request answers `202` with it; the eval's ID is that of the `gb_experiment_results` row. The eval goes on in the background.
3. The examples are processed in **batches** of `batch_size` (default 10). For each batch the model answers every question (one sidecar `POST /infer/query` per question, with the training prompt: the `instruction`, and the `input` after a blank line; `max_tokens` 1024 and `temperature` 0.1 unless set), then the batch is judged by **one** LLM call. The judge LLM is the one the server uses for knowledge ingestion (the Anthropic client; `judge_model` picks another of its models). Its calls are recorded in `gb_inferences` (type `eval_judge`, stage `judge`) like the other LLM calls, and totalled on the eval.
4. After each batch its items are stored in `gb_training_eval_items` and the counters of the eval's metadata row are updated, so the progress can be read at any time. The eval ends `completed`, `cancelled` (by the user; what was done is kept) or `failed` (with an `error`: the model or the judge kept failing, see the [subsystem README](../../evals/training/README.md#how-it-works)). Evals that were running when the server stopped are marked `failed` at the next start.

Only one eval per trained model runs at a time (`409` otherwise). Inference requests of an eval are not stored in the model's request history (`gb_trained_model_inferences`), they are the eval's items.

The trained model is asked **one question per prompt, one question at a time**, in dataset order; only the judge works in batches (see [Why one question per prompt](#why-one-question-per-prompt)). A cold model makes the first question slow (the model load, see [../modal.md](../modal.md#cold-and-warm-starts)); the rest run warm. A full eval of a 500-example dataset is 500 inferences, so it takes a while: use `limit` to try it on the first few.

## Why one question per prompt

The trained model is asked **exactly one question per prompt**, never several questions in one prompt. Only the judge works in batches. This matters for the Gemma 4 models gligabao fine-tunes (E4B, the "4b", and the 12b), and the eval is built around it:

- **The model was trained on single turns.** Every training example is one user turn with one question and one model turn with its answer (`<|turn>user\nQ<turn|>\n<|turn>model\nA<turn|>`, see [TRAINING_DATA_FORMAT.md](TRAINING_DATA_FORMAT.md)). A prompt that holds ten questions is a shape the model never saw. It may answer only the first one, run the answers together, drop the short answer style it learned, or invent a list format. An eval like that would measure how well the model follows a multi-question instruction, not what it learned from the dataset, and it would score a model lower for a reason that has nothing to do with its training.
- **Small models interfere across items.** At 4B and 12B parameters the questions in one context bleed into each other: a fact asked about in question 2 shows up in the answer to question 5, and the answers get worse as the prompt grows. Which questions are answered well would then depend on what they were batched with, so the same question could score differently from one eval to the next.
- **The output budget is shared.** One `max_tokens` would cover all the answers, so the last ones are cut off first, and the answers have to be split back out by position, which is one more place to go wrong (a missing or merged answer shifts all the later ones onto the wrong questions).
- **The check is on the training prompt.** The eval asks whether the model reproduces its training answers for the prompt shape it was trained on, one question and one answer. Only the single-question prompt tests that.

So the model is asked sequentially (one sidecar `POST /infer/query` per question, in dataset order), and the answers are kept per question.

**Two things that are not the same as putting several questions in one prompt:**

- *Batching at the inference engine*: running several independent prompts as separate sequences in one GPU `generate` call (left-padded, with attention masks). Each sequence still sees only its own question, so the answers match the one-at-a-time ones up to small numerical differences, and it would be much faster. gligabao does not do it: the sidecar's `/infer/query` takes one prompt per call. If it is added, compare its answers with the single-prompt ones before trusting its scores.
- *Parallel calls*: several `/infer/query` calls at once are correct too, but a Modal inference function that gets parallel calls starts more GPU containers, each of which loads the model, so the eval does not offer it.

**The judge is different.** It is a large LLM that grades *independent* items (a JSON array of question, reference and answer, with an id each), not a small model being asked to answer from its own weights, so batching them (10 by default, at most 25) costs one call per batch and does not change what is measured. Batching can still have small effects on the judge's scores: it may drift or anchor on earlier items in the batch. To check it, run the same eval on a sample (`limit`) with `batch_size` 1 and with 10 and compare the mean scores; if they differ noticeably, use a smaller batch.

## The score

The judge gives each answer 0 to 5 against the dataset's answer: 5 same, 4 correct with minor differences, 3 partly correct, 2 mostly wrong, 1 wrong, 0 empty/nonsense/refusal. 4 or 5 is **correct**, 2 or 3 **partial**, 0 or 1 **incorrect**. An eval reports the mean score of the judged items (0 to 5), the share of correct ones, the three counts, and the number of failed items (not counted in the mean). The full rubric and the prompt are in `evals/training/judge.go`.

The judge is an LLM: its scores are not exact, and repeating an eval can differ a little. The reference answer is the dataset's `output`, so what is measured is agreement with it, not truth.

## API

All under `/v1/gb/training/`, with `?db=<name>` to pick the database like the other training endpoints.

| Endpoint | |
|---|---|
| `POST /models/{id}/evals` | Starts an eval. Optional JSON body: `limit` (only the first n questions; default all), `batch_size` (pairs per judge call, 1 to 25, default 10), `judge_model`, `max_tokens` (default 1024), `temperature` (default 0.1), and `experiment` (a name of at most 120 characters: the eval becomes a run of that [experiment](#experiments), made if needed; with `experiment_description` and `experiment_config`, an object, stored when it is made). `202` with the eval; `400` bad body or dataset not usable, `404` unknown model, `409` one running, `503` no sidecar or no LLM. |
| `GET /models/{id}/evals[?limit=n]` | `{"evals": [...]}`, newest first (without items). |
| `GET /evals/{id}` | `{"eval": {...}, "items": [...]}` with the items stored so far, in dataset order. |
| `GET /evals/{id}/events` | Live feed (Server-Sent Events) of an eval, used by the admin UI's live view. The first event is a `snapshot` (`{eval, items}`: stored items plus answers the judge has not scored yet, status `answered`); then an `answer` for each question as the model answers it (before it is judged), a `batch` (`eval` with the progress, and the batch's judged `items`) when a batch is judged, and an `end` (the stream then closes). For an eval that is over: the snapshot and the `end`. Items are applied by `idx`, so a reconnect is safe. `404` unknown eval. |
| `GET /experiments` | `{"experiments": [...]}`, the [training experiments](#experiments), newest first, each with `num_runs`. |
| `GET /experiments/{id}` | `{"experiment": {...}, "runs": [...]}`, one experiment with its runs, oldest first. `404` unknown experiment. |
| `POST /evals/{id}/cancel` | Stops a running eval (`202`); `409` if it is not running. |

An eval has `status`, `dataset`, `dataset_sha256`, `params` (batch size, max tokens, temperature, limit), `judge_model`, `num_examples`, the counters `num_answered`, `num_judged`, `num_failed`, `num_correct`, `num_partial`, `num_incorrect`, `score_sum` (mean = `score_sum / num_judged`), `num_batches`, `judge_calls`, `judge_input_tokens`, `judge_output_tokens`, `judge_cost_usd`, `error`, `created_at` and `finished_at`. An item has `idx`, `batch`, `instruction`, `input`, `reference` (the dataset's answer), `response` (the model's), `status` (`judged` or `failed`), `error`, `score`, `reason`, `input_tokens`, `output_tokens` and `duration_ms`.

## Experiments

A **training experiment** is a named series of evals, to compare trained models (training recipes) by their scores: "lr-sweep-4b", "dataset-v2 vs v1". Start an eval with `"experiment": "<name>"` and it becomes a **run** of that experiment; the first eval with a name makes the experiment (with the `experiment_description` and `experiment_config` of that request), the later ones with the same name join it and their description and config are ignored. An eval without a name is in no experiment, and an eval is in at most one.

A run holds no results of its own, only the link to its eval: the status, the counters, the judge's cost and the items stay in the eval, so there is one copy, and a running run shows its progress. `GET /experiments/{id}` returns each run with:

- `eval`: the eval as it is now (`status`, the counters, `judge_model`, `judge_cost_usd`, `params`, `dataset`, ...);
- `model`: the trained model that was evaluated (`id`, `name`, `base_model`, `model_size`, `num_train_steps`, `final_loss` and `params`, the training settings), so runs can be compared by recipe;
- `status` (the eval's), `mean_score` (0 to 5) and `correct_rate` (the share of judged answers that is correct), both `null` until an answer is judged.

These are separate from the knowledge-extraction experiments of [`evals/knowledge`](../experiments.md) (the same tables, with `type = 'knowledge'`). Deleting a trained model deletes its evals and so their runs; the experiment stays.

## Tables

Both kinds of eval (knowledge and training) share one set of tables; every universal table has a `type` column (`knowledge` or `training`), and what only one kind has is in its metadata table. The full description is in `internal/db/relational/sqlite/db_schema.go` (and [../experiments.md](../experiments.md#data-model)).

| table | what a training eval stores there |
|---|---|
| `gb_experiments` | the experiment (`type = 'training'`; the name is unique within a type): `name`, `description`, `config` |
| `gb_experiment_evals` | the evals the experiment has executed: `suite` (`''` until training suites exist) and `eval_name` (the dataset's name for now) |
| `gb_experiment_runs` | the run: ONE per eval. `experiment_id` is NULL for an eval started without an experiment name; `params` are the eval's settings (`batch_size`, `max_tokens`, `temperature`, `limit`); `status` is the eval's once it ends; totals (`total_evals` 1, `passed_evals` 1 if it completed, `cost_usd` the judge's) are set then |
| `gb_experiment_results` | the eval, universal part: `status` (`running`, `completed`, `failed`, `cancelled`), `error`, `cost_usd` (the judge's, updated after every batch), `started_at`, `finished_at` (NULL while it runs) |
| `gb_training_eval_meta` | the training-only part, one row per eval: `trained_model_id`, `dataset`, `dataset_sha256`, `judge_model`, the counters (`num_examples` ... `score_sum`, `num_batches`) and the judge's `judge_calls` and tokens |
| `gb_training_eval_items` | one row per question (cascading from the metadata row) |

Go: `relational.RecordTrainedModelEval`, `SaveTrainedModelEvalBatch`, `FinishTrainedModelEval`, `GetTrainedModelEval`, `ListTrainedModelEvals`, `ListTrainedModelEvalItems`; `relational.EnsureTrainingExperiment`, `GetTrainingExperiment`, `ListTrainingExperiments`, `StartTrainingExperimentRun` (puts the eval's run in the experiment), `GetTrainingExperimentRun`, `ListTrainingExperimentRuns`.
