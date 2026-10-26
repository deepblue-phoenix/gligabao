# Hooks

A program that embeds gligabao (see [Embedding in Go programs](../README.md#embedding-in-go-programs)) can plug its own code into the server through **hooks**: Go callbacks supplied in `gligabao.ServeOptions` that gligabao calls at defined points. This page lists every hook that is available to external code.

| Hook | Where it is set | Called | Purpose |
|---|---|---|---|
| [`Hooks.SystemEvent`](#system-events) | `ServeOptions.Hooks` | after every `remember`, `remember` of an image, `query` and timeseries request, successful or not | logging, metrics, shipping events to a monitoring service |
| [`Auth`](#auth) | `ServeOptions.Auth` | at the start of every request to a user route (`remember`, `query`, datasets, timeseries, ontologies), with the API key of the request | authenticate the caller: map an API key to the user it belongs to |
| [`TimeseriesProvider.Fetch`](#timeseries-providers) | `ServeOptions.TimeseriesProviders` | when a `remember` request asks to fetch timeseries | supply timeseries from your own data source |
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
- It covers the user routes: `POST /v1/gb/remember`, `POST /v1/gb/query`, the datasets, timeseries and ontologies endpoints. It does not cover the admin UI and the training and eval endpoints, which are not tied to a user; with `Mount`, protect those with `MountOptions.Wrap`.
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
