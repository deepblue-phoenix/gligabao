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

| `POST /v1/gb/model/train/dataset` | `{"model_type": "gcnii", "dataset"?, "classes"?, "class_pick"?, "top_classes"?, "other_class"?, "min_edges"?, "min_graphs_per_class"?, "validation_fraction"?, "split_seed"?, "epochs"?, "lr"?, "batch"?, "hidden"?, "depth"?, "seed"?}` | `202 {"user_id", "job", "dataset", "train", "validation", "classes", "run"}` |

`/train` also takes `"validation"`: graphs (each with a `class`) the model is not trained on; when the job is `done` it has `val_accuracy` and `val_loss` on them.

A graph is `n` nodes numbered `0..n-1`, undirected `edges` `[a, b]` and optional `nodes` (`{"name", "type"}` each; the types are what the model sees). Defaults: 30 epochs, `lr` 0.01, `batch` 4, `hidden` 16, `depth` 16, random `seed`.

Errors: `400` bad request (no user, missing or unknown `model_type`, no graphs, a graph without `class`, an edge outside the graph, ...), `404` the user has no trained model (infer, embed), `409` the user is already training, `503` sargo has no per-user models, `502` sargo did not answer. A new training replaces the user's model when it finishes.

## Training on a dataset

`POST /v1/gb/model/train/dataset` trains on the graphs of one of the user's datasets, labelled with the classes an LLM gave them when they were remembered (`classify_graph`, see `remember/remember.md#graph-classes`):

1. **Fetch (SQL):** the artifacts, entities, facts and `gb_artifact_classes` of the dataset are read (`memory.LoadKnowledgeDataset`).
2. **Eligible graphs:** a graph needs at least `min_edges` edges (default 1; the model learns from structure) and an LLM class.
3. **One class per graph:** the classifying training hook (`docs/hooks.md`) if the program set one, else the default, by `class_pick`: `majority` (the class given most often to the graph, earliest on a tie) or `latest` (the first class of the most recent model run). `classes` restricts the choice to those names.
4. **Class set:** `top_classes` N keeps the N most given classes; the graphs of the others are trained as `other` if `other_class` is true, else left out. Then the classes with fewer than `min_graphs_per_class` graphs are dropped. At least 2 classes must be left (`422`).
5. **Partition:** per class, `validation_fraction` (in `[0, 1)`, default 0 = no validation) of the graphs go to the validation set, in an order fixed by `split_seed`; each class keeps at least one training graph.
6. **Convert and train:** entities become nodes (`name`, `type`), facts undirected edges (a fact to an entity outside the graph adds it as a node named after it, without a type; self loops and repeats are dropped), the class the label; both sets go to sargo (`/train` with `validation`). Follow the job with `GET /v1/gb/model/train`.

Errors as `/train`, plus `404` no such dataset, `422` no graph left to train on or fewer than 2 classes (remember the dataset with `classify_graph` on).

### History

Every training on a dataset is recorded in `<sargo_gcnii_dir>/_history/<user>/<dataset>/<run id>/` (the run ID is the start time): `run.json` (user, dataset, options, training settings, classes, graphs per class in each set, artifact IDs of each set) and `classes.json`. The answer of the training has the `run` ID; `GET /v1/gb/model/train/dataset/runs?dataset=` returns `{"runs": [...]}`, newest first. The weights are not kept: sargo has one model per user, the last trained.

## Where things are

- `internal/modeltrain`: the dataset-to-sets pipeline above (`Build`, `DefaultHook`, `PickClass`, `Split`, `ToGCNIIGraph`) and its history (`history.go`); the handler is `internal/api/model/dataset.go`.

- `rust/sargo/src/gcnii/users.rs`: the models of the users and their background trainings; `router.rs`: the `/v1/gcnii/...` endpoints.
- `internal/sargo/gcnii.go`: the HTTP client methods (`GCNIITrain`, `GCNIITrainStatus`, `GCNIIInfer`, `GCNIIEmbed`) and types.
- `internal/api/model`: the handlers of the routes above; mounted by `api.WithGCNII` (`server.Options.GCNII`, set in `gligabao.go` from the sargo client).
- The models are saved in `sargo_gcnii_dir` (config; default a `gcnii_models` directory in the working directory), one subdirectory per user.
