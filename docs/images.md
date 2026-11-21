# Images (the image pipeline)

`POST /v1/gb/remember` also remembers images. An image goes through a pipeline of its own, separate from the text pipeline ([remember/remember.md](remember/remember.md)): a vision LLM reads the text in the image (OCR: its title and text blocks) and classifies the whole image by a theme (`plot`, `graph`, ...). The image file is kept on the file system or in an S3 bucket, tracked by an **image artifact** row (metadata and the file's path, never the bytes), and what the model read is stored as an **image extraction**, in their own tables.

The model also describes what the image shows and how its parts relate (the links of a graph or diagram, what a plot or chart says). That **description** is then remembered as a text artifact (kind `knowledge`) by the text pipeline, so the graph of the image (entities, edges, vectors, sessions) is extracted and stored like that of any text, and `query` sees it. See [The graph of an image](#the-graph-of-an-image).

Code: `internal/memory/memory_image/image.go` (`RememberImage`, `ExtractImage`, `ResolveImage`, `LoadImage`, `MaxImageBytes`), `internal/memory/memory_image/image_prompts.go` (the prompt), `internal/api/remember/remember.go` (the handler `rememberImage`), `internal/db/imagestore/` (the files: `fs.go`, `s3.go`), `internal/db/graph/{sqlite,postgres}/db_image.go` (SQL), `internal/llm/ai.go` (the `Image` type sent to the model), `internal/api/admin/admin.go` (the admin statistics) and `admin_ui/src/{image,imagestats}.ts` (the admin UI).

## Request

```
POST /v1/gb/remember
content-type: application/json
gb-user-id: u1

{"dataset": "charts", "image": "<base64>", "reference_time": "2025-01-31T09:00:00Z"}
```

- `image`: the image, base64-encoded (standard alphabet): a JPEG, PNG, GIF or WebP, at most 5 MiB (`memory_image.MaxImageBytes`, the Anthropic API limit) before encoding. The type is detected from the bytes. A request with `image` is an image request; the body may be up to about 7 MiB (instead of 1 MiB for text).
- `dataset` (optional, default `main`, created if missing) and `reference_time` (optional, RFC 3339, default now: when the image was captured or observed) work as for text.
- `session_id` (optional) works as for text ([sessions.md](sessions.md)): it is stored on the new image artifact (`gb_image_artifacts.session_id`) and echoed as `session_id` in the response. Images add no entities or edges, so there are no session links beyond it. `400` if it is too long or has control characters.
- `models` (optional): `{"extract_image": "<model>"}` picks the vision model that reads the image; without it the default model is used (see [remember/remember.md](remember/remember.md#choosing-the-model-of-each-stage)). A repeated image is answered from its stored extraction without an LLM call, so the model reported is the one that made it.
- `stream` (optional, default `false`): answer with a Server-Sent Events stream of the steps of the pipeline instead of one JSON body: see [Streaming progress](#streaming-progress).
- `text` and `kind` must not be given together with `image` (`400`): an image is not a text artifact.

From the CLI, `gligabao remember --image FILE` sends the file the same way (see [cli.md](cli.md)).

## Response

`200 OK`:

```json
{
  "user_id": "u1",
  "dataset": "charts",
  "pipeline": "image",
  "image_artifact_id": 1,
  "session_id": "chat-42",
  "extraction_id": 1,
  "media_type": "image/png",
  "sha256": "9f86d081...",
  "size_bytes": 20481,
  "path": "3/9f86d081....png",
  "cached": false,
  "valid_at": "2025-01-31T09:00:00Z",
  "created_at": "2025-01-31T09:00:05Z",
  "theme": "plot",
  "title": "Revenue by year",
  "description": "The plot shows revenue by year for the EU.",
  "artifact_id": 7,
  "entities": [{"name": "EU", "type": "region"}],
  "edges": [],
  "text": "Revenue by year\nYear\nEU",
  "text_blocks": [
    {"kind": "title", "text": "Revenue by year"},
    {"kind": "axis", "text": "Year"},
    {"kind": "legend", "text": "EU"}
  ],
  "provider": "anthropic",
  "model": "claude-haiku-5-5",
  "input_tokens": 1500,
  "output_tokens": 80,
  "cost_usd": 0.00019
}
```

`path` is where the file is, relative to the [image directory](#where-the-files-are). `cached` is `false` here and `true` for an [already processed image](#repeated-images). `pipeline` is always `image`; it tells this response from the text one. `text_blocks` is always an array. In an eval run (the eval `remember` call) the response also carries the call's ATIF trajectory, as for text; the trajectory notes the image (`[image: image/png, 20481 bytes]`) instead of embedding it.

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body; `image` is not base64, is empty, larger than 5 MiB, or not a JPEG, PNG, GIF or WebP; `text` or `kind` given with it; `reference_time` is not RFC 3339; or `session_id` is invalid |
| 502 | The LLM call failed, or its output could not be parsed or was truncated (nothing stored) |
| 503 | LLM not configured (`ANTHROPIC_API_KEY` is not set) |

## Streaming progress

With `"stream": true` the response is `200` with `content-type: text/event-stream` and the call can be watched while it runs. Request errors (`400`, as above) are still a plain JSON response, sent before the stream starts. The stream sends:

- `progress` events: one step of the pipeline, `{"step", "status", "message", "data", "at"}`. `status` is `started`, `succeeded`, `failed`, `skipped` or `info`; `message` and `data` are optional.
- one `result` event, with the response documented above, when the call succeeds.
- one `error` event, `{"error": "..."}`, if the pipeline failed after the stream started (the HTTP status is already `200` then). The stream closes after `result` or `error`.

The comment line `: connected` is sent first. The steps are, in order: `received_image` (`info`: media type and size), then either `find_existing_image` (the image is [repeated](#repeated-images)) or `extract_image` (`started`; `succeeded` with `theme`, `title`, `text_blocks`, `model` and the tokens), `image_store`, and `image_graph` (only if the image has a [description](#the-graph-of-an-image)). The stages of the text pipeline that builds the graph (`extract_graph`, `resolve_edges`, `store`, ... see [remember.md](remember/remember.md#how-the-pipeline-is-declared)) are reported too, between `started` and `succeeded` of `image_graph`, as `started`, `succeeded`, `failed` or `skipped` events named after the stage; they come from the [run log](remember/remember.md#run-log).

Progress is not replayed: a client that reconnects starts a new call (the same image is then answered from its stored extraction), so close the connection when the stream ends (a browser `EventSource` otherwise reconnects by itself). From Go: `memory_core.WithProgress(func(memory_core.ProgressEvent))` is an option of `memory_image.RememberImage`; it may be called from several goroutines.

## Repeated images

The SHA-256 of the decoded image is compared with the image artifacts the dataset already holds. If one has the same hash, the image is not read again: `remember` returns the stored result of the earlier call (its newest extraction) with `"cached": true`, makes no LLM call and stores nothing new (no artifact, extraction, text block or `gb_inferences` row). In the response, `image_artifact_id`, `session_id`, `extraction_id`, `path`, `valid_at` and `created_at` are those of the stored artifact (so `session_id` is the session that first stored the image, not necessarily the one of this request; the repeat leaves no trace of its own session) (a `reference_time` in the repeated request is ignored), and `model`, `provider`, `input_tokens`, `output_tokens` and `cost_usd` describe the call that made the extraction, which was spent then, not now. If the image file has gone missing from the image directory, it is written again from the uploaded bytes.

Only the same dataset of the same user is searched: datasets are isolated, so the same image in another dataset (or another user's) is read and stored again. The comparison is on the bytes: a re-encoded or resized copy is a different image. Two identical requests that arrive at the same moment may both miss and both be read.

In an eval trajectory a repeat is a single `find_existing_image` step instead of the LLM and store steps.

## What the model returns

One LLM call (`Complete` with the image as an image content block, max 8192 output tokens) with the default model, `claude-haiku-5-5` (or the `extract_image` model of the request's `models`), whose price is in the built-in price table. It is asked for only JSON:

```json
{"theme": "...", "title": "...", "description": "...", "text_blocks": [{"kind": "...", "text": "..."}]}
```

Code fences or prose around the JSON are tolerated. Then it is normalized:

- `theme` is lowercased and must be one of `plot` (a data plot with axes), `chart` (pie, donut, ...), `graph` (nodes and links), `diagram`, `table`, `document`, `screenshot`, `slide`, `map`, `photo`, `illustration` or `other`; anything else becomes `other`.
- `title` is the image's own title, `""` if it has none. If the model gave none but returned a block of kind `title`, that block's text is used.
- `text_blocks` are the text regions in reading order, each with a `kind`: `title`, `subtitle`, `heading`, `paragraph`, `caption`, `label` (a drawn element's text), `axis`, `legend`, `annotation`, `table` (a row, cells joined by ` | `) or `other` (the fallback for an unknown kind). Blank blocks are dropped.
- `description` is plain prose with the facts the image shows: every entity named as labelled in the image and every relationship between them, one sentence each (every link of a graph or diagram; what is measured, the series and the main values of a plot or chart; what the rows of a table say). `""` if there is nothing to relate (a photo of a scene, an abstract illustration). It is trimmed; it is stored in `gb_image_graphs`.
- `text` is not asked of the model: it is the blocks joined by newlines.

The call is recorded in `gb_inferences` with type `remember_image` (stage `extract_image`, with the reasoning level it ran with: low unless the request sets `reasoning.extract_image`) (the text pipeline's calls are `remember`), even if a later step fails.

## The graph of an image

When the extraction has a `description`, `RememberImage` calls the text pipeline (`memory_remember.Remember`, kind `knowledge`) with the text `Image (<theme>): <title>` followed by the description, with the same options as the call (session, models, reasoning, vectors, ...; the `extract_graph` model of `models` picks the model of the graph step). The text pipeline extracts the entities and edges, reconciles them with the dataset's graph, stores them and embeds them, exactly as for a text, and the artifact it creates (a row of `gb_artifacts`) owns them: `gb_entity_artifacts` and `gb_edge_artifacts` link them to it, so `GET /v1/gb/datasets/{name}/artifacts/{id}/graph` returns the graph of the image by its `artifact_id`. `gb_image_graphs` links the extraction to that artifact.

The response of a new image carries `artifact_id`, `description` and the `entities` and `edges` just extracted (the same shape as for a text). Their LLM calls are recorded as the text pipeline's (`remember`, stages `extract_graph`, ...). A repeated image ([Repeated images](#repeated-images)) returns `artifact_id` and `description` but no `entities` or `edges` (read the artifact graph). If the graph step fails after the image was stored (`502`), the image and its extraction stay stored and a repeat of the request retries only the graph step. An image without a description has no graph step and no `artifact_id`. The graph is not classified (`classify_graph` does not apply to an image), and `web_search`, `timeseries` and `graph_index` still do not apply.

## Where the files are

Image bytes are never stored in the database. They are files in the image directory, `<db path without extension>.images` next to the SQLite file (e.g. `build/gligabao.images`, or `/data/gligabao.images` in the container). Override it with `serve --image-dir <path>`, or `ServeOptions.ImageDir` when embedding; the directory is created when the first image is stored. In Go, `memory_image.ImageDir` is the default and `memory_image.WithImageDir(dir)` overrides it for one call.

Instead of a directory the files can go to an **Amazon S3 bucket**: set `image_store = "s3"` and `image_s3_bucket` in the config file (optional: `image_s3_region`, `image_s3_prefix`, the "subdirectory" of the bucket so deployments can share one, and `image_s3_endpoint` for S3-compatible servers such as MinIO or LocalStack). The environment variables `IMAGE_STORE`, `IMAGE_S3_BUCKET`, `IMAGE_S3_REGION`, `IMAGE_S3_PREFIX` and `IMAGE_S3_ENDPOINT` set the same (the file wins). The bucket must exist; credentials come from the standard AWS chain, never from the config file. In S3 every user has a directory of their own: the objects are `<prefix>/<user id>/<dataset id>/<sha256>.<ext>` (the user id is URL-encoded into one path segment). `path` in the database is the part after the user, the same relative path as in the file system store, so switching the store only needs the files to be copied (S3: into the user's directory). **Access control:** every store operation names its owner (the user of the request) and the S3 store builds the key itself from the owner and a cleaned path, so a user can only read, write or delete inside their own directory (`..`, absolute paths and ids with `/` cannot reach another one). Listing all users' images is for operators only (admin statistics, export). The application enforces this; to enforce it in AWS too, limit the credentials with an IAM policy on the bucket prefix. `--image-dir` is not used with S3. The admin image statistics, the database export and the research import use the configured store too.

All code reads and writes images through the `imagestore.Store` interface (`internal/db/imagestore`: `Put`, `Read` and `Remove`, each with the owner user id, and `List`; implementations `FS` and `S3`), kept in `db.DB.ImageStore`. In Go, `memory_image.WithImageStore(store)` sets it for one call.

A file is `<dataset id>/<sha256>.<ext>` (`jpg`, `png`, `gif` or `webp`) inside it, and `gb_image_artifacts.path` holds that relative path, so the directory can be moved. The name is the SHA-256 of the content, which is also how a repeated image is recognised (see [Repeated images](#repeated-images)). The file is written first (atomically, via a temporary file and a rename) and the rows after; if storing the rows fails, a file that this call created is removed again. Only a crash between the two steps can leave a file that no row tracks. With PostgreSQL the files still go to the local directory, so the directory must be on storage the server keeps (a volume, in the container).

`memory_image.LoadImage(ctx, db, userID, dataset, imageArtifactID)` reads a stored image back.

## Schema

Created on startup in the same database as the other tables (`internal/db/graph/{sqlite,postgres}/sql/schema.sql`).

| Table | Columns |
| ----- | ------- |
| `gb_image_artifacts` | `id`, `dataset_id` → `gb_datasets`, `session_id` (NULL if none), `media_type`, `sha256`, `size_bytes`, `path` (the file, relative to the image directory), `valid_at`, `created_at` |
| `gb_image_extractions` | `id`, `image_artifact_id` → `gb_image_artifacts` (cascade), `provider`, `model`, `theme`, `title`, `text`, `raw_output` (the model's reply as received), `input_tokens`, `output_tokens`, `cost_usd`, `created_at` |
| `gb_image_graphs` | `extraction_id` → `gb_image_extractions` (primary key, cascade), `description`, `artifact_id` → `gb_artifacts` (NULL until the graph is stored; set NULL if the artifact is deleted) |
| `gb_image_text_blocks` | `id`, `extraction_id` → `gb_image_extractions` (cascade), `position` (reading order from 0), `kind`, `text`; unique (`extraction_id`, `position`) |

The image rows (artifact, extraction, text blocks and the `gb_image_graphs` row with the description) of a call are written in one transaction, after the LLM call: if it fails nothing is stored (and a dataset that did not exist is not created). Each new image adds one image artifact (a repeat adds nothing, see above); an artifact can have several extractions (for example from different models) but `remember` makes one. Image artifacts are not in `gb_artifacts`; the text artifact of the description is, and its graph shows up in the [graph export](graph.md).

Go access, in package `graph`: `InsertImage`, `ListImageArtifacts`, `GetImageArtifact`, `ListImageExtractions`, `FindImageBySHA256` (the dedup lookup of [Repeated images](#repeated-images)), `ImageStatistics` (the admin statistics), `GetImageGraph` and `SetImageGraphArtifact`. Each dispatches to the `sqlite` or `postgres` implementation (`db_image.go`, `db_image_graph.go`).

## Admin UI

The admin UI (`admin_ui/src/`) has two image parts:

- **Remember an image** (`image.ts`, `initImageOverlay`): paste an image anywhere on the page and it opens as an overlay. "Remember" posts it to `POST ../remember` (the endpoint above) with the user and dataset last used in the UI, and shows the result next to the image (theme, title, text blocks, cache state, tokens and cost), with a spinner while it waits. Images over `MaxImageBytes` or of another type are refused in the browser.
- **Image statistics** (`imagestats.ts`, `initImageStats`): loads `GET /admin/api/images[?db=<name>]` (`ImagesOf` in `internal/api/admin/admin.go`) and shows the counts and sizes of the image artifacts and extractions, grouped by media type and theme. The response also has `disk`, when the image directory is known: `missing` counts artifacts whose file is not in the directory and `untracked` counts files that no artifact points to (for example left by a crash). It is read from the configured store (the directory, or S3).

## Limitations

- No endpoint reads, lists or deletes image artifacts or extractions yet; use the database.
- The graph is only as good as the description: it is one LLM read of the image, and the entities are extracted from the prose, not from the pixels (no node positions or geometry). Images of a graph with many nodes may exceed the output limit.
- A text artifact made from a description is a normal `knowledge` artifact: nothing in `gb_artifacts` says it came from an image (follow `gb_image_graphs.artifact_id`).
- The files are on the local file system only (no object storage), and nothing deletes them: deleting a dataset (a soft delete) keeps its image rows and files, and there is no endpoint to remove an image.
- The theme is the model's single best guess; there is no confidence value and no bounding boxes for the text blocks.
- Huge images are not resized, and a dense image can exceed the output limit (`502`).
