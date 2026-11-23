# Per-user models: GCNII* graph classifier (sargo)

gligabao can train and run a GCNII* graph classifier for each user. The work is done by [sargo](../rust/sargo/README.md), the Rust server embedded in the gligabao binary. gligabao starts it **once at startup** (`sargo = true` or `serve --sargo`, binary built with `./run_build.sh --sargo`) and from then on only talks to it over HTTP. Without sargo the routes below are not mounted.

The routes are generic: the type of model is the `model_type` argument (for now only `"gcnii"`, the GCNII* graph classifier). Every model belongs to one user: the user of a request is the one of the `gb-user-id` header (or of the API key), never a field of the body, and it is passed on to sargo as `user_id`. Training one user never changes another's model.

## Routes

All of them act as the user of the request.

| Route | Body | Answer |
| --- | --- | --- |
| `POST /v1/gb/model/train` | `{"model_type": "gcnii", "graphs": [{"n", "edges", "nodes"?, "class"}, ...], "epochs"?, "lr"?, "batch"?, "hidden"?, "depth"?, "seed"?}` | `202 {"user_id", "job"}`: the training runs in the background |
| `GET /v1/gb/model/train?model_type=gcnii` | | `{"user_id", "trained", "job"}`; `job.status` is `running`, `done` (`accuracy`, `loss` on the training graphs) or `failed` (`error`) |
| `POST /v1/gb/model/infer` | `{"model_type": "gcnii", "graph": {"n", "edges", "nodes"?}, "classes"?}` | `{"class", "index", "logits", "probabilities", "nodes", "edges"}` |
| `POST /v1/gb/model/embed` | `{"model_type": "gcnii", "graph": {...}, "normalize"?}` | `{"embedding", "dim"}` |

| `POST /v1/gb/model/train/dataset` | `{"model_type": "gcnii", "dataset"?, "classes"?, "min_graphs_per_class"?, "validation_fraction"?, "split_seed"?, "epochs"?, "lr"?, "batch"?, "hidden"?, "depth"?, "seed"?}` | `202 {"user_id", "job", "dataset", "train", "validation", "classes"}` |

`/train` also takes `"validation"`: graphs (each with a `class`) the model is not trained on; when the job is `done` it has `val_accuracy` and `val_loss` on them.

A graph is `n` nodes numbered `0..n-1`, undirected `edges` `[a, b]` and optional `nodes` (`{"name", "type"}` each; the types are what the model sees). Defaults: 30 epochs, `lr` 0.01, `batch` 4, `hidden` 16, `depth` 16, random `seed`.

Errors: `400` bad request (no user, missing or unknown `model_type`, no graphs, a graph without `class`, an edge outside the graph, ...), `404` the user has no trained model (infer, embed), `409` the user is already training, `503` sargo has no per-user models, `502` sargo did not answer. A new training replaces the user's model when it finishes.

## Training on a dataset

`POST /v1/gb/model/train/dataset` trains on the graphs of one of the user's datasets, labelled with the classes an LLM gave them when they were remembered (`classify_graph`, see `remember/remember.md#graph-classes`):

1. **Fetch (SQL):** the artifacts, entities, facts and `gb_artifact_classes` of the dataset are read (`memory.LoadKnowledgeDataset`).
2. **One class per graph:** the classifying training hook (`docs/hooks.md`) if the program set one, else the default: the class given most often to the graph (earliest on a tie). `classes` restricts the choice to those names; classes with fewer than `min_graphs_per_class` graphs are dropped; graphs without a class are left out.
3. **Partition:** per class, `validation_fraction` (in `[0, 1)`, default 0 = no validation) of the graphs go to the validation set, in an order fixed by `split_seed`; each class keeps at least one training graph.
4. **Convert and train:** entities become nodes (`name`, `type`), facts undirected edges (self loops, repeats and facts to entities outside the graph are dropped), the class the label; both sets go to sargo (`/train` with `validation`). Follow the job with `GET /v1/gb/model/train`.

Errors as `/train`, plus `404` no such dataset, `422` no graph left to train on (remember the dataset with `classify_graph` on).

## Where things are

- `internal/modeltrain`: the dataset-to-sets pipeline above (`Build`, `DefaultHook`, `PickClass`, `Split`, `ToGCNIIGraph`); the handler is `internal/api/model/dataset.go`.

- `rust/sargo/src/gcnii/users.rs`: the models of the users and their background trainings; `router.rs`: the `/v1/gcnii/...` endpoints.
- `internal/sargo/gcnii.go`: the HTTP client methods (`GCNIITrain`, `GCNIITrainStatus`, `GCNIIInfer`, `GCNIIEmbed`) and types.
- `internal/api/model`: the handlers of the routes above; mounted by `api.WithGCNII` (`server.Options.GCNII`, set in `gligabao.go` from the sargo client).
- The models are saved in `sargo_gcnii_dir` (config; default a `gcnii_models` directory in the working directory), one subdirectory per user.
