# Link prediction with the GCNII* model (sargo)

Link prediction answers: *a new node arrives in a cluster (a graph); how likely is it to be connected to each of the nodes already there?* It is a pipeline of the per-user GCNII* model next to `infer` (class of a graph) and `embed` (embedding of a graph); see [gcnii.md](gcnii.md) for the model, the training and the per-user storage. For the link head and the tests in full detail see [gcnii-link-head.md](gcnii-link-head.md). The work is done by [sargo](../rust/sargo/README.md) (`rust/sargo/src/gcnii/link.rs`).

There are **two ways** to predict links, and both can be trained and tested side by side:

| Method | What scores a pair | Needs |
| --- | --- | --- |
| `cosine` | the cosine of the two node embeddings of the classification model | only a trained classification model (no extra training) |
| `head` | a **link-prediction head** trained to tell the edges of a graph from non-edges | a link training (see [Training the link head](#training-the-link-head)) |

A model can hold both: a user's model keeps classifying and gets a link head. A user without a trained model gets `404`.

## Route

`POST /v1/gb/training/gcnii/link`, acting as the user of the request (the `gb-user-id` header or the API key; never a body field). Needs training mode and sargo, as the other GCNII* routes.

Body:

```json
{
  "graph": {"n": 4, "edges": [[0,1],[1,2],[2,3]], "nodes": [{"name": "a", "type": "person"}, ...]},
  "node":  {"name": "new", "type": "person", "neighbors": [1]},
  "top_k": 3
}
```

| Field | Meaning |
| --- | --- |
| `graph` | The cluster: the same graph document as `infer` and `embed` (`n`, undirected `edges`, optional `nodes` with `name` and `type`). |
| `node.type` | The type of the new node (as in the training graphs; an unknown or empty type shares the "unknown type" feature). Optional. |
| `node.name` | Metadata only, not a feature. Optional. |
| `node.neighbors` | Indices of the nodes the new node is already known to be connected to. Optional (see [Limits](#limits)). |
| `top_k` | Keep only the best `top_k` nodes (a whole number >= 1). Default: all. |
| `method` | `cosine` or `head`. Default: the head if the user's model has one, else the cosine (`400` for `head` on a model without one). |

Answer:

```json
{"method": "head", "node": 4, "nodes": 4, "scores": [{"node": 2, "name": "c", "score": 0.93, "connected": false}, ...]}
```

`method` is the one that scored; `node` is the index the new node gets in the augmented graph (`= nodes`). `scores` has one item per node of the graph (or `top_k`), best first (ties by node index); `name` is only there when the node has one; `connected` is true for the nodes given in `neighbors` (those links are known, not predicted).

Errors: `400` (no user, a bad graph or node, a neighbor outside `0..n-1`, `top_k` < 1), `404` the user has no trained model, `503` sargo has no per-user models, `502` sargo did not answer.

## How the score is made

For both methods:

1. The new node is appended to the graph as node `n`, with its `type` and an edge to each of `neighbors`.
2. The trained model runs its layers on this augmented graph and gives one embedding per node: the output of the last GCNII* layer (`hidden` floats), the vector that is averaged into the graph embedding of `embed`, before the classification head.
3. `cosine`: the score of a pair (new node, node `i`) is the **cosine similarity** of their two node embeddings. Layer outputs are ReLU outputs, so the score is in `[0, 1]` (0 also when one vector is all zeros).
   `head`: the score is `sigmoid(w · (ĥ_u ⊙ ĥ_v) + b)` on the unit-length embeddings, with `w` and `b` the `hidden + 1` weights of the link head: a probability of the link.

Because the new node is part of the graph the model sees, its neighbors (and through them the rest of the cluster) change the embeddings of the existing nodes too.

## Training the link head

The head is trained on the same kind of data as the classification (graphs `{n, edges, nodes?}`), next to it. In every epoch, for every graph, a `holdout` fraction of its edges (default 0.2, at least one) is taken out and becomes the positive pairs, the encoder sees the graph **without** them, and as many random non-edges are the negative pairs; the loss is the binary cross-entropy of the pairs. A new head starts as a scaled cosine scorer, so it starts where the `cosine` method is and has to improve on it.

`encoder` says what else is trained:

| `encoder` | Trains | Result |
| --- | --- | --- |
| `frozen` (default when starting from the user's model) | only the head, on top of the encoder of a classification model (`init`) | the model classifies exactly as before, and has a head |
| `train` | the encoder too, by the link loss | with `class_weight` > 0 the classification loss (labels from the graphs) is added at that weight, so both train at once; with 0 the classification head is left as it was (a model made from scratch this way is only good for links) |

A job on `POST /v1/gb/training/jobs` with `"model_type": "gcnii"` takes `"task": "link"` and, besides the usual settings, `holdout`, `encoder`, `class_weight` and `init` (start from the user's current model; default true for a frozen encoder, false otherwise). Graphs sent in the body need no `class` unless `class_weight` > 0 (a dataset still needs its classes, as for the classification). The job reports `link_loss`, and when it is done `link_eval` (and `val_link_eval` if it had validation graphs): the [test below](#testing-both-methods) on the training and validation graphs, for both methods; they are also in the run meta (`GET /v1/gb/training/jobs/{id}/meta`) and the `params` of the model row. A later **classification** training replaces the model and so drops its link head.

## Testing both methods

`POST /v1/gb/training/gcnii/link/eval` with `{"graphs": [{n, edges, nodes?}, ...], "nodes"?, "reveal"?, "seed"?}` tests both methods with the user's model on the same cases. From each graph, up to `nodes` (default 5) nodes with at least 2 neighbours are taken out; a `reveal` fraction of their neighbours (default 0.5, at least one) are given as the `neighbors` of the new node and the others are the targets, which the model has to rank first among the nodes that are not known neighbours (the way the route above is used). The same `seed` (default 1) gives the same cases. Answer:

```json
{"cases": 200, "cosine": {"cases": 200, "auc": 0.87, "mrr": 0.61, "hits_at_10": 0.98}, "head": {"cases": 200, "auc": 0.88, "mrr": 0.62, "hits_at_10": 0.99}}
```

- `auc`: the chance that a random target outranks a random non-neighbour (0.5 = random);
- `mrr`: the mean reciprocal rank of the best-ranked target (ties count against it);
- `hits_at_10`: the fraction of cases with a target in the first 10.

`head` is `null` for a model without a link head. Test on graphs the model was not trained on, or the numbers are in-sample.

## Limits

- The `cosine` method uses a model trained to **classify graphs**, not to predict links. A high score means "this node looks like that one, in the context of this graph" (same kind of type and neighborhood); it is a ranking, not a calibrated probability of an edge.
- A new node **without `neighbors`** has only its `type` (and degree 0) as features, so it mostly ranks the nodes of the same type first. Give it the links it is already known to have for a better signal.
- The model has many layers, which smooths the embeddings: scores of a cluster can be close together; use the order, not the absolute values.
- The `head` is trained on held-out edges, not on held-out nodes: a new node differs a bit from that (it has only the links you know), so test with the node-level test above. On graphs where the node types already tell the communities (the usual case in a toy), both methods can be about equal; the head pays off when the structure says more than the cosine can.

## Other entry points

- **sargo:** `POST /v1/gcnii/link` and `POST /v1/gcnii/link/eval` take the same bodies plus an optional `user_id` (without it the global `[gcnii] weights` model is used). `POST /v1/gcnii/train` takes `"task": "link"` with `holdout`, `encoder`, `class_weight`, `init`; without a `class` on the graphs unless `class_weight` > 0.
- **Command line** (`sargo gcnii ...`, JSON on stdout):
  - `link GRAPH.json|- --load w.bin [--type T] [--name N] [--neighbors 0,3] [--top-k K] [--method cosine|head]`
  - `train-link DIR --save out.w [--init cls.w] [--encoder frozen|train] [--class-weight X] [--holdout X] [--epochs N] [--lr X] [--batch N] [--seed N] [--hidden N] [--depth N]`
  - `eval-link DIR --load w.bin [--nodes N] [--reveal X] [--seed N]`
  
  To compare the approaches: `train` a classifier, `eval-link` it (the `cosine` numbers), `train-link --init` it (frozen head) and `train-link --encoder train [--class-weight 1]` (from scratch), then `eval-link` each.
- **Weights file:** a model with a link head is saved as `GCNL` (the `GCN*` layout plus `hidden + 1` floats); without one it stays the `GCN*` file of deepblue.
- **Go client:** `sargo.Client.GCNIILink`, `GCNIILinkEval` and the `Task`/`Holdout`/`Encoder`/`ClassWeight`/`Init` fields of `GCNIITrainRequest` (`internal/sargo/gcnii.go`) check the input before sending. The handlers are `model.Link` and `model.LinkEval` (`internal/api/model`); the training goes through `internal/training/gcnii` like the classification.
