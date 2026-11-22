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

A graph is `n` nodes numbered `0..n-1`, undirected `edges` `[a, b]` and optional `nodes` (`{"name", "type"}` each; the types are what the model sees). Defaults: 30 epochs, `lr` 0.01, `batch` 4, `hidden` 16, `depth` 16, random `seed`.

Errors: `400` bad request (no user, missing or unknown `model_type`, no graphs, a graph without `class`, an edge outside the graph, ...), `404` the user has no trained model (infer, embed), `409` the user is already training, `503` sargo has no per-user models, `502` sargo did not answer. A new training replaces the user's model when it finishes.

## Where things are

- `rust/sargo/src/gcnii/users.rs`: the models of the users and their background trainings; `router.rs`: the `/v1/gcnii/...` endpoints.
- `internal/sargo/gcnii.go`: the HTTP client methods (`GCNIITrain`, `GCNIITrainStatus`, `GCNIIInfer`, `GCNIIEmbed`) and types.
- `internal/api/model`: the handlers of the routes above; mounted by `api.WithGCNII` (`server.Options.GCNII`, set in `gligabao.go` from the sargo client).
- The models are saved in `sargo_gcnii_dir` (config; default a `gcnii_models` directory in the working directory), one subdirectory per user.
