# Model exporting

Exporting a trained model means handing its weights to code of yours. gligabao does the part that is the same for everybody, getting the weights from where they were trained onto the local disk and checking them, and calls a **hook** with the result: an *exporter*, a Go callback registered by the program that embeds gligabao. What the exporter does with the files (upload them to a service, copy them to a bucket, convert them to another format, ...) is its own business; gligabao has no built-in destination.

This page describes how exporting works inside and the hook's contract. The hook's entry in the list of all hooks is [hooks.md](hooks.md#trained-model-exporters); the trained models themselves (the `gb_trained_models` table, where the weights live) are in [training/sidecar.md](training/sidecar.md#trained-models).

Exporting is part of the training API: it is there only when the server runs in training mode (`--training`) and, to fetch the weights, with an active sidecar.

## The flow

```
POST /v1/gb/training/models/{id}/exports   {"exporter": "<name>", "options": {...}}
  1. look up the trained model {id}                          404 if unknown
  2. find the exporter by name, check the options            400 if none is registered, the name is unknown or the options are bad
  3. record the export in gb_trained_model_exports           status "running"
  4. fetch the model's files from the sidecar into Dir       each file verified (size, sha256)
  5. call Export(ctx, ExportTrainedModelRequest)             your code; the request waits for it; it reports progress with req.Update
  6. update the record                                       status "completed" (ref, details) or "failed" (error)
  7. emit the system event training.model_export.completed / .failed
  8. answer 200 with the stored export                       502 if step 4 or 5 failed
```

The whole thing is **synchronous**: the HTTP request is open while the files are downloaded and while `Export` runs, which can take minutes for a large model. Callers must not use a short timeout. If the client disconnects, the request's `ctx` is cancelled, which `Export` should notice; the export is then recorded as failed.

## Registering an exporter

Exporters are passed in `ServeOptions.TrainedModelExporters` (there is no command-line or configuration-file way to register one, since an exporter is code):

```go
import "github.com/deepblue-phoenix/gligabao"

err := gligabao.Serve(ctx, cfg, gligabao.ServeOptions{
    TrainedModelExporters: []gligabao.TrainedModelExporter{{
        Name: "bucket",
        Description: "Uploads the model's files to the releases bucket.",
        Export: func(ctx context.Context, req gligabao.ExportTrainedModelRequest) (gligabao.ExportTrainedModelResult, error) {
            dest := fmt.Sprintf("models/%s/export-%d", req.Model.JobID, req.ExportID)
            for _, f := range req.Model.Files {
                // f.Path is relative to req.Dir; the file is complete and verified
                if err := upload(ctx, filepath.Join(req.Dir, filepath.FromSlash(f.Path)), dest+"/"+f.Path); err != nil {
                    return gligabao.ExportTrainedModelResult{}, err
                }
            }
            return gligabao.ExportTrainedModelResult{
                Ref:     dest,
                Details: map[string]any{"files": len(req.Model.Files)},
            }, nil
        },
    }},
})
```

| Type | Fields |
|---|---|
| `gligabao.TrainedModelExporter` | `Name`, `Description` (optional, shown in the admin UI), `Export` |
| `gligabao.ExportTrainedModelFunc` | `func(ctx context.Context, req ExportTrainedModelRequest) (ExportTrainedModelResult, error)`, the type of `Export` |
| `gligabao.ExportTrainedModelRequest` | `ExportID`, `Model`, `Dir`, `Options`, `Update` |
| `gligabao.ExportTrainedModelResult` | `Ref`, `Details` |

### What `Export` receives

| Field | Meaning |
|---|---|
| `ExportID` | the ID of this export (`gb_trained_model_exports.id`): unique to each call, so use it when the exporter needs a unique name for what it creates |
| `Model` | the trained model (`gligabao.TrainedModel`): `ID`, `Name`, `JobID`, `BaseModel`, `ModelSize`, `Dataset`, `NumExamples`, `NumTrainSteps`, `FinalLoss`, `Params`, `Path`, and `Files` (`gligabao.TrainedModelFile`: `Path`, `SizeBytes`, `SHA256`), ... |
| `Dir` | the absolute path of a local directory that holds the model's files, at the paths `Model.Files` lists, relative to it |
| `Options` | the `options` object of the request, unchanged, as a `map[string]any` (`nil` if the request gave none) |
| `Update` | `func(message string)`: reports what the exporter is doing (see [Progress updates](#progress-updates)); never `nil` |

### Progress updates

An export can take minutes, so every exporter gets `req.Update`, a function to say what it is doing as it goes:

```go
req.Update("uploading the archive to S3")
// ... the upload ...
req.Update("waiting for the import job to finish")
```

- A message is a short plain-text line (steps, not bytes). Blank messages are ignored, messages longer than **500** characters are cut, and only the newest **200** are kept.
- Messages are for display and are **not stored**: they are kept in memory while the export runs and shown by the admin UI (they are in `updates` of `GET .../exports` for a running export, with their times), and the answer of the `POST` that ran the export has them too. Once it is over they are gone (a restart loses them as well). What must survive belongs in the result's `Details`.
- gligabao adds its own messages around the exporter's: `Fetching the model's N files from the sidecar`, `Files ready, running the <name> exporter` and, at the end, `Completed` or `Failed: <error>`.
- Safe for concurrent use. Calls after `Export` has returned are ignored.

### What `Export` returns

- `Ref`: identifies what the export made, in the exporter's own terms (the name of a model created elsewhere, an object-store path, ...). A string, stored and returned with the export.
- `Details`: anything else worth keeping; a `map[string]any` that must be JSON-encodable (an exporter that returns details that are not is treated as failed). May be `nil`.
- A non-nil error, or a panic (it is caught), makes the export `failed`; the error text is stored and returned.

### Rules of the hook

- **Read only.** `Dir` is shared with later exports of the same model. `Export` must not modify or delete the files in it; copy, archive or convert into a place of its own.
- **Concurrent.** Export requests are handled concurrently, also several for the same model, so `Export` must be safe for concurrent use. The files of one model are fetched once even then (see below).
- **Honour `ctx`.** It is the request's context: it is cancelled if the client goes away. The fetch has its own limit of 30 minutes; `Export` has none besides `ctx`.
- **No retries.** gligabao runs `Export` once per request. A failed export stays failed; ask for a new one to try again (the files are already on the disk by then).
- **No sidecar.** The exporter never talks to the sidecar; it finds the weights on the local disk.

### Validation at startup

`Serve` checks the registered exporters and returns an error (`invalid trained model exporter: ...`) for any of these, before the server starts:

- more than **20** exporters;
- a `Name` that is empty, longer than **64** characters, or not letters, digits, `_`, `-` and `.` starting with a letter or digit;
- two exporters with the same `Name`;
- an exporter with no `Export` function.

Exporters are sorted by name; each one registered is logged at startup. With no exporter registered, every export request is answered `400`.

## Fetching the weights

The weights of a trained model live on the sidecar's storage (Modal: the models volume), not on the server. Before it calls the exporter, gligabao copies them to the server's disk. This is the part of exporting that is the same for every exporter.

- **Where.** `<trained model export dir>/<job id>`, an absolute path (`Dir`). The export directory is `trained_model_export_dir` in the config file, overridden by `serve --trained-model-export-dir` (or `ServeOptions.TrainedModelExportDir`); the default is a `trained_models` directory next to the database file (`bin/trained_models`). There is one subdirectory per training job, so models never mix. A job ID that is not usable as a directory name is refused (`400`).
- **What.** Every file in the model's `files` (the list recorded when the job completed), at its recorded relative path; nested paths are allowed. A path that is absolute, contains `..` or uses a backslash is refused.
- **How.** One `GET /models/file?model_path=trained/<job id>&file=<path>` request per file to the sidecar, which streams the raw bytes out of the volume (see [training/sidecar.md](training/sidecar.md#fetching-the-files-of-a-model)); the file is never held in memory.
- **Verified.** Each file must have exactly the recorded size and the recorded sha256 (a model with no sha256 recorded for a file is checked by size only). A mismatch fails the export; nothing is exported from unverified bytes.
- **Atomic.** A file is downloaded under a temporary name (`.fetch-*`, in its own directory) and renamed to its real name only when it is complete and verified, so the real names never hold half a file, even if the server dies halfway.
- **Reused.** A file that is already in `Dir` with the right size and sha256 is kept and not downloaded again; only missing or damaged files are fetched. Exporting the same model a second time therefore costs only the verification.
- **Serialized.** Fetches into one directory are serialized: two simultaneous exports of the same model do not download it twice; the second waits and then finds the files in place.
- **Never deleted.** gligabao does not clean `Dir`. The files stay until you remove them (`rm -r bin/trained_models/<job id>`); the next export fetches them again. Model weights are large, so plan for the disk space.
- **No recorded files.** A model with no recorded files (for instance one saved by a version before files were listed) cannot be exported: the export fails with "the trained model has no recorded files".

## The record of an export

Every export that gets past the checks of steps 1 and 2 is stored in `gb_trained_model_exports`, when it starts and again when it ends, so each can be reviewed later, failures included:

| Column | Meaning |
|---|---|
| `id` | the export's ID (`ExportID` in the hook) |
| `trained_model_id` | the model (`gb_trained_models.id`; deleting the model deletes its exports) |
| `exporter` | the name of the exporter asked |
| `options` | the request's `options`, as a JSON object (`{}` if none) |
| `status` | `running` (fetching, or the exporter is working), `completed` or `failed` |
| `dir` | the local directory the files were fetched to; empty until they are |
| `ref`, `details` | the exporter's result; empty and `{}` until `completed` |
| `error` | why it failed; empty otherwise |
| `duration_ms` | how long the whole export took (0 while running) |
| `created_at`, `completed_at` | when it started and when it ended (null while running) |

The table exists on SQLite and PostgreSQL and is created automatically. A row that stays `running` long after it started was interrupted, for example by a restart of the server: nothing resumes it, and nothing marks it failed.

## HTTP API

| Endpoint | |
|---|---|
| `POST /v1/gb/training/models/{id}/exports` | Body `{"exporter": "<name>", "options": {...}}`. `exporter` is required (the error lists the registered names); `options` is a free-form JSON object of at most 64 KB that reaches the hook unchanged. Fetches the files, calls the exporter, waits, and answers `200` with the stored export (`id`, `trained_model_id`, `exporter`, `options`, `status` `completed`, `dir`, `ref`, `details`, `error`, `updates`, `duration_ms`, `created_at`, `completed_at`). `updates` is `[{"time", "message"}]`, not stored (see [Progress updates](#progress-updates)). |
| `GET /v1/gb/training/exporters` | `{"exporters": [{"name": "...", "description": "..."}]}`: the registered exporters, sorted by name (empty if none). The admin UI's *Exporters* block shows them. |
| `GET /v1/gb/training/models/{id}/exports[?limit=]` | `{"exports": [...]}`: the stored exports of the model, newest first, failed ones too (default 100, at most 500). |

```
$ curl -X POST localhost:7878/v1/gb/training/models/3/exports \
       -d '{"exporter": "bucket", "options": {"prefix": "release"}}'
{"id":7,"trained_model_id":3,"exporter":"bucket","options":{"prefix":"release"},"status":"completed",
 "dir":"/srv/gligabao/bin/trained_models/fc4a...","ref":"models/fc4a.../export-7","details":{"files":3},
 "error":"","duration_ms":41873,"created_at":"...","completed_at":"..."}
```

| Status | When |
|---|---|
| `200` | the export completed |
| `400` | invalid JSON body; no exporter registered; `exporter` missing or unknown; options too large or not a JSON object; a job ID unusable as a directory name |
| `404` | unknown model |
| `500` | the export could not be recorded or read back |
| `502` | fetching the files failed, or the exporter returned an error or panicked; the failed export is stored (`error`) and has an ID, listed by the `GET` endpoint |
| `503` | there is no active sidecar |

## Admin UI

In the admin UI's *Training* view, the action box of a trained model (click its row) has an **Export** button, after *Audit*: it opens a dialog listing the registered exporters with their descriptions, each with its own *Export* button; the one pressed calls `POST .../exports` with that exporter. While it runs, its row shows the progress messages the exporter reported through `Update` (read every 1.5 s from `GET .../exports`), the same for every exporter; when it ends the row shows the outcome (status, `ref`, duration, or the error) and keeps the messages it received. The *Exporters* block at the bottom of the view lists the registered exporters and their descriptions (from `GET /v1/gb/training/exporters`); with none registered, *Export* is disabled.

## System events

Each export that reaches the fetch emits a system event to the [`SystemEvent`](hooks.md#system-events) hook:

| Event type | Fields |
|---|---|
| `training.model_export.completed` | `model_id`, `export_id`, `exporter`, `duration_ms` (`int64`) |
| `training.model_export.failed` | the same, and `error` |

## In the code

`internal/training/export.go` (the whole flow), `internal/training/db/` (`RecordTrainedModelExport`, `FinishTrainedModelExport`, `GetTrainedModelExport`, `ListTrainedModelExports`; the table for SQLite and PostgreSQL), `internal/api/training/models.go` (`ExportOf`, `ListExportsOf`), `internal/sys/sidecar/` (`Client.DownloadModelFile`), `gligabao.go` (the public types and `ServeOptions`).

- `training.ExportModel(ctx, db, fetcher, exporters, dir, model, ExportRequest{Exporter, Options})` runs steps 2 to 7 and returns the stored export. Its errors are `*training.Error` with a kind that the handler maps to a status (invalid: 400, upstream: 502, internal: 500).
- `training.FetchModelFiles(ctx, fetcher, model, dir)` is the fetch alone; `fetcher` is a `training.ModelFileFetcher`, one method, `DownloadModelFile(ctx, modelPath, file, w)`, which `*sidecar.Client` implements. `training.ModelDir(root, model)` gives the directory of a model.
- `training.ResolveExporters(list)` is the startup validation.

## Limits and not included

- Exporters are code in the embedding program; none is built in. There is no endpoint to register one at run time.
- An export is one synchronous request. There is no background export, no cancellation besides the client leaving, and no resuming.
- gligabao does not merge LoRA adapters into the base model, and does not convert formats: the exporter gets the files as they were saved (for the LoRA fine-tuning: `adapter_config.json`, `adapter_model.safetensors` and the tokenizer files, not the base model; `adapter_config.json` names the base model).
- Exports are never deleted, and neither are the fetched files.
