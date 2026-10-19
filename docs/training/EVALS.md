# Evals of trained models (LLM as a judge)

> **Audit or eval.** An **eval** is for cases the model never saw, so its score says how well the model generalizes. Asking a model the questions of its own fine-tuning dataset is a **[training data audit](AUDIT.md)** (a sanity check that the training took, `/audits`), not an eval: every case is something the model was trained on. Both are runs of the same kind (one Go type, one manager, the same endpoints to follow a run, the same judge, the same live view); they differ in where the cases come from. Everything about the judge, the score and the one-question-per-prompt rule on this page applies to both.

An **eval run** of a trained model is one run of an **eval** (a named set of cases) against the model (`TrainedModelRun` of kind `eval` in the code; the HTTP API and the admin UI just call it an *eval*, hence the `/evals` URLs): it asks the model the **cases** of the eval and has an external LLM judge how closely its answers agree with the expected ones. A case is one input/output combination, plus metadata, to be evaluated; here it is a name, a question and the answer expected.

The evals are the files of [`bin/evals_training/`](../../bin/evals_training) (`*.json`, read from disk: `serve --training-eval-dir`, default `evals_training` next to `--db`, so `bin/evals_training`): a list of `{name, instruction, input?, output}`. An eval is named after the **training dataset** its cases ask about (`world-knowledge-base`, the file `world-knowledge-base.json`, 10 cases). The cases ask about knowledge in that dataset but are phrased differently from its examples (several facts per question), so they are cases the model never saw: they check whether it learned the facts, not the exact training prompts. A model trained on a dataset has the eval of that name by default; any eval can be asked for by name. The code is the eval subsystem [`evals/training`](../../evals/training/README.md) (the logic), `internal/api/training/evals.go` (the endpoint that starts an eval) and `runs.go` (the manager and the background runner, shared with the audits). It is separate from the knowledge-extraction evals ([evals.md](../evals.md), `evals/knowledge`), whose runs and cases use the same tables.

The command-line runner (`./run_evals.sh -t training -model ID`) runs the same evals the same way as the Eval button and stores the run in the database in the same way, so a run from the command line shows in the admin UI.

## What happens

`POST /v1/gb/training/models/{id}/evals` (the **Eval** button in the admin UI):

1. The server picks the eval: the `eval` of the request, else the one named like the model's training dataset (`dataset`, without `.jsonl`). An unknown eval or case name, or no `eval` in the request for a model with no dataset (trained on the sidecar's default dataset) or whose dataset no eval is named after, answers `400`. The training datasets directory is not read: the cases are in the eval.
2. A run of type `training` is made (a row of `gb_experiment_runs` with its `gb_training_eval_run_meta`, `eval` = the eval's name, status `running`) and the request answers `202` with it; the eval run's ID is that of the run. The eval goes on in the background.
3. The cases (the eval's, or the ones named in `cases`, the first `limit` of them) are processed in **batches** of `batch_size` (default 10). For each batch the model answers every question (one sidecar `POST /infer/query` per question, with the training prompt: the `instruction`, and the `input` after a blank line; `max_tokens` 1024 and `temperature` 0.1 unless set), then the batch is judged by **one** LLM call. The judge LLM is the one the server uses for knowledge ingestion (the Anthropic client; `judge_model` picks another of its models). Its calls are recorded in `gb_inferences` (type `training_eval_judge`, stage `judge`) like the other LLM calls, and totalled on the eval.
4. After each batch its cases are stored (one `gb_experiment_case_results` row each, whose `case_name` is the case's name, with a `gb_training_case_meta` row) and the counters of the eval's metadata row are updated, so the progress can be read at any time. The eval ends `completed` (it ran to the end; a score is not a verdict), `cancelled` (by the user; what was done is kept) or `error` (with an `error` text: the model or the judge kept failing, see the [subsystem README](../../evals/training/README.md#how-it-works)). Evals that were running when the server stopped are marked `error` at the next start.

Only one eval per trained model runs at a time (`409` otherwise). Inference requests of an eval are not stored in the model's request history (`gb_trained_model_inferences`), they are the eval's cases.

The trained model is asked **one question per prompt, one question at a time**, in the eval's order; only the judge works in batches (see [Why one question per prompt](#why-one-question-per-prompt)). A cold model makes the first question slow (the model load, see [../modal.md](../modal.md#cold-and-warm-starts)); the rest run warm. An eval has a handful to some tens of cases, so it is quick; use `limit` or `cases` to try it on a few.

## Why one question per prompt

The trained model is asked **exactly one question per prompt**, never several questions in one prompt. Only the judge works in batches. This matters for the Gemma 4 models gligabao fine-tunes (E4B, the "4b", and the 12b), and the eval is built around it:

- **The model was trained on single turns.** Every training example is one user turn with one question and one model turn with its answer (`<|turn>user\nQ<turn|>\n<|turn>model\nA<turn|>`, see [TRAINING_DATA_FORMAT.md](TRAINING_DATA_FORMAT.md)). A prompt that holds ten questions is a shape the model never saw. It may answer only the first one, run the answers together, drop the short answer style it learned, or invent a list format. An eval like that would measure how well the model follows a multi-question instruction, not what it learned from the dataset, and it would score a model lower for a reason that has nothing to do with its training.
- **Small models interfere across cases.** At 4B and 12B parameters the questions in one context bleed into each other: a fact asked about in question 2 shows up in the answer to question 5, and the answers get worse as the prompt grows. Which questions are answered well would then depend on what they were batched with, so the same question could score differently from one eval to the next.
- **The output budget is shared.** One `max_tokens` would cover all the answers, so the last ones are cut off first, and the answers have to be split back out by position, which is one more place to go wrong (a missing or merged answer shifts all the later ones onto the wrong questions).
- **The check is on the training prompt.** The eval asks whether the model reproduces its training answers for the prompt shape it was trained on, one question and one answer. Only the single-question prompt tests that.

So the model is asked sequentially (one sidecar `POST /infer/query` per question, in the eval's order), and the answers are kept per question.

**Two things that are not the same as putting several questions in one prompt:**

- *Batching at the inference engine*: running several independent prompts as separate sequences in one GPU `generate` call (left-padded, with attention masks). Each sequence still sees only its own question, so the answers match the one-at-a-time ones up to small numerical differences, and it would be much faster. gligabao does not do it: the sidecar's `/infer/query` takes one prompt per call. If it is added, compare its answers with the single-prompt ones before trusting its scores.
- *Parallel calls*: several `/infer/query` calls at once are correct too, but a Modal inference function that gets parallel calls starts more GPU containers, each of which loads the model, so the eval does not offer it.

**The judge is different.** It is a large LLM that grades *independent* cases (a JSON array of question, reference and answer, with an id each), not a small model being asked to answer from its own weights, so batching them (10 by default, at most 25) costs one call per batch and does not change what is measured. Batching can still have small effects on the judge's scores: it may drift or anchor on earlier cases in the batch. To check it, run the same eval on a sample (`limit`) with `batch_size` 1 and with 10 and compare the mean scores; if they differ noticeably, use a smaller batch.

## The score

The judge gives each answer 0 to 5 against the expected answer (the case's `output`; for an audit the dataset's): 5 same, 4 correct with minor differences, 3 partly correct, 2 mostly wrong, 1 wrong, 0 empty/nonsense/refusal. 4 or 5 is **correct**, 2 or 3 **partial**, 0 or 1 **incorrect**. An eval reports the mean score of the judged cases (0 to 5), the share of correct ones, the three counts, and the number of errored items (not counted in the mean). The full rubric and the prompt are in `evals/training/judge.go`.

The judge is an LLM: its scores are not exact, and repeating an eval can differ a little. The reference answer is the case's `output`, so what is measured is agreement with it, not truth.

## API

All under `/v1/gb/training/`, with `?db=<name>` to pick the database like the other training endpoints.

| Endpoint | |
|---|---|
| `POST /models/{id}/evals` | Starts an eval. Optional JSON body: `eval` (the name of the eval to run, see `GET /eval-definitions`; default the eval named like the model's training dataset), `cases` (run only the cases with these names; default all of the eval's), `limit` (only the first n cases; default all), `batch_size` (pairs per judge call, 1 to 25, default 10), `judge_model`, `max_tokens` (default 1024), `temperature` (default 0.1), and `experiment` (a name of at most 120 characters: the eval becomes a run of that [experiment](#experiments), made if needed; with `experiment_description` and `experiment_config`, an object, stored when it is made). `202` with the eval; `400` bad body, an unknown eval or case, or no eval to default to, `404` unknown model, `409` one running, `503` no sidecar or no LLM. |
| `GET /models/{id}/evals[?limit=n]` | `{"evals": [...]}`, newest first (without cases). |
| `GET /eval-definitions` | `{"evals": [{"name": ..., "cases": [{"name", "instruction", "input", "output"}, ...]}, ...]}`: the evals the server can run (the files of the evals directory). |
| `GET /evals/{id}` | `{"run": {...}, "cases": [...]}` with the cases stored so far, in the eval's order. |
| `GET /evals/{id}/events` | Live feed (Server-Sent Events) of an eval, used by the admin UI's live view. The first event is a `snapshot` (`{run, cases}`: stored cases plus answers the judge has not scored yet, status `answered`); then an `answer` for each question as the model answers it (before it is judged), a `batch` (`run` with the progress, and the batch's judged `cases`) when a batch is judged, and an `end` (the stream then closes). For an eval that is over: the snapshot and the `end`. Cases are applied by `idx`, so a reconnect is safe. `404` unknown eval. |
| `GET /experiments` | `{"experiments": [...]}`, the [training experiments](#experiments), newest first, each with `num_runs`. |
| `GET /experiments/{id}` | `{"experiment": {...}, "runs": [...]}`, one experiment with its runs, oldest first. `404` unknown experiment. |
| `POST /evals/{id}/cancel` | Stops a running eval (`202`); `409` if it is not running. |

An eval run has `id`, `trained_model_id`, `kind` (`eval`), `status`, `eval` (the eval's name), `params` (batch size, max tokens, temperature, limit), `judge_model`, `num_examples`, the counters `num_answered`, `num_judged`, `num_errored`, `num_correct`, `num_partial`, `num_incorrect`, `score_sum` (mean = `score_sum / num_judged`), `num_batches`, `judge_calls`, `judge_input_tokens`, `judge_output_tokens`, `judge_cost_usd`, `error`, `created_at` and `finished_at`. (An audit's run, which has the same fields, has `dataset` and `dataset_sha256` in place of `eval`.) A case has `id`, `run_id`, `idx` (its position in the eval), `name` (the case's name), `batch`, `instruction`, `input`, `reference` (the expected answer), `response` (the model's), `status` (`judged`, or `error`), `error`, `score`, `reason`, `input_tokens`, `output_tokens` and `duration_ms`.

## Experiments

A **training experiment** is a named series of evals, to compare trained models (training recipes) by their scores: "lr-sweep-4b", "dataset-v2 vs v1". Start an eval with `"experiment": "<name>"` and it becomes a **run** of that experiment; the first eval with a name makes the experiment (with the `experiment_description` and `experiment_config` of that request), the later ones with the same name join it and their description and config are ignored. An eval without a name is in no experiment, and an eval is in at most one.

A run holds no results of its own, only the link to its eval: the status, the counters, the judge's cost and the cases stay in the eval, so there is one copy, and a running run shows its progress. `GET /experiments/{id}` returns each run with:

- `eval`: the eval as it is now (`status`, the counters, `judge_model`, `judge_cost_usd`, `params`, `eval`, ...);
- `model`: the trained model that was evaluated (`id`, `name`, `base_model`, `model_size`, `num_train_steps`, `final_loss` and `params`, the training settings), so runs can be compared by recipe;
- `status` (the eval's), `mean_score` (0 to 5) and `correct_rate` (the share of judged answers that is correct), both `null` until an answer is judged.

These are separate from the knowledge-extraction experiments of [`evals/knowledge`](../experiments.md) (the same tables, with `type = 'knowledge'`). Deleting a trained model deletes its evals and so their runs; the experiment stays.

## Tables

Both kinds of eval (knowledge and training) share one set of tables; every universal table has a `type` column (`knowledge` or `training`), and what only one kind has is in its metadata table. The full description is in `internal/db/relational/sqlite/db_schema.go` (and [../experiments.md](../experiments.md#data-model)).

| table | what a training eval stores there |
|---|---|
| `gb_experiments` | the experiment (`type = 'training'`; the name is unique within a type): `name`, `description`, `config` |
| `gb_experiment_cases` | the cases the experiment has executed: `eval` (the eval's name) and `case_name` (the case's name, as for a knowledge case) |
| `gb_experiment_runs` | the eval run: ONE run per eval. Its ID is the eval's. `experiment_id` is NULL for an eval started without an experiment name; `params` are the eval's settings (`batch_size`, `max_tokens`, `temperature`, `limit`); `status` (`running`, `completed`, `error`, `cancelled`) and `started_at` / `finished_at` are the eval's; totals are set then (`total_cases` the eval's cases; `judged_cases` the cases with status `judged`, a score of 0 included: it is not a verdict; `error_cases` those with status `error`; `passed_cases` and `failed_cases` are knowledge verdicts and stay 0; what is left of `total_cases` was not reached, e.g. after a cancel; `cost_usd` the judge's) |
| `gb_experiment_case_results` | one row per case, written when its judge batch is done: `eval` the eval's name, `case_name` the case's name (unique within a run), `status` (`judged`: the model answered and the judge scored it, whatever the score; or `error`: the model or the judge errored, no score), `error`, `cost_usd` 0 (the judge's cost is per batch, in the run's metadata), times |
| `gb_training_eval_run_meta` | the training-only part, one row per eval run: `trained_model_id`, `eval` (the run's eval: its case results carry it in their `eval` column too, which the universal tables key on), `judge_model`, the counters (`num_examples` ... `score_sum`, `num_batches`), the judge's `judge_calls`, tokens and `judge_cost_usd` (updated after every batch), and the eval's `error` |
| `gb_training_case_meta` | the training-only part of a case result: `idx` (the case's position in the eval), `batch`, `instruction`, `input`, `reference` (the expected answer), `response` (the model's), `score`, `reason`, tokens and `duration_ms` |

Go: `relational.RecordTrainedModelEvalRun`, `SaveTrainedModelEvalRunBatch`, `FinishTrainedModelEvalRun`, `GetTrainedModelEvalRun`, `ListTrainedModelEvalRuns`, `ListTrainedModelEvalCases`; `relational.EnsureTrainingExperiment`, `GetTrainingExperiment`, `ListTrainingExperiments`, `StartTrainingExperimentRun` (puts the eval's run in the experiment), `GetTrainingExperimentRun`, `ListTrainingExperimentRuns`.
