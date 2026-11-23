# GCNII* training and serving on a cluster of gligabao instances

This page describes how the GCNII* models (sargo, see [gcnii.md](gcnii.md)) are trained, stored and served when **several gligabao instances run side by side** behind a load balancer, and how the design keeps them consistent. For the single-instance behaviour and the endpoints see [gcnii.md](gcnii.md); this page is about what changes when a request can land on any instance.

## The problem

gligabao starts one **sargo** child process per instance. Sargo trains in memory on a background thread and keeps each user's model as files in a local directory (`<sargo_gcnii_dir>/<hex of user id>/model.weights`, `classes.json`). Nothing is shared between instances except the **database** and the **S3 bucket**. Without more, a cluster would break in five ways:

| Failure | What would happen |
| --- | --- |
| Status from another instance | `GET /v1/gb/model/train` lands on instance B, whose sargo never trained anything: "not trained", no job. |
| Concurrent trainings | Two `POST`s of the same user land on A and B: both train, and the user's model is whichever finishes last. |
| Lost disk | The Nomad container is replaced: its local directory, and the only copy of every model, are gone. |
| Stale model | A trains a new model; B keeps serving the old one from its own directory and memory. |
| Crash while training | Sargo's thread dies with the process; nobody knows, and the job looks "running" forever. |

## The design in one picture

```
        load balancer ──► any instance (A, B, ...)
                               │ handlers: internal/api/model (Recorder)
                               ▼
   ┌─────────────── shared ───────────────┐          ┌──────── per instance ────────┐
   │ database                             │          │ sargo (in memory + files)    │
   │   gb_model_train_jobs   who trains,  │◄─claim───│ <sargo_gcnii_dir>/<user>/    │
   │                         heartbeat    │◄─beat────│    model.weights             │
   │   gb_trained_models     every model  │◄─record──│    classes.json              │
   │                                      │          │    model_id   (marker)       │
   │ S3 (or a shared local dir)           │          └──────────────────────────────┘
   │   model_weights/gcnii/<user>/<job>/  │◄─ save ──  (only the instance that trained)
   │      model.weights, classes.json     │── sync ──► (any instance, before use)
   └──────────────────────────────────────┘
```

Three rules make it consistent:

1. **The database is the source of truth for what exists and who is doing what.** The jobs table and the models table decide; sargo's memory and directory are a cache and a working copy.
2. **S3 holds the weights; every key is written once.** Nothing is ever overwritten, so a reader never sees half a model or a mix of two.
3. **A model is used only after the instance has made its directory match the latest row.** Matching is checked, not assumed.

## The pieces

### `gb_model_train_jobs`: the training in the database

One row per training (`internal/training/db`, schema in `sqlite/sql/schema.sql`):

| Column | Meaning |
| --- | --- |
| `job_id` | the ID of the model the job will make (`gcnii_<unix start>_<8 hex>`), chosen when the job starts; also its S3 directory |
| `model_type`, `user_id` | what and for whom |
| `instance_id` | the gligabao process (random per start) that runs the job: the only one whose sargo has the weights |
| `status` | `running`, `done`, `failed`, `lost` |
| `job` | JSON, the last report of sargo (epochs, classes, accuracy, loss, ...) |
| `info` | JSON, what the request knew: dataset, history run, settings |
| `model_id` | the `gb_trained_models` row of a `done` job |
| `started_at`, `heartbeat_at`, `finished_at` | times |

A **partial unique index** `(model_type, user_id) WHERE status = 'running'` allows at most one running row per user and type in the whole cluster. This is the only lock; it needs no coordinator and works on SQLite and PostgreSQL.

### `gb_trained_models`: the model in the database

The same table the LoRA models use, with `model_type = 'gcnii'`, `user_id`, `storage` (`s3` or `local`), `path` (the directory in the store), and `files` with the size and sha256 of each file. It is the record the sync checks the weights against. Training again never replaces a row.

### S3 (the weights store)

`<prefix>/<model_type>/<user>/<job id>/{model.weights,classes.json}`, in the meta bucket (default prefix `model_weights`). The job ID is unique per training, so a key is never written twice. Without a bucket the same layout goes under `model_weights_local_dir`; that works for one instance (or a shared mount) but not for a real cluster.

### The marker file

`<sargo dir>/<user>/model_id` holds the `gb_trained_models.id` of the model whose files are in that directory. It is how an instance knows whether its copy is current. It is written after the weights (on the training instance, when it records; on any instance, when it restores).

### Sargo reloading

Sargo caches the loaded model together with the modification time of its weights file, and `get` reloads the file when the time differs (`rust/sargo/src/gcnii/users.rs`). This lets a sync that replaces the file take effect at once, without a sargo restart.

## The life of a training

```
client ─POST /v1/gb/model/train(/dataset)─► instance A
  1. CLAIM    INSERT gb_model_train_jobs (running, instance A)       ── 409 if the user already has a running row anywhere
  2. START    A's sargo: POST /v1/gcnii/train                        ── on refusal the row is ended `failed`, the user may retry
  3. 202      answer to the client; the Recorder's watcher starts on A
  4. WATCH    every ~2 s: read A's sargo status; heartbeat the row (and store sargo's report)
  5. DONE     sargo reports `done`:
                a. copy model.weights + classes.json to S3 under the job id        (sha256 of each)
                b. INSERT gb_trained_models (model_type gcnii, files, params, scores)
                c. write the `model_id` marker in A's directory
                d. UPDATE the job row: done, model_id
```

The order in step 5 is deliberate: the job stays `running` until the marker is written. Every reader treats "running" as "not finished", so nobody sees a model that is not yet stored and recorded. A `failed` report ends the row `failed` with sargo's error; a failure to store the weights ends it `failed` too (the model is still in A's directory).

## Reading from any instance

### Status

`GET /v1/gb/model/train` returns the **newest job of the user as the database has it**, so every instance gives the same answer. Status mapping: `running`, `done`, and `failed` (a `lost` job is `failed`, `error` says the instance stopped). A job that exists only in the local sargo and not in the table (a training from before the table existed, or the feature off) is shown from sargo; if the local one is newer than the row, the local one wins.

### Inference and embedding: the sync

Before `/infer`, `/embed` and the status, the instance runs `Recorder.Sync(user)`:

1. If the user's newest job is `running` **on this instance**, do nothing (see "Races" below).
2. Read the latest `gb_trained_models` row of the user. None: do nothing (sargo answers as before, `404` if it has no model).
3. If this instance's marker equals the row's ID: do nothing.
4. Otherwise **restore**: fetch each file from the store, check its size and sha256 against the row, write it to a temporary name and rename it into place (`classes.json` first, the weights last), then write the marker. A file that does not match the record aborts the restore and nothing is left half-written.

Then the request goes to the local sargo, which reloads the weights if the file changed. The cost of the check is two small indexed queries per request.

This one mechanism covers both a **new model from another instance** (the marker is older than the row) and a **lost disk** (no marker at all).

### Listing

`GET /v1/gb/model/models?model_type=gcnii` lists the user's rows from the database; it never touches a directory.

## Failure handling

| Event | Outcome |
| --- | --- |
| Second `POST` while a job runs (any instance) | `409`; the unique index decides, so two instances never both win. |
| Sargo refuses the request after the claim | The row is ended `failed`; the user can retry at once. |
| The instance running a job crashes or is replaced | Its heartbeat stops. Once the last beat is older than `beatStale` (90 s), the next status request or claim of that user marks the row `lost` (shown as `failed`), which frees the user to train again. The model, if sargo had not finished, is gone: sargo trains in memory. |
| Sargo is unreachable for a while | The watcher cannot read status, so it does not beat; the job is declared lost after 90 s. |
| A "lost" job is in fact still alive (a long pause) | Its owner can still end the row (`done` or `failed`); ending applies to `running` and `lost` rows, and its model is recorded normally. |
| The bucket is not writable | The job ends `failed` with the reason; nothing is recorded; the model stays in the instance's directory. |
| A stored file is damaged | The restore refuses it (size or sha256 mismatch); that instance keeps what it had. |
| Old instance (no table / marker) | A directory with no marker is treated as "unknown" and is replaced by the latest recorded model on first use; a user with no recorded model is untouched. |

## Races, and why they are safe

- **Two claims at once.** Both `INSERT`s race on the partial unique index; one wins, the other gets zero rows and a `409`. The stale-job sweep and the insert are separate statements, but the index alone guarantees the single running row.
- **Sync while training.** Sargo writes the new weights to the directory when it finishes, before the model is recorded. If a sync on that instance then ran, it would find the marker older than the latest row and restore the previous model over the new one (and the recorder would then store the wrong files). The rule "do nothing while this instance's job is `running`" closes this, and the job stays `running` until after the marker is written. Other instances are not affected: they only read S3 and the rows.
- **Partial restore.** Files are renamed into place one at a time, the weights last, and the marker after: a crash in between leaves a marker that does not match the latest row, so the next request restores again.
- **Concurrent restores on one instance.** Two requests may both restore; the writes are atomic renames of identical content, and the second finds the marker current.
- **Instance restart.** Memory is lost, the directory is not. The marker is on disk, so the restarted instance keeps its model and only restores when the database says it is behind. A running job of the old process is simply `lost` after 90 s.
- **A model recorded twice.** `gb_trained_models.job_id` is unique and the insert ignores a duplicate, so a repeated record makes no second row.
- **Clock skew.** Staleness compares the heartbeat with the clock of the instance that looks; with the 90 s margin and beats every 2 s, ordinary skew is harmless.

## What is *not* guaranteed

- **Training is not resumable and not highly available.** A job is the memory of one sargo; its loss is detected, not prevented. The user retrains.
- **Reads are consistent, not instantaneous.** An instance learns of a new model on the next request that reaches it. A request being served while another instance finishes a training still answers with the model it started with.
- **`/infer` always uses the latest model of the user.** Choosing an older row needs a per-model directory in sargo (see `leftover_tasks/2026-11-23-gcnii-weights-follow-ups.md`).
- **A local weights directory does not scale out.** Without a bucket the "store" is a directory of the instance; use S3 for more than one instance.
- **The 90 s staleness limit is a constant** (`beatStale` in `internal/api/model/weights.go`), not a setting.

## Operating it

- **Config** (`build/gligabao.toml`, or `MODEL_WEIGHTS_S3_BUCKET` / `_REGION` / `_PREFIX` / `_ENDPOINT`): `model_weights_s3_bucket` (normally the meta bucket), `model_weights_s3_prefix` (default `model_weights`), `model_weights_local_dir` (no bucket). `sargo_gcnii_dir` stays per instance and may be ephemeral.
- **Permissions:** the instance role needs `s3:PutObject` and `s3:GetObject` on `<bucket>/model_weights/*` and `s3:ListBucket` on the bucket (the `gligabao_int_meta` policy in `gloflow_emergence/ops/tf_main/gf_apps_core/gligabao_meta.tf`). Apply it before deploying the binary, or the uploads fail and every training ends `failed`.
- **Database:** every instance must use the same database (SQLite is for one instance only; PostgreSQL for a cluster). The new table and columns are created at start (`CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`).
- **Looking at it:** `SELECT * FROM gb_model_train_jobs ORDER BY id DESC` shows who trains what and when it last beat; `status = 'lost'` rows are crashed instances. Weights are in `s3://<bucket>/model_weights/gcnii/<user>/<job id>/`.
- **Cleaning up:** nothing is deleted by gligabao. Remove old job directories from the bucket by hand; their `gb_trained_models` rows stay (the sync restores from the latest row, so keep that one).

## Where it is in the code

| What | Where |
| --- | --- |
| Claim, watch, beat, record, sync, status merge | `internal/api/model/weights.go` (`Recorder`) |
| Handlers that call them | `internal/api/model/model.go`, `dataset.go` |
| Jobs table (SQL, SQLite and PostgreSQL) | `internal/training/db/{sqlite,postgres}/db_model_train_job.go`, `sql/schema.sql` |
| Models table and its new columns | `internal/training/db/db_trained_model.go`, `model/trained_model.go` |
| Weights store, restore, marker, job IDs | `internal/modeltrain/weights.go` |
| Sargo reload on file change | `rust/sargo/src/gcnii/users.rs` (`weights_mtime`, `get`) |
| Tests of the cluster behaviour | `internal/api/model/t_weights_test.go` (two instances sharing a database and a store), `internal/training/db/t_db_model_train_job_test.go` |
