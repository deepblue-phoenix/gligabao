# Images (the image pipeline)

`POST /v1/gb/remember` also remembers images. An image goes through a pipeline of its own, separate from the text pipeline ([remember.md](remember.md)): a vision LLM reads the text in the image (OCR: its title and text blocks) and classifies the whole image by a theme (`plot`, `graph`, ...). The image file is kept on the file system, tracked by an **image artifact** row (metadata and the file's path, never the bytes), and what the model read is stored as an **image extraction**, in their own tables.

For now nothing is added to the graph: no entities, edges or vectors come out of an image, and `recall` does not see it. Grounding the extractions in the rest of the graph is planned; the extraction tables are the input for it.

Code: `internal/memory/image.go` (`RememberImage`, `ExtractImage`), `internal/memory/prompts_image.go`, `internal/api/remember/remember.go` (the handler), `internal/imagestore` (the files), `internal/db/graph/{sqlite,postgres}/db_image.go` (SQL).

## Request

```
POST /v1/gb/remember
content-type: application/json

{"user_id": "u1", "dataset": "charts", "image": "<base64>", "reference_time": "2025-01-31T09:00:00Z"}
```

- `image`: the image, base64-encoded (standard alphabet): a JPEG, PNG, GIF or WebP, at most 5 MiB (the Anthropic API limit) before encoding. The type is detected from the bytes. A request with `image` is an image request; the body may be up to about 7 MiB (instead of 1 MiB for text).
- `user_id` (required), `dataset` (optional, default `main`, created if missing) and `reference_time` (optional, RFC 3339, default now: when the image was captured or observed) work as for text.
- `text` and `kind` must not be given together with `image` (`400`): an image is not a text artifact.

## Response

`200 OK`:

```json
{
  "user_id": "u1",
  "dataset": "charts",
  "pipeline": "image",
  "image_artifact_id": 1,
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

`path` is where the file is, relative to the [image directory](#where-the-files-are). `cached` is `false` here and `true` for an [already processed image](#repeated-images). `pipeline` is always `image`; it tells this response from the text one. `text_blocks` is always an array. In eval mode (`POST /v1/gb/eval/remember`) the response also carries the call's ATIF trajectory, as for text; the trajectory notes the image (`[image: image/png, 20481 bytes]`) instead of embedding it.

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body; `image` is not base64, is empty, larger than 5 MiB, or not a JPEG, PNG, GIF or WebP; `text` or `kind` given with it; or `reference_time` is not RFC 3339 |
| 502 | The LLM call failed, or its output could not be parsed or was truncated (nothing stored) |
| 503 | LLM not configured (`ANTHROPIC_API_KEY` is not set) |

## Repeated images

The SHA-256 of the decoded image is compared with the image artifacts the dataset already holds. If one has the same hash, the image is not read again: `remember` returns the stored result of the earlier call (its newest extraction) with `"cached": true`, makes no LLM call and stores nothing new (no artifact, extraction, text block or `gb_llm_calls` row). In the response, `image_artifact_id`, `extraction_id`, `path`, `valid_at` and `created_at` are those of the stored artifact (a `reference_time` in the repeated request is ignored), and `model`, `provider`, `input_tokens`, `output_tokens` and `cost_usd` describe the call that made the extraction, which was spent then, not now. If the image file has gone missing from the image directory, it is written again from the uploaded bytes.

Only the same dataset of the same user is searched: datasets are isolated, so the same image in another dataset (or another user's) is read and stored again. The comparison is on the bytes: a re-encoded or resized copy is a different image. Two identical requests that arrive at the same moment may both miss and both be read.

In an eval trajectory a repeat is a single `find_existing_image` step instead of the LLM and store steps.

## What the model returns

One LLM call (`Complete` with the image as an image content block, max 8192 output tokens) with the default model, `claude-haiku-5-5`, whose price is in the built-in price table. It is asked for only JSON:

```json
{"theme": "...", "title": "...", "text_blocks": [{"kind": "...", "text": "..."}]}
```

Code fences or prose around the JSON are tolerated. Then it is normalized:

- `theme` is lowercased and must be one of `plot` (a data plot with axes), `chart` (pie, donut, ...), `graph` (nodes and links), `diagram`, `table`, `document`, `screenshot`, `slide`, `map`, `photo`, `illustration` or `other`; anything else becomes `other`.
- `title` is the image's own title, `""` if it has none. If the model gave none but returned a block of kind `title`, that block's text is used.
- `text_blocks` are the text regions in reading order, each with a `kind`: `title`, `subtitle`, `heading`, `paragraph`, `caption`, `label` (a drawn element's text), `axis`, `legend`, `annotation`, `table` (a row, cells joined by ` | `) or `other` (the fallback for an unknown kind). Blank blocks are dropped.
- `text` is not asked of the model: it is the blocks joined by newlines.

The call is recorded in `gb_llm_calls` with type `remember_image` (the text pipeline's calls are `remember`), even if a later step fails.

## Where the files are

Image bytes are never stored in the database. They are files in the image directory, `<db path without extension>.images` next to the SQLite file (e.g. `bin/gligabao.images`, or `/data/gligabao.images` in the container). Override it with `serve --image-dir <path>`, or `ServeOptions.ImageDir` when embedding; the directory is created when the first image is stored. In Go, `memory.ImageDir` is the default and `memory.WithImageDir(dir)` overrides it for one call.

A file is `<dataset id>/<sha256>.<ext>` (`jpg`, `png`, `gif` or `webp`) inside it, and `gb_image_artifacts.path` holds that relative path, so the directory can be moved. The name is the SHA-256 of the content, which is also how a repeated image is recognised (see [Repeated images](#repeated-images)). The file is written first (atomically, via a temporary file and a rename) and the rows after; if storing the rows fails, a file that this call created is removed again. Only a crash between the two steps can leave a file that no row tracks. With PostgreSQL the files still go to the local directory, so the directory must be on storage the server keeps (a volume, in the container).

`memory.LoadImage(ctx, db, userID, dataset, imageArtifactID)` reads a stored image back.

## Schema

Created on startup in the same database as the other tables (`internal/db/graph/{sqlite,postgres}/db_schema.go`).

| Table | Columns |
| ----- | ------- |
| `gb_image_artifacts` | `id`, `dataset_id` → `gb_datasets`, `media_type`, `sha256`, `size_bytes`, `path` (the file, relative to the image directory), `valid_at`, `created_at` |
| `gb_image_extractions` | `id`, `image_artifact_id` → `gb_image_artifacts` (cascade), `provider`, `model`, `theme`, `title`, `text`, `raw_output` (the model's reply as received), `input_tokens`, `output_tokens`, `cost_usd`, `created_at` |
| `gb_image_text_blocks` | `id`, `extraction_id` → `gb_image_extractions` (cascade), `position` (reading order from 0), `kind`, `text`; unique (`extraction_id`, `position`) |

Both rows and all blocks of a call are written in one transaction, after the LLM call: if it fails nothing is stored (and a dataset that did not exist is not created). Each new image adds one image artifact (a repeat adds nothing, see above); an artifact can have several extractions (for example from different models) but `remember` makes one. Image artifacts are not in `gb_artifacts` and do not show up in the [graph export](graph.md).

Go access, in package `graph`: `InsertImage`, `ListImageArtifacts`, `GetImageArtifact`, `ListImageExtractions`.

## Limitations

- No endpoint reads, lists or deletes image artifacts or extractions yet; use the database.
- Not grounded: entities, edges and embeddings are not derived from the extraction, and `recall` ignores images.
- The files are on the local file system only (no object storage), and nothing deletes them: deleting a dataset (a soft delete) keeps its image rows and files, and there is no endpoint to remove an image.
- The theme is the model's single best guess; there is no confidence value and no bounding boxes for the text blocks.
- Huge images are not resized, and a dense image can exceed the output limit (`502`).
