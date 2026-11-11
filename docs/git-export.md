# GET /v1/gb/datasets/{name}/git

Exports a [dataset](datasets.md) as a **git repository**: its history becomes the history of the repository. Every [`remember`](remember/remember.md) call is one commit, dated when the call was made, that adds the text that was remembered and the entities and facts it yielded, and edits the files of the facts it confirmed, ended or invalidated. `git log` shows how the knowledge grew; `git log -p edges/` is the history of the facts. It reads the databases only and makes no LLM call, so it works even when no LLM is configured.

The repository is generated, read-only, and **deterministic**: the same dataset gives the same commit IDs. It is not imported back.

## Request

```
GET /v1/gb/datasets/main/git
gb-user-id: u1
```

`{name}` is the dataset, `gb-user-id` its owner (see [identity.md](identity.md)). Only the owner's live dataset can be exported.

The response is `200` with `content-type: application/gzip`: a `tar.gz` with one **bare** repository in the directory `<name>.git/`. Errors are `{"error": "..."}`: `400` for a missing or invalid user, `404` if the user has no live dataset with that name, `500` for a database error. The archive is streamed after the data is read; a failure while writing it cuts the download short (a corrupt gzip) and is logged.

```
curl -H 'gb-user-id: u1' http://localhost:7878/v1/gb/datasets/main/git | tar xz
git clone main.git main && cd main && git log --stat
```

From the CLI: `gligabao dataset git NAME [DIR]` downloads it and unpacks the bare repository into `DIR` (default `NAME.git`, which must not exist or be empty); then `git clone DIR OUT`.

## What a commit is

- **The root commit** adds `README.md` (the dataset's name, description and tags, and the layout below). It is dated at the dataset's creation, or at the first call if that is earlier.
- **One commit per remember call**, oldest first (by `created_at`, then ID). A call is a root artifact (one that is nobody's child) together with its descendants: the chunks and chunk summaries of a [long text](chunking.md), the web searches and the series observations made for it. The author and committer date is the artifact's `created_at`, i.e. **system time**: when gligabao learned the text, not when what it says was true (`valid_at`, which is in the files and in the message). Author and committer are `gligabao <gligabao@localhost>`.
- **The message** is `remember #<artifact id>: <first line of the text>`, then a summary (`+2 entities, +3 edges, 1 facts confirmed, 1 facts changed`), a line for each fact that was ended, invalidated or superseded (`- invalidated edge 1: Alice works_at Acme`), and trailers `Artifact-Id`, `Kind`, `Session-Id` (if any), `Valid-At` and `Artifacts` (how many artifacts the call stored, if more than one).
- **Query runs and failed remember runs are commits of their own**, dated when the run started, in time order among the calls: `query #<run id>: <question>` (it adds `runs/query/<id>.json` and changes nothing else), and `remember run #<id> failed: <error>` for a call that stored no artifact (it failed, was stopped, or its artifact was deleted). See [Run logs](#run-logs).
- **A last commit**, only if needed, adds entities and facts that have no source artifact any more (it was deleted), in their final state.

## Layout

| Path | Content |
|---|---|
| `artifacts/<id>.md` | The text of a root artifact: front matter (`id`, `kind`, `session`, `classes`, `valid_at`, `created_at`) and the text. Its chunks, chunk summaries, web searches and observations are in `artifacts/<id>/<child id>-chunk-<n>.md`, `-summary.md`, `-web-search.md`, `-series-observation.md`, with `parent`, and for a chunk `chunk_index`, `start_offset`, `end_offset`, `tokens`. |
| `entities/<type>/<name>-<id>.md` | An entity: `id`, `name`, `type`, `model`, `artifacts` (the artifacts that mention it so far), and its description as the body. |
| `runs/remember/<id>.json`, `runs/query/<id>.json` | The log of a remember call or of a query: see [Run logs](#run-logs). |
| `edges/<relation>/<id>.md` | A fact: `source`, `relation`, `target` (and their IDs), `valid_at`, `invalid_at`, `created_at`, `expired_at`, `model`, `artifacts` and the description as the body. |

Files have a YAML front matter between `---` lines; strings are double-quoted. File names are lowercase slugs (letters, digits, `_`, `-`; at most 40 characters) plus the ID, so they never collide. A text that does not end in a newline gets one.

How the dataset's changes become changes of files, replaying the [edge events](remember/remember.md#schema) of each call:

| Event | In the commit |
|---|---|
| a new fact (`created`) | adds `edges/<relation>/<id>.md` (with `invalid_at` if the text said it already ended) |
| a stored fact stated again (`confirmed`) | adds the artifact to the fact's `artifacts` |
| a stored fact that the text ended (`ended`) | adds `invalid_at` |
| a stored fact contradicted by a new one (`invalidated`) | adds `invalid_at` and `expired_at` |
| the new fact is older than a stored one (`superseded`) | the new fact's file has `invalid_at` and `expired_at` |
| an entity mentioned again | adds the artifact to the entity's `artifacts` |

## Graph classes

The classes an LLM gave to the graph of an artifact by the `classify_graph` stage (see [remember/remember.md](remember/remember.md#graph-classes)) are the `classes` of that artifact's front matter: a sorted list, once each (`classes: ["career_change", "hr"]`). For a [chunked](chunking.md) text they are on the chunk files, as the classes are of the chunk's graph. The classes of a call are in the call's commit.

## Run logs

- **A remember run** (`gb_remember_runs`) is `runs/remember/<run id>.json`, in the commit of the artifact it stored (the commit message has a `Remember-Run-Id` trailer). It has `kind`, `status`, `error`, `params` (how the call was made: the reference time, the session, the models and reasoning per stage, ...), `artifact_id`, `started_at`, `finished_at`, `stages` and `outputs`. `stages` lists, in pipeline order, every stage that started (a stage that never did is left out) with its `status`, `error`, times and `inferences`: the model calls it made (`model`, `provider`, `reasoning`, `input_tokens`, `output_tokens`, `cost_usd`). `outputs` are the stage outputs the run kept for debugging (what the steps produced: the extracted entities, the candidate facts, ...), as stored; they are only there if they were not garbage collected.
- **A query run** (`gb_query_runs`) is `runs/query/<run id>.json`, with the `question`, `session_id`, `status`, `error`, `params`, the times and the `stages` as above. A query does not change the knowledge, so its commit only adds this file. The answer is not part of the log (the database does not keep it).
- A remember run that stored no artifact into the dataset is a commit of its own with its file.
- Runs are recorded with the name of the dataset, not its ID. The runs of an earlier dataset of the user that had the same name and was deleted are left out (only runs that started after that deletion are taken), and so are those of other users.
- The log of a run is the one in the database when the export is made: a run that is still going shows the stages it has reached so far.

## Limits

- **Entity history is approximate.** An entity has only its latest description (`gb_entities` keeps no history), so its file appears in the commit of the first call that mentions it, already with the latest description, and never changes except for `artifacts`. Facts are exact. Exact entities would need an entity-event log (a schema change).
- **It needs `gb_edge_events`.** The facts' history is replayed from them. A fact with no events (a database made before they existed) is shown as it was created, never ended; there is no fallback that rebuilds the events from the facts' `invalid_at` and `expired_at`.
- **Events of a deleted artifact are lost**, as the log keeps no artifact for them; a fact whose artifact was deleted goes to the last commit.
- **Commit dates are system time.** A text about 2019 remembered today is a commit of today. Several calls with the same `created_at` are ordered by artifact ID.
- **Not exported**: embeddings, timeseries, geo data, images (and their extractions), the sessions as such (each artifact names its own) and the usage counters (`gb_graph_in_ctx_counters`). The README of the repository says so.
- **Run logs are the logs of the end of the call**, not of the commit's time: a run's stage outputs may have been garbage collected, and the `runs/` files can be big (the stage outputs hold what the LLM extracted). A run log can be recorded after the artifact (the run ends after it is stored); its commit still is the artifact's.
- **Classes have no time of their own.** A class is in the commit of its artifact, even if it was stored a moment later (it belongs to the same call).
- **Privacy.** The repository contains every text of the dataset: it is as sensitive as the dataset.
- **Scale.** The commits are written one after the other, with only the directories that changed rewritten; the whole archive is built from loose objects, so for a very large dataset run `git gc` after cloning. The data of the dataset (artifacts, graph) is read into memory first, as in the [graph export](graph.md), which is not paginated either.

## Code

`internal/memory/memory_gitexport` (the package writes git's loose objects itself: blobs, trees and commits, no git binary or library; `Write` takes an `Input` and a `Sink`, `DirSink` or `TarGzSink`), `memory.LoadGitExport` (reads a user's dataset into the `Input`), `internal/api/datasets` (`Git`, the handler) and `cli` (`dataset git`).
