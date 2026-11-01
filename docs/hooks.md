# Hooks

A program that embeds gligabao (see [Embedding in Go programs](../README.md#embedding-in-go-programs)) can plug its own code into the server through **hooks**: Go callbacks supplied in `gligabao.ServeOptions` that gligabao calls at defined points. This page lists every hook that is available to external code.

| Hook | Where it is set | Called | Purpose |
|---|---|---|---|
| [`Hooks.SystemEvent`](#system-events) | `ServeOptions.Hooks` | after every `remember`, `remember` of an image, `query` and timeseries request, successful or not | logging, metrics, shipping events to a monitoring service |
| [`Auth`](#auth) | `ServeOptions.Auth` | at the start of every request to a user route (`remember`, `query`, datasets, timeseries, ontologies, research), with the API key of the request | authenticate the caller: map an API key to the user it belongs to |
| [`TimeseriesProvider.Fetch`](#timeseries-providers) | `ServeOptions.TimeseriesProviders` | when a `remember` request asks to fetch timeseries | supply timeseries from your own data source |
| [`GraphEmbedder`](#graph-embedder) | `ServeOptions.GraphEmbedder` | when a `remember` has fully constructed its graph, and when a `query` has fetched its graph context | get one embedding of a whole graph, e.g. from a custom model trained for graphs (**the result is not stored yet**) |
| [`ClassifyingTrainingHook`](#classifying-training-hook) | `ServeOptions.ClassifyingTrainingHook` | when a request asks for the training dataset of a dataset (`POST /v1/gb/datasets/{name}/training`) | turn a whole knowledge dataset (artifacts and their graphs) into the training dataset of a graph classifier: all of it or a filtered subset, with a class for each graph |
| [`TrainedModelInferenceHook.Infer`](#trained-model-inference-hooks) | `ServeOptions.TrainedModelInferenceHooks` | when a trained model whose name the hook targets is prompted (admin UI prompt dialog, `POST /v1/gb/training/models/{id}/infer`) | run the prompts of a trained model on your own infrastructure or a provider, instead of the sidecar (Modal) |
| [`TrainedModelDeployer.Deploy`](#trained-model-deployers) | `ServeOptions.TrainedModelDeployers` | when a request deploys a trained model (`POST /v1/gb/training/models/{id}/deployments`) | hand the weights of a trained model to somewhere else (an upload, a copy, a conversion, ...) |

Not a callback, but also supplied by the embedding program: [admin UI extensions](admin-extensions.md) (`ServeOptions.AdminExtensions`) add views and UI to the admin page.

Everything else (the remember queue, the stores, the LLM clients) lives under `internal/` and is not part of the public API; it cannot be hooked from outside the module.

## System events

```go
import "github.com/deepblue-phoenix/gligabao"

err := gligabao.Serve(ctx, cfg, gligabao.ServeOptions{
    DBPath: "data/gligabao.db",
    Hooks: []gligabao.Hooks{{
        SystemEvent: func(ctx context.Context, eventType string, ev gligabao.SystemEvent) {
            log.Printf("%s: %v", eventType, ev)
        },
    }},
})
```

### Types

```go
type Hooks = monitoring.Hooks
type SystemEvent = monitoring.Event // map[string]any

// in package monitoring:
type SystemEventHook func(ctx context.Context, eventType string, event Event)
type Hooks struct {
    SystemEvent SystemEventHook // nil: skipped
}
```

`gligabao.Hooks` and `gligabao.SystemEvent` are aliases of the types in the `monitoring` package, which is outside `internal/` so it can also be imported directly.

### Behaviour

- **Installed for the lifetime of `Serve`.** `Serve` installs `ServeOptions.Hooks` when it starts and removes them when it returns. There is no way to add or remove a hook while the server runs.
- **Several hook sets.** `ServeOptions.Hooks` is a list; every set is called for each event, in the order of the list. A `nil` field is skipped. With no hook installed, events are dropped.
- **Synchronous.** The callback runs on the goroutine that handles the request, before the HTTP response is written, so a slow callback slows the response. Keep it fast; hand slow work (network calls) to a goroutine or a buffered queue. (`gligabao_int` does this for its BetterStack sink.)
- **Concurrent.** Requests are handled concurrently, so the callback must be safe for concurrent use.
- **Read-only event.** The `SystemEvent` map must not be modified, and should not be retained after returning if you may change it: copy it first.
- **Panics are contained.** A callback that panics does not affect the request or stop the remaining callbacks; the panic is silently discarded, so recover and log yourself if you want to know.
- **`ctx`** is the context of the HTTP request that caused the event (cancelled if the client leaves).
- Events are emitted by the regular endpoints **and** by their `eval` variants (`POST /v1/gb/eval/remember`, `POST /v1/gb/eval/query`), which use the same handlers.

### Event types

`eventType` is `<operation>.completed` or `<operation>.failed`. Events carry **counts and sizes, never the content** (no text, query or answer). Values are `string`, `int` or `int64`.

Fields common to all events: `user_id`, `dataset`, `duration_ms` (`int64`). A `.failed` event also has `error` (the error message, `string`).

#### `remember.completed`

A text was remembered (`POST /v1/gb/remember` with `text`).

| Field | Meaning |
|---|---|
| `user_id`, `dataset` | owner and dataset |
| `artifact_id` | ID of the artifact that was created |
| `session_id` | the request's `session_id` (empty if none) |
| `kind` | artifact kind |
| `text_bytes` | size of the input text |
| `chunks` | number of chunks (a long text is a document) |
| `entities`, `edges` | entities and facts extracted and stored |
| `invalidated_edges` | existing facts that this text superseded |
| `web_searches` | web searches made |
| `series`, `series_fetches`, `series_observations` | timeseries stored, fetched from providers, and observation artifacts derived |
| `duration_ms` | wall time of the request |

#### `remember.failed`

The text pipeline failed (the request returned 502; nothing is stored).

Fields: `user_id`, `dataset`, `session_id`, `remember_run_id` (the run in `gb_remember_runs`, see [remember-manager.md](remember-manager.md)), `text_bytes`, `duration_ms`, `error`.

#### `remember_image.completed`

An image was read (`POST /v1/gb/remember` with `image`).

Fields: `user_id`, `dataset`, `image_artifact_id`, `session_id`, `extraction_id`, `media_type`, `image_bytes`, `theme`, `text_blocks` (text blocks read), `duration_ms`.

#### `remember_image.failed`

Fields: `user_id`, `dataset`, `session_id`, `image_bytes`, `duration_ms`, `error`.

#### `query.completed`

A question was answered (`POST /v1/gb/query`).

Fields: `user_id`, `dataset`, `session_id`, `query_bytes`, `evidence_entities`, `evidence_edges` (the size of the evidence returned), `duration_ms`.

#### `query.failed`

Fields: `user_id`, `dataset`, `session_id`, `query_bytes`, `duration_ms`, `error`. It is emitted for any error of the query, including those answered with 400/404 (invalid user or dataset, dataset not found), not only 502.

#### `timeseries.completed`

Series were stored (`POST /v1/gb/timeseries`).

Fields: `user_id`, `dataset`, `series` (number stored), `duration_ms`.

#### `timeseries.failed`

Fields: `user_id`, `dataset`, `series` (number in the request), `duration_ms`, `error`. Only emitted for a server error (500); an anchor that cannot be resolved (422) and other client errors emit nothing.

### Example: metrics and alerts

```go
SystemEvent: func(_ context.Context, typ string, ev gligabao.SystemEvent) {
    ms, _ := ev["duration_ms"].(int64)
    requestDuration.WithLabelValues(typ).Observe(float64(ms) / 1000)
    if strings.HasSuffix(typ, ".failed") {
        alert(typ, ev["user_id"], ev["error"])
    }
},
```

A full example (logging every event with logrus and shipping it to BetterStack through a non-blocking sink) is `systemEventHook` in `gligabao_int/gligabao_int/options.go`.

### Stability

The event types and field names above are what the server emits now. New fields may be added to an event, and new event types may be added, in any release: ignore what you do not know, and read fields with a checked type assertion (`v, ok := ev["x"].(int)`). A change to or removal of an existing event or field is called out in the [changelog](../CHANGELOG.md).

## Auth

By default gligabao does not authenticate: every request says which user it acts for with a `user_id` and is trusted. The `Auth` hook turns that around: the caller sends an **API key** and gligabao asks your code which user it belongs to.

```go
gligabao.ServeOptions{
    Auth: func(ctx context.Context, apiKey string) (userID string, err error) {
        u, err := myKeyStore.Lookup(ctx, apiKey)
        if err != nil {
            return "", err // unknown, revoked, ...: the request is rejected with 401
        }
        return u.ID, nil
    },
}
```

| Type | Definition |
|---|---|
| `gligabao.Authenticator` | `func(ctx context.Context, apiKey string) (userID string, err error)` |

- The key is read from the header `Authorization: Bearer <key>`. A request without one gets `401` and the hook is not called.
- When the hook returns a user ID, the request acts as that user: its `user_id` (the JSON body of `POST`/`PUT`, the query string of `GET`/`DELETE`) is **replaced** with it, whatever the caller sent or left out. A caller can therefore only reach the data of the owner of its key.
- When it returns an error, or an empty ID, the request gets `401` with `{"error": "invalid API key"}` (the error itself is only logged, at debug level).
- It covers the user routes: `POST /v1/gb/remember`, `POST /v1/gb/query`, the datasets, timeseries, ontologies and research endpoints. It does not cover the admin UI and the training and eval endpoints, which are not tied to a user; with `Mount`, protect those with `MountOptions.Wrap`. It does not cover the [MCP](mcp.md) endpoint (`/v1/gb/mcp`, off by default, no authentication yet) either: where MCP is enabled, anyone who can reach it can act as any user; with `Mount`, `MountOptions.Wrap` covers it.
- It is called once per request, concurrently: it must be safe for concurrent use and fast (cache the lookups if the key store is remote). It does not need to compare keys in constant time, only to return the user.
- With no hook, nothing changes: `user_id` is trusted. See [identity.md](identity.md).

The Python client sends the key it finds in `GLIGABAO_API_KEY`, and does not take a `user_id` at all.

## Timeseries providers

A provider is a callback, `Fetch`, with which `remember` pulls timeseries from a source of the embedding program's, for the entities an LLM decides it covers. It is registered with `ServeOptions.TimeseriesProviders`:

```go
gligabao.ServeOptions{
    TimeseriesProviders: []gligabao.TimeseriesProvider{{
        Name:        "prices",
        Description: "Daily stock prices. Use for publicly traded companies.",
        QueryFormat: "the ticker symbol, e.g. AAPL",
        Fetch: func(ctx context.Context, req gligabao.TimeseriesFetchRequest) ([]gligabao.TimeseriesData, error) {
            /* ... */
        },
    }},
}
```

| Type | Fields |
|---|---|
| `gligabao.TimeseriesProvider` | `Name`, `Description`, `QueryFormat`, `Fetch` |
| `gligabao.TimeseriesFetchRequest` | `Entity` (`TimeseriesEntity`: `Name`, `Type`, `Description`), `Query`, `From`, `To`, `ReferenceTime` |
| `gligabao.TimeseriesData` | `Key`, `Field`, `Name`, `Unit`, `Frequency`, `Points` (`[]TimeseriesPoint{TS, Value}`) |

Contract, in short: `Fetch` is called concurrently, with a 60 s timeout per call, must honour `ctx`, may return no series and no error when it has nothing, and a panic in it is caught and reported as an error. The providers are validated when `Serve` starts and it returns the error for a malformed one. Full description, validation rules and the request side: [timeseries.md](timeseries.md#providers).

## Graph embedder

```go
err := gligabao.Serve(ctx, cfg, gligabao.ServeOptions{
    DBPath: "data/gligabao.db",
    GraphEmbedder: func(ctx context.Context, req gligabao.GraphEmbedRequest) ([]float32, error) {
        // req.Entities and req.Edges are the graph; call your model and return its embedding
        return myModel.EmbedGraph(ctx, req.Entities, req.Edges)
    },
})
```

> **IMPORTANT: the embeddings are not stored yet.** For now gligabao calls the hook and **discards** the embedding it returns (it only logs its size at debug level). These embeddings **must be stored in the vector store**: until that is done nothing can be searched or retrieved by them, and the hook is only a place where the call happens. The hook is the first step; storing the embeddings (in a vector dataset of their own, keyed by pipeline, run and artifact, see below) is the next one and is not done.

The hook turns a **graph as a whole** into one embedding. It is not the text embedding of the entities, edges and chunks that `remember` already writes to the vector store (those come from the OpenAI embedder); it is meant for a custom model that takes a graph.

### Types

```go
type GraphEmbedder = memory_core.GraphEmbedder // func(ctx context.Context, req GraphEmbedRequest) ([]float32, error)
```

| Type | Fields |
|---|---|
| `gligabao.GraphEmbedRequest` | `Pipeline` (`gligabao.GraphEmbedRemember` or `gligabao.GraphEmbedQuery`), `Index` (the name of the graph embedding index the call asked for, see below; empty if none), `UserID`, `Dataset`, `RunID` (`gb_remember_runs.id` or `gb_query_runs.id`, by `Pipeline`; 0 if it could not be recorded), `ArtifactID` (remember only: the artifact of the remembered text, the parent artifact of a document), `Question` (query only), `Entities` (`[]gligabao.GraphEntity`), `Edges` (`[]gligabao.GraphEdge`) |

`GraphEntity` and `GraphEdge` are the stored entity and edge (with their IDs, the edge's endpoints and its bi-temporal validity), as `GET /v1/gb/datasets/{name}/graph` returns them.

### The index name

A `remember` and a `query` request can name the graph embedding index they are for, with the optional field **`graph_index`** (see [remember.md](remember.md), [query.md](query.md)); the hook gets it as `req.Index`, for the embedding it makes (remember) and for the search it does (query). It is how a hook that keeps several indexes (one per model, per size bin, per tenant, ...) knows which one: the graph engine of `baby_thinker` has named indexes (`-index NAME`) and its client passes `req.Index` there. gligabao only carries the name and does not interpret it.

- The name is 1-64 letters, digits, `.`, `_` or `-`, starting with a letter or digit (the names the graph engine accepts, so it can never be a path); gligabao validates it (`400` otherwise) and trims it.
- **Empty means "none given"**: the hook uses its own default (the engine's is `default`).
- A request that names an index on a server **without** a graph embedder hook is a `400` (the name would be dropped silently otherwise), and so is one with an `image` (the image pipeline does not call the hook).
- In Go, the option is `memory_core.WithGraphIndex(name)` (internal); the MCP tools do not carry it yet.

### When it is called

- **`remember`**: once, when the call has finished, so the graph of the text is fully constructed (stored, reconciled with the facts already there and enriched by geo, web search and series). The graph is what the text yielded: the entities it and its facts mention and its facts, in their stored state (for a document, those of all its chunks). It is not the whole dataset, and not what the web search and series observation stages made of their own texts. A text that yielded nothing does not call it.
- **`query`**: once, when the graph context of the question has been fetched (the `retrieve_graph` stage): the entities and edges around the ones that matched the question, the ones found by similarity, and the facts between them. This is the initial context: what the geo stages add afterwards is not in it, and the passages are not part of the graph. When there is nothing to answer from the hook is not called. A retrieval-only query (`evidence_only`) calls it too.
- The hook is part of the options of every call of the regular endpoints, the `eval` variants, the MCP tools, the admin UI's query button and the research agents.

### Behaviour

- **Advisory.** An error or a panic of the hook is logged (warning) and dropped; it never fails the `remember` or the `query`.
- **Synchronous.** It runs on the goroutine of the call: the remember manager's worker for a `remember` (the next queued remember waits for it), the request's for a `query` (the answer waits for it). A slow model slows them. Keep it fast.
- **Concurrent.** Queries run concurrently, so the callback must be safe for concurrent use. `ctx` is the context of the call.
- **Read-only request.** Do not modify `req.Entities` and `req.Edges`.
- **Off by default.** With no `GraphEmbedder` nothing is called and nothing is loaded.
- **No system event, no run log stage.** The hook is not a stage of the pipelines, so it does not appear in the run logs.

### To do: storing the embeddings

When the hook is completed, the embedding it returns must be written to the vector store (`CallOptions.Vectors`, the same store `remember` writes its other vectors to), into a vector dataset of the user's dataset for graph embeddings, in the index named by `Index`, keyed by `Pipeline` + `RunID` (and `ArtifactID` for a remember), so that a graph can be found by similarity. The places to do it are marked `IMPORTANT` in the code: `memory_core.EmbedGraph` (where the result is dropped), `memory_remember.job.embedGraph` and `memory_query.job.embedGraph`.

## Classifying training hook

A hook for training, next to the [graph embedder](#graph-embedder) (which is for embeddings). gligabao gives it a **whole dataset**, every artifact and the graph that comes with each, and it answers with the **training dataset** to train on: the dataset, or a filtered subset of it, plus the class of each of its graphs. For now it is a *classifying* hook (the training is of a classifier of graphs); other kinds of training will get hooks of their own.

```go
gligabao.Serve(ctx, cfg, gligabao.ServeOptions{
    DBPath: "data/gligabao.db",
    ClassifyingTrainingHook: func(ctx context.Context, ds gligabao.KnowledgeDataset) (gligabao.TrainingDataset, error) {
        td := gligabao.TrainingDataset{Classes: map[int64]string{}}
        td.UserID, td.Dataset, td.DatasetID = ds.UserID, ds.Dataset, ds.DatasetID
        for _, g := range ds.Graphs {
            if len(g.Edges) < 2 {
                continue // a filter: too small a graph is left out
            }
            td.Graphs = append(td.Graphs, g)
            td.Classes[g.ArtifactID] = myClassifier.Label(ctx, g)
        }
        keep := map[int64]bool{}
        for _, g := range td.Graphs {
            keep[g.ArtifactID] = true
        }
        for _, a := range ds.Artifacts {
            if keep[a.ID] {
                td.Artifacts = append(td.Artifacts, a)
            }
        }
        return td, nil
    },
})
```

### Types

```go
type ClassifyingTrainingHook = memory_core.ClassifyingTrainingHook // func(ctx context.Context, ds KnowledgeDataset) (TrainingDataset, error)
```

| Type | Fields |
|---|---|
| `gligabao.KnowledgeDataset` | `UserID`, `Dataset` (the resolved name), `DatasetID`, `Artifacts` (`[]gligabao.Artifact`: every artifact of the dataset, of every kind, by ID), `Graphs` (`[]gligabao.ArtifactGraph`: the graph of every artifact that has one, by artifact ID) |
| `gligabao.ArtifactGraph` | `ArtifactID` (the artifact the graph is of; it identifies the graph), `Entities` (`[]gligabao.GraphEntity`), `Edges` (`[]gligabao.GraphEdge`) |
| `gligabao.TrainingDataset` | the `KnowledgeDataset` embedded (what is kept of it: all, or a subset) and `Classes` (`map[int64]string`: the class of each graph in it, by the `ArtifactID` of the graph) |

`TrainingDataset` is a superset of `KnowledgeDataset`: the knowledge dataset it wraps is the part of the dataset that is trained on, and `Classes` is what is added to it.

The graph of an artifact is what is linked to it (see [artifacts.md](artifacts.md#provenance-links)): the entities that mention it, the ones it created and the ones it found already stored, and the facts it stated, the ones it created and the ones whose duplicate it was merged into, in their stored state (expired facts included). An artifact that yielded nothing (no entity, no fact) has no graph. The graphs of two artifacts can share entities and facts.

### Contract

- **The hook may return a subset.** It does not have to return everything it got: the artifacts and graphs it leaves out are not trained on (a filter, a sample, a train/test split). A graph it keeps needs its artifact, and a class.
- **It can only choose.** gligabao checks the result and rejects it, as a `502`, if it has an artifact or a graph (by artifact ID) that was not in the dataset or one twice, a graph whose artifact it does not keep, a graph without a class (or with a blank one), or a class for something that is not one of its graphs. The content of the artifacts and graphs is not compared: the IDs are the contract.
- **Not advisory.** Unlike the graph embedder, the result is what the call is for: an error or a panic of the hook (and an invalid result) fails the request, with `502` and the message.
- **Synchronous and concurrent.** It runs on the goroutine of the request, with the context of the request, and may be called concurrently (several requests, other datasets). The whole dataset is loaded in memory first: a large dataset means a large call.
- **Read-only input.** Do not modify what it gets (`Artifacts`, `Graphs` and their slices).
- **Off by default.** With no hook the route does not exist (`404`).

### Endpoint

`POST /v1/gb/datasets/{name}/training`, body `{"user_id": "u1"}` (under [`Auth`](#auth) like the other routes of users). It loads the dataset (`404` for one that does not exist), calls the hook and answers `200` with a summary of the training dataset it made:

```json
{"dataset": "main", "artifacts": 2, "artifacts_total": 5, "graphs": 2, "graphs_total": 4,
 "classes": {"3": "employee", "5": "customer"}, "class_counts": {"employee": 1, "customer": 1}}
```

`artifacts` and `graphs` are how many the hook kept, `*_total` how many the dataset has, `classes` the class of each kept graph by artifact ID. **Nothing is stored yet**: gligabao only calls the hook and reports what it returned. The training itself, and keeping the training dataset, are the next steps.

## Trained model inference hooks

By default a trained model (a LoRA adapter on the sidecar's models volume) is prompted on the sidecar, i.e. on Modal. An inference hook **bypasses Modal for one trained model**: it names the trained model it targets, and when that model is prompted gligabao calls the hook's `Infer` callback instead of the sidecar. The hook runs the inference wherever it wants (a provider that serves the model, your own GPU server) and returns the response. Hooks are registered with `ServeOptions.TrainedModelInferenceHooks`; **several can be supplied**, each naming its own model:

```go
gligabao.ServeOptions{
    TrainedModelInferenceHooks: []gligabao.TrainedModelInferenceHook{
        {
            Name:  "together",
            Model: "4b-mine.jsonl-100steps-1a2b3c4d", // the name of the trained model (as listed in the admin UI)
            Infer: func(ctx context.Context, req gligabao.InferTrainedModelRequest) (gligabao.InferTrainedModelResult, error) {
                // req.Model is the trained model, req.Prompt, req.MaxTokens (0: none chosen), req.Temperature (nil: none chosen)
                return gligabao.InferTrainedModelResult{Response: "...", InputTokens: 120, OutputTokens: 80}, nil
            },
        },
        {Name: "other", Model: "12b-other.jsonl-50steps-9f8e7d6c", Infer: otherInfer},
    },
}
```

| Type | Fields |
|---|---|
| `gligabao.TrainedModelInferenceHook` | `Name`, `Description` (optional), `Model` (the name of the trained model it targets), `Infer` |
| `gligabao.InferTrainedModelRequest` | `Model` (`gligabao.TrainedModel`), `Prompt`, `MaxTokens`, `Temperature` |
| `gligabao.InferTrainedModelResult` | `Response`, `InputTokens`, `OutputTokens` (0 if unknown) |

### How gligabao resolves which model a hook runs

When a trained model is prompted, gligabao looks at the model's **name** (`gb_trained_models.name`, e.g. `4b-mine.jsonl-100steps-1a2b3c4d`, shown in the admin UI) and uses the hook whose `Model` equals it exactly (case-sensitive, surrounding whitespace ignored). If there is one, the hook runs the prompt and the sidecar is not called; if there is none, the model runs on the sidecar as before. Model names are not unique (there is no rename), so a hook runs every trained model with that name; at most one hook may target a name.

### Behaviour

- **Where it applies: prompting only.** The prompt dialog of the admin UI and `POST /v1/gb/training/models/{id}/infer` (the answer has `hook`: the name of the hook, `""` for the sidecar). `GET /v1/gb/training/inference-hooks` lists the hooks (`name`, `description`, `model`).
- **Where it does not apply: evals and audits.** The [training evals](evals/training.md) and the [training data audits](training/audit.md) of a model **always run on the sidecar (Modal)**, whether or not a hook targets the model: Modal is where the models are trained and where their weights live, so an eval or audit measures those weights, not whatever the hook serves. They still need the sidecar (and an LLM judge). Likewise training jobs, deployments and fetching the weights.
- **No sidecar needed for prompting a hooked model.** A hooked model can be prompted when the sidecar is not active; a model without a hook then answers `503`.
- **Recorded the same.** The request is stored in `gb_trained_model_inferences` (prompt, response, status, error, tokens, duration; `cold_start` is unknown) before the hook is called, and updated when it ends, failures included.
- **Synchronous, concurrent.** `Infer` runs on the goroutine of the request, may be called concurrently and must honour `ctx`, which times out after 15 minutes (as for the sidecar).
- **Limits.** `MaxTokens` and `Temperature` are what the caller chose (validated: 1 to 8192 tokens, temperature above 0 and at most 2); when they were not given they are `0` and `nil` and the hook uses its own defaults.
- **Failure.** An error returned by `Infer` (or a panic, which is caught) fails the request: it is stored as `failed` and answered `502` with the hook's message; a failed question of an eval or audit is reported with it. Negative token counts are an error too.
- **Validated at startup.** `Serve` returns an error for a hook with no `Infer`, no `Model`, a `Name` that is not 1-64 letters, digits, `_`, `-` or `.` (starting with a letter or digit), a duplicate name, or two hooks for the same model. Each registered hook is logged at startup. At most 50.

## Trained model deployers

A deployer is a callback, `Deploy`, that receives a trained model's weights, as files in a directory on the local disk, and does with them whatever the embedding program wants: upload them to a service, copy them somewhere, convert them. gligabao does not know or care what; it only fetches the files and records the outcome. The full description of how deploying works is in [model-deploying.md](model-deploying.md). Deployers are registered with `ServeOptions.TrainedModelDeployers`:

```go
gligabao.ServeOptions{
    TrainedModelDeployers: []gligabao.TrainedModelDeployer{{
        Name: "bucket",
        Description: "Uploads the model's files to the releases bucket.",
        Deploy: func(ctx context.Context, req gligabao.DeployTrainedModelRequest) (gligabao.DeployTrainedModelResult, error) {
            // req.Dir has req.Model.Files: adapter_config.json, adapter_model.safetensors, ...
            return gligabao.DeployTrainedModelResult{Ref: "models/my-adapter", Details: map[string]any{"job_id": "..."}}, nil
        },
    }},
}
```

A deployment is asked for with `POST /v1/gb/training/models/{id}/deployments` and a body `{"deployer": "bucket", "options": {...}}`; `options` is a free-form JSON object that reaches the callback unchanged (`req.Options`). It is available in training mode (`--training`) only, like the rest of the training API.

| Type | Fields |
|---|---|
| `gligabao.TrainedModelDeployer` | `Name`, `Description` (optional, shown in the admin UI), `Deploy` |
| `gligabao.DeployTrainedModelRequest` | `DeployID`, `Update` (`func(message string)`: reports progress, shown in the admin UI), `Model` (`gligabao.TrainedModel`: `ID`, `Name`, `JobID`, `BaseModel`, `ModelSize`, `Dataset`, `NumExamples`, `NumTrainSteps`, `FinalLoss`, `Params`, `Path`, `Files` (`gligabao.TrainedModelFile`: `Path`, `SizeBytes`, `SHA256`), ...), `Dir`, `Options` |
| `gligabao.DeployTrainedModelResult` | `Ref`, `Details` |

### Behaviour

- **gligabao fetches the files; the deployer does not.** The weights live on the sidecar's storage (Modal: the models volume). Before calling the deployer gligabao downloads every file of `Model.Files` from the sidecar (`GET /models/file`, see [training/sidecar.md](training/sidecar.md#fetching-the-files-of-a-model)) into `Dir`, and checks each against its recorded size and sha256. The deployer gets files that are complete and verified, and never talks to the sidecar.
- **`Dir`** is an absolute path, `<trained model deploy dir>/<job id>` (`trained_model_deploy_dir` in the config file, `serve --trained-model-deploy-dir`, `ServeOptions.TrainedModelDeployDir`, default `trained_models` next to the database file). The files are at the paths `Model.Files` lists, relative to it (`adapter_config.json`, `adapter_model.safetensors`, `tokenizer.model`, ...). **gligabao never deletes it**: it is kept for later deployments of the same model, which find the files in place (verified again) and download only what is missing or damaged. Clean it up yourself. The deployer must treat it as read only; copy or archive what it needs.
- **Synchronous.** The request that asked for the deployment waits for the fetch and then for `Deploy`; its HTTP response is the stored deployment. Fetching a large model and a slow upload can take minutes: callers must not use a short timeout. `ctx` is the request's context (cancelled if the client leaves), so `Deploy` should honour it.
- **Progress.** `req.Update("message")` reports what the deployer is doing. The messages are not stored: they are kept in memory while the deployment runs, listed as `updates` by `GET .../deployments` and shown by the admin UI; see [model-deploying.md](model-deploying.md#progress-updates).
- **Result.** On success `Ref` identifies what the deployer made, in its own terms (e.g. the name of the uploaded model), and `Details` is anything else worth keeping; it must be JSON-encodable. Both are stored and returned with the deployment.
- **Failure.** An error returned by `Deploy` (or a panic, which is caught) marks the deployment `failed` with the error text and answers `502`. So does a failure to fetch the files. A failed deployment is not retried by gligabao.
- **Concurrent.** Requests are handled concurrently, so `Deploy` must be safe for concurrent use, also for the same model. `DeployID` is unique to each call: use it if the deployer needs a unique name.
- **Recorded.** Every deployment that reaches the fetch is stored in `gb_trained_model_deployments` (status `running`, then `completed` or `failed`; the deployer, its options, the directory, `ref`, `details`, the error, the duration) and listed by `GET /v1/gb/training/models/{id}/deployments`. A row still `running` long after it started was interrupted (the server restarted).
- **Validated at startup.** `Serve` returns an error for a deployer with no `Deploy`, a `Name` that is not 1-64 letters, digits, `_`, `-` or `.` (starting with a letter or digit), or a duplicate name. At most 20.
- **System events.** Each deployment emits `training.model_deploy.completed` or `training.model_deploy.failed` to the [`SystemEvent`](#system-events) hook, with `model_id`, `deploy_id`, `deployer`, `duration_ms` (`int64`) and, on failure, `error`.

Endpoints: [training/sidecar.md](training/sidecar.md#trained-models). How deploying works inside (fetching and verifying the files, the record of each deployment, the API, the limits): [model-deploying.md](model-deploying.md).
