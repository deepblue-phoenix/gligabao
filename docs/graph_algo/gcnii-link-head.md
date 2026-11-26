# The link-prediction head of the GCNII* model

This page explains, in detail, the second way gligabao predicts links with a GCNII* model: a small **link head** trained to tell the edges of a graph from the non-edges, next to the classification training. For the routes and a quick start see [gcnii-link.md](gcnii-link.md); for the model, the per-user storage and the classification training see [gcnii.md](gcnii.md). The code is in `rust/sargo/src/gcnii/` (`link.rs`, `link_train.rs`, `link_eval.rs`, `model.rs`, `weights.rs`, `users.rs`) and in Go in `internal/sargo/gcnii.go`, `internal/api/model` and `internal/training/gcnii`.

## 1. The problem

Given a graph (a cluster) and a **new node** with a type and, maybe, some links it is already known to have, give each existing node a score for "the new node is connected to it". This is link prediction at the node level.

The model of a user is a GCNII* graph classifier. Its layers turn the graph into one embedding per node (the output of the last layer, `hidden` floats per node); the classifier averages them into one graph embedding and applies a classification head. Link prediction reuses the node embeddings. Two methods score a pair of nodes `(u, v)`:

| Method | Score | Where the knowledge comes from |
| --- | --- | --- |
| `cosine` | `cos(h_u, h_v)`, in `[0, 1]` (the embeddings are ReLU outputs) | nowhere extra: whatever the classification training made the embeddings mean |
| `head` | `sigmoid(w · (ĥ_u ⊙ ĥ_v) + b)`, a probability | a training whose only goal is to predict edges |

`cosine` needs no training. It is a heuristic: nodes that the classifier embeds alike score high. The head is learned: it is trained on held-out edges, so its score is something the model was asked to be right about.

## 2. The head

`ĥ` is `h` scaled to unit length (a zero embedding stays zero). The head has `hidden + 1` parameters: a weight `w_f` per embedding feature and a bias `b` (`GcniiStar::link`, a `Vec<f32>` of `hidden` weights followed by the bias).

```
logit(u, v) = b + Σ_f  w_f · ĥ_u[f] · ĥ_v[f]
score(u, v) = sigmoid(logit(u, v))
```

- **Symmetric:** `score(u, v) = score(v, u)`, the links are undirected.
- **Unit-length embeddings:** the features of the embeddings of a deep GCNII* are small and vary in scale between graphs; normalizing them makes the head's input comparable and the training stable. With `w_f = 1` and `b = 0` the logit is exactly the cosine.
- **Initial state:** a new head starts as a *scaled cosine scorer*: every `w_f` is `6` plus a little noise (std 0.1) and `b = -3` (`LINK_INIT_SCALE` in `model.rs`). So at step 0 the head ranks the nodes like the `cosine` method (and spreads the probabilities over the sigmoid's useful range); training has to improve on that, and a head that does not learn is as good as the cosine, not worse.
- **A diagonal bilinear form** was chosen on purpose: few parameters, symmetric by construction, cheap exact gradients. It can re-weight the embedding features (which ones matter for "these two are linked") but cannot model interactions between different features.

## 3. How the head is trained

One training example is **one graph**, rebuilt every epoch (`make_example` in `link_train.rs`):

1. **Split the edges.** A `holdout` fraction (default 0.2, rounded, at least 1 and at least 1 left) of the edges, picked at random, are taken out. They are the **positive pairs**.
2. **Hide them.** The adjacency of the graph is rebuilt **without** those edges: the encoder has to predict links it cannot see.
3. **Negative pairs.** As many random node pairs that are **not edges of the full graph** (neither kept nor held-out) as there are positives, distinct, `a < b`. For a nearly complete graph there may be fewer; a graph with no non-edge, with fewer than 2 edges or fewer than 3 nodes is skipped for that epoch.
4. **Forward.** The encoder runs on the hidden-edge graph; the head scores every pair from the node embeddings.
5. **Loss.** The mean binary cross-entropy (with logits) of the pairs; the "pair acc" of the log is the share of pairs on the right side of 0.5.
6. **Backward.** The gradient of the head is exact. The gradient on the embeddings goes back through the unit-length normalization (`dh = (dĥ − ĥ (ĥ·dĥ)) / |h|`) and, if the encoder is trained, through the GCNII* layers (`backward_encoder` in `model.rs`, the same code as the classification backward after its pooling step). The finite-difference test of `link_train.rs` checks the head and every encoder tensor.
7. **Step.** Plain SGD with learning rate `lr`, one step per `batch` graphs (the gradients of the batch are summed, each already divided by the batch size).

The randomness is per graph and epoch (`seed`, the epoch and the graph's index), so an example does not depend on the order the graphs are visited in; the same `seed` repeats the training.

### What is trained: `encoder`

| `encoder` | Trained | The classification part |
| --- | --- | --- |
| `frozen` | only the head (`hidden + 1` numbers) | untouched, bit for bit; needs a model to start from (`init`) |
| `train` | the head and the whole encoder, by the link loss | with `class_weight` = 0 left as it was; with `class_weight` > 0 the classification loss of each graph (cross-entropy of its label, on the full graph) times `class_weight` is added to the encoder's gradient, so **both heads train at once** |

- `frozen` is the cheap, safe way: it adds link prediction to a model people already use, without changing a single class probability. The head can only use what the classification encoder already knows.
- `train` lets the encoder organize the embeddings for links. Alone (`class_weight` 0) the classification head becomes stale and meaningless; the model is a link model. With `class_weight` > 0 it is a multi-task model: how well each task does depends on the weight and has to be measured (see [section 4](#4-testing)).
- Not allowed (an error, `400` over HTTP): `frozen` without `init`; `class_weight` > 0 with `frozen`; `class_weight` > 0 with a graph whose label is outside the classes of the starting model.

### Where it starts: `init`

- `init` **true**: start from the user's current model (its encoder, its classification head and its classes; a head it already has is trained on). The default when the encoder is `frozen`. A user without a model gets `404`.
- `init` **false**: a new model: `hidden`/`depth` (default 16/16) and the node-type vocabulary from the training graphs, a new classification head with one output per class found, a new link head. Needs `encoder` `train`.

### Data

The same graph documents as the classification: `{n, edges, nodes?: [{name?, type?}]}`. A `class` is **not needed** for a link training unless `class_weight` > 0 (sargo fills in a placeholder class `_` otherwise; the model's classes are then those of the starting model, or `["_"]`). Through the jobs API a **dataset** is built as for the classification (graphs and LLM classes, at least 2 classes), so a dataset training still needs the classes; graphs sent in the body do not.

Only graphs with at least 2 edges (and 3 nodes) give link examples; small graphs are skipped; a dataset with none is an error.

### Settings

`epochs` (30), `lr` (0.01), `batch` (4 graphs), `hidden` (16) and `depth` (16) (only for a new model), `seed` (random), `holdout` (0.2, in `(0, 1)`), `encoder`, `class_weight` (0), `init`. Stopping a job (`POST /v1/gb/training/jobs/{id}/stop`) works as for the classification: it ends at the next batch, the user's previous model is kept. The mean link loss of each epoch is reported like the classification loss (`epoch_losses`, `epoch_loss`).

## 4. Testing

How good is a way to predict links? The test (`link_eval.rs`) imitates how it is used: a node arrives, some links are known, find the others.

1. **Hold out nodes.** From each graph, up to `nodes` (default 5) nodes with at least 2 neighbours (graphs of at least 4 nodes) are picked at random (`seed`, default 1).
2. **Split its neighbours.** The node is removed with its edges (the other nodes are renumbered). A `reveal` fraction of its neighbours (default 0.5, rounded, at least 1 and at least 1 left) are given as the `neighbors` of the new node; the rest are the **targets**.
3. **Score.** The node is added back as a new node (its type, the known neighbours) and the method scores it against every node of the graph.
4. **Rank.** The candidates are all nodes **except the known neighbours**; the targets are the positives, the other candidates the negatives.

Metrics, averaged over the cases (a case without a positive or a negative candidate is skipped):

| Metric | Meaning |
| --- | --- |
| `auc` | the probability that a random target scores above a random non-neighbour (ties count 0.5). 0.5 is random, 1 is perfect |
| `mrr` | the mean of `1 / rank` of the best-ranked target; ties are ranked **against** the target (pessimistic) |
| `hits_at_10` | the fraction of cases with a target among the 10 best candidates |

Both methods are scored on **exactly the same cases**, so the numbers are comparable (`cases` is the same). The result is `{"cases", "cosine": {...}, "head": {...} | null}`; `head` is `null` for a model without a head. A graph set where no node qualifies is an error.

Remember:

- The test is **in-sample** for the graphs a model was trained on (the training saw the edges of those graphs, in a different split). A link job reports `link_eval` on its training graphs and, if it had validation graphs, `val_link_eval`: **use validation graphs** (graphs the training never saw) to compare fairly.
- With few cases the numbers are noisy: raise `nodes` or give more graphs, and compare on the same `seed`.
- Training is on held-out **edges**, the test is on held-out **nodes**. The difference is on purpose: it measures what the pipeline is used for.

## 5. The weights file

A model with a link head is a `GCNL` file: the same layout as the deepblue-compatible `GCN*` file (header, type vocabulary, all the parameters of the model) with the `hidden + 1` floats of the head appended, and `GCNL` as the magic. A model without a head is still the exact `GCN*` file. `weights::load` reads both; the head is `None` for a `GCN*` file. So:

- the classification training writes `GCN*` (and so **drops** a link head: it makes a new model);
- a link training writes `GCNL` to the same place (`<models_dir>/<hex user>/model.weights`), and the weights store and the cluster sync (see [gcnii.md](gcnii.md#weights-and-the-trained-models-table)) copy it as any other file; a `GCNL` file is not loadable by the deepblue engine.

## 6. How to use it

### On the command line (no server)

```
# 1. a classifier (as before)
sargo gcnii train DS --save cls.w --epochs 15 --hidden 16 --depth 4
# 2. the cosine method, measured on the classifier alone
sargo gcnii eval-link DS --load cls.w
# 3. a frozen head on top of it (the model keeps classifying)
sargo gcnii train-link DS --save frozen.w --init cls.w --epochs 40 --lr 0.05
# 4. both heads at once, from scratch
sargo gcnii train-link DS --save joint.w --encoder train --class-weight 1 --epochs 40 --lr 0.02 --hidden 16 --depth 4
# 5. compare: every eval prints both methods on the same cases
sargo gcnii eval-link DS --load frozen.w
sargo gcnii eval-link DS --load joint.w
sargo gcnii eval DS --load joint.w          # and what the classification kept
# 6. predict
sargo gcnii link cluster.json --load frozen.w --type person --neighbors 1,4 --top-k 5 --method head
```

### Over HTTP (gligabao)

1. Train the classification as usual (`POST /v1/gb/training/jobs`, `"model_type": "gcnii"`).
2. Train the head: the same endpoint with `"task": "link"`, e.g. `{"model_type": "gcnii", "task": "link", "graphs": [...], "validation": [...], "epochs": 40, "lr": 0.05}` (frozen, from the user's model) or with `"encoder": "train", "class_weight": 1` (both, `init` is false so a new model; set `"init": true` to fine-tune the current one). The job record has `task`, and when it is done `link_loss`, `link_eval` and `val_link_eval`; the same numbers are in `GET /v1/gb/training/jobs/{id}/meta` and the `params` of the model row. A running link job shows up in `GET /v1/gb/training/jobs?model_type=gcnii` as any job.
3. Test: `POST /v1/gb/training/gcnii/link/eval` with graphs the model has not seen.
4. Use: `POST /v1/gb/training/gcnii/link` (`method` `head`; the default once the model has a head, `cosine` to compare).

## 7. Choosing, and reading the results

- **Start with `cosine`** (free) and measure it. Train a frozen head and measure again: if `head` is not clearly better on validation graphs, keep the cosine.
- **`frozen`** when the classification must not change; **`train` with `class_weight`** when both tasks matter and you can afford to check that the classification did not get worse (`eval` on the classification, and the class metrics of the job); **`train` with 0** for a model only meant for links.
- The head pays off when the structure says more about links than "looks alike": when types or the embeddings already separate the communities (a toy graph made of typed clusters) both methods are about equal. In a first run on synthetic community graphs, AUC was 0.872 for the cosine, 0.873 for the frozen head and 0.881 for both heads trained from scratch; expect differences to show on harder, real data.
- A new node **without known neighbours** is scored from its type alone, whichever method: give it the links you know.
- The score of the head is a probability in the sense of the training distribution (balanced positives and negatives); it is not calibrated to how rare real links are, so use it to **rank**, and pick a threshold on data you have.
- The model is deep and smooths embeddings; very different graph sizes between training and use change the picture. Test on data like the use.

## 8. Where things are

| What | Where |
| --- | --- |
| the head, the unit embeddings, the pair logit | `model.rs` (`link`, `with_new_link_head`, `unit_columns`, `pair_logit`, `backward_encoder`) |
| examples, loss, gradients, the training | `link_train.rs` (`make_example`, `link_grads`, `train_link`) |
| scoring a new node, the two methods | `link.rs` (`Method`, `grow`, `score_all`, `predict`) |
| the test | `link_eval.rs` (`cases`, `case_metrics`, `evaluate`, `compare`) |
| the file format | `weights.rs` (`GCN*` / `GCNL`) |
| per-user jobs, the settings of a body | `users.rs` (`start_link`, `link_options_from_value`, `run_link_training`) |
| HTTP of sargo, CLI | `router.rs` (`/v1/gcnii/link`, `/link/eval`, `/train` with `task`), `cli.rs` (`link`, `train-link`, `eval-link`) |
| Go: client, handlers, jobs | `internal/sargo/gcnii.go`, `internal/api/model`, `internal/training/gcnii`, `internal/modeltrain` |
