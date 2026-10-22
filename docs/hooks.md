# Hooks

A program that embeds gligabao (see [Embedding in Go programs](../README.md#embedding-in-go-programs)) can plug its own code into the server through **hooks**: Go callbacks supplied in `gligabao.ServeOptions` that gligabao calls at defined points. This page lists every hook that is available to external code.

| Hook | Where it is set | Called | Purpose |
|---|---|---|---|
| [`Hooks.SystemEvent`](#system-events) | `ServeOptions.Hooks` | after every `remember`, `remember` of an image, `recall` and timeseries request, successful or not | logging, metrics, shipping events to a monitoring service |
| [`TimeseriesProvider.Fetch`](#timeseries-providers) | `ServeOptions.TimeseriesProviders` | when a `remember` request asks to fetch timeseries | supply timeseries from your own data source |
| [`TrainedModelExporter.Export`](#trained-model-exporters) | `ServeOptions.TrainedModelExporters` | when a request exports a trained model (`POST /v1/gb/training/models/{id}/exports`) | hand the weights of a trained model to somewhere else (an upload, a copy, a conversion, ...) |

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
- Events are emitted by the regular endpoints **and** by their `eval` variants (`POST /v1/gb/eval/remember`, `POST /v1/gb/eval/recall`), which use the same handlers.

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

#### `recall.completed`

A question was answered (`POST /v1/gb/recall`).

Fields: `user_id`, `dataset`, `session_id`, `query_bytes`, `evidence_entities`, `evidence_edges` (the size of the evidence returned), `duration_ms`.

#### `recall.failed`

Fields: `user_id`, `dataset`, `session_id`, `query_bytes`, `duration_ms`, `error`. It is emitted for any error of the recall, including those answered with 400/404 (invalid user or dataset, dataset not found), not only 502.

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

A full example (logging every event with logrus and shipping it to BetterStack through a non-blocking sink) is `systemEventHook` in `gligabao_int/cmd/serve.go`.

### Stability

The event types and field names above are what the server emits now. New fields may be added to an event, and new event types may be added, in any release: ignore what you do not know, and read fields with a checked type assertion (`v, ok := ev["x"].(int)`). A change to or removal of an existing event or field is called out in the [changelog](../CHANGELOG.md).

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

## Trained model exporters

An exporter is a callback, `Export`, that receives a trained model's weights, as files in a directory on the local disk, and does with them whatever the embedding program wants: upload them to a service, copy them somewhere, convert them. gligabao does not know or care what; it only fetches the files and records the outcome. The full description of how exporting works is in [model-exporting.md](model-exporting.md). Exporters are registered with `ServeOptions.TrainedModelExporters`:

```go
gligabao.ServeOptions{
    TrainedModelExporters: []gligabao.TrainedModelExporter{{
        Name: "bucket",
        Export: func(ctx context.Context, req gligabao.ExportTrainedModelRequest) (gligabao.ExportTrainedModelResult, error) {
            // req.Dir has req.Model.Files: adapter_config.json, adapter_model.safetensors, ...
            return gligabao.ExportTrainedModelResult{Ref: "models/my-adapter", Details: map[string]any{"job_id": "..."}}, nil
        },
    }},
}
```

An export is asked for with `POST /v1/gb/training/models/{id}/exports` and a body `{"exporter": "bucket", "options": {...}}`; `options` is a free-form JSON object that reaches the callback unchanged (`req.Options`). It is available in training mode (`--training`) only, like the rest of the training API.

| Type | Fields |
|---|---|
| `gligabao.TrainedModelExporter` | `Name`, `Export` |
| `gligabao.ExportTrainedModelRequest` | `ExportID`, `Model` (`gligabao.TrainedModel`: `ID`, `Name`, `JobID`, `BaseModel`, `ModelSize`, `Dataset`, `NumExamples`, `NumTrainSteps`, `FinalLoss`, `Params`, `Path`, `Files` (`gligabao.TrainedModelFile`: `Path`, `SizeBytes`, `SHA256`), ...), `Dir`, `Options` |
| `gligabao.ExportTrainedModelResult` | `Ref`, `Details` |

### Behaviour

- **gligabao fetches the files; the exporter does not.** The weights live on the sidecar's storage (Modal: the models volume). Before calling the exporter gligabao downloads every file of `Model.Files` from the sidecar (`GET /models/file`, see [training/sidecar.md](training/sidecar.md#fetching-the-files-of-a-model)) into `Dir`, and checks each against its recorded size and sha256. The exporter gets files that are complete and verified, and never talks to the sidecar.
- **`Dir`** is an absolute path, `<trained model export dir>/<job id>` (`serve --trained-model-export-dir`, `ServeOptions.TrainedModelExportDir`, default `trained_models` next to the database file). The files are at the paths `Model.Files` lists, relative to it (`adapter_config.json`, `adapter_model.safetensors`, `tokenizer.model`, ...). **gligabao never deletes it**: it is kept for later exports of the same model, which find the files in place (verified again) and download only what is missing or damaged. Clean it up yourself. The exporter must treat it as read only; copy or archive what it needs.
- **Synchronous.** The request that asked for the export waits for the fetch and then for `Export`; its HTTP response is the stored export. Fetching a large model and a slow upload can take minutes: callers must not use a short timeout. `ctx` is the request's context (cancelled if the client leaves), so `Export` should honour it.
- **Result.** On success `Ref` identifies what the exporter made, in its own terms (e.g. the name of the uploaded model), and `Details` is anything else worth keeping; it must be JSON-encodable. Both are stored and returned with the export.
- **Failure.** An error returned by `Export` (or a panic, which is caught) marks the export `failed` with the error text and answers `502`. So does a failure to fetch the files. A failed export is not retried by gligabao.
- **Concurrent.** Requests are handled concurrently, so `Export` must be safe for concurrent use, also for the same model. `ExportID` is unique to each call: use it if the exporter needs a unique name.
- **Recorded.** Every export that reaches the fetch is stored in `gb_trained_model_exports` (status `running`, then `completed` or `failed`; the exporter, its options, the directory, `ref`, `details`, the error, the duration) and listed by `GET /v1/gb/training/models/{id}/exports`. A row still `running` long after it started was interrupted (the server restarted).
- **Validated at startup.** `Serve` returns an error for an exporter with no `Export`, a `Name` that is not 1-64 letters, digits, `_`, `-` or `.` (starting with a letter or digit), or a duplicate name. At most 20.
- **System events.** Each export emits `training.model_export.completed` or `training.model_export.failed` to the [`SystemEvent`](#system-events) hook, with `model_id`, `export_id`, `exporter`, `duration_ms` (`int64`) and, on failure, `error`.

Endpoints: [training/sidecar.md](training/sidecar.md#trained-models). How exporting works inside (fetching and verifying the files, the record of each export, the API, the limits): [model-exporting.md](model-exporting.md).
