# Roadmap

The main list of planned features and plans for gligabao. Supporting or more detailed roadmap documents live in this
directory (`docs/roadmap/`) and are linked from here. What is already shipped is in [CHANGELOG.md](../../CHANGELOG.md);
what was considered and deliberately left out is in [not-included.md](../not-included.md).

Status values: **Planned** (we intend to build it), **Considering** (undecided), **In progress**, **Done** (move to the
changelog and remove it from here after a release or two).

## Planned

| Feature | Status | Notes |
| ------- | ------ | ----- |
| Ground image extractions in the graph | Planned | Today an image yields only rows in `gb_image_extractions` / `gb_image_text_blocks`: no entities, edges or vectors, and `query` does not see it. The extraction tables are the input. See [images.md](../images.md). |
| Tune and extend the similarity retrieval of `query` | Planned | `query` searches the entity, edge, chunk and summary indexes (see [query.md](../query.md#2-retrieve-by-similarity-and-passages)), but the similarity thresholds (0.75 for each index, the value `remember` uses for the same entity) and the numbers of matches taken are a first guess: tune them with the knowledge evals. Next steps: rank the seeds together (keyword and similarity) instead of a plain union, score facts cognee-style by the distances of their two endpoints and the edge, follow the provenance links (`gb_entity_artifacts`, `gb_edge_artifacts`) from a retrieved passage to the entities and facts extracted from it, and embed the question together with the conversation of its `session_id`. |
| Ontologies used by `remember` and `query` | Planned | Ontologies are loaded and stored as a graph of their own, but neither endpoint uses them yet. See [ontologies.md](../ontologies.md). |
| `query` uses timeseries | Planned | `query` does not read stored series, and treats `series_observation` artifacts like any other. See [timeseries.md](../timeseries.md). |
| Fetch timeseries from the standalone endpoint | Planned | Fetching from providers is in `remember` only; a series is not refreshed automatically. See [timeseries.md](../timeseries.md). |
| Lookahead protection for timeseries | Planned | A series that runs past a text's `reference_time` is stored whole, so a memory of an event can contain its future. Clip fetched windows, or label the post-event part. See [timeseries-design.md](../timeseries-design.md). |
| `query` uses geo | Partly done | With geo on, `query` finds the places a question mentions and the matched entities that are places, and gives the geo entities within a radius (default 50 km, `geo_radius_km` in the config or the request) to the answering LLM, when the request asks for it (`geo_nearby` / `geo_radius_km`; off by default), and stores the places of the question that the resolver finds. Not yet: filtering by a bounding box or a country ("in France"), a radius or "N nearest" read from the question itself, and ranking the nearby entities by relevance. See [geo.md](../geo.md#nearby-entities). |
| Geo for more than `remember` text | Planned | Only the entities of the text itself are geocoded: not those of web search results or series observations, and not those of an image. Geo metadata is not refreshed or corrected once stored, and a place the resolver did not know is asked again on every mention. See [geo.md](../geo.md). |
| Web search for image requests | Planned | `web_search` is rejected (`400`) together with `image`, since the image pipeline extracts no entities yet. Depends on grounding image extractions. See [web-search.md](../web-search.md). |
| MCP: authentication and more tools | Planned | MCP (`/v1/gb/mcp`, off by default) exposes only `remember` (text only) and `query`, with no authentication and outside the `Auth` hook. Missing: auth, the image/models/web_search/timeseries options of `remember`, the other endpoints as tools, resources and prompts. See [mcp.md](../mcp.md). |
| OpenAI as a main reasoning provider | Planned | `remember` and `query` use only Anthropic for the main reasoning; OpenAI is used for embeddings only. Add OpenAI as a supported reasoning provider too. |
| Export and import a whole dataset as one package | Planned | No way today to move or archive a dataset as a unit. Export the graph DB, the relational DB and the embedding vectors into a single dedicated gzip'ed file, with a manifest inside that holds the package format version. The file can be sent, archived, and imported by other gligabao instances (which check the format version). |
| Async `remember` (`remember_job`) | Planned | `remember` always blocks until the whole pipeline is done. Add an `async` flag to the request: when true, `remember` returns right away with a job ID, and the pipeline runs in a goroutine tied to that job. The job type is `remember_job`; its status is updated as each pipeline stage proceeds. A new endpoint, on both REST and MCP, returns the status of a job by ID. |
| Python SDK: `Start()` to launch the server from Python | Planned | A third-party Python program has no way to start the Go server itself. Add a `Start()` function to the `gligabao-server` package (`clients/py_server/`) that launches the bundled server binary as a subprocess, takes the Go server's params as named args, waits until it is ready and returns a server object (host/port, process handle, stop, context manager). The regular `gligabao` package (`clients/py/`) then optionally takes that server object, instead of the host string, to say which server to use. |
| CLI for the rest of the API, for agents | Partly done | `remember`, `query` (with `--evidence-only`), `dataset`, `api`, `status` and `stop` are done, on a local daemon the CLI starts (see [docs/cli.md](../cli.md)). Still missing: commands for the timeseries, training and models, evals and read-only admin info (today only through `gligabao api`), the status of a run (and of `remember_job` once `async_remember_job` ships), forgetting one artifact or fact, a way for the CLI to authenticate to remote servers, a lock or lighter start for very large stores, and an agent guide (`gligabao help agents`). |

## Considering

| Feature | Status | Notes |
| ------- | ------ | ----- |
| Session memory (per-session turn cache) | Considering | Left out for now; the reasoning, and the least invasive design if revisited, are in [not-included.md](../not-included.md#session-memory). |

## Adding to the roadmap

Add a row to the right table (or a new section), keep the notes short, and link to the doc that holds the details. A larger
plan gets its own file in this directory, linked from its row.
