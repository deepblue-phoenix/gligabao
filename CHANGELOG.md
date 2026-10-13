# Changelog

All notable changes to gligabao are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/) (while below 1.0.0: a minor bump may break things).

Newest entry first; its version must equal the contents of `VERSION` (checked by `go test`). See [AGENTS.md](AGENTS.md).

## [0.4.0] - 2026-10-13

### Added
- Entities and edges now record the model that inferred them: a new `model_id` column of `gb_entities` and `gb_edges`, a reference to the new `gb_models` table (NULL if unknown). It is the model the LLM API reported for the extraction call. An entity shared by several artifacts keeps the model that wrote its stored state: the one that created it, then the one that last replaced its description. The value is set by the pipeline, never taken from the LLM's output.
- New table `gb_models` (`id`, `name`, `provider`, `kind` = `llm` or `embedding`, `supported`, `created_at`). The ID is derived from the name alone (first 8 bytes of the SHA-256 of the trimmed, lowercased name, in hex), so a model has the same ID in every database.
- On every startup gligabao inserts the models it supports into `gb_models` and marks them `supported`: the default chat and embedding models and every key of the price table (built-in prices plus `[prices]` in the config). Models that are no longer supported keep their row with `supported` cleared. A model that is only seen in an API response (e.g. a dated snapshot like `claude-haiku-4-5-20251001`) is inserted, not supported, when something it inferred is stored.
- `model_id` appears in the graph JSON (`GET /v1/gb/admin/api/graph`, the eval graph endpoint), which also lists the referenced models (`models`: id, name, provider, kind, supported), in recall evidence and in the remember result.
- Admin UI (eval graph view): the model name is shown in the tooltips, in the details of the clicked entity and of each of its facts, and the status line lists the models that inferred the graph.

### Changed
- DB schema: new `gb_models` table and `model_id` columns. There is no migration: existing databases must be recreated.

## [0.3.0] - 2026-10-13

### Added
- Admin UI: paste an image (Ctrl+V) anywhere on the page to remember it. The image opens in an overlay over a tinted background; enter the user and dataset and press Remember to call `POST /v1/gb/remember` with the image. A spinning circle shows while the server works, and the extracted metadata (theme, title, text blocks, hashes, model, tokens, cost, cached or new) appears next to the image. Esc, the Close button or a click on the background closes it.
- Admin UI: an "Image store" card on the dashboard with the number and total size of the stored images, their extractions, LLM tokens and cost, counts by media type and by theme, and a comparison of the image directory with the database (files on disk, missing files, untracked files). It refreshes after an image is remembered from the UI.
- `GET /v1/gb/admin/api/images[?db=<name>]`: the statistics behind the card.

## [0.2.0] - 2026-10-13

### Added
- Image knowledge extraction: `POST /v1/gb/remember` (and the eval endpoint) accepts a base64 `image` (JPEG, PNG, GIF or WebP, up to 5 MiB) instead of `text`. It runs a separate image pipeline: one vision-LLM call (default model `claude-haiku-5-5`) reads the title and text blocks in the image (OCR) and classifies the whole image by a theme (`plot`, `chart`, `graph`, `diagram`, `table`, `document`, `screenshot`, `slide`, `map`, `photo`, `illustration`, `other`). See [docs/images.md](docs/images.md).
- Image files are stored on the file system, never in the database: in an image directory next to the SQLite file (`<name>.images`, e.g. `bin/gligabao.images`) as `<dataset id>/<sha256>.<ext>`. Set it with `serve --image-dir` or `ServeOptions.ImageDir`.
- New tables, created on startup in existing databases too: `gb_image_artifacts` (metadata only: media type, SHA-256, size and the file's path relative to the image directory), `gb_image_extractions` (theme, title, text, the model's raw output, tokens and cost) and `gb_image_text_blocks` (the text regions in reading order). Nothing is added to the graph, the vector store or `gb_artifacts` yet; grounding comes later.
- An image already in the dataset (same SHA-256) is not processed again: `remember` returns the stored extraction with `"cached": true`, without an LLM call or new rows. Only the same user's dataset is searched.
- Image calls are logged in `gb_llm_calls` with type `remember_image`, and emit the `remember_image.completed` / `remember_image.failed` system events.
- The LLM client can send images (`ai.Message.Images`).
- Python client: `Client.remember(user_id, image=<bytes or path>)`; `text` is now optional (exactly one of `text` and `image` is required).

### Changed
- A text larger than 1 MiB is still rejected with `400`, but the request body limit is now about 7 MiB, to fit images.

## [0.1.0] - 2026-10-13

First tracked version; summarizes everything built so far.

### Added
- Temporal knowledge graph memory: `POST /v1/gb/remember` extracts entities and facts (with validity intervals and artifact provenance) from text; `POST /v1/gb/recall` answers questions with evidence.
- Datasets owned by users, OWL ontologies, graph export (`GET /v1/gb/datasets/{name}/graph`).
- SQLite and PostgreSQL relational backends; chromem-go (default) and Pinecone vector stores; entities and edges embedded into per-dataset vector stores.
- Public Go API at the repo root for embedding gligabao, and the `gligabao` CLI (`serve`).
- Eval mode: endpoints returning ATIF trajectories, eval suites, experiments and runs kept in an eval database.
- Embedded admin UI at `/v1/gb/admin/`: datasets, graphs, vectors, evals and remember traces.
- System event hooks (`monitoring`), structured logging with logrus.
- Version tracking: `VERSION` embedded in the binary, this changelog, `gligabao version`, `--version`, `GET /version`.
