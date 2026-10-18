# Trained-model evals (LLM as a judge)

Checks how well a freshly trained model learned its training dataset: the model is asked the dataset's questions (`instruction`, plus `input` as context, as in training) and an external LLM, the one gligabao uses for knowledge ingestion (Anthropic, `ANTHROPIC_API_KEY`), judges how closely each answer agrees with the dataset's `output`.

It is separate from the knowledge-extraction evals in [`../knowledge`](../knowledge/README.md).

## Running one

In the admin UI, training view, press **Eval** on a trained model (the table's last column, or the Evals section of the model's dialog, which also lets you ask only the first n questions and set the batch size). The eval runs in the background on the server; the dialog shows its progress, the score so far, and (expanding "Answers") every question with the dataset's answer, the model's, the score and the judge's reason, lowest score first. Each eval has a **Watch live** button (and one opens when you start an eval): a dialog that follows the eval's SSE feed, showing each question with the dataset's answer and the model's as soon as the model answers it, then its score and the judge's reason when its batch is judged, with a one-square-per-question strip, running totals, and an "only problems" filter. An eval can be cancelled, and the batches done so far are kept. Over HTTP: [docs/training/EVALS.md](../../docs/training/EVALS.md#api).

For now the questions are those of the model's own training dataset, so the score says how closely the model reproduces its training answers (memorization / fit), not how well it generalizes.

## How it works

1. The examples are read from the model's dataset (`ParseExamples`), the first `limit` of them if a limit is set.
2. They go in **batches** (`Config.BatchSize`, default 10, at most 25). For each batch the model answers the questions (`GenerateFunc`: one sidecar inference per question, strictly one at a time, in order), then **one** call to the judge LLM grades the whole batch, so n questions cost about n / batch size judge calls, not n.
3. The judge gets the batch as JSON (question, reference, answer per item) and a rubric, and replies with a JSON array of `{id, score, reason}`. Score is 0 to 5: 5 same as the dataset, 4 correct with minor differences, 3 partly correct, 2 mostly wrong, 1 wrong, 0 empty/nonsense/refusal. 4 and 5 count as **correct**, 2 and 3 as **partial**, 0 and 1 as **incorrect**. Wording, order and length do not matter, facts, numbers, names and actions do. The items are given as data, with a rule to ignore any instructions in them. Texts over 4,000 bytes are cut before being sent.
4. Items the judge skipped or answered with an invalid score are sent once more; still without a verdict they are marked failed. A question the model failed to answer is marked failed and not sent to the judge. The run stops with an error after 2 batches in a row where the model answered nothing, or 3 where the judge graded nothing (a dead sidecar, a bad API key), instead of burning GPU time.
5. After each batch the items and the running totals (answered, judged, failed, correct/partial/incorrect, score sum, the judge's tokens and cost) are handed to a `Sink`, which the server stores, so progress is visible while the eval runs.

The package knows nothing about HTTP, the sidecar or the database: `Run(ctx, cfg, examples, generate, judge, sink)` takes the model as a function, the judge as an `LLM` (`Complete(ctx, ai.Request)`) and the results go to the sink. `internal/api/training/evals.go` wires them to the sidecar, `memory_core.WithRecording` (so the judge's calls are in `gb_inferences` as type `eval_judge`) and the database. Tests (`go test ./evals/training`) use a fake model and judge, no network.
