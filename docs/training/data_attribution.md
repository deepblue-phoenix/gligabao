# Training data attribution: tracing an answer back to the examples that steered it

Can an answer of a trained model be attributed to the training examples that made it answer that way, or at least with some probability? **Yes, approximately.** The field is called *training data attribution* (TDA). No method gives an exact, single cause: an answer is usually shaped by many examples, and every practical method is an estimate. This page lists the methods, what each costs, and how they apply to gligabao's training setup.

**Status: a design note. gligabao does not implement any of this yet.** For the data format see [training_data_format.md](training_data_format.md), for the datasets directory see [datasets.md](datasets.md), for how the model is trained and run see [model_internals.md](model_internals.md).

## 1. Methods

From the most principled to the cheapest.

### 1.1 Gradient-based influence

Estimate how the loss on a test example (the prompt and the answer the model gave) would change if a training example were upweighted or removed.

| Method | Idea | Cost / needs |
|---|---|---|
| **Influence functions** (Koh & Liang 2017) | Test gradient times the inverse Hessian times the training gradient. The Hessian inverse is approximated (for example with EK-FAC, as in Anthropic's *Studying Large Language Model Generalization with Influence Functions*, 2023). | Per-example gradients and a Hessian approximation. Heavy; noisy on deep nets. |
| **TracIn** (Pruthi et al. 2020) | Sum, over saved checkpoints, of the dot product of the test gradient and the training gradient (weighted by the learning rate at that checkpoint). No Hessian. | Per-example gradients and **several checkpoints**. Cheap and works well in practice. |
| **TRAK** (Park et al. 2023) | Random projection of the gradients, then a linear model on them. | Much faster than influence functions; scores correlate well with real retraining. |

### 1.2 Counterfactual retraining (the ground truth)

Remove an example (leave-one-out), or a random subset (datamodels, Data Shapley), retrain, and see whether the answer changes. Exact, and very expensive: it is mainly used to **validate** the cheaper methods, not to run them.

### 1.3 Similarity (cheap heuristics)

Find the training rows nearest to the question and the answer: embedding nearest neighbours, or lexical / n-gram overlap. It looks only at the data, not at the model, so it finds *similar* examples, not *causal* ones. For fact-style data it is often good enough as a first filter.

### 1.4 Memorization and extraction checks

Check whether the answer reproduces a training output verbatim (string match against the data file), or whether a training row has unusually low loss (membership-inference signals). It finds regurgitation; it explains nothing about generalization.

### 1.5 Attribution by design

Retrieval-augmented generation (RAG) with citations, or data-partitioned models (SISA training), make the sources explicit: the model cannot answer without naming them. There is nothing to estimate afterwards. gligabao's memory already works this way: every fact is anchored to its artifact (see [../artifacts.md](../artifacts.md)); a fine-tuned model has no such link.

## 2. Limits

- **Spread-out influence.** An answer comes from many rows; the result is a ranked distribution, not one cause.
- **Noisy estimates.** Influence functions on deep nets often poorly approximate what retraining would do.
- **The base model.** Fine-tuning starts from a pretrained model (`google/gemma-4-*-it`) whose own training data is not available. Only the influence of the **fine-tuning rows** can be measured; a correct answer may come from the base model and show no fine-tuning influence at all.
- **Training details matter.** Row order, the learning-rate schedule, the seed and quantization all change the result.

## 3. How this applies to gligabao

What the training does today (`sidecar/ai_train/ai_train_core/`):

- The base model is frozen and in 4 bits; only a **LoRA adapter** (rank 16 on `q_proj`, `k_proj`, `v_proj`, `o_proj`) is trained and saved (`save_adapters`). The adapter is small, which makes per-example gradients affordable: they are taken on the adapter parameters only, not on the whole model.
- Training runs for `max_steps` steps (`SFTConfig`, seed 42, learning rate 2e-4, `warmup_steps=10`, linear schedule). **Only the final adapter is saved**: `output_dir` is `/tmp/outputs` and no intermediate checkpoint is kept. That matters for TracIn (see 3.2).
- The datasets are small. `bin/datasets_training/world-knowledge-base/` has 500 rows of `{"instruction", "input", "output"}`, one fact per row (for example `"In what year was Shell founded?"` and `"Shell was founded in 1907."`), generated from `world_knowledge_base_triples.json`.

### 3.1 Why this is a favourable case

- **Small data and a small trainable part** make gradient methods and even retraining feasible.
- **One fact per row.** The answer to a question about a fact should depend on a few rows, so the ranking can be sharp.
- **Ground truth exists.** `world_knowledge_base_triples.json` holds the source triples. An attribution can be **checked**: the top-ranked rows should contain the fact the model stated. This gives a measurable test (recall@k of the rows containing the triple) without any retraining.

### 3.2 Suggested pipeline

1. **Candidates by similarity (cheap).** Embed the rows (`instruction` and `output`) and find the top-k (for example 50) nearest to the prompt and to the generated answer. Add an exact / n-gram match of the answer against the `output` fields. This probably finds most of the real sources.
2. **Re-rank with gradients.** For the candidates, compute the LoRA-parameter gradient of the loss of each row, and of the loss of (prompt, answer), and score by the dot product (TracIn / TRAK style). Return the top rows with their scores.
3. **Validate, occasionally.** For a few answers, fine-tune again without the top row(s) and check that the answer changes (leave-out). This is a few small jobs on a 500 row dataset.

For TracIn, training would have to **save a few intermediate adapters** (for example every N steps through `SFTConfig` `save_steps`, kept on the models volume). With only the final adapter, use the single-checkpoint form (a TRAK-like gradient dot product at the end of training); it is less faithful to the training trajectory but needs no change to the training job.

### 3.3 What to expose

If implemented, the result of an inference could carry an optional `attribution` field: a list of `{row_index, dataset, score}` ranked by score, with the method named (`similarity`, `gradient`). Scores are **relative**, not probabilities: they rank rows, and say nothing calibrated about how likely a row is to be the cause. Converting them to a probability needs calibration against leave-out runs (3.2, step 3). Inferences are described in [../inferences.md](../inferences.md).

## 4. Summary

| Question | Answer |
|---|---|
| Can an answer be attributed to training examples? | Approximately, yes; never exactly. |
| With some probability? | As a ranking with scores; real probabilities only after calibration against retraining. |
| Cheapest useful start | Embedding / lexical similarity over the dataset rows, checked against the source triples. |
| Better, still affordable here | Gradient dot products on the LoRA parameters (TracIn / TRAK), at the cost of saving checkpoints or per-example gradients. |
| What cannot be attributed | What the base model already knew. |

## References

- Koh & Liang, *Understanding Black-box Predictions via Influence Functions* (2017).
- Pruthi et al., *Estimating Training Data Influence by Tracing Gradient Descent* (TracIn, 2020).
- Park et al., *TRAK: Attributing Model Behavior at Scale* (2023).
- Grosse et al., *Studying Large Language Model Generalization with Influence Functions* (2023).
- Ilyas et al., *Datamodels: Predicting Predictions from Training Data* (2022).
