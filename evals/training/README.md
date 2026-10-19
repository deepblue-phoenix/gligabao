# Trained-model audits and evals (LLM as a judge)

Checks a trained model's answers with an LLM as the judge. The model is asked each case's question (`instruction`, plus `input` as context, as in training) and an external LLM, the one gligabao uses for knowledge ingestion (Anthropic, `ANTHROPIC_API_KEY`), judges how closely each answer agrees with the reference `output`. Two things use this engine, and they are kept apart in the code, the database and the API:

- A **training data audit** (`relational.TrainedModelAuditRun`, `internal/api/training/audits.go`, tables `gb_training_audit_runs` / `gb_training_audit_cases`, HTTP `/audits`): the cases are the model's **own fine-tuning dataset**, so the score says whether the training took (memorization / fit) and, through the examples it fails, which training data is bad. It does not measure generalization. [docs/training/AUDIT.md](../../docs/training/AUDIT.md).
- An **eval** (`relational.TrainedModelEvalRun`, `internal/api/training/evals.go`, the `training` rows of the universal eval tables, HTTP `/evals`): meant for cases the model never saw. Until evals have cases of their own, the server's eval still has the scheme `finetune_dataset` (`TrainingSchemeFineTuneDataset`), the model's own dataset, which is what the audit does. [docs/training/EVALS.md](../../docs/training/EVALS.md).

Both are separate from the knowledge-extraction evals in [`../knowledge`](../knowledge/README.md). The outcome of one example is a case: a `trainingeval.CaseResult` here, stored as `relational.TrainedModelAuditCase` (an audit) or `relational.TrainedModelCaseResult` (an eval, a row of `gb_experiment_case_results` like a knowledge case's).

## Running one

In the admin UI, training view, press **Audit** (or **Eval**) on a trained model (the table's last columns, or the sections of the model's dialog, which also let you ask only the first n cases and set the batch size). It runs in the background on the server; the dialog shows its progress, the score so far, and (expanding "Answers") every question with the dataset's answer, the model's, the score and the judge's reason, lowest score first. Each run has a **Watch live** button (and one opens when you start it): a dialog that follows its SSE feed, showing each question with the dataset's answer and the model's as soon as the model answers it, then its score and the judge's reason when its batch is judged, with a one-square-per-question strip, running totals, and an "only problems" filter. A run can be cancelled, and the batches done so far are kept. Over HTTP: [audits](../../docs/training/AUDIT.md#api), [evals](../../docs/training/EVALS.md#api).

For an audit the questions are those of the model's own training dataset, so the score says how closely the model reproduces its training answers (memorization / fit), not how well it generalizes.

## Eval cases and running them from the command line

The cases of the evals are in `evals/*.json` (embedded in the package, loaded by `LoadEvals`): a list of `{name, instruction, input?, output}`, `output` being the expected answer. They ask about knowledge that is in a training dataset but are phrased differently from its examples (several facts per question). `world_knowledge_base.json` has 10 cases on the `world-knowledge-base` dataset (`bin/datasets_training/world-knowledge-base/`).

```
./run_evals.sh -t training -list-models          # the ids of the trained models in bin/gligabao.db
./run_evals.sh -t training -model 3 [-v] [-e world_knowledge_base] [case-name ...]
```

It needs `ANTHROPIC_API_KEY` (the judge) and a running sidecar (`SIDECAR_URL` or `-sidecar`; the model is loaded there). The runner (`evals/training/cmd`) asks the model each question, has the judge grade the answers with the same engine as below, and prints a verdict per case and the totals; nothing is stored. A case passes with a score of 4 or 5; the exit status is 1 if any case does not. Other options: `-db`, `-batch-size`, `-judge-model`, `-max-tokens`, `-temperature` (`go run ./evals/training/cmd -h`).

## How it works

1. The examples are read from the model's dataset (`ParseExamples`), the first `limit` of them if a limit is set.
2. They go in **batches** (`Config.BatchSize`, default 10, at most 25). For each batch the model answers the questions (`GenerateFunc`: one sidecar inference per question, strictly one at a time, in order), then **one** call to the judge LLM grades the whole batch, so n questions cost about n / batch size judge calls, not n.
3. The judge gets the batch as JSON (question, reference, answer per case) and a rubric, and replies with a JSON array of `{id, score, reason}`. Score is 0 to 5: 5 same as the dataset, 4 correct with minor differences, 3 partly correct, 2 mostly wrong, 1 wrong, 0 empty/nonsense/refusal. 4 and 5 count as **correct**, 2 and 3 as **partial**, 0 and 1 as **incorrect**. Wording, order and length do not matter, facts, numbers, names and actions do. The cases are given as data, with a rule to ignore any instructions in them. Texts over 4,000 bytes are cut before being sent.
4. Cases the judge skipped or answered with an invalid score are sent once more; still without a verdict they are marked failed. A question the model failed to answer is marked failed and not sent to the judge. The run stops with an error after 2 batches in a row where the model answered nothing, or 3 where the judge graded nothing (a dead sidecar, a bad API key), instead of burning GPU time.
5. After each batch the cases and the running totals (answered, judged, failed, correct/partial/incorrect, score sum, the judge's tokens and cost) are handed to a `Sink`, which the server stores, so progress is visible while the eval runs.

The package knows nothing about HTTP, the sidecar or the database: `Run(ctx, cfg, examples, generate, judge, sink)` takes the model as a function, the judge as an `LLM` (`Complete(ctx, ai.Request)`) and the results go to the sink. `internal/api/training/audits.go` and `evals.go` wire them to the sidecar, `memory_core.WithRecording` (so the judge's calls are in `gb_inferences`, as type `training_audit_judge` for an audit and `eval_judge` for an eval) and the database. Tests (`go test ./evals/training`) use a fake model and judge, no network.
