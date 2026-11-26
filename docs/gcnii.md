# Per-user models: GCNII* graph classifier (sargo)

gligabao can train and run a GCNII* graph classifier for each user. The work is done by [sargo](../rust/sargo/README.md), the Rust server embedded in the gligabao binary. gligabao starts it **once at startup** (`sargo = true` or `serve --sargo`, binary built with `./run_build.sh --sargo`) and from then on only talks to it over HTTP. These routes are part of the training endpoints (`/v1/gb/training/`), so they need training mode (`training = true` or `serve --training`) as well as sargo: with training mode on but without sargo the other training endpoints are mounted and the routes below are not (a part that needs an optional backend is mounted only when the backend is there).

The trained models of every type (`lora` from the ML sidecar, `gcnii`) are listed together by `GET /v1/gb/training/models` (`?model_type=` narrows it); the routes below are the ones of the GCNII* graph classifier. A `model_type` in a body, if sent, must be `"gcnii"`. Every model belongs to one user: the user of a request is the one of the `gb-user-id` header (or of the API key), never a field of the body, and it is passed on to sargo as `user_id`. Training one user never changes another's model.

## Routes

All of them act as the user of the request.

| Route | Body | Answer |
| --- | --- | --- |
| `POST /v1/gb/training/jobs` with `"model_type": "gcnii"` | `{"model_type": "gcnii", "graphs": [{"n", "edges", "nodes"?, "class"}, ...], "validation"?, "epochs"?, "lr"?, "batch"?, "hidden"?, "depth"?, "seed"?}`, or on a dataset `{"model_type": "gcnii", "dataset"?, "classes"?, "class_pick"?, "top_classes"?, "other_class"?, "min_edges"?, "min_graphs_per_class"?, "validation_fraction"?, "split_seed"?, "epochs"?, ...}` | `201` the run (`gb_training_job_runs`, `model_type` `gcnii`): the same endpoint, record, events and feed as the LoRA jobs; the training runs in the background |
| `GET /v1/gb/training/jobs?model_type=gcnii` | | `{"jobs": [...]}`: the runs of the user, the same on every instance; `status` is `running`, `completed`, `failed` (`error`) or `cancelled`; `last_step` is the last finished epoch and `last_loss` its loss, `final_loss` the loss on the training graphs. The events are on `GET /v1/gb/training/events` (`provider_type` `sargo`) |
| `POST /v1/gb/training/jobs/{id}/stop` | | `202`: sargo stops the training at its next batch (`cancelled`, the previous model of the user is kept); `409` if it is over, or runs on another instance |
| `POST /v1/gb/training/gcnii/infer` | `{"graph": {"n", "edges", "nodes"?}, "classes"?}` | `{"class", "index", "logits", "probabilities", "nodes", "edges"}` |
| `GET /v1/gb/training/models?model_type=gcnii` | | `{"models": [...]}`: the trained models of the user (rows of `gb_trained_models`, see below), newest first |
| `POST /v1/gb/training/gcnii/embed` | `{"graph": {...}, "normalize"?}` | `{"embedding", "dim"}` |
| `POST /v1/gb/training/gcnii/link` | `{"graph": {...}, "node": {"name"?, "type"?, "neighbors"?}, "top_k"?}` | link prediction: `{"node", "nodes", "scores": [{"node", "name"?, "score", "connected"}]}`, see [gcnii-link.md](gcnii-link.md) |
| `POST /v1/gb/training/gcnii/link/eval` | `{"graphs": [...], "nodes"?, "reveal"?, "seed"?}` | both ways to predict links tested on held-out nodes: `{"cases", "cosine": {auc, mrr, hits_at_10}, "head": {...} \| null}`; the link head is trained by a job with `"task": "link"`, see [gcnii-link.md](gcnii-link.md) |


The body also takes `"validation"`: graphs (each with a `class`) the model is not trained on; the `completed` event (its `data`) and the model have `val_accuracy` and `val_loss` on them.

A graph is `n` nodes numbered `0..n-1`, undirected `edges` `[a, b]` and optional `nodes` (`{"name", "type"}` each; the types are what the model sees). Defaults: 30 epochs, `lr` 0.01, `batch` 4, `hidden` 16, `depth` 16, random `seed`.

Errors: `400` bad request (no user, unknown `model_type`, no graphs, a graph without `class`, an edge outside the graph, ...), `404` the user has no trained model (infer, embed, link, link/eval), `409` the user is already training (or `stop` of a job that is over), `503` sargo has no per-user models, `502` sargo did not answer. A new training replaces the user's model when it finishes.

## Training on a dataset

`POST /v1/gb/training/jobs` with `"model_type": "gcnii"` and no `graphs` trains on the graphs of one of the user's datasets, labelled with the classes an LLM gave them when they were remembered (`classify_graph`, see `remember/remember.md#graph-classes`):

1. **Fetch (SQL):** the artifacts, entities, facts and `gb_artifact_classes` of the dataset are read (`memory.LoadKnowledgeDataset`).
2. **Eligible graphs:** a graph needs at least `min_edges` edges (default 1; the model learns from structure) and an LLM class.
3. **One class per graph:** the classifying training hook (`docs/hooks.md`) if the program set one, else the default, by `class_pick`: `majority` (the class given most often to the graph, earliest on a tie) or `latest` (the first class of the most recent model run). `classes` restricts the choice to those names.
4. **Class set:** `top_classes` N keeps the N most given classes; the graphs of the others are trained as `other` if `other_class` is true, else left out. Then the classes with fewer than `min_graphs_per_class` graphs are dropped. At least 2 classes must be left (`422`).
5. **Partition:** per class, `validation_fraction` (in `[0, 1)`, default 0 = no validation) of the graphs go to the validation set, in an order fixed by `split_seed`; each class keeps at least one training graph.
6. **Convert and train:** entities become nodes (`name`, `type`), facts undirected edges (a fact to an entity outside the graph adds it as a node named after it, without a type; self loops and repeats are dropped), the class the label; both sets go to sargo (`POST /v1/gcnii/train` with `validation`). Follow the job with `GET /v1/gb/training/jobs?model_type=gcnii`, and its events on `GET /v1/gb/training/events`.

Errors as `/train`, plus `404` no such dataset, `422` no graph left to train on or fewer than 2 classes (remember the dataset with `classify_graph` on).

### History

Every training on a dataset is recorded in `<sargo_gcnii_dir>/_history/<user>/<dataset>/<run id>/` (the run ID is the start time): `run.json` (user, dataset, options, training settings, classes, graphs per class in each set, artifact IDs of each set) and `classes.json`. The `run` ID is in the `params` of the model the training makes. The history does not hold the weights; those are in the [weights store](#weights-and-the-trained-models-table) below.

## Weights and the trained models table

Sargo trains and serves the model of a user from a local directory (`sargo_gcnii_dir/<hex of the user id>/model.weights` and `classes.json`): its **working copy**, one model per user, replaced by every training. gligabao keeps every finished model in a durable store and tracks it in the same table as the LoRA models of the ML sidecar, `gb_trained_models` (see [training/ml_sidecar.md](training/ml_sidecar.md#trained-models)).

**Several instances.** (The full design, its races and failure handling are in [gcnii-cluster.md](gcnii-cluster.md).) In a cluster the gligabao instances take requests behind a load balancer. Each has its own sargo and its own directory; they share only the database and the bucket. So a training runs in the sargo of ONE instance, and the others learn about it from the database, in the table `gb_model_train_jobs` (one row per training: `job_id`, `model_type`, `user_id`, `instance_id`, `status` `running`/`done`/`failed`/`lost`, `error`, `job` (sargo's last report), `info` (dataset, history run, settings), `model_id`, `started_at`, `heartbeat_at`, `finished_at`):

- **Claim.** Before sargo is asked to train, the instance inserts the running row. A unique index allows one running row per user and model type in the whole cluster, so a second `POST` of the user, on any instance, is a `409` (also while the first is still running on another instance). If sargo then refuses the request, the row is ended as `failed` (the user can try again).
- **Watch.** The instance follows its sargo in the background (the training monitor, `gcnii.Backend.Stream`) and beats the row (`heartbeat_at`, with sargo's latest report) about every 2 seconds. When sargo reports the job `done` it copies the two files into the store, inserts the model row, writes the marker (below) and ends the job row `done` with the `model_id`. A `failed` job ends the row `failed` with sargo's error; storing the weights failing ends it `failed` too (the model is still in the instance's directory).
- **Lost.** A running row whose last beat is older than 90 seconds (the instance crashed or was replaced; sargo trains in memory, so the job is gone) is marked `lost` by whichever instance looks next (a status request, or the next claim of the user), and the user can train again.
- **Status.** `GET /v1/gb/training/jobs?model_type=gcnii` shows the runs of the user as the database has it, the same on every instance: `running` until the weights are stored and recorded, `completed`, or `failed` (a `lost` job is `failed`, `error` says the instance stopped). The lease row (`gb_model_train_jobs`) belongs to the GCNII* backend: the run row is what the API shows.
- **Sync.** Before a status, `/infer`, `/embed` or `/link`, the instance compares the model in its directory with the latest row of the user: the directory has a `model_id` marker file with the ID of the row whose weights it holds. If it is not the latest row (another instance trained a newer model, or the directory was lost with its machine) the files of the latest row are written into the directory (each checked against the recorded size and sha256; the weights file last, then the marker). Sargo notices the replaced weights file (it checks its modification time on each use) and loads it. Nothing is written while this instance has a running job of the user: the directory then holds the newest model, not yet recorded. A user with no row gets sargo's own answer (`404` when it has no model).

**Where.** The store is the weights bucket (`model_weights_s3_bucket`, normally the meta bucket, the same as the evals), under its own subdirectory per model type: `<model_weights_s3_prefix>/<model_type>/<user>/<job id>/model.weights` and `classes.json`, e.g. `model_weights/gcnii/<user>/gcnii_1700000000_1a2b3c4d/model.weights`. The user is one encoded path segment (as for the evals); the job id is the start time and a random suffix, chosen at the claim; a key is written once and never overwritten or deleted. Without a bucket the same layout is written under `model_weights_local_dir` (default `model_weights` in the working directory), so a local run is tracked too (a local directory is not shared by a cluster). Settings (also `MODEL_WEIGHTS_S3_BUCKET`, `_REGION`, `_PREFIX`, `_ENDPOINT`): `build/gligabao.toml`. Credentials come from the AWS chain; the bucket must already exist (the Terraform of `gloflow_emergence` has the IAM policy: `s3:PutObject` and `s3:GetObject` on `model_weights/*`, `s3:ListBucket` on the bucket).

**The model row.** `model_type = "gcnii"`, `user_id` the user, `storage` `s3` (or `local`), `job_id` the id above, `path` the directory in the store (`model_weights/gcnii/<user>/<job id>`), `files` (path, size, sha256 of the two files), `size_bytes`, `dataset` the dataset trained on (`""` for a job started with `graphs`), `num_examples` the training graphs, `num_train_steps` the epochs, `final_loss` the loss on the training graphs, `params` (JSON: `run` the history run, `train` the settings the request gave, `classes`, `accuracy`, `loss`, `val_accuracy`, `val_loss`), `base_model` and `model_size` empty, `run_id` null. LoRA rows have `model_type = "lora"`, `user_id = ""`, `storage = "modal_volume"` (what every row had before these columns, which an existing database gets at start).

**Not the ml_sidecar.** The training API (`/v1/gb/training/models`) lists the rows of every type and user (it is the admin view). A `gcnii` row cannot be run through the ML sidecar: inference, evals, audits and deployment answer `409` for it.

## Where things are

- `internal/modeltrain`: the dataset-to-sets pipeline above (`Build`, `DefaultHook`, `PickClass`, `Split`, `ToGCNIIGraph`) and its history (`history.go`); the backend that uses it is `internal/training/gcnii`.

- `rust/sargo/src/gcnii/users.rs`: the models of the users and their background trainings; `router.rs`: the `/v1/gcnii/...` endpoints.
- `internal/sargo/gcnii.go`: the HTTP client methods (`GCNIITrain`, `GCNIITrainStatus`, `GCNIICancel`, `GCNIIInfer`, `GCNIIEmbed`) and types.
- `internal/training/gcnii`: the **training backend** of GCNII* (`training.Backend`): the same pipeline, handlers (`internal/api/training`, `POST /v1/gb/training/jobs`), run rows, events and feed as the LoRA jobs, with sargo instead of the ML sidecar (it polls sargo's status and turns it into events). `internal/api/model`: the handlers of `/infer` and `/embed`; mounted by `api.WithGCNII` (`server.Options.GCNII`, set in `gligabao.go` from the sargo client).
- The models are saved in `sargo_gcnii_dir` (config; default a `gcnii_models` directory in the working directory), one subdirectory per user: sargo's working copy.
- `internal/modeltrain/weights.go`: the weights store (`Weights`: `Save`, `Restore`, `NewModel`); `internal/training/gcnii` (`lease.go`: claim, beat, model record, reap, sync); `internal/training/db/db_model_train_job.go`: the table `gb_model_train_jobs`; the row is `internal/training/db/model.TrainedModel`.
