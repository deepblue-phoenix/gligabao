# Changelog

All notable changes to gligabao are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/) (while below 1.0.0: a minor bump may break things).

Newest entry first; its version must equal the contents of `VERSION` (checked by `go test`). See [AGENTS.md](AGENTS.md).

## [0.127.0] - 2026-10-31

### Added
- A very simple deep-research agent in Go (`internal/agent`): a plain tool-calling loop with no agent framework. `agent.Research(ctx, client, question)` lets the model search the web (Anthropic's web search tool) and read pages (`fetch_url`) over several steps, and returns a report with its sources, the token counts and the cost. The last step cannot call tools, so a run always ends with an answer. It is a Go package only for now: no API endpoint or CLI command uses it yet.
- `ai.Client.Raw`: sends a Messages API payload as it is (tools, tool results) and returns the content blocks, tokens and cost.

## [0.126.0] - 2026-10-30

### Added
- CLI commands that use the API, so an agent can use gligabao without HTTP calls: `gligabao remember`, `query`, `dataset ls|create|rm|graph`, `api METHOD PATH` (any other endpoint), `status` and `stop`. They send the same requests as the REST API and print the JSON response; errors are JSON on stderr, with exit codes (2 usage/400, 3 not found, 4 conflict, 5 not available, 1 other). The request can be given as flags or as the exact REST body (`--json`, `@file` or `-`). See `docs/cli.md`.
- Local daemon: the commands talk to a `gligabao serve` that the first command starts in the background (on `127.0.0.1`, no authentication) if none answers on the port, and that later commands reuse. `--server URL` (or `GLIGABAO_URL`) targets another server instead. The default user of the CLI is `local_user` (`--user`, `GLIGABAO_USER`).
- Stable data directory for the CLI: without a `gligabao.toml` in the current directory (or `--config`), the daemon uses `~/.gligabao` (`GLIGABAO_HOME`) for its config, database, log and pid file.
- Retrieval-only `query`: `evidence_only: true` in the request (`gligabao query --evidence-only`) skips the LLM call that writes the answer; the response has an empty `answer` and, as evidence, everything that was retrieved. See `docs/query.md`.
- `serve --host` and `host` in the config: the address to listen on (default: every interface, as before).

### Fixed
- The server panicked, instead of returning the error, when it failed to start after opening the databases (e.g. no `OPENAI_API_KEY`).

## [0.125.2] - 2026-10-29

### Changed
- Code naming: every variable, argument and field of type `error` is now named `err` or ends with `Err` (`parseErr`, `updateErr`, `causeErr`, ...), so the type is clear at a glance. The rule is in `AGENTS.md`. Renames only; no change in behaviour.

## [0.125.1] - 2026-10-29

### Changed
- Code navigation: every use of `memory_pipeline` and of `memory_dataset` in the code (including tests) is now wrapped in a block marked `//------------------` / `// MEMORY PIPELINE` or `// MEMORY DATASET`, so the uses can be found at a glance (`rg "MEMORY PIPELINE"`). The rule for new code is in `internal/memory/AGENTS.md`. Comments only; no change in behaviour.

## [0.125.0] - 2026-10-29

### Added
- Admin UI: a **query** view lists the runs of `query` (`GET /v1/gb/admin/api/query-runs`) like the **remember** view does for `remember`: the counts by status (click one to filter), the runs newest first, and for each the question, the session, the parameters and a table with the status, time, inferences and error of every stage, with a **?** button that describes it. A query run keeps no stage outputs, so it has no **Outputs** button. The view is `query_runs` for the slots of [admin extensions](docs/admin-extensions.md).
- Docs: `docs/pipeline.md` describes the pipeline pattern that `remember` and `query` share: the list of stages, the policies and `Stop`/`Each`, what the runner tells the run log, how inferences are filed under their stage, the run log, and how to add a stage or an operation.

### Changed
- Admin UI: the code of the remember runs view (`remember_runs.ts`) is now `runs.ts`, shared by the remember and query views. No change in what the remember view shows.

## [0.124.0] - 2026-10-29

### Added
- Query run log: every `query` call is recorded in the new table `gb_query_runs`, one row per execution, like `gb_remember_runs` is for `remember`: the question, how the call was made, its status and error, and for each stage of the pipeline (`retrieve_graph`, `detect_geo`, `nearby_geo`, `query`) its status, error, start and end times and the IDs of the inferences it made. The response has the new field `query_run_id`. A request that is rejected before the pipeline starts (a `400`, an unknown dataset) has no row. See `docs/query.md#run-log`.
- Admin API: `GET /v1/gb/admin/api/query-runs` lists the query runs, with the counts by status, like `remember-runs`.
- Run logs: a stage that a call does not use (no web search was asked for, the text is not a document, there is no geo resolver, a query with nothing to answer from, ...) is now recorded as `skipped`, a new stage status next to `not_started`, `started`, `succeeded` and `failed`. A run that succeeded no longer has stages left `not_started`; `not_started` now means that the pipeline never reached the stage.

### Changed
- The stages of `remember` and `query` are now declared as a list (`rememberPipeline` in `memory_remember/remember_pipeline.go`, `queryPipeline` in `memory_query/query.go`) and run by the new package `internal/memory/memory_pipeline`, which tells the run log when each stage starts, ends (with its error) or is skipped, and files the inferences of each stage by itself (`memory_core.WithInferenceSink`), instead of every stage doing it. A stage that never fails the call is `Advisory`. The behaviour of `remember` and `query` does not change; in the run log of `remember`, the error of `chunk_text` now includes the `memory: chunk text:` prefix, and the `store` stage of a document also covers the check of its session ID.
- Admin UI, remember runs view: a `skipped` stage has no **Outputs** button.
- Schema: `gb_remember_runs` accepts the status `skipped` for its stages, and `gb_query_runs` is new. There is no migration: an existing database has to be recreated.

## [0.123.2] - 2026-10-29

### Changed
- Code layout: in `internal/memory/memory_remember` and `internal/memory/memory_query`, the struct types are now declared at the top of their files. No change in behaviour or API.

## [0.123.1] - 2026-10-29

### Changed
- The geo resolvers package moved from `internal/geo` to `internal/memory/memory_geo` (package name `memory_geo`). No change in behaviour or API.
- The chunker package moved from `internal/chunk` to `internal/memory/memory_chunk` (package name `memory_chunk`). No change in behaviour or API.
- The timeseries endpoints package moved back from `internal/timeseries` to `internal/api/timeseries`, next to the other endpoint packages. No change in behaviour or API.

## [0.123.0] - 2026-10-29

### Added
- Admin UI, remember runs view: the stages table has a new narrow last column, without a title, with a **?** button for each stage. Clicking it opens a tooltip that describes what the stage does (click it again, click elsewhere or press Esc to close it).

## [0.122.3] - 2026-10-29

### Removed
- Admin UI, remember runs view: the **Only this dataset** button under the stages of an opened run.

## [0.122.2] - 2026-10-29

### Changed
- Admin UI, remember runs view: the **Outputs** button of a stage now opens the outputs in a modal instead of inline under the stage row, and the JSON is colour coded (keys yellow, values green). While the modal is open the rest of the admin panel does not scroll.

## [0.122.1] - 2026-10-29

### Changed
- The eval runners (`./run_evals.sh`, also the one of `gligabao_int`) colour their output when it goes to a terminal: passed cases green, failed cases red, eval names and the `answer:` label yellow, and the passed/failed totals green/red. `NO_COLOR` turns the colours off, `FORCE_COLOR` turns them on when the output is piped.

## [0.122.0] - 2026-10-29

### Added
- Stage outputs of the remember pipeline, for debugging: every stage now files what it produced in a new table, `gb_remember_run_stage_outputs` (one row per output: `run_id`, `stage`, `name`, `label`, `output` JSON, `created_at`), as it takes each step. That includes the entities and facts the LLM extracted together with its raw answer (so a malformed answer is visible), the candidate facts and how each was found, what `resolve_edges` decided, the summaries, the places the geo resolver returned, the web searches and what the timeseries providers returned. A failed step files what it has. The table is separate from `gb_remember_runs` so it can be cleared or garbage collected on its own; deleting a run deletes its outputs, and an output over 64 KiB is replaced by a stub. See docs/remember.md#stage-outputs.
- Admin UI: in the remember runs view, each stage of an opened run that ran has an **Outputs** button; clicking it fetches (only then) and shows the outputs of that stage as collapsible JSON. Backed by `GET /v1/gb/admin/api/remember-run-outputs?run_id=<id>[&stage=<name>]`.
- `relational.AddRememberStageOutput`, `ListRememberStageOutputs` (of a run, or of one stage of it) and `DeleteRememberStageOutputs` (delete those made before a time, or all), for SQLite and PostgreSQL.

## [0.121.0] - 2026-10-28

### Added
- Knowledge evals: the `text` of an entry of a case's `remember` can be a list of strings as well as a string. The strings are joined with a single space, so a long text can be split over several lines of the eval file and be read by a human. See docs/evals/knowledge.md.

## [0.120.1] - 2026-10-28

### Changed
- Docs: the query trajectory in docs/evals/knowledge.md now lists the `search_vectors` step (the embedding of the question and what the indexes returned) before `retrieve_graph`, which now also holds the passages; docs/artifacts.md no longer says `query` never uses artifact text (the best chunks of long texts are given to the LLM as passages); the Query sections of the README describe the similarity retrieval, the passages and that superseded facts are not retrieved.

## [0.120.0] - 2026-10-28

### Added
- `query` now reads the embeddings `remember` writes. It embeds the question once and searches the four vector indexes of the dataset: similar entities are added to the seeds of the subgraph (next to the keyword matches), similar facts are added directly with their endpoints (so the `relation` and `description` of an edge are searched now), and the chunks and summaries of long texts that are close to the question become *passages*. See docs/query.md and docs/vectors.md.
- Passages: the best 3 are given to the LLM with the text of the chunk (and its summary, if found), and up to 5 more with their summary only, in a new `<passages>` section of the prompt. The LLM cites the ones it used (`"passages": [ids]`) and the response has them in `evidence.passages` (`artifact_id`, `parent_id`, `index`, `summary`, `text`); the list is always an array.
- The embedding request of a query is recorded as an inference (type `query`, stage `embed`) and as the `search_vectors` step of the trajectory.
- `graph.EdgesByID` and `graph.ArtifactsByID` (SQLite and PostgreSQL).

### Changed
- The embedder (`OPENAI_API_KEY` / `openai_api_key`) and the vector store are now required: the server and the knowledge eval runner do not start without the key (they used to start and skip the vectors), and `memory_query.Query` needs `memory.WithVectors`. Vector retrieval is always on; there is no request field for it.
- A query that finds no entity, fact or passage still answers without calling the LLM; with only passages it now calls it.
- The similarity threshold is a cosine of 0.75 for every index (the value `remember` uses to tell that two names are the same entity); it and the numbers of matches taken are a first guess, not yet tuned with the evals (see the roadmap).
- `query` no longer puts expired facts (superseded ones, with an `expired_at`) in the context, however they are found (`graph.EdgesAround` and `graph.EdgesByID` return live edges only); facts that ended without being superseded (`invalid_at` only) are still given, with it. The prompt no longer mentions superseded edges.

## [0.119.2] - 2026-10-28

### Changed
- Roadmap: added the planned Python SDK `Start()` in `gligabao-server`: it launches the Go server from Python with named args and returns a server object, which the `gligabao` package can take instead of the host string.

## [0.119.1] - 2026-10-28

### Changed
- Roadmap: added the planned async `remember` (`remember_job`): an `async` flag that returns a job ID right away, runs the pipeline in the background with per-stage status, and a REST/MCP endpoint to read the job status.

## [0.119.0] - 2026-10-28

### Added
- Admin UI: a new **vectors** view. Per database it lists the vector indexes of every live memory dataset (not training datasets): the backend store, the number of vectors, the storage the index occupies and how long ago (minutes, hours, days, months) the last vector was written. Served by `GET /v1/gb/admin/api/vectors/all?db=` (`memory.VectorStatsAll`).
- `vector.Stats` has two new fields, `disk_bytes` (the size of the index's files in the store; 0 if the store cannot say) and `last_write` (absent if not known). Chromem reports both for a persisted store (`last_write` is the newest modification time of the vector files); Pinecone reports neither, and the view shows `n/a` for its last write and an estimate (`~`) for its storage. The vector DB modal of the main view shows the two new columns as well. See docs/vectors.md.

### Changed
- Admin UI: the nav order is now main, vectors, remember, inferences, evals, timeseries, training, geo. The vectors view has no user column (the user is in the tooltip of the dataset name).
- Admin UI: fetching and formatting of vector index statistics is shared between the vector DB modal (main view) and the new view (`admin_ui/src/vectors.ts`); byte and age formatting moved to `admin_ui/src/format.ts`.

## [0.118.0] - 2026-10-27

### Added
- `GET /v1/gb/geo/nearby?user_id=&dataset=&entity_id=|latitude=&longitude=[&radius_km=]`: the geo entities near a stored geo entity or a point, with no question and no LLM. It runs the same search as the proximity step of `query` (same radius rules, limits and order), so the two select the same entities; the server's `geo_radius_km` is its default radius. It needs an API key when the server has authentication. Go: `memory_query.NearbyEntityOf`, `NearbyPoint`, `query.NearbyHandler`.
- Admin UI, geo view: clicking a geo entity now opens an interactive map (Leaflet on OpenStreetMap tiles) that shows the entity, the search radius and the geo entities near it, as the query pipeline would select them, with a radius field and a list of them. The place's box and the radius circle light up when the pointer is over them (the circle also explains itself in a tooltip). The "Open in OpenStreetMap" link of the old modal is gone. Served by `GET /v1/gb/admin/api/geo/nearby`. Leaflet is bundled with the admin UI (`admin_ui/dist/leaflet`).

## [0.117.3] - 2026-10-28

### Changed
- `internal/remember` and `internal/query` moved back into `internal/api` (`internal/api/remember`, `internal/api/query`): they are part of the API. This undoes that part of 0.117.1; `internal/sys/httputil` stays. No change in behaviour or API. The docs point at the paths.

## [0.117.2] - 2026-10-28

### Changed
- The memory dataset logic (`CreateDataset`, `ListDatasets`, `DeleteDataset`, `ErrDatasetExists`, `Dataset`, the vector indexes and `VectorDataset`, `EntityDataset`, `EdgeDataset`, `ChunkDataset`, `SummaryDataset`) moved from `internal/memory/memory_core` to the new package `internal/memory/memory_dataset`. No change in behaviour or API. The docs refer to the new names.

## [0.117.1] - 2026-10-27

### Changed
- Three packages moved out of `internal/api`: `internal/api/remember` to `internal/remember`, `internal/api/query` to `internal/query`, and `internal/api/httputil` to `internal/sys/httputil`. No change in behaviour or API. The docs (`docs/images.md`, `docs/query.md`, `docs/listings/go-structs.md`) point at the new paths.

## [0.117.0] - 2026-10-27

### Added
- `query` can give the LLM that answers the geo entities that lie near the places of the question. **It is off by default**: the request asks for it with `geo_nearby: true`, or by setting `geo_radius_km` (which turns it on by itself; `geo_nearby: false` with a radius is a `400`). It needs geo on in the server (`400` otherwise). The `query` MCP tool has both arguments. Go: `memory_core.WithGeoNearby`.
- The places are the entities that matched the question and have coordinates, and the places the `detect_geo` stage finds in the question. For each, the geo index finds the geo entities of the dataset within a radius (SQLite: the R*Tree; PostgreSQL: PostGIS `ST_DWithin` on the GiST index; both fall back to the latitude/longitude index when there is none), the nearest 20 per place and 30 in all. They and the edges around them are added to the graph the LLM reads, with a `<nearby>` section that gives each one's distance from the place. The response has a new `nearby` field (the radius, the places searched around and the entities found; omitted when there was no place) and the trajectory a `nearby_geo` step. The step never fails the query.
- **Places of the question are stored.** A place of the question that is not an entity with coordinates is looked up with the geo resolver, and one it finds is stored: as the coordinates (`gb_entity_geo`) of the entity of that name if the dataset has one (not a person, concept, product or event), else as a new entity of the type `place`. The next question with the same place finds it in the database (no resolver call), and it can show up as a nearby entity in later searches, not only places that `remember` stored. This is the one write of `query`; it happens only with the nearby search on, and a failure only means the place is not stored. The anchor in `nearby.anchors` has `"stored": true` for a place stored by this call.
- The radius is 50 km by default. `geo_radius_km` in `gligabao.toml` sets the server's default (it does not turn the search on; Go: `server.Options.GeoRadiusKm`, `api.WithGeoRadius`, `memory_core.WithGeoRadius`), and `geo_radius_km` in the request overrides it for one call. It must be greater than 0 and at most 20015 km (`400` otherwise).
- Go: `graph.NearbyEntityGeo` (proximity query, exact: bounding box from the index refined with the haversine distance, antimeridian and poles handled), `geo.DistanceKm`, `geo.BoundingBox` and `geo.NotPlaceTypes`.

### Changed
- With geo on and the nearby search asked for, `query` may call the geo resolver (for the places of the question that are not entities with coordinates), not only `remember`.

## [0.116.0] - 2026-10-30

### Added
- Geo entities are geo indexed when `remember` stores them, ready for the proximity queries planned for `query`. SQLite: the R*Tree table `gb_entity_geo_rtree`, kept in step with `gb_entity_geo` by triggers (store, move, and removal with the entity). PostgreSQL: when PostGIS is enabled, the generated column `gb_entity_geo.geo` (`geography(Point, 4326)`) with the GiST index `idx_gb_entity_geo_gist`. The index is created at startup only when geo is on (`geo = true`, `--geo` or a `GeoResolver`; Go: `db.Config.Geo`, `graph.EnableGeoIndex`), never with geo off. There is no backfill: places stored before the index existed are not indexed on SQLite. On PostgreSQL without PostGIS startup logs a warning and the places are stored without an index (restart with geo on after `CREATE EXTENSION postgis`). The `-geo` flag of the knowledge evals does the same. See [docs/geo.md](docs/geo.md#the-geo-index).

## [0.115.0] - 2026-10-29

### Added
- The in-process server of the knowledge evals has a geo resolver: with it `remember` finds the places among the entities of a text and stores their coordinates (`gb_entity_geo`), and `query` finds the places of the question, so the trajectories of the evals have the `select_geo_entities`, `detect_geo`, `resolve_geo` and `store_geo` steps. The runner turns it on by default with the public Nominatim geocoder of OpenStreetMap (cached; needs internet; one more LLM call per text); `-geo=false` turns it off, and the setting is recorded in the experiment run's `params.geo`. Go: `knowledge.Executor.Geo`; without it geo stays off. The `detect_geo` model and reasoning of `-models`, `-reasoning` and the cases now take effect.

- A knowledge case has a new check, `expect_geo`: places that must be stored for entities of the dataset (`gb_entity_geo`), each with the entity's name and, optionally, `kind`, `country_code` and `latitude`/`longitude` with `radius_km` (default 100 km). Go: `core.KnowledgeExpectGeo`; the column `expect_geo` of `gb_case_def_meta_knowledge` (SQLite and PostgreSQL). A case with it errors when geo is off.
- The `geo` eval, `build/evals_knowledge/geo.json`, with five cases: a city, a city and its country, a landmark, two cities and a mountain. Each remembers a sentence, asks a question and checks the facts and the stored places.

### Changed
- Schema: `gb_case_def_meta_knowledge` has the column `expect_geo`. There is no migration: an existing eval database has to be recreated.
- `server.NewHandler` takes the geo resolver (after the web searcher; nil: geo off).

## [0.114.0] - 2026-10-29

### Added
- The admin UI has a `geo` view: how many entities are geo entities (have coordinates) out of all entities, per database, and a table of the 100 most connected geo entities (by live facts, as source or target). Clicking an entity opens a modal with its place on an OpenStreetMap map (the embedded map of openstreetmap.org: the browser needs internet access), with the details of the place and a link to the full map. New endpoint `GET /v1/gb/admin/api/geo[?db=<name>]` (Go: `admin.GeoOf`, `graph.GeoStatistics`).

## [0.113.0] - 2026-10-28

### Added
- The admin UI shows whether the database has a geo index, as a `geo index` badge in the top bar: R*Tree for SQLite, PostGIS for PostgreSQL. Geo queries (proximity) need it. SQLite always has R*Tree (it is compiled into gligabao); for PostgreSQL the badge is red until `CREATE EXTENSION postgis` has been run in the database, and its tooltip says if PostGIS is installed on the server but not enabled. New endpoint `GET /v1/gb/admin/api/geo-index[?db=<name>]` (a live probe; Go: `admin.GeoIndexOf`, `admin.CheckGeoIndex`). It only reports: nothing creates the index or enables the extension, and nothing uses it yet.

## [0.112.0] - 2026-10-27

### Added
- With geo on (`geo = true`, `serve --geo`, or a `GeoResolver`: the same switch as for `remember`), `query` finds the places the question mentions. It is one more LLM call, separate from the one that answers (the `detect_geo` stage, on the question alone), made after the graph is retrieved and not at all for an empty dataset. For now the places are only extracted, not used: retrieval and the answer do not change, the geo resolver is not asked, and they are not in the response. They are in the eval trajectory (a `detect_geo` step) and in `memory_query.QueryResult.Geo` for Go callers. A failure of the step never fails the query.
- `query` takes `models` (also a `models` argument of the MCP tool and of the Python client's `query`): the model of each stage, `query` (the answer) and `detect_geo`. A stage left out uses the default model. Any other stage, including those of `remember`, is `400`. Go: `memory_core.ResolveQueryModels`, `memory_core.QueryStages`; the options are the same `WithModels`.
- `reasoning` of `query` takes a level per stage, an object like that of `remember`: `{"query": "medium", "detect_geo": "low"}`. The string form (the level of the answering call) still works. A stage left out runs with `none`; another stage is `400`. The MCP tool and the Python client's `query` take both forms. Go: `memory_core.ResolveQueryReasoningStages`, `memory_core.QueryReasoningStages`.
- The calls of `query` that find the places are recorded as inferences of type `query`, stage `detect_geo`.
- The knowledge eval runner sets models and reasoning for the query pipeline too. `-models` and a case's `"models"` take `query` (the answering call) next to the stages of `remember` (`knowledge.ParseModels`, `knowledge.ModelStages`), and are sent as `models` of the query request; the query's reasoning is sent as a per-stage object (`query` and `detect_geo`). `detect_geo` is a stage of both pipelines, so one setting applies to both; the in-process server of the evals has no geo resolver, so it has no effect there yet. Cases and the shipped evals need no change.

### Changed
- Docs: `docs/geo.md`, `docs/query.md`, `docs/mcp.md`, `docs/evals/knowledge.md` and the roadmap describe the above. `query.Handler` and `query.EvalHandler` take options (`memory_core.Option`), as `api.Register` passes `WithGeoResolver` to them.

## [0.111.1] - 2026-10-27

### Changed
- Docs: the README describes gligabao as a self-hosted MCP/HTTP server (it said HTTP service) and points at the MCP section.

## [0.111.0] - 2026-10-27

### Added
- MCP (Model Context Protocol) support: with `mcp = true` in the config (or `serve --mcp`) the server serves `remember` and `query` as MCP tools at `/v1/gb/mcp` (streamable HTTP, stateless, JSON answers), built on [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) v1.6.1. Each tool runs the handler of the matching REST endpoint in process, so validation, monitoring events and results are the same; an error status of the endpoint becomes a tool error (`isError`). `remember` takes `user_id`, `text`, `dataset`, `kind`, `session_id` and `reference_time` (no image, `models`, `reasoning`, `web_search` or `timeseries` yet); `query` takes `user_id`, `query`, `dataset`, `session_id` and `reasoning`.
- New config key `mcp` (default `false`, so nothing changes unless it is turned on) and flag `serve --mcp`; `ServeOptions` follows `Config.MCP`.
- There is no authentication yet: callers choose the `user_id`, and the `Auth` hook does not cover the endpoint (a warning is logged at startup when MCP is on, and another if `Auth` is set as well). `MountOptions.Wrap` does cover it.
- Docs: [docs/mcp.md](docs/mcp.md); README, [docs/identity.md](docs/identity.md), [docs/hooks.md](docs/hooks.md) and the roadmap mention it.

## [0.110.1] - 2026-10-27

### Changed
- Docs: [docs/geo.md](docs/geo.md) gets the trajectory steps of the geo stage (`select_geo_entities`, `detect_geo`, `resolve_geo`, `store_geo`); [docs/remember.md](docs/remember.md) describes the geo step of the pipeline (4c) and the `geo` list of the response; [docs/evals/knowledge.md](docs/evals/knowledge.md) lists the geo steps of a remember trajectory; the table `gb_entity_geo` and the geo structs are in `docs/listings/`.

## [0.110.0] - 2026-10-27

### Added
- New package `internal/geo`: geo resolvers that turn the name of a place into coordinates (`geo.Resolver`; `geo.NewNominatim` for OpenStreetMap's Nominatim, with its one request per second and a User-Agent; `geo.Cached`; `geo.Func`), the kinds of place (country, region, city, place) and `geo.ErrNotFound`.
- `remember` finds the places among the entities it extracts and stores their coordinates. After the text is stored, a new stage (`resolve_geo`) asks an LLM (the new `detect_geo` stage, which takes a model and a reasoning level like the others) which entities are places (a city, a country, a region, a landmark, ...) and what to ask the resolver, asks the geo resolver, and stores the result. Entities that already have geo metadata are not looked up again. It never fails the call; the response has a new `geo` list with the outcome for each place.
- New table `gb_entity_geo` (SQLite and PostgreSQL): one row per geo entity keyed by `entity_id` (latitude, longitude, kind, display name, country, country code, region, bounding box, provider, ...), deleted with its entity. Go: `graph.GetEntityGeo`, `graph.ListEntityGeo`, `Tx.UpsertEntityGeo`, `Tx.GetEntityGeo`.
- Geo is off by default (it costs one more LLM call per text and sends place names to the geocoder). `geo = true` in the config or `serve --geo` turns it on with Nominatim; `ServeOptions.GeoResolver` (`gligabao.GeoResolver`) supplies another resolver and turns it on. Go callers: `memory_core.WithGeoResolver`, `api.WithGeoResolver`.
- Docs: [docs/geo.md](docs/geo.md); the roadmap lists what is left (`query` using geo, geo for more than the text's entities).

### Changed
- Schema: `gb_remember_runs` has the columns of the new `resolve_geo` stage, and the knowledge evals in `build/evals_knowledge/` set the reasoning of `detect_geo` to `none` like the other stages. There is no migration: an existing database has to be recreated.

## [0.109.3] - 2026-10-27

### Changed
- Docs: the roadmap lists OpenAI as a planned main reasoning provider for `remember` and `query` (today it is used for embeddings only).

## [0.109.2] - 2026-10-27

### Changed
- Docs: the README and `docs/hooks.md` point at the new `gligabao_int` locations (`gligabao_int/gligabao_int`, `gligabao_int/gligabao_int_embed`, `gligabao_int/gligabao_int/options.go`) instead of `gligabao_int/cmd`.

## [0.109.1] - 2026-10-27

### Changed
- The timeseries endpoints package moved from `internal/api/timeseries` to its own package `internal/timeseries`. No change in behaviour or API.

## [0.109.0] - 2026-10-26

### Changed
- The `bin/` directory is now `build/`: the default config (`build/gligabao.toml`), database, eval database, vectors, images, trajectories and trained models, the built binary (`./run_build.sh` builds `build/gligabao`), and the bundled `datasets_training/`, `evals_knowledge/`, `evals_training/` and `ontology/` directories. The Go defaults (`DefaultConfigPath`, `DefaultSQLitePath`, `DefaultEvalDir`, `TrajectoryDir`, `ImageDir`), the scripts, the Dockerfile, `.gitignore`, `.dockerignore` and the docs use the new paths. `build/pypi/` (the `run_pypi.sh` staging directories) lives in the same directory.
- Migration: move your state files from `bin/` to `build/`, or pass `--config`, `--db` and the other path flags; other programs that point at `gligabao/bin/...` (for example the `*_dirs` lists of a config) need `build/`.

## [0.108.1] - 2026-11-08

### Added
- A roadmap: `docs/roadmap/ROADMAP.md` is the main list of planned features and plans, and `AGENTS.md` says all roadmap material belongs in `docs/roadmap/`.

## [0.108.0] - 2026-11-07

### Added
- A second PyPI package, `gligabao-server` (`clients/py_server`): `pip install gligabao-server` installs the server, as a platform-specific wheel with the prebuilt binary (admin UI embedded) and the default `gligabao.toml`, and the commands `gligabao-server` and `gligabao` that run it. `pip install gligabao` still installs only the Python client. The sidecar is not part of the package. Wheels are built for Linux, macOS and Windows on amd64 and arm64. See `docs/pypi.md`.
- `run_pypi.sh` builds one platform at a time: `--build_client`, `--build_server`, `--build_server_linux`, `--build_server_darwin`, `--build_server_windows`, narrowed with `--arch amd64|arm64`.

### Changed
- `run_pypi_upload.sh` is now `run_pypi.sh`, and uploading is a flag, `--upload` (with `--test` for TestPyPI and `--yes` to skip the confirmation). It no longer builds unless build flags are given, and it uploads every built file of the current versions (all platforms), not only what the same call built. `--dry-run` is gone: building without `--upload` is the dry run.

## [0.107.0] - 2026-11-06

### Added
- The `Auth` hook (`ServeOptions.Auth`, type `gligabao.Authenticator`): when set, the user routes (`remember`, `query`, datasets, timeseries, ontologies) require an API key sent as `Authorization: Bearer <key>`; gligabao calls the hook with it, and the hook returns the ID of the user the key belongs to. The request then acts as that user (a `user_id` the caller sent is replaced), and a missing or rejected key is a `401`. Without the hook nothing changes. See `docs/hooks.md` and `docs/identity.md`.
- Python client: the API key is read from the `GLIGABAO_API_KEY` environment variable (or passed as `Client(api_key=...)`) and sent as the bearer token. It replaces the `user_id` of `remember`, `query` and `attach_timeseries`, which no longer take one. `Client.query()` is new.

## [0.106.0] - 2026-11-05

### Changed
- Breaking: `recall` is now called `query`, everywhere. The endpoints are `POST /v1/gb/query` and (with `--eval`) `POST /v1/gb/eval/query`; the docs page is `docs/query.md`; the Go packages are `internal/memory/memory_query` (`memory_query.Query`, `QueryResult`) and `internal/api/query`; `memory_core.StageQuery`, `DefaultQueryReasoning` and `ResolveQueryReasoning` replace the `Recall` names.
- The values `recall` became `query` in: the monitoring events (`query.completed`, `query.failed`), the type and stage of inferences (`gb_inferences.type` / `stage`), the `reasoning` key of an eval case and of the runner's `-reasoning` flag, the ATIF agent name (`gligabao-query`), and the eval's trajectory kind.
- Schema: in the eval database, `gb_eval_runs_meta_knowledge.recall_calls` and `recall_cost_usd` are `query_calls` and `query_cost_usd` (JSON `query_calls`, `query_cost_usd`), and the trajectory `kind` check accepts `'query'` instead of `'recall'`. There is no migration: an existing database has to be recreated (or altered by hand).
- "Precision and recall" in the docs is unchanged. Older changelog entries keep the old name.

## [0.105.0] - 2026-11-04

### Changed
- Breaking: what was called *exporting* a trained model is now called *deploying* it, since it is a deployment of the weights that happens. Renamed throughout: the hook `ServeOptions.TrainedModelExporters` is `ServeOptions.TrainedModelDeployers` (`gligabao.TrainedModelDeployer{Name, Description, Deploy}`; types `DeployTrainedModelRequest` (`DeployID` instead of `ExportID`), `DeployTrainedModelResult` and `DeployTrainedModelFunc`); the endpoints are `POST|GET /v1/gb/training/models/{id}/deployments` (body `{"deployer", "options"?}`, answer `{"deployments": [...]}` with a `deployer` field) and `GET /v1/gb/training/deployers`; the config key `trained_model_export_dir`, the flag `--trained-model-export-dir` and `ServeOptions.TrainedModelExportDir` are `trained_model_deploy_dir`, `--trained-model-deploy-dir` and `ServeOptions.TrainedModelDeployDir`; the system events are `training.model_deploy.completed` / `.failed` (with `deploy_id` and `deployer`). Docs: `docs/model-exporting.md` is now [docs/model-deploying.md](docs/model-deploying.md).
- Admin UI, training view: the **Export** button and dialog are **Deploy**, the **Exporters** block is **Deployers**.
- Schema: the table `gb_trained_model_exports` is `gb_trained_model_deployments` (column `exporter` is `deployer`). There is no migration: an existing database has to be recreated (or the old table renamed by hand).


## [0.104.0] - 2026-11-04

### Added
- Inference hooks for trained models: `gligabao.ServeOptions.TrainedModelInferenceHooks` is a list of `gligabao.TrainedModelInferenceHook{Name, Description, Model, Infer}` that bypass the sidecar (Modal). `Model` is the name of the trained model the hook targets; when a trained model with that name is prompted, gligabao calls the hook's `Infer` callback (it gets the model, the prompt, and the max tokens and temperature if the caller chose them, and returns the response and optionally the token counts) and does not call the sidecar. A model without a hook still runs on the sidecar. Several hooks can be supplied, each for a different model. It applies to the prompt dialog of the admin UI and `POST /v1/gb/training/models/{id}/infer` only: the training evals and audits of a model always run on the sidecar (Modal), where the models are trained, hooked or not. The requests are stored in `gb_trained_model_inferences` as before. A hooked model can be prompted without an active sidecar. A failing or panicking hook fails the request (`502`, with its message). Hooks are validated at startup. New types: `InferTrainedModelFunc`, `InferTrainedModelRequest`, `InferTrainedModelResult`. See [docs/hooks.md](docs/hooks.md#trained-model-inference-hooks).
- The answer of `POST /v1/gb/training/models/{id}/infer` has a new `hook` field (the hook that ran it, `""` for the sidecar). New `GET /v1/gb/training/inference-hooks` lists the hooks. The admin UI's prompt dialog says "Runs on inference hook <name> (not on Modal)" for a hooked model, and shows no Modal container status for it.

### Changed
- Internal: `server.NewHandler` takes a `training.Hooks` (exporters and inference hooks) in place of the list of exporters; the infer endpoint takes a `training.InferRouter`.

## [0.103.0] - 2026-11-03

### Added
- Trained model exporters can report progress: `gligabao.ExportTrainedModelRequest` has a new `Update func(message string)` (never nil, safe for concurrent use) that every exporter gets. The messages are kept in memory only, not stored: while an export runs they are returned as `updates` (`[{"time", "message"}]`, at most the newest 200 messages of at most 500 characters) by `GET /v1/gb/training/models/{id}/exports`, and the answer of `POST .../exports` has them when it ends. gligabao adds its own messages (fetching the files, running the exporter, completed or failed). See [docs/model-exporting.md](docs/model-exporting.md#progress-updates).
- Admin UI, training view: while an export runs, its row in the Export dialog shows the progress messages as they arrive (the same for every exporter) and keeps them under the outcome when it ends, also for a failed export.

## [0.102.1] - 2026-11-03

### Changed
- Admin UI, training view: each exporter (in the **Exporters** block and in the Export dialog) is one line with just its name; its description opens in a dialog from a **?** button next to the name. The hint text in the Exporters block ("Registered through hooks...") is gone; the "no exporter is registered" and load error messages remain.

## [0.102.0] - 2026-11-02

### Added
- `gligabao.Mount(ctx, cfg, opts, gligabao.MountOptions{Mux: mux})`: a program that already runs its own HTTP server can mount gligabao's handlers on its `*http.ServeMux` instead of letting gligabao start a server on `port`. All routes are registered as one subtree, `/v1/gb/` (`gligabao.MountPrefix`): the API and, when enabled by the config, the admin UI, eval and training endpoints. `Mount` does the whole setup (databases, sidecar check, providers, exporters, admin extensions), returns when the handlers are registered and gives back a `release` function that stops the remember manager and closes the databases (idempotent, and also called when `ctx` is cancelled; after it the mounted routes answer `503`). It does not handle SIGINT/SIGTERM, the host owns the process. gligabao's own `/`, `/health` and `/version` are not registered (they belong to the host), and `port` is not used. `MountOptions.Wrap` wraps the handler, e.g. with the host's authentication (gligabao has no access control of its own). A conflict with a route of the host under `/v1/gb/` is an error, not a panic.
- `server.Mount` and `NewHandler`'s internals: the routes are registered per method (`GET /v1/gb/`, `POST /v1/gb/`, ...) so that they do not conflict with a host's `GET /`.

### Changed
- Internal: `server.Run` is split into the setup (`prepare`) and the listening part; `Serve` and the standalone CLI behave exactly as before.

## [0.101.0] - 2026-10-29

### Added
- Admin UI, training view: every trained model's action box has an **Export** button, after Audit. It opens a dialog that lists the registered trained model exporters (the hooks of `ServeOptions.TrainedModelExporters`) with their descriptions, each with its own **Export** button; pressing one exports the model with that exporter and shows the outcome (status, ref, duration, or the error) in its row.
- Admin UI, training view: a new **Exporters** block at the bottom lists the exporters registered through hooks, with their descriptions (or says that none is).
- `TrainedModelExporter` has an optional `Description` field: what the exporter does and where the model goes. The admin UI shows it.
- `GET /v1/gb/training/exporters`: `{"exporters": [{"name": "...", "description": "..."}]}`, the registered trained model exporters, sorted by name (empty list if none).

## [0.100.1] - 2026-10-28

### Changed
- Admin UI, training view, datasets table: long dataset names and file paths are shortened to first 14 letters + `...` + last 14 (the full text is the tooltip), so they no longer overflow; "Updated" shows how long ago (minutes/hours/days) with the UTC time as tooltip; the Size column is a little wider.

## [0.100.0] - 2026-10-28

### Added
- Content directories are lists, set in the configuration file, so a program that embeds gligabao can keep its own datasets, evals and ontologies next to gligabao's: `datasets_training_dirs`, `evals_training_dirs`, `evals_knowledge_dirs` and `ontology_dirs` in `gligabao.toml`. The directories are searched in order and the first one wins when a name (a training dataset, an eval) is in more than one. Relative paths are relative to the config file, not to the working directory. `bin/gligabao.toml` lists gligabao's own.
- Training datasets: uploads are saved in the first directory of `datasets_training_dirs`; the others are only read (an upload named like a dataset of another directory is a `409`). When there are several directories, a dataset problem's `dir` is the full path of its subdirectory.
- Ontologies: `POST /v1/gb/ontologies` and `PUT /v1/gb/ontologies/{name}` take `"file": "market.owl"` instead of `content`, a file of the `ontology_dirs` (the first directory that has it). Nothing is scanned or loaded at startup, the file is named in the request, and the ontology belongs to the `user_id` of the request. `file` must be a plain file name (`400` otherwise, or when no directories are configured), `404` if no directory has it.
- `serve --training-dataset-dir` and `--training-eval-dir` can be repeated, and `serve --ontology-dir` is new (also repeatable). All of them override the config file's lists. `ServeOptions` has `TrainingDatasetDirs`, `TrainingEvalDirs` and `OntologyDirs` for embedding programs; they too override the config.
- The eval runners read their directories from the config file: `-config` (default `bin/gligabao.toml`) is read for `evals_knowledge_dirs` (knowledge) or `evals_training_dirs` (training). `-evals` can be repeated and still overrides the config; with neither, the directory is the old default. `./run_evals.sh` passes `-config` (`GLIGABAO_CONFIG` changes the file). A case's `text_file` is looked up in the eval directories in order.

### Changed
- Embedding programs: `ServeOptions.TrainingDatasetDir` and `TrainingEvalDir` (strings) are now `TrainingDatasetDirs` and `TrainingEvalDirs` (lists). The run params the knowledge and training runners store with a run have `evals_dirs` (a list) instead of `evals_dir`.
- The server's training evals skip a directory of `evals_training_dirs` that does not exist (it has no evals) instead of failing to load the evals, so a list can name directories that exist only in some deployments.
- The container image copies `bin/ontology` to `/app/ontology` (so `"file"` works with the config's `ontology_dirs`), and its `CMD` passes `--training-dataset-dir /data/datasets_training`, so uploaded datasets stay on the `/data` volume (the config's `datasets_training_dirs` would resolve to `/app/datasets_training`, not writable there).
- Internal: `training.Dirs.Datasets` and `Evals` are lists, as are the directory arguments of the training dataset and eval functions; `api.Register` takes `api.WithOntologyDirs(...)`.

## [0.99.0] - 2026-10-27

### Added
- The directory of trained models' exports can be set in the configuration file: `trained_model_export_dir` in `gligabao.toml` (the same as `serve --trained-model-export-dir`, which still overrides it, and `ServeOptions.TrainedModelExportDir` for embedding programs). Default: a `trained_models` directory next to the database file.

### Changed
- Internal: the directories of the training endpoints (datasets, evals, exported models) are no longer part of the database configuration (`db.Config` / `db.DB`, which only stored them for the server to read back). They are a `training.Dirs` value that the server takes in `server.Options.TrainingDirs`, with the defaults (next to the SQLite file) resolved in one place, `Dirs.WithDefaults`. No change in behaviour, flags or defaults.

## [0.98.0] - 2026-10-26

### Added
- The command-line interface is now a public package, `github.com/deepblue-phoenix/gligabao/cli` (it was `internal/cli`). A program that embeds gligabao builds its command tree with `cli.New(cli.Options{...})` and gets `serve`, `version`, `--config` and every flag of `gligabao serve` without redefining them; it only adds its own flags and commands (on the returned `Root` and `Serve` commands) and customizes `ServeOptions` through `Options.ServeOptions`. A flag added to `gligabao serve` later reaches such programs with no change on their side.

## [0.97.0] - 2026-10-25

### Added
- Admin UI extensions: a program that embeds gligabao can add views (each with a button in the nav on the left) and buttons, modals and blocks inside the existing views to the admin UI, with plain JavaScript and no build step. `ServeOptions.AdminExtensions` takes a list of `gligabao.AdminExtension{Name, Assets, Handler}`: `Assets` is an `fs.FS` with an `index.js` ES module (and optionally `style.css`), `Handler` an optional `http.Handler` that is the extension's own backend. They are validated at startup and served only with the admin UI on: `GET /v1/gb/admin/api/extensions` lists them, `/v1/gb/admin/ext/<name>/` serves the files, `/v1/gb/admin/ext/<name>/api/` is the handler. The admin page loads them before it draws and gives each a `gb` object (`addView`, `slot`, `api`, `json`, `button`, `modal`, `confirm`, `alert`, `escape`); one that fails to load is reported and skipped.
- Slots, the places in the existing admin UI that extensions can add to: `training.model.actions` (the box that opens on a click on a trained model: add buttons, given the model), and a `.top` and `.bottom` block in every built-in view (`main`, `evals`, `timeseries`, `inferences`, `remember_runs`, `training`) and `topbar` next to the health badges.
- The admin UI's palette is available as CSS variables (`--gb-bg`, `--gb-surface`, `--gb-border`, `--gb-text`, `--gb-muted`, `--gb-accent`, `--gb-ok`, `--gb-err`) for the stylesheets of extensions.
- [docs/admin-extensions.md](docs/admin-extensions.md): how to write an extension (quick start, registering it, the `gb` API, views, slots, a backend, styling, how it works, caveats), and `admin_ui/gligabao-admin.d.ts`, the types of `gb` for editor autocomplete.

### Changed
- Admin UI: the view switching moved from `main.ts` to a new `views.ts` and also handles views and buttons added after the page was built; the page starts after the extensions have loaded (a few milliseconds with none).

## [0.96.1] - 2026-10-24

### Changed
- New page [docs/model-exporting.md](docs/model-exporting.md): how model exporting works inside (the flow of an export request, fetching and verifying the weights, the `gb_trained_model_exports` record, the HTTP API and its statuses, system events, limits) and the contract of the exporter hook. [docs/hooks.md](docs/hooks.md#trained-model-exporters) links to it and its example uses a neutral exporter name.

## [0.96.0] - 2026-10-24

### Added
- Trained model exporters, a new hook: `ServeOptions.TrainedModelExporters` takes a list of `gligabao.TrainedModelExporter{Name, Export}`. `POST /v1/gb/training/models/{id}/exports` with `{"exporter", "options"?}` fetches the model's files from the sidecar to a local directory and calls the named exporter's `Export` callback with it (`ExportTrainedModelRequest`: the trained model, `Dir`, the options, an export ID; result: `Ref` and `Details`), waits for it (synchronous) and answers with the stored export; `502` if fetching or the exporter failed. What an exporter does with the files (upload them to a third party, ...) is up to the embedding program. Exporters are validated at startup. Documented in [docs/hooks.md](docs/hooks.md#trained-model-exporters).
- `GET /v1/gb/training/models/{id}/exports`: the exports of a model, newest first, failed ones too. They are stored in the new table `gb_trained_model_exports` (exporter, options, status running/completed/failed, directory, ref, details, error, duration), created automatically; a system event `training.model_export.completed` / `.failed` is emitted for each.
- The files of an exported model are fetched into `<trained model export dir>/<job id>` (`serve --trained-model-export-dir`, `ServeOptions.TrainedModelExportDir`, default `trained_models` next to the database file). Every file is checked against the size and sha256 recorded for the model; files already there and intact are reused by the next export. gligabao never deletes the directory.
- Sidecar: `GET /models/file?model_path=trained/<job id>&file=<path>` streams one file of a trained model from the Modal models volume (`ai_train/providers/modal/read_model_file.py`), so the Go server can fetch weights; `400` for an invalid path, `404` for a missing file, `502` if it cannot be read. Redeploy nothing on Modal: the sidecar reads the volume itself.

## [0.95.0] - 2026-10-24

### Added
- Gemma 4 31B (`google/gemma-4-31B-it`) as a third model size, `31b`: train and infer functions on the sidecar (`train_31b`, `infer_31b`, default GPU `a100-80gb`), accepted by the training API (`model_size`) and offered in the admin UI training size picker.

## [0.94.3] - 2026-10-23

### Fixed
- Admin UI, audit Open dialog: the little square of a case scored 0 / 5 in the top strip, and the left border of its card, are magenta too (only the score text was).

## [0.94.2] - 2026-10-23

### Changed
- Admin UI, audit dialog: a case scored 0 / 5 in an audit is shown in bold magenta, in the Open view too (it was only in the inline answers table).

## [0.94.1] - 2026-10-23

### Changed
- Admin UI, audit dialog: the past audits no longer have the "Answers, lowest score first" expander; their answers are viewed with the Open button. Evals keep it.

## [0.94.0] - 2026-10-23

### Changed
- Admin UI, training view: the per-model Prompt, Audit and Eval columns are gone. Clicking a trained model's row opens a small box next to the click with the actions: info, prompt, eval, audit (in that order). It closes on an outside click, Escape, scroll or after picking an action.

## [0.93.0] - 2026-10-23

### Changed
- Admin UI, training view: the "Training details and files" block moved out of the trained model's eval dialog into its own dialog, opened by an info-icon button next to the `Prompt` button.

## [0.92.3] - 2026-10-23

### Changed
- Admin UI, training view: trained models get a `Prompt` button (new column) that opens a separate Prompt dialog with the warm indicator, the inference form and the past requests. The model's own dialog now only holds its training details, files and evals.

## [0.92.2] - 2026-10-23

### Changed
- Admin UI, training view: the `Eval` button of a trained model no longer starts an eval right away. It opens the model's dialog at its Evals section, and the eval only runs when you click `Run` (the form's button, formerly `Eval`).

## [0.92.1] - 2026-10-23

### Changed
- Admin UI, training view, datasets: only the right-aligned `Upload` button is shown; it opens a small modal with the file chooser (and `Upload` / `Cancel`). Upload errors show in the modal.
- Admin UI: the native `Choose file` button of file inputs is now styled like the other buttons.

## [0.92.0] - 2026-10-23

### Added
- Admin UI, audit dialog: a square in the dialog's head shows the state of the Modal inference container of the audited model. It blinks while the container starts and loads the model (an audit is running but has no answer yet), is green once the model is loaded, amber when a container holds another model and grey when none is up.

### Changed
- Admin UI: the `Cancel` / `Cancel audit` / `Cancel eval` buttons of audit and eval runs are now just `Stop`.
- Admin UI, audit dialog: the help text under `Training data audit` is now behind a `?` button that opens a small modal.

## [0.91.1] - 2026-10-23

### Changed
- `run_tests.sh` now runs the Go tests and then the Python tests (sidecar and `clients/py`). `-g`/`--go` and `-p`/`--py` select only one of them; without flags both run.

## [0.91.0] - 2026-10-23

### Changed
- Admin UI, training view: the help texts of `Trained models` and `Datasets` are now behind a `?` button that opens a small modal, instead of a block of text under the heading.
- Admin UI, training view: the `Selected job` section is hidden until a job is picked from the list. The newest or running job is no longer selected automatically when the list loads.

## [0.90.0] - 2026-10-23

### Changed
- **`internal/evals_neo` is now `internal/evals`** (package `evals`, import path `.../internal/evals`), and the `gb_neo_*` tables are `gb_*` (e.g. `gb_neo_eval_runs` is `gb_eval_runs`; the indexes lose `neo` too). The `neo` part only marked the migration from the old evals, which is complete. **Breaking:** the eval database must be rebuilt, nothing is migrated. `./run_evals.sh`, the docs and the comments follow.

## [0.89.0] - 2026-10-23

### Changed
- The default keep-alive of the GPU inference containers (`SIDECAR_INFER_IDLE_TIMEOUT_S`) is now `60` seconds instead of `300`: a container shuts down 1 minute after its last request, which costs less idle GPU time, and a request after that pays the model load again. Set `SIDECAR_INFER_IDLE_TIMEOUT_S` to keep the old behaviour. Redeploy the Modal app for the change to take effect. (The training container is not kept warm.)

## [0.88.1] - 2026-10-23

### Changed
- Docs and comments no longer point at the removed `internal/evals`: `docs/evals/experiments.md` and `docs/evals/training.md` describe the `gb_neo_*` tables, statuses (`pending`, `running`, `completed`, `errored`, `cancelled`) and verdicts of `internal/evals_neo` (and its runner and manager in place of the old `experiments` package and `evalsdb` functions), `docs/listings/db-tables.md` lists only the current tables, and the links in `README.md`, `docs/remember.md`, `docs/web-search.md`, `docs/evals/knowledge.md` and `docs/training/audit.md` lead to `internal/evals_neo`. The package comments of `internal/evals_neo`, `internal/training_audits`, `internal/training/db` and `internal/db/relational` are updated the same way.

## [0.88.0] - 2026-10-23

### Removed
- **The old evals package `internal/evals`** (`db` with its sqlite and postgres backends, `experiments`, `knowledge`, `training`, and their READMEs), which nothing of the server used any more; `internal/evals_neo` replaces it. **Breaking:** `internal/db.Open` (and the server and the test helper `dbtest`) no longer create the old tables (`gb_experiments_defs`, `gb_experiment_*`, `gb_knowledge_case_meta`, `gb_training_eval_run_meta`, `gb_training_case_meta`); an existing database keeps them with their data, and they can be dropped by hand. The docs and listings point to `internal/evals_neo`.

## [0.87.0] - 2026-10-23

### Changed
- **Admin UI: the evals views show neo's status and verdict as they are**, not statuses derived from them (`passed`/`failed`/`error`). **Breaking (the JSON of `/v1/gb/admin/api/evals`):** a run and a case run have `status` (`pending`, `running`, `completed`, `errored`, `cancelled`), a case run also `verdict` (`pass`, `fail`, absent if none), `score` and `error`, and a run has `error` and `counts` (`core.Counts`) in place of `total_cases`, `passed_cases`, `failed_cases`, `error_cases` and `judged_cases`. The overview has `totals.counts` in place of `executions`, `passed`, `failed`, `errors` and `judged`, and an experiment has `runs_by_status` in place of `runs_passed` and `runs_failed`. The tables show a status badge and (for a case) a verdict badge, the counts in neo's words (`3 pass · 1 fail · 1 errored of 5`), and a case's detail has its status, verdict and score. The history of a case plots its verdict (or its status if it has no verdict). 
- **Admin UI and training API: the training views use neo's status and verdict too.** **Breaking (the JSON of the evals under `/v1/gb/training`):** a run's `status` is `pending`, `running`, `completed`, `errored` or `cancelled` (was `error` for errored), and the run has `counts` (`core.Counts`) in place of `num_examples` and `num_errored`; a case's `status` is `completed`, `errored` or (live feed only, an answer the judge has not scored yet) `running` (was `judged`, `error`, `answered`), and a completed case has `verdict` (`pass` if the score is at least `core.TrainingPassScore`, else `fail`). `training.RunRunning` and the other `Run*`/`View*` status constants are gone: the views use `core.Status`. The experiment runs' `status` is the same. The live view and the lists of evals and audits in the models dialog show the status badge, the verdict badge and the score as they come (an audit has no verdict, only a score); the audits' JSON already had these statuses, so the UI no longer converts them.

## [0.86.0] - 2026-10-23

### Changed
- **The admin UI and the training API use `internal/evals_neo`.** The evals views of the admin UI (`/v1/gb/admin/api/evals/*`, the web-search totals of the eval database) read the `gb_neo_*` tables, so the runs of `./run_evals.sh` show in it again. An experiment is an `ExperimentDef`, its run an `ExperimentRun`, and a run's cases are those of its knowledge eval runs; the JSON the UI reads is unchanged (the statuses `passed`/`failed`/`error` are made from the status and verdict of the case run). Eval runs that were run on their own (`-experiment ""`) belong to no experiment and are not listed. The evals of trained models (the **Eval** button, `POST /v1/gb/training/models/{id}/evals` and the other eval and experiment endpoints under `/v1/gb/training`) are run and stored through the `training` runner of `internal/evals_neo`; the JSON of the endpoints is unchanged. An eval started with `experiment` is an experiment def with its own experiment run; the experiments listed are those that have a training eval. **Breaking:** the old tables are no longer read, so the eval runs made before this version do not show.
- `internal/evals_neo/runner`: `EvalOptions.OnCreated` (called when the eval run is recorded) and `EnsureExperimentWith` (stores the experiment's params).
- `internal/evals_neo/db`: the reads of the runs, written once for both backends (`db.Reads`, in `db.Store`): the experiment defs, eval defs and case defs, the runs of an experiment, the eval runs (with their metadata) of an experiment run, an experiment or a trained model, the case runs of an eval run (with their metadata and trajectories), the history of a case, trajectories by ID, and `FailRunning`.

### Added
- New package `internal/evals_neo/training` `Manager` (with `feed.go`, `view.go`, `options.go`) and `internal/evals_neo/training/api` (the handlers), which replace the eval manager of `internal/training` and `internal/api/training`.

### Removed
- From `internal/training`: the old run manager (`RunManager`, `RunEvent`, `JoinExperiment`, `HasEval`), and from `internal/api/training` the eval handlers. The old eval runner command `internal/evals/training/cmd` (`./run_evals.sh` runs the one of `evals_neo`). The rest of the old `internal/evals` is still in the tree and used by nothing of the server.

## [0.85.0] - 2026-10-22

### Added
- **`internal/evals_neo`: the runners.** New packages: `runner` (puts the evals of the JSON files in the database as defs, `SyncEval` and `EnsureExperiment`, and records experiment runs, eval runs and case runs as they go: `RunEval`, `EvalRecorder`, `ExperimentRecorder`), `evalfile` (reads the eval files), `knowledge` (the executor of the knowledge evals, ported from the old runner: the checks of the stored facts, chunks, web searches and timeseries, and of the answer), `training` (the executor of the training evals: the engine that asks the model and has the LLM judge the answers in batches), and the commands `knowledge/cmd` and `training/cmd`. `internal/evals_neo/db` got the functions the runners need, in both backends and as `db.Store`: the updates of defs and runs, and the reads of an eval def (with its cases and their metadata) and of an experiment def. A run is stored when it starts and rewritten as its cases finish. The runners do not use anything of `internal/evals` (the old code is still in the tree, to be removed).
- **`internal/evals_neo` is the primary evals package.** `internal/db.Open` (and so every database of the server and of the runners) applies its schema (the `gb_neo_*` tables) next to the old one, `./run_evals.sh` runs its commands, and it has a `README.md`. The tables are listed in `docs/listings/db-tables.md`.

### Changed
- **`./run_evals.sh` runs the new runners**: `go run ./internal/evals_neo/knowledge/cmd` and `go run ./internal/evals_neo/training/cmd`. The flags are those of the old ones, except: the knowledge runner's `-experiment ""` runs the evals on their own (an eval run needs no experiment), and the training runner has no `-no-store` (a run is always stored); the dataset of a knowledge case is named after the eval run (`<case>_run<eval run id>`, was the experiment run's id). **Breaking:** the runs are stored in the `gb_neo_*` tables, which the admin UI's evals views and the Eval button of the server do not read yet (they still use the old tables, which the runners no longer write): a run of `./run_evals.sh` does not show in the admin UI until those move over.
- The old evals code (`internal/evals`, its READMEs and the docs of its tables) is marked as old.

## [0.84.0] - 2026-10-22

### Added
- **`internal/evals_neo`: the verdict of a training case.** A training case run now has a pass/fail verdict: pass if the judge's score (0 to 5) is at least `core.TrainingPassScore` (4), fail if it is below; a case that was not judged has none. The score stays as the detail. The pass/fail counts of a training eval run agree with its `TrainingEvalRunMeta` (`Passed` = `NumCorrect`, `Failed` = `NumPartial` + `NumIncorrect`). This is documented in `core`; nothing enforces or uses it yet.

## [0.83.0] - 2026-10-22

### Changed
- **`internal/evals_neo`: one naming pattern for the per-type tables.** They are all `gb_neo_<kind>_meta_<type>` now: `gb_neo_case_def_meta_<type>` (unchanged), `gb_neo_eval_runs_meta_<type>` (was `gb_neo_eval_runs_<type>_meta`) and `gb_neo_case_runs_meta_<type>` (was `gb_neo_<type>_case_run_meta`). The knowledge trajectories table is `gb_neo_case_runs_meta_knowledge_trajectories` (was `gb_neo_knowledge_case_run_trajectories`), and the indexes follow (`idx_gb_neo_eval_runs_meta_training_model`, `idx_gb_neo_case_runs_meta_knowledge_trajectories_tid`). **Breaking:** data in the old tables is not read any more. Nothing uses these tables yet.

## [0.82.0] - 2026-10-22

### Changed
- **The audit tables lose the `neo` part of their names:** `gb_neo_training_audit_runs` is now `gb_training_audit_runs` and `gb_neo_training_audit_case_runs` is `gb_training_audit_case_runs` (and the index `idx_gb_neo_training_audit_runs_model` is `idx_gb_training_audit_runs_model`). **Breaking:** audits stored in the old `gb_neo_*` tables are not read any more (copy them over or drop them). An existing database that still has the tables of the removed old audit code, also called `gb_training_audit_runs`, must have them dropped first, or `CREATE TABLE IF NOT EXISTS` will keep their old columns.

## [0.81.1] - 2026-10-22

### Changed
- **Docs: the listings have a `TRAINING_AUDIT` section.** `docs/listings/go-structs.md` and `docs/listings/db-tables.md` now list the structs and tables of the training data audits (`internal/training_audits`) in a section of their own instead of among the other training entries.

## [0.81.0] - 2026-10-22

### Changed
- **Admin UI: audits have their own dialog.** The **Audit** button in each row of the trained models list now opens an `audit` dialog for that model (the form with the number of questions and the judge's batch size, and the list of its past audits, with their live view), so a model is audited in place. The audit section is no longer in the trained model's dialog, which keeps the inference and the evals. The **Eval** button is unchanged.

## [0.80.0] - 2026-10-22

### Removed
- **The old training data audits are removed**: the package `internal/training/audits`, the `AuditManager` and the audit endpoints of `internal/api/training`, the audit functions of `internal/evals/db` (`RecordTrainedModelAuditRun`, ... in both backends), `evalsdb.TrainedModelKindAudit`, the `Dataset` and `DatasetSHA256` fields of `evalsdb.TrainedModelRun`, and `trainingeval.ParseExamples`. The audits are those of `internal/training_audits` (see 0.79.0). The schema of the evals no longer creates the tables `gb_training_audit_runs` and `gb_training_audit_cases`; an existing database keeps them with the audits made before 0.79.0 (the server does not read them), and they can be dropped by hand. The docs (`docs/training/audit.md`, the listings) describe the new audits.

## [0.79.0] - 2026-10-22

### Changed
- **The server and the admin UI now use the new training data audits of `internal/training_audits`.** The routes are the same (`POST/GET /v1/gb/training/models/{id}/audits`, `GET /v1/gb/training/audits/{id}`, `.../events`, `.../cancel`), but they are served by `training_audits/api`, and the audits are stored in the new tables `gb_neo_training_audit_runs` and `gb_neo_training_audit_case_runs` (created when the database is opened, `training_audits.Open`). **The audits that were run before this version stay in the old `gb_training_audit_*` tables and no longer show in the admin UI.** The JSON of an audit changed: counts are in `counts` (`total`, `errored`, ...), the cost is `cost_usd`, a failed audit is `errored`, and the case runs are `case_runs` with `judge_score` and `judge_reason`. The admin UI turns this into the shape it shows evals in (`auditRun`, `auditCase` in `eval_live.ts`). The evals are unchanged. The old audit code (`internal/training/audits`, `AuditManager`) is no longer used by the server.

## [0.78.0] - 2026-10-22

### Added
- **`internal/training_audits`: the engine, the manager, the live feed and the HTTP handlers of the new training data audits.** `Run` is the engine: it asks the trained model the dataset's examples one at a time, has the LLM judge grade them in batches (one call per batch, a retry for what it missed), and reports each batch as `core.AuditCaseRun`s and a `Progress` (the run's `Counts`, `AuditStats` and cost); it gives up after repeated batches of only failures, and when it is cancelled it hands over the cases it had reached as `cancelled`. `Manager` starts an audit (`Start`, with `Options`), runs it in the background, one at a time per trained model, writes every batch (its case runs and the run's updated progress) in one transaction, and lists, reads (`GetRun`, `GetRunWithCaseRuns`), cancels and watches (`Watch`) the audits; audits that a restart interrupted are marked errored when it is made. `Ops` (`SQLiteOps`, `PostgresOps`, `OpsFor`) is the database backend it works over. `Feed` and `Event` are the live feed: a snapshot, an `answer` per question as the model answers it, a `batch`, an `end`. The package `training_audits/api` has the handlers (`StartOf`, `ListOf`, `GetOf`, `CancelOf`, `EventsOf` and `Register` to put them on a mux under a path prefix); `GET /audits/{id}` answers the `AuditRun` with its `case_runs`.
- **`internal/training_audits/db`: update, finish, get and list, in both `db/sqlite` and `db/postgres`.** `UpdateAuditRun` (status, counts, statistics, cost, times), `FinishAuditRun`, `FailRunningAuditRuns` (also ends the pending ones), `GetAuditRun`, `ListAuditRuns` and `ListAuditCaseRuns`, plus `db.ErrNotFound` and `db.Store`, the functions of a backend as one value (`sqlite.Store`, `postgres.Store`). `db.Execer` gains `QueryContext`. Case runs are append-only, so there is no update of a case run.
- `internal/training_audits/audittest`: databases per backend (SQLite, and PostgreSQL with `POSTGRES_TEST_DSN`), a fake sidecar, a fake judge and a dataset directory for the tests. Nothing in the server uses the package yet, and the old audits keep working unchanged.

## [0.77.0] - 2026-10-22

### Added
- **`internal/training_audits`: new package for the training data audits of trained models, detached from the evals.** Built like `internal/evals_neo` (same vocabulary, rules and layout) but sharing no types, tables or code with it or with `internal/evals`. `core` has the data model: `AuditRun` (one audit of a trained model over one dataset, with its `AuditStats` and `Counts`) and `AuditCaseRun` (one dataset example, asked and judged; `Status` is a lifecycle, the judge's `JudgeScore` is the outcome). `db/sqlite` and `db/postgres` have the schema (tables `gb_neo_training_audit_runs` and `gb_neo_training_audit_case_runs`, next to the old `gb_training_audit_*` ones) and `CreateAuditRun` and `CreateAuditCaseRun`. The dataset loader of `internal/training/audits` is ported as `LoadDataset` (with its own `Example` and `ParseExamples`). Only creation for now; nothing uses the package yet, and the old audits keep working unchanged.

## [0.76.0] - 2026-10-22

### Added
- **`internal/evals_neo`: eval runs have type-specific metadata, like case runs.** The statistics of an eval run as a whole differ between the eval types, so each type has its own struct and table keyed by the eval run, reached through `EvalRun.Meta`: `KnowledgeEvalRunMeta` (`gb_neo_eval_runs_knowledge_meta`: chunks, ATIF steps, remember and recall calls and their cost, checks passed and failed) and `TrainingEvalRunMeta` (`gb_neo_eval_runs_training_meta`: trained model, judge model, answered, judged, correct, partial and incorrect counts, score sum, judge batches, calls, tokens and cost). New `CreateKnowledgeEvalRunMeta` and `CreateTrainingEvalRunMeta` in both `db/sqlite` and `db/postgres`.

### Changed
- **`internal/evals_neo`: `EvalRun` has a `Type`, tied by composite keys.** `gb_neo_eval_runs` has a `type` column that must be that of its eval def, and a case run must have the type of its eval run. `CreateEvalRun` writes it, so callers must set `EvalRun.Type`.

## [0.75.0] - 2026-10-22

### Added
- **`internal/evals_neo/db`: functions that create the records of each table, in both `db/sqlite` and `db/postgres`.** `CreateEvalDef`, `CreateCaseDef`, `CreateKnowledgeCaseDefMeta`, `CreateTrainingCaseDefMeta`, `CreateExperimentDef`, `CreateExperimentDefEval`, `CreateExperimentRun`, `CreateEvalRun`, `CreateCaseRun`, `CreateKnowledgeCaseRunMeta`, `CreateKnowledgeTrajectory` and `CreateTrainingCaseRunMeta`. Each writes one table, takes a `*sql.DB` or a `*sql.Tx` (`db.Execer`), and sets the generated ID and the times on the record. Only creation for now; nothing uses them yet.

## [0.74.0] - 2026-10-22

### Changed
- **`internal/evals_neo`: the type-specific parts of cases move out of the universal case tables into one metadata table per eval type.** `CaseDef` and `CaseRun` (tables `gb_neo_case_defs`, `gb_neo_case_runs`) keep only what every eval shares, plus a `type` column tied to the eval's type by a composite key; their `Input`, `Expected`, `Params`, `Output` and `Details` payloads are gone (so is `core.Payload`). Each type has its own structs and tables keyed by the case def or case run: `gb_neo_case_def_meta_knowledge`, `gb_neo_case_def_meta_training`, `gb_neo_knowledge_case_run_meta` (with `gb_neo_knowledge_case_run_trajectories`) and `gb_neo_training_case_run_meta`, reached through `CaseDef.Meta` and `CaseRun.Meta`. A new eval type adds its own tables. Nothing uses them yet.

## [0.73.0] - 2026-10-22

### Added
- **`internal/evals_neo/core`: the case structs of the knowledge and training evals (`KnowledgeCase`, `TrainingCase`).** They mirror the JSON cases of `bin/evals_knowledge` and `bin/evals_training`, and are split into the parts a `CaseDef` stores in `Input`, `Expected` and `Params` (`KnowledgeInput`, `KnowledgeExpected`, `KnowledgeParams`, `TrainingInput`, `TrainingExpected`). The Types are `TypeKnowledge` and `TypeTraining`. Nothing uses them yet.

## [0.72.0] - 2026-10-22

### Added
- **`internal/evals_neo/db`: the schema of the rewritten evals data model, as `db/sqlite` (`sqlite.Schema`) and `db/postgres` (`postgres.Schema`).** It has tables `gb_neo_eval_defs`, `gb_neo_case_defs`, `gb_neo_experiment_defs`, `gb_neo_experiment_def_evals` (the ordered eval list of an experiment), `gb_neo_experiment_runs`, `gb_neo_eval_runs` and `gb_neo_case_runs`. Nothing applies it yet.

### Changed
- **The structs of `internal/evals_neo` are moved to the subpackage `internal/evals_neo/core`.** Nothing used them yet.

## [0.71.0] - 2026-10-22

### Added
- **New package `internal/evals_neo`: the data model of the rewritten evals system (structs only, no DB tables or logic yet).** It defines `ExperimentDef`, `ExperimentRun`, `EvalDef`, `EvalRun`, `CaseDef` and `CaseRun`, with one ID type per record, a run lifecycle `Status` kept apart from the case `Verdict`, and `Counts`/`Timing` shared by the runs. Nothing uses it yet; the existing `internal/evals` is unchanged.

## [0.70.0] - 2026-10-21

### Changed
- **The `gb_experiment_case_results` table is renamed `gb_experiment_case_runs` (breaking: SQLite and PostgreSQL eval databases must be rebuilt, nothing is migrated).** It holds one row per case in a run (the outcome of that case execution); its indexes are renamed `idx_gb_experiment_case_runs_run` and `idx_gb_experiment_case_runs_training_case` too. The `case_result_id` columns of the tables that reference it are now `case_run_id`. The admin API follows: the `case_results` JSON fields are `case_runs`, `case_result_id` is `case_run_id`, and `GET /v1/gb/admin/api/evals/case_results/{id}/graph` is `.../evals/case_runs/{id}/graph` (the admin UI is updated). In Go, `ExperimentCaseResult` is `ExperimentCaseRun` (and `RecordExperimentCaseResult`, `GetExperimentCaseResultDataset`, `experiments.CaseResultDataset`, `Run.CaseResults` are renamed alike). The queries and the docs use the new names.

## [0.69.0] - 2026-10-21

### Changed
- **The `gb_experiment_trajectories` table is renamed `gb_experiment_case_runs_trajectories` (breaking: SQLite and PostgreSQL eval databases must be rebuilt, nothing is migrated).** It holds the ATIF trajectory references of one case result (one case in one run); its index is renamed `idx_gb_experiment_case_runs_trajectories_tid` too. The queries and the docs use the new name.

## [0.68.0] - 2026-10-21

### Changed
- **The `gb_experiments` table is renamed `gb_experiments_defs` and `gb_experiment_cases` is renamed `gb_experiment_cases_defs` (breaking: SQLite and PostgreSQL eval databases must be rebuilt, nothing is migrated).** `gb_experiments_defs` holds the definition of an experiment (name, description, config) and `gb_experiment_cases_defs` the cases that belong to it (one row per distinct case); `gb_experiment_runs` and `gb_experiment_case_results` hold the executions. The foreign keys, the queries and the docs use the new names.

## [0.67.0] - 2026-10-20

### Changed
- **The training API works with one database (breaking).** `training.Database` (a named database with its datasets directory) and `training.Pick` are gone: the training code and the handlers take the `*sql.DB` and, where they need it, the training datasets directory (`StartJobOf(db, datasetDir, sc, mon)`, `UploadDatasetOf(db, datasetDir)`, `NewAuditManager(ctx, db, datasetDir, sc, llm)`, `training.NewMonitor(ctx, db, sc)`, ...). The `?db=` parameter of the `/v1/gb/training/` endpoints is removed (it was always the one main database; it is now ignored), and the feed events no longer carry a `db` field. A dedicated training database can be added back when it is needed.

## [0.66.3] - 2026-10-19

### Changed
- Internal reorganisation: everything in `internal/api/training` that is not HTTP moved to `internal/training` (package `training`, next to `internal/training/db`): the datasets directory and its manifests (`DiscoverDatasets`, `ListDatasets`, `SaveDataset`, `PreviewDataset`, `ValidateDataset`), starting and stopping jobs (`StartJob`, `StopJob`, `JobHistory`), the `Monitor`, inferences (`Infer`), and the evals and audits (`RunManager` with its `RunStream`, `Generator`, `CaseRow`, `RunProgress`, `LoadModelDataset`). Failures are `*training.Error` with a `Kind`, which the handlers turn into the HTTP status. `internal/api/training` now only holds the handlers (`StartJobOf`, `EvalManager`, ...), which read the request and write the response; they take `training.Database`, `*training.Monitor` and `training.Inferer` from the new package. Endpoints and behaviour are unchanged.

## [0.66.2] - 2026-10-19

### Changed
- Internal reorganisation: all database code for training moved from `internal/db/relational` to a new `internal/training/db` package (with `model`, `sqlite` and `postgres` subpackages): the training job runs and their events, the trained models and their inferences, and the training datasets. Their tables and types (`trainingdb.TrainedModel`, `trainingdb.TrainingJobRun`, ...) are no longer part of `relational`; `db.Open` applies the training schema after the relational and graph ones and before the eval ones. Tables, API and behaviour are unchanged.
- Internal reorganisation: all database code for evals moved from `internal/db/relational` to a new `internal/evals/db` package (with `model`, `sqlite` and `postgres` subpackages, like `internal/db/graph`): the experiments, runs and case results of the knowledge evals, and the eval and audit runs of trained models. Their tables and types (`evalsdb.TrainedModelRun`, `evalsdb.ExperimentRun`, ...) are no longer part of `relational`; `db.Open` applies the eval schema after the relational and graph ones. Tables, API and behaviour are unchanged.

## [0.66.1] - 2026-10-19

### Changed
- Internal reorganisation: the eval code now lives in one package tree, `internal/evals/` (`experiments/`, `knowledge/`, `training/` and the evals README). The top-level `evals/` directory and `internal/experiments` are gone. The eval runners are started as `go run ./internal/evals/knowledge` and `go run ./internal/evals/training/cmd` (`./run_evals.sh` already does this); behaviour is unchanged.

## [0.66.0] - 2026-10-19

### Removed
- The unused placeholder table `gb_items` (SQLite and PostgreSQL). Nothing read or wrote it. It is dropped from existing databases on startup.

## [0.65.1] - 2026-10-19

### Changed
- The ontology parser moved from `internal/ontology` to `internal/memory/memory_ontology`, as it is part of the memory subsystem. The package is now `memory_ontology`. No behaviour change.

## [0.65.0] - 2026-10-19

### Changed
- The example ontology moved from `ontology/` to `bin/ontology/`, next to the other static definitions (evals, datasets, config). Nothing reads it at runtime; docs and tests point to the new path.

## [0.64.1] - 2026-10-19

### Fixed
- Stale comments and the `-eval` flag help of `evals/knowledge` (and the `Eval` field comment in `internal/experiments`) said the eval is the JSON file name; they now say it is the `name` set in the file. No behaviour change.

## [0.64.0] - 2026-10-19

### Changed
- The eval files of `bin/evals_knowledge/` and `bin/evals_training/` are no longer a bare list of cases but a map: `name` is the name of the eval the file represents and `cases` the list of cases. The loaders (`evals/knowledge`, `evals/training`) read the eval name from `name` (required) instead of the file name; old list-style files are rejected.

## [0.63.1] - 2026-10-19

### Fixed
- Stale paths of the eval definitions in comments and READMEs (`evals/README.md`, `evals/training/README.md`, `evals/training/dataset.go`) now say `bin/evals_training/*.json`, and the admin UI's live-view comment names the real `/v1/gb/training/evals/<id>/events` URL. No behaviour change.

## [0.63.0] - 2026-10-19

### Changed
- `./run_evals.sh -t training` has defaults: without `-model` it evaluates the newest trained Gemma 4 4B model of the database (`evals/training/cmd` takes `-model 0`/no flag as "the newest 4b"), and it starts the sidecar the way `./run_server_local.sh` does (if nothing answers on the health endpoint and the URL is local; it is stopped when the run ends). Listing evals or models does not start it.

## [0.62.0] - 2026-10-19

### Changed
- **The eval definitions are static files in `bin/`, no longer embedded in the code (breaking for `go`-API users of `evals/training`, and for deployments of the server).** `evals/knowledge/evals/` is now `bin/evals_knowledge/` (the evals and `corpus/`) and `evals/training/evals/` is `bin/evals_training/`, next to `bin/datasets_training/`; both are tracked in git like it.
  - The knowledge runner reads `bin/evals_knowledge` by default (`-evals dir` for another one; it is relative to the working directory, so run it from the repository root, as `./run_evals.sh` does).
  - The training evals are read from disk by the server, from the new `serve --training-eval-dir` (`ServeOptions.TrainingEvalDir`, `db.Config.TrainingEvalDir`; default: `evals_training` next to `--db`, so `bin/evals_training`) and by the command (`-evals dir`, default `bin/evals_training`). The Docker image copies them to `/app/evals_training` and passes the flag. In Go, `trainingeval.LoadEvals`, `Evals` and `EvalNames` take the directory, and `training.NewEvalManager` and `DefinitionsOf` too.

## [0.61.0] - 2026-10-19

### Changed
- **The server's eval of a trained model now runs real evals, and evals and audits share their types, shapes and code (breaking for the eval database, the training API and the admin API: existing eval and audit databases must be recreated, nothing is migrated; SQLite and PostgreSQL).** The Eval button used to be the audit's twin: it only knew the `finetune_dataset` scheme (the model's own training dataset, what the audit does), through its own manager, tables and endpoints, while the embedded eval cases (`evals/training/evals/*.json`) were only run by the command line and never stored. An eval now asks the cases of an eval, which the model never saw; asking its own dataset is the audit's job alone.
  - **Evals have cases.** `POST /v1/gb/training/models/{id}/evals` takes `eval` (the name of an eval; default: the eval named like the model's training dataset) and `cases` (only the cases with these names), next to `limit`, `batch_size`, `judge_model`, `max_tokens`, `temperature` and `experiment`. `scheme` is gone (`finetune_dataset` is gone: use an audit), and so is the dependence on the training datasets directory: the cases are in the eval. `400` for an unknown eval or case, or when no eval is named like the model's dataset (and `eval` is not given). New `GET /v1/gb/training/eval-definitions` lists the evals the server can run, with their cases. The admin UI's Eval section has a choice of eval (the one named like the model's dataset is preselected), and shows each case's name.
  - **The command line stores its runs.** `./run_evals.sh -t training -model ID` does what the Eval button does (same eval choice, same questions, same judge) and stores the run, its cases and the judge's calls in the database, so the admin UI shows it; `-no-store` keeps the old print-only behaviour. Without `-e` it runs the eval named like the model's dataset. The stale claims in `evals/README.md` and the runner's comment that it works "as the Eval button does" are now true, and the README's description of an eval ("cases the model never saw") too.
  - **Names.** `case_name` of a training case result is now the case's name (as for a knowledge case), not the example's index in a dataset. The eval embedded as `world_knowledge_base.json` is `world-knowledge-base.json`: an eval is named after the training dataset it is about, so the eval and the dataset have the same name. `gb_training_eval_run_meta` has the run's `eval` instead of `scheme`, `dataset` (which repeated the eval column of the run's case results, with the dataset's name) and `dataset_sha256`; an eval run has no dataset any more (an audit still has `dataset` and `dataset_sha256`).
  - **One type and one set of field names for the two kinds of run.** `relational.TrainedModelEvalRun` and `TrainedModelAuditRun` are one `TrainedModelRun` (with `Kind` `eval` or `audit`, `Eval` for an eval and `Dataset`/`DatasetSHA256` for an audit), and `TrainedModelCaseResult` and `TrainedModelAuditCase` are one `TrainedModelCase` (with the case's `Name` for an eval's). The JSON of a case has `run_id` (was `eval_run_id` or `audit_run_id`), the run has `kind`. The detail endpoints answer `{"run": ..., "cases": [...]}` and the SSE events carry the run in `run`, for evals and for audits alike (was `eval` / `audit`; the lists stay `{"evals": [...]}` and `{"audits": [...]}`). `ListTrainedModelCaseResults` is `ListTrainedModelEvalCases`. In the Go API `training.EvalEvent` and `AuditEvent` are `RunEvent`; `EvalParams` and `AuditParams` are `RunParams`.
  - **One manager.** `EvalManager` and `AuditManager` are thin over one `runManager` (`internal/api/training/runs.go`, `runs_stream.go`) that holds the background runner, the list, detail, cancel and live-feed endpoints, which were two copies; only the endpoint that starts a run (where the cases come from) is specific to each. The judge calls are recorded as `training_eval_judge` (was `eval_judge`) and `training_audit_judge`, and their stage constants are one `StageJudge`; the log fields are `run_id` and `kind` (were `eval_run_id`, `audit_run_id`).
  - Docs: `docs/evals/training.md`, `docs/training/audit.md`, `evals/README.md`, `evals/training/README.md`, `docs/evals/experiments.md` and the schema comments describe the above.

## [0.60.0] - 2026-10-19

### Changed
- **Each status word and each counter of an eval means one thing (breaking for the eval database, the training API and the admin API: existing eval and audit databases must be recreated, nothing is migrated; SQLite and PostgreSQL).**
  - `failed` meant three things: a knowledge check did not hold, the model or the judge errored (training), or a run itself errored. Now `failed` is only a verdict about checks, so only the knowledge evals have it, and `error` means "could not be run to the end" everywhere. A training case is `judged` or `error` (was `failed`), a training eval or audit run is `running`, then `completed`, `error` or `cancelled` (was `failed`). The knowledge statuses are unchanged (case: `passed`, `failed`, `error`; run: `passed` or `failed`, `failed` when at least one case did not pass). The CHECKs of `gb_experiment_runs`, `gb_experiment_case_results`, `gb_training_audit_runs` and `gb_training_audit_cases` follow, so a database made by an older version rejects the new values. Runs that a restart interrupted are set to `error`.
  - Training counters: `num_failed` is `num_errored` (`gb_training_eval_run_meta`, `gb_training_audit_runs`, and `num_errored` in the eval and audit JSON of the API). Go: `TrainedModelRunFailed`/`TrainedModelCaseFailed` are `TrainedModelRunErrored`/`TrainedModelCaseErrored` (value `error`), `trainingeval.CaseFailed` is `CaseErrored`, `Progress.Failed` is `Errored`, `NumFailed` is `NumErrored`.
  - `passed_cases` of a training run was the number of *judged* cases (a count, not a pass) and `failed_cases` was "all the others", including the examples not reached after a cancel. `gb_experiment_runs` now has one count per case status and nothing else: `passed_cases`, `failed_cases`, `error_cases` and the new `judged_cases`, in `ExperimentRun` as `PassedCases`, `FailedCases`, `ErrorCases` and `JudgedCases` (JSON `error_cases`, `judged_cases`). Knowledge runs fill the first three, training runs the last two, the unused ones are 0. `total_cases` minus the four is the cases that were not reached. A knowledge run's `failed_cases` no longer includes the cases that errored (they are `error_cases`).
  - The admin API's eval totals have `errors` and `judged` next to `passed` and `failed`; the admin UI's run table has an *Errors* column, and the live view of an eval or audit shows `error` and "errored" where it showed `failed`.

## [0.59.1] - 2026-10-19

### Changed
- The status words of the runs and cases of a trained model are Go constants defined once, in `internal/db/relational/model/trained_model_status.go`, instead of one set for evals and another for audits: `TrainedModelRunRunning`, `TrainedModelRunCompleted`, `TrainedModelRunFailed`, `TrainedModelRunCancelled` (were `TrainedModelEvalRun...` and `TrainedModelAuditRun...`) and `TrainedModelCaseJudged`, `TrainedModelCaseFailed`, `TrainedModelCaseAnswered` (were also `TrainedModelAuditCase...`). The engine's `trainingeval.CaseJudged` and `CaseFailed` are those same constants, so evals, audits and the engine cannot use different words, and a typo does not compile. The words themselves, the API and the schema are unchanged.
- The admin UI's live view has the same words as TypeScript constants (`RunStatus`, `CaseStatus` in `eval_live.ts`) instead of string literals.

## [0.59.0] - 2026-10-19

### Changed
- **Evals and audits of trained models use the same statuses (breaking for the eval database and the training API: existing eval databases must be recreated, nothing is migrated; SQLite and PostgreSQL).** The two kinds of run share the engine and the admin UI's live view, but an eval's run ended `passed` and its cases were `passed` while an audit's run ended `completed` and its cases were `judged`. Now both use one vocabulary: a run is `running`, then `completed`, `failed` or `cancelled`, and a case is `judged` or `failed` (plus `answered` in the live feed, never stored).
  - Evals: the status of a finished eval and of a training experiment run is `completed` (was `passed`), and the status of a case is `judged` (was `passed`). The audits are unchanged. The CHECKs of `gb_experiment_runs` (training: `completed`, `failed`, `cancelled`) and `gb_experiment_case_results` (training: `judged`, `failed`) follow, so a database created by an older version rejects the new values.
  - Why: `passed` meant two things for training. In the database a case was `passed` when it was judged, even with a score of 0, while the eval CLI (`evals/training/cmd`) counted a case as passing only with a score of 4 or 5. The word now belongs to the knowledge evals alone, where it is a verdict; for training, `passed_cases` of a run is the number of `judged` cases (a count, not a verdict) and the share of correct answers is `correct_rate`.
  - Go: `TrainedModelEvalRunPassed` is `TrainedModelEvalRunCompleted` and `TrainedModelCasePassed` is `TrainedModelCaseJudged`. The statuses of the trained-model evals and audits are no longer described as `Experiment*` constants (those are the knowledge evals': `ExperimentRun.Status` and `ExperimentCaseResult.Status` list only them); `ExperimentFailed` and `ExperimentErrored` mean only what they mean for knowledge (a check did not hold; the case could not be run), and training has its own `...Failed` constants. The engine's statuses (`trainingeval.CaseJudged`, `CaseFailed`) are stored as they are (the conversion `judged` to `passed` in the eval path is gone), and a test checks that evals and audits agree on every status word. `trainingeval.PassScore` and `Progress.PassRate` are `CorrectScore` and `CorrectRate`: in the engine a score of 4 or 5 is "correct", not "passed".
  - The 0.57.0 entry mentions a status constant `ExperimentCompleted`; it does not exist (the training statuses are the `TrainedModelEvalRun...` ones above).

### Fixed
- **The live view of a training data audit showed no results.** `admin_ui/src/eval_live.ts` (shared by evals and audits) only recognised the eval's `passed`, so an audit's judged cases were skipped in the summary (0 judged, no mean score, no correct share), its `completed` badge was drawn as "active" and the "Finished." message never appeared. The view now handles the one vocabulary above, and `training.ts` no longer needs a badge that accepts both words.

## [0.58.0] - 2026-10-19

### Added
- **Training data audit of trained models, separate from evals.** Asking a trained model the questions of its *own* fine-tuning dataset is not an eval: every case is something the model was trained on, so the score says whether the training took (memorization / capacity, a too-low learning rate, prompt/format bugs) and, through the examples the model keeps failing, which training data is bad (label noise, contradictions); it says nothing about generalization, which is what an eval, with cases the model never saw, is for. The audit now has its own code, tables and API, apart from the evals, which are untouched and stay for the dedicated eval cases coming next.
  - Admin UI: a trained model has an **Audit** button (a column of the models table and a "Training data audit" section of the model's dialog) next to the existing **Eval** one; both open the same live view.
  - API: `POST /v1/gb/training/models/{id}/audits` (body: `limit`, `batch_size`, `judge_model`, `max_tokens`, `temperature`; no `scheme`, no experiments), `GET /v1/gb/training/models/{id}/audits`, `GET /v1/gb/training/audits/{id}`, `GET /v1/gb/training/audits/{id}/events` (SSE, the run is in the `audit` field of the events) and `POST /v1/gb/training/audits/{id}/cancel`.
  - Schema (SQLite and PostgreSQL, created on start-up, nothing existing is touched or migrated): `gb_training_audit_runs` (one row per audit: the model, status, dataset, judge, counters and score) and `gb_training_audit_cases` (one row per example). Deleting a trained model deletes its audits.
  - Go: `relational.TrainedModelAuditRun` and `TrainedModelAuditCase` with `RecordTrainedModelAuditRun`, `SaveTrainedModelAuditRunBatch`, `FinishTrainedModelAuditRun`, `FailRunningTrainedModelAuditRuns`, `GetTrainedModelAuditRun`, `ListTrainedModelAuditRuns` and `ListTrainedModelAuditCases`; `training.AuditManager` in `internal/api/training`. The judge's calls are recorded in `gb_inferences` as type `training_audit_judge` (the evals' stay `eval_judge`). The audit reuses the engine of `evals/training` (one question at a time to the model, judged in batches).
  - Docs: `docs/training/audit.md` (what an audit is for, how it differs from an eval, the API and the tables).

### Changed
- The model dialog and the models table of the admin UI call the two checks by their names: *Training data audit* and *Evals* (the eval section says that the server's eval still reads the model's training dataset until evals have cases of their own). The shared UI code (`admin_ui/src/training.ts`, `eval_live.ts`) handles both kinds of run.
- The eval code is unchanged: `POST /v1/gb/training/models/{id}/evals` and the `finetune_dataset` scheme still work as before.

## [0.57.0] - 2026-10-19

### Changed
- **A training eval has one case result per example (breaking for the eval database and the training API: the tables must be rebuilt, nothing is migrated; SQLite and PostgreSQL).** "Case result" now means the same for both kinds of eval: one row of `gb_experiment_case_results` per case. Before, a training eval run was ONE such row (`case_name ''`) and its examples were in `gb_training_case_results`.
  - Schema: a training case is a `gb_experiment_case_results` row (`eval` = the dataset, `case_name` = the example's index, unique per run through a partial unique index; `status` `passed` when judged (whatever the score), `failed` otherwise; `error`) with a `gb_training_case_meta` row (`idx`, `batch`, `instruction`, `input`, `reference`, `response`, `score`, `reason`, tokens, `duration_ms`), the counterpart of `gb_knowledge_case_meta`. `gb_training_case_results` is gone. The eval run is now just the run: `gb_training_eval_run_meta` is keyed by `run_id` (was `case_result_id`) and also holds the `judge_cost_usd` and the `error` that were columns of the whole-eval result row; the status and times are the run's. `gb_experiment_cases` lists an experiment's training cases (one per example) instead of the whole eval with `case_name ''`.
  - Go: the ID of a `TrainedModelEvalRun` is the ID of its `gb_experiment_runs` row (was that of the whole-eval result row); `TrainedModelCaseResult.ID` is the ID of its case result row and `EvalRunID` that of the run. `TrainingExperimentRun.EvalRunID` is gone (`ID` is the eval run's). New status constant `ExperimentCompleted`.
  - Training API: the IDs of evals and training experiment runs are the run IDs, and a training experiment run has no `eval_run_id` (its `id` is the eval's).

## [0.56.0] - 2026-10-19

### Changed
- **A set of cases is an eval; there are no suites (breaking for the eval database: the tables must be rebuilt, nothing is migrated; SQLite and PostgreSQL).** An *eval case* (a *case*) is one input/output combination, plus metadata, to be evaluated; cases are grouped into *evals*; a *run* executes cases (or a whole eval) once; an *experiment* is a named series of runs. What was an "eval suite" is now an **eval**, and "suite" is no longer a concept anywhere (nothing groups evals together). The word eval keeps meaning the whole activity too.
  - Schema: the column `suite` of `gb_experiment_cases` and `gb_experiment_case_results` is `eval`; `gb_training_suite_run_meta` is `gb_training_eval_run_meta` (and its index and delete trigger/function follow), and `suite_run_id` of `gb_training_case_results` is `eval_run_id`.
  - Go: `CaseRef.Suite`, `ExperimentCaseResult.Suite`, `experiments.CaseOutcome.Suite` and `experiments.CaseResult.Suite` are `Eval`; `TrainedModelSuiteRun` is `TrainedModelEvalRun` (with `RecordTrainedModelEvalRun`, `GetTrainedModelEvalRun`, `ListTrainedModelEvalRuns`, `SaveTrainedModelEvalRunBatch`, `FinishTrainedModelEvalRun`, the `TrainedModelEvalRunRunning/Completed/Failed/Cancelled` statuses, ...), `TrainedModelCaseResult.SuiteRunID` is `EvalRunID`, `TrainingExperimentRun.SuiteRun` / `SuiteRunID` are `EvalRun` / `EvalRunID`.
  - Admin API (`/v1/gb/admin/api/evals/...`): the `suite` of a case result and of an experiment's cases is `eval`, and `GET /experiments/{id}/history?suite=..&name=..` is `?eval=..&name=..` (its answer has `eval`). Training API: `suite_run_id` (of a case and of a training experiment run) is `eval_run_id`. The URLs of the training evals (`/v1/gb/training/models/{id}/evals`) are unchanged: the API and the UI call an eval run of a trained model just an eval.
  - Eval runner (`go run ./evals/knowledge`): the directory `evals/knowledge/suites/` is `evals/knowledge/evals/`; the flags `-suites dir`, `-suite name` and `-list-suites` are `-evals dir`, `-eval name` and `-list-evals`; the run's `params` keys `suites_dir` and `suite` are `evals_dir` and `eval`. `run_evals.sh` takes `-e|--eval NAME` (was `-s|--suite NAME`).
  - Admin UI: the Evals block has an *Eval* column and "eval/case" labels where it said suite.
  - Docs, schema comments and READMEs say eval where they said eval suite, suite or "suite run" (now *eval run*).


## [0.55.0] - 2026-10-19

### Changed
- **The outcome of a case in a run is a case result, everywhere (breaking for the eval database: the tables must be rebuilt, nothing is migrated; SQLite and PostgreSQL).** The table `gb_experiment_results` is `gb_experiment_case_results`, and the column that points to it, `result_id`, is `case_result_id` (in `gb_knowledge_case_meta`, `gb_training_suite_run_meta` and `gb_experiment_trajectories`). A training suite run is still one row of that table (`case_name ''`) until training evals have cases of their own.
  - Go: `ExperimentResult` is `ExperimentCaseResult` (`experiments.Result` is `experiments.CaseResult`), `ExperimentRun.Results` is `CaseResults`, `ExperimentTrajectory.ResultID` is `CaseResultID`, `RecordExperimentResult` and `GetExperimentResultDataset` are `RecordExperimentCaseResult` and `GetExperimentCaseResultDataset`, `experiments.ResultDataset` is `experiments.CaseResultDataset`.
  - Admin API (`/v1/gb/admin/api/evals/...`): a run's `results` is `case_results`, a trajectory's `result_id` is `case_result_id`, `GET /runs/{id}/web-searches` answers `case_results` (each with `case_result_id`) and `GET /results/{id}/graph` is `GET /case_results/{id}/graph`.

## [0.54.0] - 2026-10-19

### Changed
- **A case's history is made of its results.** `ExperimentCasePoint` is gone: `ListExperimentCaseHistory` and `experiments.CaseHistory` return the case's `ExperimentResult`s over the runs of the experiment (oldest run first, without trajectories), the same type a run's cases have.
- The admin API's `GET /v1/gb/admin/api/evals/experiments/{id}/history` sends each point with only what the UI plots (`run_id`, `run_started_at`, `status`, `problems`, `passed_checks`, `error`, `steps`, `started_at`, `finished_at`); `result_id`, `run_status` and `cost_usd` are no longer in it.

## [0.53.0] - 2026-10-19

### Changed
- **Eval terminology: an eval case is one input/output combination (plus metadata) to be evaluated, and cases are part of an eval suite.** What the code, schema, API and docs called an "eval" when it meant one case of a suite is now a **case** everywhere (**breaking for the eval database: the tables must be rebuilt, nothing is migrated**; SQLite and PostgreSQL):
  - tables and columns: `gb_experiment_evals` is `gb_experiment_cases`, `eval_name` is `case_name`, `total_evals` / `passed_evals` / `failed_evals` are `total_cases` / `passed_cases` / `failed_cases`, `gb_knowledge_eval_meta` is `gb_knowledge_case_meta`;
  - Go: `EvalRef` is `CaseRef`, `Experiment.Evals` is `Experiment.Cases`, `ExperimentRun.TotalEvals` / `PassedEvals` / `FailedEvals` are `TotalCases` / `PassedCases` / `FailedCases`, `ExperimentEvalPoint` is `ExperimentCasePoint`, `experiments.EvalHistory` and `ListExperimentEvalHistory` are `CaseHistory` and `ListExperimentCaseHistory`, `admin.EvalHistory` is `admin.EvalCaseHistory`;
  - admin API (`/v1/gb/admin/api/evals/...`, URLs unchanged): `evals` is `cases` in the overview (`totals.cases`, each experiment's `cases`) and in an experiment's detail, and a run's `total_evals` / `passed_evals` / `failed_evals` are `total_cases` / `passed_cases` / `failed_cases`;
  - admin UI: the Evals block says case, cases and "Case history" where it said eval.
- **Training evals: an eval of a trained model is a suite run, its items are cases.** An eval asks the model the cases of an eval suite; the suite is the model's fine-tuning dataset and each example is a case. In the code the eval is a `TrainedModelSuiteRun` (was `TrainedModelEval`) and its per-example outcomes are `TrainedModelCaseResult` (was `TrainedModelEvalItem`; `evals/training` has `CaseResult` for `Item`). Tables: `gb_training_eval_meta` is `gb_training_suite_run_meta` and `gb_training_eval_items` is `gb_training_case_results` (its `eval_id` is `suite_run_id`). A training eval's result row now has `suite` = the dataset's name and `case_name` = `''` (the whole suite); it had `suite = ''` and the dataset as the name. The run's `total_cases` / `passed_cases` / `failed_cases` count the suite's cases (all of them, the judged ones, the others). The HTTP and UI name of the thing stays "eval" (`/v1/gb/training/models/{id}/evals`).
- Training API: the `items` of `GET /v1/gb/training/evals/{id}` and of the live feed events are now `cases`; `eval_id` (of a case, and of a training experiment run) is `suite_run_id`. The judge's prompt says "cases" where it said "items".

### Added
- **Eval schemes for trained models.** A training eval has a `scheme`, which says how its suite and cases are made. The only one is `finetune_dataset` (`TrainingSchemeFineTuneDataset`): the model is tested with its own fine-tuning dataset and the judge scores whether it returns the dataset's expected outputs, which shows its error rate on what it was trained on. `POST /v1/gb/training/models/{id}/evals` accepts `scheme` (default and only value `finetune_dataset`, others answer `400`), and evals carry it (column `gb_training_suite_run_meta.scheme`). A second scheme, like the knowledge evals' (cases with inputs and expected outputs never seen in training), is not implemented yet.

## [0.52.0] - 2026-10-26

### Changed
- **One set of eval tables for knowledge and training evals** (SQLite and PostgreSQL; **breaking: the tables must be rebuilt, nothing is migrated**). The universal tables `gb_experiments`, `gb_experiment_evals`, `gb_experiment_runs` and `gb_experiment_run_evals` now hold both kinds, each with a `type` column (`knowledge` or `training`). Composite foreign keys keep the type the same from experiment to run to eval, and an experiment name is unique per type. What only one kind has moved to a metadata table with one row per eval: `gb_knowledge_eval_meta` (answer, problems, passed checks, dataset, steps) and `gb_training_eval_meta` (trained model, dataset, judge model, counters, judge tokens), plus `gb_training_eval_items` for the per-question outcomes. `gb_trained_model_evals`, `gb_trained_model_eval_items`, `gb_training_experiments` and `gb_training_experiment_runs` are gone.
- **Renamed `gb_experiment_run_evals` to `gb_experiment_results`**, because a row is the outcome of one eval in one run, not an eval. Everything follows: the column `run_eval_id` is `result_id` (in `gb_experiment_trajectories`, `gb_knowledge_eval_meta` and `gb_training_eval_meta`); the Go type `ExperimentRunEval` is `ExperimentResult` (`experiments.RunEval` is `experiments.Result`, the input of `Tracker.Record` is `experiments.CaseOutcome` instead of `EvalResult`); `RecordExperimentRunEval`, `GetExperimentRunEvalDataset` and `experiments.RunEvalDataset` are `RecordExperimentResult`, `GetExperimentResultDataset` and `experiments.ResultDataset`; `ExperimentRun.Evals` is `Results`. **Admin API:** a run's `evals` is `results`, `run_eval_id` is `result_id` (trajectories, history points and `/runs/{id}/web-searches`, whose list is also `results`), and `GET /v1/gb/admin/api/evals/run-evals/{id}/graph` is `.../results/{id}/graph`. The admin UI is updated.
- A training eval is now a run (of type `training`) with one eval in it. Its ID is that of its `gb_experiment_run_evals` row (it was the ID in `gb_trained_model_evals`); its settings are the run's `params`, its status, error, times and judge cost are the eval row's, and a run in no experiment (`experiment_id` NULL) is an eval started without an experiment name. The run is totalled and takes the eval's status when the eval ends. Deleting a trained model still deletes its evals and their runs.
- Both kinds have **suites** in the schema: `suite` and `eval_name` on `gb_experiment_evals` and `gb_experiment_run_evals`. Training evals have no suites yet and store `suite = ''` with the dataset's name as `eval_name`.
- The `Experiment`, `ExperimentRun` and `ExperimentRunEval` structs (and so the admin evals API) have a `type` field. The HTTP API of the training evals and experiments is unchanged.

## [0.51.0] - 2026-10-25

### Added
- **Training experiments.** Evals of trained models can be tracked as experiments: start an eval with `"experiment": "<name>"` (and optionally `experiment_description`, `experiment_config`) and it becomes a run of that named experiment, made on first use. Each run links to its eval, so it shows the eval's status, progress, mean score, share of correct answers and judge cost, and the trained model that was evaluated (base model, steps, final loss, training settings), to compare training recipes. API: `GET /v1/gb/training/experiments` and `GET /v1/gb/training/experiments/{id}`. They have their own tables, apart from the knowledge-extraction experiments: `gb_training_experiments` and `gb_training_experiment_runs` (SQLite and PostgreSQL, created on start-up). Details: [docs/training/EVALS.md](docs/evals/training.md#experiments).

## [0.50.0] - 2026-10-24

### Added
- **Live view of an eval in the admin UI.** Starting an eval opens a dialog that streams it as it runs, and every eval in the model's dialog has a **Watch live** button (**Open** once it is over). Each question appears as soon as the model answers it, with the dataset's answer beside the model's, and gets its score and the judge's reason when its batch is judged (the judge still grades a batch per call, so a score arrives per batch while the answers arrive one by one). It also has a progress bar with running totals (answered, judged, failed, mean score, % correct, average answer time), a strip with one square per question coloured by outcome (click one to jump to it), an **only problems** filter, a **follow** toggle and a Cancel button. Closing the dialog leaves the eval running; opening it on a running or finished eval shows everything so far.
- API: `GET /v1/gb/training/evals/{id}/events`, a Server-Sent Events feed (`snapshot`, `answer`, `batch`, `end`); see [docs/training/EVALS.md](docs/evals/training.md#api).
- `evals/training`: `Config.OnAnswer`, called with each item as soon as the model has answered it, before the batch is judged.

## [0.49.1] - 2026-10-24

### Changed
- Docs: `docs/evals/training.md` has a new section, **Why one question per prompt**: the trained model is asked one question per prompt because Gemma 4 4B and 12B were fine-tuned on single turns (several questions in one prompt are a shape they never saw, interfere with each other at that size, share one output budget and make scores depend on the batching), what is *not* the same (batching at the inference engine, parallel calls) and why batching the judge's items is different, with a way to check it. `docs/training/model_internals.md` links to it.

## [0.49.0] - 2026-10-24

### Added
- **Evals of trained models, with an LLM as the judge.** Every trained model in the admin UI's training view has an **Eval** button (in the models table, and in the model's dialog, which also takes the number of questions and the judge batch size). An eval asks the model the questions of its **training dataset** (the instruction, plus the input as context, as in training) through the sidecar, and has the external LLM that gligabao uses for knowledge ingestion score each answer from 0 to 5 against the dataset's answer (4 and 5 correct, 2 and 3 partial, 0 and 1 incorrect). The trained model is asked **one question at a time**; the question/answer pairs go to the judge **in batches** (10 per LLM call by default, up to 25), not one call each. It runs in the background: the dialog shows the progress, the mean score, the correct/partial/incorrect split, the judge's tokens and cost, and every question with the dataset's answer, the model's answer, the score and the judge's reason, lowest score first. An eval can be cancelled (the batches done are kept). It stops by itself when the model or the judge keeps failing. Details: [docs/training/EVALS.md](docs/evals/training.md).
- API: `POST /v1/gb/training/models/{id}/evals` (options `limit`, `batch_size`, `judge_model`, `max_tokens`, `temperature`), `GET /v1/gb/training/models/{id}/evals`, `GET /v1/gb/training/evals/{id}` (with the items) and `POST /v1/gb/training/evals/{id}/cancel`. One eval per model runs at a time.
- New tables `gb_trained_model_evals` and `gb_trained_model_eval_items` (SQLite and PostgreSQL, created on start-up); evals that a restart of the server interrupted are marked failed. The judge's LLM calls are recorded in `gb_inferences` as type `eval_judge`.

### Changed
- **The two eval systems are separate in `evals/`.** The knowledge-extraction runner (`main.go`, its tests, `suites/` and its README) moved to `evals/knowledge/`; the new trained-model eval is `evals/training/` (a library the server uses, with its own README and tests); `evals/README.md` explains both. `go run ./evals` is now `go run ./evals/knowledge` (`./run_evals.sh` is unchanged; update scripts that call the runner directly, as `../gligabao_int/run_evals.sh` did).

## [0.48.0] - 2026-10-24

### Changed
- **Training jobs list in the admin UI.** The *Started* column shows how long ago the job started ("5 minutes ago", "3 hours ago", "2 days ago"; the UTC time is in the tooltip), and a new *Duration* column shows start to finish (a running job counts up every second). Completed jobs have a slight green background (opacity 0.5).
- The blinking cube next to a running job: the first cube blinks while the job sets up (container, model load) and goes still once the steps start; from then a second, brighter blue cube blinks to the right of it while training.

## [0.47.0] - 2026-10-23

### Added
- **Training datasets are found by manifests in a training datasets directory.** A dataset is a subdirectory of `bin/datasets_training/` with a `dataset.toml` manifest (`name`, `description`, `format`, `file`, `version`, `generator`); the server scans the directory for manifests, and reads `file` to learn where the dataset's JSONL file is. It does so every time it lists the datasets or starts a job, so a dataset added by hand (or a regenerated data file) is available at once. Subdirectories whose manifest cannot be used (a TOML error, no `file`, a bad name, a missing data file, a duplicate name) are not datasets and are reported as `problems` in `GET /v1/gb/training/datasets` and under the datasets table of the admin UI. See `docs/training/datasets.md`.
- `bin/datasets_training/world-knowledge-base/`: the world knowledge base dataset (500 examples) with its manifest and `generate_dataset.py`, moved here from `sidecar/ai_train/finetune_data/`. `bin/datasets_training/` is tracked in git (the rest of `bin/` is still ignored); the generated data files are ignored by a `.gitignore` in the dataset's subdirectory.
- The Modal CLI reads the same manifests (`training_data.py`): `modal run app.py --action datasets` lists them (`GLIGABAO_DATASETS_TRAINING_DIR` overrides the directory).

### Changed
- **A job needs a dataset, by name.** `POST /v1/gb/training/jobs` takes the dataset's `name` in the now **required** `dataset` field (it used to be a file name, and optional); a missing or unknown name is a `400`. The sidecar's `POST /training/job_start` requires `dataset` too (the file name `<name>.jsonl` on its data volume). There is no default dataset any more: `modal run app.py --action sync|validate|train` need `--dataset <name>`.
- `GET /v1/gb/training/datasets` answers `{"datasets": [...], "problems": [...]}`; each dataset has the manifest's `name`, `description`, `format`, `file`, `version`, `generator` and `dir` besides its record. `GET /v1/gb/training/datasets/preview` takes `name=` instead of `filename=`.
- The default training datasets directory is `datasets_training` next to the database (`bin/datasets_training`; `/data/datasets_training` in the Docker image) instead of `<db>.training_datasets`. `serve --training-dataset-dir` and `ServeOptions.TrainingDatasetDir` set it as before. Datasets in an old `<db>.training_datasets` directory are not found: move each file into a subdirectory of the new one with a manifest (or upload it again).
- An uploaded file becomes a dataset of its own: the subdirectory named like the file (without `.jsonl`) with a manifest the server writes. Uploading a name again replaces its file; a dataset of that name with a manifest of its own (naming another data file) answers `409`.
- `gb_training_datasets` rows are made from the files the server finds (when it first sees one or sees it changed), keyed by `<name>.jsonl`; the table is unchanged.
- `modal run app.py --action sync` uploads a dataset only when its content differs from the file on the volume (it compared modification times, with a `.metadata.json`, for the one default dataset).
- Admin UI, training view: the datasets table shows each dataset's name and file, the picker has no "sidecar default dataset" entry, `Run` needs a dataset, and the unusable ones are listed.

### Removed
- The sidecar's default dataset (`gemma4_tool_calling_news_finance_v1.jsonl`), `finetune_data/`, `get_local_paths()` and the sidecar's `.metadata.json` bookkeeping.

## [0.46.1] - 2026-10-23

### Fixed
- **Trained models did not stop: replies ran to `max_tokens` and invented further turns.** The training text (`sidecar/ai_train/ai_train_core/prompt_format.py`) used Gemma 2/3's turn markers `<start_of_turn>` / `<end_of_turn>`, which are not in Gemma 4's vocabulary (they were tokenized as plain text), so the model learned to write those characters after an answer instead of Gemma 4's end-of-turn token `<turn|>` (id 106, one of the base model's stop ids `[1, 106, 50]`). The text now uses Gemma 4's own markers (`<|turn>user\n…<turn|>\n<|turn>model\n…<turn|>`), for training and inference. **Adapters trained before this version must be retrained.**
- `<bos>` is handled explicitly: the Gemma 4 tokenizer does not necessarily add it, so `bos_prefix(tokenizer)` prepends it to the training text and the inference prompt only when the tokenizer does not (one `<bos>` in both).
- Inference passes `eos_token_id` (`<turn|>` and `<eos>`) to `generate`, and decodes only the newly generated tokens, so replies no longer come back starting with the echoed prompt (`user\n…\nmodel\n…`).

### Added
- Tests: the format with the new markers, no Gemma 3 markers, one `<bos>`, stop ids, and (against the real Gemma 4 tokenizer from the Hugging Face Hub, skipped when unavailable) that `<turn|>` / `<|turn>` are single tokens, the Gemma 3 markers are not, and the encoded training text ends with id 106.

### Changed
- Docs (`docs/training/training_data_format.md`, `docs/training/model_internals.md`): the real Gemma 4 markers, where the end-of-turn token comes from (the dataset file has no EOS; `format_instruction` adds it), BOS handling, the stop chain (now verified, no longer marked *Unverified*) and the base model's sampling defaults.

## [0.46.0] - 2026-10-22

### Added
- **Timing of each trained-model inference in the admin UI.** Every request in the history of the trained-model dialog now shows whether it was a cold start (and how long the model load took) or found the model already loaded, the time without the load, and the input / output token counts. The "Done in …" line after a request also shows the token counts.
- `cold_start` and `load_seconds` are stored with each request in `gb_trained_model_inferences` (new columns, added to existing SQLite and PostgreSQL databases on start-up; `cold_start` is NULL for requests made before, failed ones, or answered by a sidecar that does not report it) and returned by `GET /v1/gb/training/models/{id}/inferences`. In Go, `FinishTrainedModelInference` takes the two values.

### Changed
- Docs (`docs/training/modal.md`, `docs/training/sidecar.md`): a **Cold and warm starts** section (`modal.md#cold-and-warm-starts`) with what a cold request does in the GPU inference container, step by step (container start, volume reload, cache check, adapter lookup, 4-bit load of the base model from the HF cache or Hugging Face, adapter attach, generation), what `load_seconds` does and does not include, how to read the numbers, and how to get fewer cold requests. `docs/training/sidecar.md` no longer says that every request loads the model (true before 0.39.0) and lists the new fields of the answer.
- Docs: new page `docs/training/model_internals.md` on how the Gemma-4 models work in gligabao: the prompt format, how generation stops (and why a reply that does not stop runs to `max_tokens`), sampling and the returned text, where the time of a request goes (prefill, decode, tokens per second) with a table for diagnosing slow warm requests, and a short training summary. What is not verified about Gemma 4 itself is marked.

## [0.45.0] - 2026-10-21

### Added
- **Inference container indicator** in the admin UI's trained-model dialog: a small dot above the prompt field (upper right) is green when a warm Modal inference container holds this model (prompts run right away), amber when one is up with another model, grey when none is up. It refreshes every 15 s while the dialog is open and after each request.
- `POST /v1/gb/training/models/{id}/infer` also answers `cold_start` and `load_seconds` (as reported by the sidecar), and the dialog shows whether a request loaded the model or found it already loaded.

### Changed
- The "the first request loads the model on a GPU: this can take minutes" notice is shown only when the container is not known to be warm.

## [0.44.0] - 2026-10-20

### Changed
- **Training progress no longer goes through a Modal volume.** The training container appends each job's events to an *event log* in a Modal Dict named `<SIDECAR_APP_NAME>-events` (one request per event; the log keeps all of them, and entries expire 7 days after last use) instead of a `.jsonl` file on the `/progress` volume with a commit after every write. The web container reads the Dict instead of reloading the volume and re-reading the file, polls every second instead of every two, and `POST /training/job_cancel` sets a flag in the Dict instead of writing a stop file. The sidecar and Go protocol is unchanged: the same SSE events with the same ids, so a Go server or sidecar that was offline while a job trained resumes with `from=<last id + 1>` and gets everything it missed.
- A job cancelled right after it started (before its container wrote an event) is now cancelled instead of answering 404.
- `GET /training/job_progress` requires `job_id`.

### Added
- A watchdog in the sidecar's Modal web API: when a training container dies without reporting (out of memory, timeout), the idle event stream notices that the job's Modal call is over and writes the `failed` event with Modal's error, so the Go server marks the job failed at once instead of after its 3 hour limit.

### Removed
- The progress volume and `SIDECAR_VOL_PROGRESS`. **Redeploy the Modal app** after upgrading; jobs running during the redeploy are not followed (they report to the old volume), and the `gligabao-train-progress` volume can be deleted with `modal volume delete`.

## [0.43.1] - 2026-10-19

### Changed
- The sidecar no longer logs successful `GET /health`, `GET /backends` and `GET /backends/{name}/apps` requests (polled constantly by the admin UI). Failed requests and all other requests are still logged.

## [0.43.0] - 2026-10-18

### Added
- `serve` prints the URL the server is reachable on under the startup ASCII art, and, if the admin UI is active, the admin UI URL on the next line. The URLs are yellow when stderr is a terminal and `NO_COLOR` is not set.

## [0.42.2] - 2026-11-06

### Changed
- Internal: the Modal-specific code of the sidecar moved into `sidecar/ai_train/providers/modal/`: `modal_provider.py` is `provider.py`, and the admin scripts `deploy_modal.py`, `stop_modal.py`, `list_apps.py`, `list_volumes.py` and `inference_containers.py` are `deploy.py`, `stop.py`, `list_apps.py`, `list_volumes.py` and `inference_containers.py`. The sidecar runs them as modules (`python -m providers.modal.deploy`, with `sidecar/ai_train/` as cwd). No change in behaviour.
- `sidecar/requirements.txt` is `sidecar/requirements_sidecar.txt`, to tell it from the `requirements_*.txt` of the Modal images in `sidecar/ai_train/`; `Dockerfile.sidecar` and the docs follow.

## [0.42.1] - 2026-10-17

### Changed
- Internal: the version code moved from `version.go` into `gligabao.go`, and the version/changelog test moved to `internal/buildinfo`, so the repo root has fewer files. No change in behaviour or in the public Go API.

## [0.42.0] - 2026-11-05

### Added
- **Token counts of trained-model inferences.** The sidecar's `POST /infer/query` answer now has `input_tokens` (the prompt as the model saw it, chat template and special tokens included) and `output_tokens` (the generated tokens, end-of-sequence included), counted by the model's tokenizer. They are stored with each inference of a trained model (0 for failed ones) and returned by `POST /v1/gb/training/models/{id}/infer` and by the list of inferences of a model. Redeploy the Modal app to get the counts from the sidecar.

### Changed
- **"Model run" is now "inference"**, the standard name. Databases created by an older version must be recreated (no migration):
  - the table `gb_model_runs` (every LLM call, embedding request, web search and Jev classification) is **`gb_inferences`**; its `id` is referenced as `inference_id` instead of `model_run_id` (`gb_artifacts.inference_id`, the `<stage>_inference_ids` of `gb_remember_runs`);
  - the table `gb_inference_requests` (a prompt run through a trained model), which an earlier development build had as `gb_model_trained_runs`, is **`gb_trained_model_inferences`**, with the two new columns `input_tokens` and `output_tokens`;
  - the admin endpoint `GET /v1/gb/admin/api/model-runs` is `GET /v1/gb/admin/api/inferences` (its `runs` lists are `inferences`), and the admin UI view and the docs page are *Inferences*;
  - in Go, `ModelRun` is `Inference` (`RecordInference`, `GetInference`, `ListInferences`, `InferenceStats`, `ai.Response.InferenceID`, ...) and the trained-model request is `TrainedModelInference` (`RecordTrainedModelInference`, `FinishTrainedModelInference`, which also takes the two counts, ...).
- The HTTP endpoints of the trained models (`/infer`, `/inferences`) are unchanged.

## [0.41.1] - 2026-11-04

### Changed
- Sidecar (Modal app): the `generate` naming is now `infer`, consistent with the `POST /infer/query` route. The Modal functions `generate`, `generate_4b` and `generate_12b` are `infer`, `infer_4b` and `infer_12b`, `modal run app.py --action generate` is `--action infer`, and `inference.generate_response` / the `create_api` argument `generate_response` are `infer_response`. The HTTP API is unchanged. The deployed app keeps working under the old names until you redeploy the Modal app.

## [0.41.0] - 2026-10-17

### Added
- **Inference requests are tracked in the database.** Every `POST /v1/gb/training/models/{id}/infer` is stored in the new table `gb_inference_requests` (SQLite and PostgreSQL; created on start-up): the trained model ID, prompt, response, status (`running`, `completed`, `failed`), error text, the model size and path sent to the sidecar, max tokens, temperature, duration and timestamps. Failed requests (and ones whose client gave up) are stored too; a row left `running` was interrupted by a restart. If the request cannot be stored, the inference is not started (500).
- `GET /v1/gb/training/models/{id}/inferences[?limit=]` lists the stored requests of a model, newest first. The infer answer now also carries `id`, `status` and `duration_ms`.

### Changed
- Admin UI: clicking a trained model opens it in a **modal** (training details and files, the inference form, and the **past requests** of the model with prompt, response, status and errors) instead of expanding under the table.

### Fixed
- Admin UI: the reply of an inference is shown to the user: after a run the history in the modal is reloaded from the server and the new request (response, or the error) is at its top; an empty reply is shown as such.

## [0.40.3] - 2026-11-03

### Fixed
- **Run inference** no longer fails with `502: sidecar failed to run the inference` (sidecar log: `sidecar POST /infer/query: status 303`) when the request takes more than ~150 s (a cold start). Modal answers such a request with `303 See Other` and a Location to poll for the result; the sidecar did not follow it and passed the bare 303 on. It now follows Modal's redirects (up to 50).

### Added
- The sidecar logs each forwarded request (method, URL, status, duration), every 303 redirect it follows, backend failures (with the 502 it answers) and the body of error answers (first 2000 bytes).

## [0.40.2] - 2026-11-02

### Changed
- Admin UI, trained models list: a model name is shortened to its first 10 and last 5 characters (`abcdefghij...vwxyz`) with the full name on hover; "Trained" shows how long ago (minutes, hours or days) with the exact UTC time on hover; the table no longer overflows the page (long dataset names are clipped, and the table scrolls horizontally if needed).

## [0.40.1] - 2026-11-02

### Fixed
- The Modal apps list in the training view no longer hangs on "Loading the Modal apps…" and then fails with `502: listing the Modal apps timed out after 30s`. `list_apps.py` (and the Stop inference script) made their Modal client in a separate event loop from the one the Modal SDK uses for Dicts, so reading the warm-containers Dict never returned. They now run on the SDK's own loop (`run_in_modal_loop` in `inference_containers.py`). No redeploy of the Modal app is needed.

## [0.40.0] - 2026-11-02

### Added
- **Inference-only image** for the Modal app (`sidecar/ai_train/requirements_infer.txt`, `INFER_IMAGE` in `app.py`): `generate_4b` and `generate_12b` no longer run in the Unsloth training image. It has only `torch`, `transformers`, `peft`, `accelerate`, `bitsandbytes` and `modal` (no Unsloth, `trl`, `datasets`, `torchao` or `torchvision`) and a Debian slim base without the CUDA toolkit, so it builds and cold-starts smaller than the training image.
- `ImageSpec.cuda_toolkit` (default `True`): with `gpu=True`, `False` selects a slim base with only the CUDA libraries the pip wheels bring instead of `nvidia/cuda:*-devel`.
- `ai_train_core/model_load_infer.py`: loads the base model 4-bit (nf4, `bitsandbytes`) and attaches the trained LoRA adapter with `peft`.

### Changed
- Inference loads through `transformers` + `peft` instead of Unsloth (`inference.py`); the Unsloth loader keeps only the training functions. Training is unchanged. Redeploy the Modal app to build the new image. Generated text can differ slightly from before (plain 4-bit nf4 of the base model instead of Unsloth's quantization and kernels); check an inference after redeploying.

## [0.39.0] - 2026-11-01

### Added
- **Stop inference** button in the apps list of the training view (action `stop_inference`, script `sidecar/ai_train/inference_containers.py`): ends the warm GPU inference containers of an app without touching the app, its web API or training jobs. The apps list shows how many are warm (`inference` in `GET /backends/modal/apps`).
- `SIDECAR_INFER_IDLE_TIMEOUT_S` (default `300`): how long an inference container stays up after its last request.
- The answer of `POST /infer/query` has `cold_start` and `load_seconds`.

### Changed
- Inference containers are kept around: the loaded model stays in memory and the container stays up for 5 minutes (`SIDECAR_INFER_IDLE_TIMEOUT_S`) after its last request, so only the first request pays the model load (before, every request loaded the base model and the adapter again). With no further request it shuts down by itself. Redeploy the Modal app for the change to take effect.
- A failing reload of the models volume before an inference no longer fails the request: the container uses what it sees.

## [0.38.0] - 2026-10-31

### Added
- **Trained models**: a new table `gb_trained_models` (SQLite and PostgreSQL) with one row per completed training job: the set of weights the job saved on the sidecar's models volume. Each row tracks the job (`run_id`, `job_id`), the base model and size, the dataset (name, sha256 of the file the job read, number of examples), the steps and final loss, the other training settings (`params`: LoRA rank and alpha, learning rate, batch size, ...), where the weights are (`path`) and the list of their files with sizes and sha256. Training again (other data, steps or size) always makes a new row; nothing is replaced.
- The training job's `completed` event carries the description of the saved weights (`model`); the Go monitor records it as a trained model (once, even if the event is replayed).
- `GET /v1/gb/training/models`, `GET /v1/gb/training/models/{id}` and `POST /v1/gb/training/models/{id}/infer` (runs a prompt through the chosen model's weights; Go client: `sidecar.Client.Infer`).
- Sidecar `POST /infer/query` takes `model_path` (`trained/<job id>`) to pick the weights; `modal run app.py --action generate --model-path ...` too.
- `docs/training/sidecar.md`, section Trained models: where the weights are saved (volume layout, what is and is not stored, who reads and writes it), how a model is recorded, how an inference request reaches the weights, and how to add an inference endpoint.
- Admin UI, training view: a **Trained models** table. Clicking a model shows its settings and files and a form to run a prompt through it.

### Changed
- Every training job now saves its adapter in `/models/trained/<job_id>` of the models volume instead of overwriting `/models/finetuned_model_<size>`. Inference without `model_path` still loads the legacy directory, so models trained by earlier versions remain usable that way (they are not in the table). Redeploy the Modal app (`Redeploy` in the training view) for the training and web containers to get the change; until then completed jobs record no trained model.

## [0.37.0] - 2026-10-30

### Added
- **Stop job** button in the admin UI's training view, and `POST /v1/gb/training/jobs/{id}/stop` (sidecar: `POST /training/job_cancel`, Go client: `CancelTrainingJob`). The job ends at its next step (or when the model load is done) with the new final status `cancelled`; the adapters of a stopped job are not saved. The stop request travels as a `.stop` file on the progress volume, which the training container checks between steps, so the training container stays the only writer of the progress file.
- `log` events (`{"type":"log","message"}`) say what a job is doing: importing the libraries, dataset loaded, model loaded, LoRA added, trainer set up, adapters saved. They show in the job's feed in the admin UI and in the server log.
- The training container prints every step to its own log (loss, learning rate, seconds per step and an estimate of the time left); the sidecar logs the events it sends, why a job's stream is quiet, and a failed volume reload.

### Changed
- The trainer logs every step (`logging_steps=1`, was 5), so the dashboard gets a progress event and a loss point per step.
- The sidecar's job event stream no longer ends when reloading the progress volume fails: it logs the error and tries again at the next poll.
- `cancelled` ends a job like `completed` and `failed` (the monitor stops following it, and it is not resumed at startup). Redeploy the Modal app (`Redeploy` in the training view) for the training and web containers to get the changes.

## [0.36.0] - 2026-10-29

### Changed
- Training image modernised (`sidecar/ai_train/requirements_train.txt`): `unsloth`/`unsloth_zoo` 2026.10.6, `torch>=2.10`, `torchao>=0.16`, `transformers` up to 5.17, `trl` up to 1.15. The old caps are gone. Base image and wheels are now CUDA 12.8 (`nvidia/cuda:12.8.1`, `cu128`), also for the 12B fallback requirements. Redeploy the Modal app to rebuild.
- The 12B model (`google/gemma-4-12B-it`, `gemma4_unified`) now trains and serves through Unsloth, in the same image as 4B; `train_12b` and `generate_12b` use the shared image.

### Removed
- The plain transformers + peft + trl path for 12B (`model_train_gemma4_12b.py`, `requirements_gemma4_12b.txt`) and the per-size `backend` key in `model_config.py`: training and inference always go through Unsloth.

### Fixed
- The training job failed in `get_peft_model` with `Found an incompatible version of torchao. Found version 0.13.0, but only versions above 0.16.0 are supported`: the previous `torchao<0.14` pin conflicted with the current `peft`.

## [0.35.0] - 2026-10-29

### Added
- The Modal backend lists the **active Modal apps of this system**: `GET /backends/modal/apps` (sidecar; read only, from `sidecar/ai_train/list_apps.py`, the equivalent of `modal app list`) returns the deployed / running apps named `SIDECAR_APP_NAME` or `SIDECAR_APP_NAME-<suffix>` (name, app id, state, running tasks, creation time) with the state of their actions. The configured app is always listed (as `not deployed` when it is not up) so that it can be deployed. Other apps of the Modal workspace are not shown and cannot be touched.
- Per-app action routes: `/backends/{name}/apps/{app}/actions/{action}[/events|/cancel]` (sidecar), and the same under `/v1/gb/admin/api/sidecar/backends/…` (server). Deploy and stop run with `SIDECAR_APP_NAME` set to that app, one run at a time per app and action. The plain `/backends/{name}/actions/…` routes keep acting on the configured app. `Backend.apps()` and `Backend.app_actions()` are the hooks for other backends.
- Admin UI, Training view: under the Modal backend, one row per app with its state and buttons **Redeploy** (**Deploy** if it is not up), **Last log** (**View log** while running; the log of the last action run on that app), **Stop** and **Volumes**.

### Changed
- Admin UI: the Deploy / Stop / Volumes buttons moved from the Modal backend line into the app rows.

### Fixed
- Starting a training job failed with `decode: invalid character '('`: the sidecar relayed Modal's zstd-compressed answer without its `Content-Encoding` header. It now asks the backend for an uncompressed answer and relays decoded bytes.
- The 4B training job failed with `cannot import name 'ScalingType' from 'torch.nn.functional'`: `torchao` (pulled in by `unsloth_zoo`) resolved to a release that needs torch 2.10+, but the image caps torch below 2.9. `requirements_train.txt` pins `torchao>=0.13.0,<0.14.0` (redeploy the Modal app to rebuild the image).
- A failed training job now logs an error on the Go server (`training: training job failed`, with the job's Python traceback), and the `failed` event carries the traceback (`traceback` field).

## [0.34.0] - 2026-10-28

### Added
- The Modal app can be deployed from the sidecar: `POST /backends/modal/actions/deploy` runs `sidecar/ai_train/deploy_modal.py` (the Modal SDK's `app.deploy()`, no `modal` CLI or `~/.modal.toml` needed; credentials come from `MODAL_TOKEN_ID` / `MODAL_TOKEN_SECRET`). One run at a time (`409` otherwise). `POST /backends/{name}/actions/{action}/cancel` stops a running one (SIGINT so it can clean up, killed after 10 s; `409` if not running). `GET /backends/{name}/actions/{action}` returns the state and log, `.../events` streams them live (SSE). Backends can offer more actions through `Backend.actions()` (`sidecar/backends/actions.py`); `GET /backends` lists them.
- `POST|GET /v1/gb/admin/api/sidecar/backends/{name}/actions/{action}[/events|/cancel]`: the server passes those routes on to the sidecar, flushing the SSE stream as it arrives.
- Admin UI, Training view: a **Deploy** / **Redeploy** button next to the Modal backend, and a live log dialog with the stages (starting, building images, creating functions, deployed), the state, the elapsed time and the timestamped build output, with follow-scroll. A **Cancel run** button stops it (`cancelled` state). Closing the dialog leaves the deploy running; **View log** reopens it, also after a page reload.

- Stop the Modal app from the sidecar: a `stop` action next to `deploy` (`POST /backends/modal/actions/stop`, same log, cancel and SSE routes) runs `sidecar/ai_train/stop_modal.py`, which does what `modal app stop` does (running containers, training jobs included, are terminated; the web API goes offline until the next deploy; volumes are kept). `deploy` and `stop` do not run at the same time (`409`). `GET /backends` leaves `stop` out while the app is not deployed.
- `GET /backends/{name}/volumes` (sidecar) and `GET /v1/gb/admin/api/sidecar/backends/{name}/volumes` (server): the Modal volumes the app uses (mount path, name, whether it exists in Modal, creation time, number of top-level entries), read only, from `sidecar/ai_train/list_volumes.py`.
- Admin UI, Training view: a red **Stop** button on the Modal backend (with a confirmation that names the consequences; shown while the app is deployed) and a **Volumes** button that lists the volumes in a small table.

- `docs/training/modal.md`: the Modal app lifecycle (what the admin UI shows, deploy and its stages, the live log, cancel, stop, volumes, the API, the container, troubleshooting); referenced from `docs/training/sidecar.md` and the README.

### Changed
- `Dockerfile.sidecar` now copies `sidecar/ai_train/` (the code the deploy uploads) into the image.

## [0.33.0] - 2026-10-28

### Added
- Sidecar: `GET /backends` lists the training backends with their status (`name`, `configured`, `healthy`, plus backend-specific fields). For `modal`: `deployed` (whether the app's web function is found in Modal; `null` if it could not be checked), `app` and, when unhealthy, `error`.
- `GET /v1/gb/admin/api/sidecar/backends` (and `sidecar.Client.Backends`): the same list for the admin UI; with the sidecar down it answers an empty list and an `error` text.
- Admin UI, Training view: a small list of the available backends with a healthy/unhealthy badge, and for Modal whether the app is deployed or still needs `modal deploy app.py`.

## [0.32.1] - 2026-10-27

### Changed
- Admin UI: the dataset preview modal colors each JSON line (keys, strings, numbers, true/false/null, punctuation).

## [0.32.0] - 2026-10-27

### Added
- `GET /v1/gb/training/datasets/preview?filename=<name>[&lines=<n>][&db=]`: the first lines (default 100, at most 1000) of a dataset's file, with the record count and whether the file has more. Only the start of the file is read.
- Admin UI, Training view: clicking a dataset in the list opens a modal with the first 100 lines of its file, so it can be inspected.

## [0.31.0] - 2026-10-26

### Removed
- The sidecar (FastAPI and the Modal web app) no longer has dataset endpoints: `POST /datasets/upload` and `GET /datasets/list` are gone (a request now gets 404). Training datasets are managed by the Go server and its database alone. The Go client lost `UploadTrainingDataset`, `ListTrainingDatasets` and the `sidecar.TrainingDataset` type.
- `GET /v1/gb/training/dataset-records`: use `GET /v1/gb/training/datasets`.

### Kept
- The Python `TrainingDataset` pydantic model (`sidecar/ai_train/api/models.py`) stays, as the sidecar's internal way to load and pass a dataset around (`payload()` decodes its base64 `content`; new `TrainingDataset.from_path`). It is no longer an endpoint body or answer.

### Changed
- `GET /v1/gb/training/datasets` now lists the datasets from the Go server's database (the records `dataset-records` returned) instead of asking the sidecar, so it works with the sidecar or Modal down. The admin UI's dataset picker uses it.
- A dataset reaches the sidecar inside the request that starts a job on it: `POST /training/job_start` takes an optional `dataset_content` (the jsonl file, base64) next to `dataset`, and the sidecar stores it on the data volume internally before it spawns the job (a plain `*.jsonl` name, else 400). The file is now sent with every job that uses the dataset; the record still becomes `synced` once a job has taken it.
- Starting a job on a dataset name the server has no record of now answers 400 (it used to be passed through to the sidecar). A job without a dataset still uses the sidecar's default dataset. A client cannot send `dataset_content` itself; the server adds it from its own copy.

## [0.30.0] - 2026-10-26

### Added
- **Training API under `/v1/gb/training/`**, served whether or not the admin UI is on: `POST` and `GET /v1/gb/training/jobs` (start and list training jobs), `GET /v1/gb/training/datasets` (the datasets on the sidecar), `POST /v1/gb/training/datasets` (upload a dataset), `GET /v1/gb/training/dataset-records[?db=]` (the datasets uploaded through the server) and `GET /v1/gb/training/events` (SSE feed of the jobs' progress). The training monitor, which resumes following unfinished jobs after a restart, runs without the admin UI too.
- Admin UI, training view: **upload JSONL training datasets**. Pick a `.jsonl` file (one JSON object per line with `instruction`, `output` and optionally `input`) and press `Upload`; the `Datasets` table lists what was uploaded (status, records, size). The dataset picker lists the uploaded datasets together with the ones on the sidecar, and still shows the uploaded ones when the sidecar cannot be reached.
- Uploading does not involve the sidecar: the Go server validates the file (UTF-8, every line a JSON object with string `instruction`/`output`, plain `*.jsonl` name, at most 64 MB), saves it as a file in a local directory (default `<db path without extension>.training_datasets`, e.g. `bin/gligabao.training_datasets`; `serve --training-dataset-dir`, `ServeOptions.TrainingDatasetDir`) and records it in the new `gb_training_datasets` table (SQLite and PostgreSQL; created automatically). Uploading a file name again replaces the dataset. It works with the sidecar or Modal down.
- A dataset uploaded through the server is sent to the sidecar when a training job that uses it is started, once per version of the file: the record's status goes from `stored` to `synced`, and uploading the name again makes it `stored` again. If the sidecar fails to take it the job is not started (502); if the file is gone from the directory the job answers 500 and asks for a new upload. A dataset name the server has no record of is passed through to the sidecar as it is.

### Changed
- The training endpoints moved out of the admin path: `/v1/gb/admin/api/training/{jobs,datasets,events}` are now `/v1/gb/training/{jobs,datasets,events}`; the old paths are gone. `GET /v1/gb/admin/api/sidecar/status` stays where it is (it is the admin UI's health indicator).
- The training HTTP handlers (jobs, datasets, the progress monitor and its SSE feed) moved from `internal/api/admin` to their own package, `internal/api/training` (`training.StartJobOf`, `training.UploadDatasetOf`, `training.NewMonitor`, `training.Database`, ...). `admin.SidecarStatusOf` takes a `bool` (whether the sidecar is active) instead of the sidecar.

## [0.29.4] - 2026-10-26

### Added
- `run_server_sidecar_local.sh` runs the Python sidecar locally (dev only), and `run_server_local.sh` now starts it first if nothing answers on its health endpoint, and stops it when the server ends. The sidecar needs `MODAL_TOKEN_ID` and `MODAL_TOKEN_SECRET`; without them the server still starts, with a warning.

### Changed
- Docs: `docs/training/sidecar.md` and the README now say that the Go server only ever talks to the sidecar and the sidecar proxies to Modal (with a diagram), no longer call the sidecar a skeleton, and document the startup health check and `GET /v1/gb/admin/api/sidecar/status`, the Go client's training methods and `StatusError`, that the container copies `routes.py` and `backends/`, that the sidecar needs the Modal tokens (and that `docker-compose.yml` does not pass them yet), and the new steps for adding an endpoint.

## [0.29.3] - 2026-10-26

### Added
- Admin UI: a **sidecar health indicator** in the top bar next to the server's own (`sidecar: up` / `sidecar: down`, refreshed every 15 s). It is a live check made by the server, so it shows when the sidecar goes down or comes back. If the sidecar is up but was down when the server started, it shows `sidecar: up (restart server)`.

### Changed
- The admin endpoint `GET /v1/gb/admin/api/training/status` (0.29.2) is now `GET /v1/gb/admin/api/sidecar/status` and answers `{"sidecar_up": bool, "sidecar_active": bool}`: `sidecar_up` is the live check, `sidecar_active` whether the server uses the sidecar (decided on startup). The training view's notice uses `sidecar_active`.

## [0.29.2] - 2026-10-26

### Added
- On startup the Go server pings the sidecar's `/health` (5 s timeout). If the sidecar is not operating correctly, the server still starts: it logs a warning, sets the `server.SidecarActive` flag to false and runs without a sidecar client, so everything that needs the sidecar answers 503 ("sidecar is not active") or does nothing instead of failing. The check runs once; restart the server after starting the sidecar.
- Admin endpoint `GET /v1/gb/admin/api/training/status` (`{"sidecar_active": bool}`). The admin UI's training view shows a "Sidecar not active" notice and disables `Run` when it is false.

### Changed
- The 503 the training endpoints answer without a sidecar now says "sidecar is not active" (was "not configured").

## [0.29.1] - 2026-10-26

### Added
- The sidecar now serves the training and dataset API itself (`POST /training/job_start`, `GET /training/job_progress`, `GET /training/job_events` (SSE), `POST /infer/query`, `POST /evals/add_to_dataset`, `GET /evals/get_dataset_count`, `POST /datasets/upload`, `GET /datasets/list`; `sidecar/routes.py`) and forwards each request to a pluggable **backend** (`sidecar/backends/`). The Go server only knows the sidecar API, not what runs behind it. Previously the local sidecar had only `/health`, so the admin UI's dataset list and training calls failed with 404. A backend that cannot be reached gives `502 {"error": ...}`.
- `modal` backend: forwards to the web API of the deployed Modal app (`modal deploy app.py` in `sidecar/ai_train`), whose URL it looks up through the Modal SDK on the first request. New settings: `SIDECAR_BACKEND` (default `modal`), `SIDECAR_APP_NAME` (default `gligabao-train`) and `SIDECAR_MODAL_WEB_URL` (skip the lookup, e.g. for `modal serve`).

### Changed
- The Modal credentials (`MODAL_TOKEN_ID`, `MODAL_TOKEN_SECRET`) are required only when the backend is `modal`.
- The sidecar's `requirements.txt` adds `httpx` and `modal`; `Dockerfile.sidecar` copies `routes.py` and `backends/`.

## [0.29.0] - 2026-10-26

### Changed
- The sidecar's settings (`SIDECAR_HOST`, `SIDECAR_PORT`) are now held in one immutable pydantic struct, `SidecarConfig` (`sidecar/sidecar_config.py`), loaded once at startup with `SidecarConfig.from_env()` and passed to `create_app(config)` (kept on `app.state.config`). An invalid value (e.g. a non-numeric or out-of-range port) stops the sidecar at startup with a validation error. `Dockerfile.sidecar` copies the new file.
- The sidecar now requires Modal API credentials: `SidecarConfig` has `modal_token_id` and `modal_token_secret` (env `MODAL_TOKEN_ID`, `MODAL_TOKEN_SECRET`), and the sidecar refuses to start if either is missing or empty, instead of failing at the first training call. They are `SecretStr`s, so they do not appear in logs. Pass them to the container with `docker run -e MODAL_TOKEN_ID -e MODAL_TOKEN_SECRET ...`.

## [0.28.0] - 2026-10-26

### Added
- Admin UI **training** view: a `Run` button that starts a Gemma 4 fine-tuning job (model size 4B/12B, number of steps, and the training dataset, picked from the sidecar's datasets), the list of the training jobs in the database, and, for the selected job, a live feed of its events and a graph of its loss. Finished jobs show their stored history.
- Progress goes sidecar → Go → admin UI as Server-Sent Events. The sidecar writes status and progress events for each job (`running`, `training`, `completed` with the final loss, `failed` with the error, and a progress event per Trainer log) and streams them at `GET /training/job_events?job_id=[&from=]`. The Go server follows that stream for every job it starts (and resumes unfinished jobs after a restart), stores the events, keeps the job's row current, and forwards the events at `GET /v1/gb/admin/api/training/events[?db=][&run_id=]`, which replays a job's stored events before going live (`Last-Event-ID` is honoured). A job that never reports its end is marked `failed` 3 hours after it started.
- Admin endpoints `GET /v1/gb/admin/api/training/jobs[?db=][&limit=]` (the jobs, newest first) and `GET /v1/gb/admin/api/training/datasets` (the sidecar's training datasets).
- Training on a chosen dataset: `POST /training/job_start` (and `POST /v1/gb/admin/api/training/jobs`) take a `dataset` (file name on the sidecar's data volume; the sidecar answers 400 for an unknown one, which the admin endpoint passes on as 400). `modal run app.py --action train` takes `--dataset`.
- `gb_training_job_runs` gains `provider_job_id`, `dataset`, `last_step`, `last_loss`, `final_loss`, `error` and `finished_at`; new table `gb_training_job_events` (one row per event of a job). Go: `relational.ListTrainingJobRuns`, `UpdateTrainingJobRun`, `RecordTrainingJobEvent`, `ListTrainingJobEvents`, `NextTrainingJobEventSeq`; `sidecar.Client.StreamTrainingJobEvents`, `sidecar.TrainingEvent` and `sidecar.StatusError`.
- `GET /training/job_progress` takes an optional `job_id`.

### Changed
- `job_id` of a training job is now assigned by the sidecar (it names the job's progress file); the compute provider's id is the new `provider_job_id`. The job's `status` in `gb_training_job_runs` now follows the job (`started`, `running`, `training`, `completed`, `failed`) instead of staying `started`.
- The sidecar's `POST /datasets/upload` now stores the dataset under its own `filename` (a plain `*.jsonl` name; anything else is a 400) instead of always overwriting the default dataset, so several datasets can be kept and picked per job. The default dataset is still used when a job names none.
- The newest progress file (`GET /training/job_progress` without `job_id`) is the most recently modified one.
- `server.NewHandler` takes an `admin.TrainingSidecar` (adds dataset listing and event streaming to `admin.TrainingJobStarter`; `*sidecar.Client` is one) and `admin.StartTrainingJobOf` takes the `*admin.TrainingMonitor`.
- **Schema change**: existing SQLite database files are not migrated; delete the file and let the server recreate it (PostgreSQL: drop `gb_training_job_runs` first; it was added in 0.27.0).

## [0.27.0] - 2026-10-26

### Added
- `TrainingJob` struct on both sides: the sidecar's `POST /training/job_start` now returns it (`job_id`, `status`, `model_size`, `num_train_steps`, `started_at`, a timezone-aware UTC timestamp; `sidecar/ai_train/api/models.py`), and the Go sidecar client (`internal/sidecar`) decodes it with the new `Client.StartTrainingJob`. The earlier response fields are unchanged. The sidecar now also rejects a `num_train_steps` that is not a positive integer (400).
- `gb_training_job_runs` table (sqlite and postgres): one row for each training job the sidecar reported as started (job ID, status, model size, steps, when it started, when the row was made). Go: `relational.RecordTrainingJobRun` and `GetTrainingJobRun`.
- Admin endpoint `POST /v1/gb/admin/api/training/jobs[?db=<name>]`, body `{"num_train_steps"?, "model_size"?}` (`"4b"` or `"12b"`; omitted fields take the sidecar's defaults). It calls the sidecar's REST API through the Go client, records the job and answers 201 with the stored row. 400 for a bad body, 502 if the sidecar fails (nothing is recorded), 503 if no sidecar is set. Served with the admin UI.
- Config `sidecar_url` (env `SIDECAR_URL`, default `http://127.0.0.1:8787`): where the sidecar listens.

### Changed
- `server.NewHandler` takes the sidecar client (`admin.TrainingJobStarter`, may be nil) after `providers`.

## [0.26.2] - 2026-10-16

### Changed
- Sidecar HTTP API routes reorganised under prefixes (breaking for direct callers): `POST /training/job_start` and `GET /training/job_progress` (were `/start_training_job`, `/get_training_progress`), `POST /infer/query` (was `/generate_response`), `POST /evals/add_to_dataset` and `GET /evals/get_dataset_count` (were `/add_to_eval_dataset`, `/get_eval_dataset_count`). Docs updated.

### Removed
- The `/upload_dataset` and `/list_datasets` sidecar aliases; use `POST /datasets/upload` and `GET /datasets/list`.

## [0.26.1] - 2026-10-25

### Added
- Sidecar training datasets: a `TrainingDataset` pydantic model (`sidecar/ai_train/api/models.py`) used by the dataset endpoints, served at `POST /datasets/upload` and `GET /datasets/list`.
- The Go sidecar client (`internal/sidecar`) gains `UploadTrainingDataset` and `ListTrainingDatasets` and a `TrainingDataset` type, kept separate from the memory `Dataset`.

### Changed
- Dataset timestamps returned by the sidecar (`uploaded_at`, `modified_at`) are now timezone-aware UTC, so the Go client can parse them.

## [0.26.0] - 2026-10-25

### Added
- Gemma-4 fine-tuning and inference in `sidecar/ai_train/`, ported from `gloflow_phoenix/gf_phoenix/ai_train`: 4B (`google/gemma-4-E4B-it`, Unsloth, A10G) and 12B (`google/gemma-4-12B-it`, plain transformers + peft + trl, A100, because its `gemma4_unified` architecture cannot be loaded by Unsloth). Each size has its own image (`requirements_train.txt`, `requirements_gemma4_12b.txt`).
- `modal run app.py` actions `sync`, `validate`, `train --model-size 4b|12b` (replaces the stub) and `generate --prompt ...`; `modal serve app.py` serves the web API (`/training/job_start`, `/training/job_progress`, `/infer/query`, `/evals/*` and `/datasets/*` endpoints, `/healthz`).
- Live training progress written to the progress volume, adapters saved per size under `/models/finetuned_model_<size>`, the 325-example tool-calling dataset and its generator in `finetune_data/`, Gemma-4 docs in `docs/training/`, and local unit tests (`python -m unittest test_ai_train_core`).
- Provider interface gains `asgi_app`, `reload_volumes` and per-path `commit_volumes`; new config `SIDECAR_VOL_EVAL` and `SIDECAR_VOL_HF_CACHE`.

### Changed
- `SIDECAR_GPU` is now an optional override; by default 4b uses `a10g` and 12b uses `a100`.
- Fixed in the port: 12B inference now loads through transformers + peft (the original routed it through Unsloth, which cannot load that model).

## [0.25.0] - 2026-10-25

### Added
- Training bootstrap skeleton in `sidecar/ai_train/`, modelled on `gloflow_phoenix/gf_phoenix/ai_train`: `app.py` declares the remote functions (`ping`, a stub `train`) and a CLI entrypoint (`modal run app.py [--action ping|train]`); `config.py` holds provider-neutral settings (env overridable: `SIDECAR_PROVIDER`, `SIDECAR_GPU`, `SIDECAR_HF_SECRET`, volume names); `ai_train_core/bootstrap.py` is the code that runs inside the container.
- Compute providers sit behind `providers/base.py` (`Provider`, `FunctionSpec`, `ImageSpec`); Modal is the only implementation (`providers/modal_provider.py`, the only file importing `modal`), so others such as AWS can be added later.
- [docs/training/sidecar.md](docs/training/sidecar.md) gains a "Remote training bootstrap" section: layout, how to run it, the `SIDECAR_*` settings, the provider abstraction and how to add a provider.

## [0.24.1] - 2026-10-24

### Added
- `docker-compose.yml` runs the server and the Python sidecar together on one private network (`gligabao`). The server waits for the sidecar's health check, reaches it at `http://sidecar:8787`, keeps its data in the `gligabao-data` volume, and both ports are published on `127.0.0.1` only. `docker compose up --build` starts both (set `ANTHROPIC_API_KEY`, optionally `OPENAI_API_KEY`).

## [0.24.0] - 2026-10-24

### Added
- Python sidecar skeleton in `sidecar/`: `main_sidecar.py` is a minimal FastAPI server (`GET /health`) listening on `SIDECAR_HOST:SIDECAR_PORT` (default `127.0.0.1:8787`); dependencies in `sidecar/requirements.txt`.
- `Dockerfile.sidecar` packages the sidecar, and `run_build_sidecar.sh [image[:tag]]` builds the image (default `gligabao-sidecar:latest`).
- Go client for the sidecar in `internal/sidecar` (`sidecar.New`, `Client.Health`).
- [docs/training/sidecar.md](docs/training/sidecar.md): documents the sidecar, its container and the Go client, linked from the README.

## [0.23.1] - 2026-10-24

### Added
- [docs/hooks.md](docs/hooks.md): documents the hooks available to programs embedding gligabao (`ServeOptions.Hooks` system events with every event type and its fields, and the timeseries provider callback), linked from the README.

## [0.23.0] - 2026-10-15

### Added
- Remember manager. One goroutine now runs every run of the remember text pipeline, **one at a time** (runs are serialized for now). `POST /v1/gb/remember` and `POST /v1/gb/eval/remember` no longer run the pipeline themselves: they send the manager a request, get the ID of the run back at once, and wait for it to complete, so the HTTP behaviour is unchanged. Images are a different pipeline and are still remembered in the handler. See [docs/remember-manager.md](docs/remember-manager.md).
- A run is queued with its row in `gb_remember_runs` already created (status `started`, every stage `not_started`), so its ID is known before it starts and its status can be checked later; the row is overwritten when the run begins. A queued run that is cancelled (the client left) or never runs (shutdown) is marked `failed`.
- The queue is replaceable: by default a Go channel, or callbacks supplied with `memory_remember.WithQueue(QueueHooks{Enqueue, Dequeue})` so that an external queue system can be used. The queue carries run IDs only.
- Go: `memory_remember.NewManager`, `Manager.Submit`, `Manager.Status`, `Manager.Wait`, `Request`, `Outcome`, `QueueHooks`, `WithQueue`, `ErrManagerClosed`; `memory_core.WithRememberRunID`.

### Changed
- Go API: `remember.Handler`, `remember.EvalHandler`, `api.Register` and `api.RegisterEval` take the manager as an argument; `server.NewHandler` takes a context (it bounds the manager) and creates the manager. On shutdown the server stops the manager after the graceful HTTP shutdown.
- A remember request that fails before the pipeline starts, once queued, now leaves a `failed` row in `gb_remember_runs` (before, it left none).

## [0.22.0] - 2026-10-23

### Added
- Admin UI: a **remember runs** view with, per database, the number of runs of the remember pipeline in each status (`started`, `succeeded`, `failed`; click one to filter) and the list of the runs (newest first, paged). Clicking a run shows its parameters and, for each stage, its status, start, duration, model runs and error; a run that succeeded with a failed stage (web search, ...) is flagged in the list. Backed by `GET /v1/gb/admin/api/remember-runs`. See [docs/remember.md](docs/remember.md#admin-ui).

## [0.21.0] - 2026-10-22

### Added
- Remember run log. New table `gb_remember_runs`: one row per execution of the remember pipeline, the authoritative record of how it was executed. It has the `status` (`started`, `succeeded`, `failed`) and `error` of the call as a whole, so a run can be checked without looking at the stages, and for each stage of the pipeline (`classify_complexity`, `chunk_text`, `extract_graph`, `summarize_chunk`, `load_candidates`, `resolve_edges`, `store`, `store_series`, `web_search`, `fetch_series`, `observe_series`) a `<stage>_status` (`not_started`, `started`, `succeeded`, `failed`), an `<stage>_error` (`NULL` if none), `<stage>_started_at` / `<stage>_finished_at` and, for the stages that run a model, `<stage>_model_run_ids`: the IDs of their `gb_model_runs`. The row is written when the call starts and updated at every step. Also stored: the user, dataset, kind, `params` (how the call was made) and `artifact_id`. See [docs/remember.md](docs/remember.md#run-log).
- The `remember` response has `remember_run_id`, the ID of the call's row.
- Go: `relational.CreateRememberRun`, `SaveRememberRun`, `GetRememberRun`, `ListRememberRuns`, `RememberRun`, `RememberStage`, the status constants `RememberNotStarted`, `RememberStarted`, `RememberSucceeded`, `RememberFailed`, and `RememberStages`; In `memory_remember`, the pipeline keeps the log with single calls of `runLog` (`runlog.go`): `j.run.Start(stage)`, `j.run.Done(err, stage)` and `j.run.AddModelRun(stage, id)` for each model run; the calls never fail a call. `Extraction`, `EdgePlan` get a `ModelRunID` for that.
- **Schema change**: a new table; existing database files get it when the server starts (`CREATE TABLE IF NOT EXISTS`).

## [0.20.0] - 2026-10-21

### Added
- Admin UI: a **model runs** view with, per database, the totals of the recorded model runs (runs, tokens, cost), their breakdown by type, stage, model and thinking level, and the list of the runs (newest first, paged, filterable by clicking a breakdown row). Backed by `GET /v1/gb/admin/api/model-runs`. See [docs/model-runs.md](docs/model-runs.md#admin-ui).
- Go: `relational.ModelRunGroups`, `ListModelRuns`, `ModelRunGroup`, `ModelRunFilter`.

## [0.19.0] - 2026-10-20

### Added
- Model runs. New table `gb_model_runs`, the dynamic counterpart of the static `gb_models`: one row per invocation of a model, with `type` (`remember`, `remember_image`, `recall`), `stage` (`extract_graph`, `summarize_chunk`, `resolve_edges`, `resolve_series`, `extract_image`, `web_search`, `classify_complexity`, `embed`, `recall`), `model_id` (→ `gb_models`), `reasoning` (the thinking level it ran with: `none`, `low`, `medium`, `high`), `input_tokens`, `output_tokens`, `cost_usd` and `created_at`. **Every** model call is a run: each LLM call, each embedding request, each web search and each Jev classification. See [docs/model-runs.md](docs/model-runs.md).
- Thinking levels for every stage. `POST /v1/gb/remember`'s `reasoning` now takes any LLM stage (`extract_graph`, `summarize_chunk`, `resolve_edges`, `resolve_series`, `extract_image`), and `none` is kept as given. `POST /v1/gb/recall` takes `reasoning` too. `400` for `web_search` (it does not think) or an unknown level. Thinking is **off by default** (`none`) in both pipelines, so no thinking tokens are paid for unless a request asks for them.
- Eval runner: reasoning levels. A case takes `"reasoning"` (a level per stage, and `"recall"` for the recall's answer) and the runner `-reasoning extract_graph=low,recall=medium`, combined stage by stage like models (the case wins) and recorded in the experiment run's `params`. Every case of the suites now sets all of them explicitly to `none` (checked by a test), as the baseline for comparing levels.
- The chunk summary artifact references its run (`gb_artifacts.model_run_id`); `chunks[].summary` of the `remember` response gets `model_run_id`, and the `summarize_chunk` trajectory step too.
- A model without extended thinking (Claude 3 and 3.5) is called without a thinking budget, and its run records `none` (`ai.SupportsReasoning`, `ai.Response.Reasoning`).
- The Python client's `remember` accepts `reasoning` for any LLM stage and with an image (client version 0.7.0).
- Go: `relational.RecordModelRun`, `GetModelRun`, `ModelRunStats`, `ModelRun`, `ModelRunStat`; `memory_core.RecordRun`, `WithStage`, `StageOf`, `StageRecall`, `DefaultReasoning`, `ResolveRecallReasoning`; `ai.Response.ModelRunID`; `graph.Artifact.ModelRunID`, `graph.NewChunkSummaryArtifact.ModelRunID`.

### Changed
- `gb_llm_calls` is replaced by `gb_model_runs`; `relational.RecordLLMCall`, `LLMCallStats` and `LLMCallStat` are replaced by `RecordModelRun`, `ModelRunStats` and `ModelRunStat`; `memory_core.RecordCall` by `RecordRun`.
- A chunk summary artifact keeps only a reference to its run: `gb_artifacts.model_id` and `gb_artifacts.reasoning` (added in 0.18.0) are replaced by `gb_artifacts.model_run_id` (`graph.Artifact.ModelID` and `Reasoning` are removed; the run holds the model and the level, see `relational.GetModelRun`). `reasoning` is accepted with an `image` (stage `extract_image`).
- **Schema change**: existing database files are not migrated; delete the file and let the server recreate it.

## [0.18.0] - 2026-10-19

### Added
- Chunk summaries. `POST /v1/gb/remember` now also summarizes every chunk of a [chunked](docs/chunking.md#chunk-summaries) text with an LLM call of its own (new stage `summarize_chunk`, choosable in `models`; at most 512 output tokens; made with the extractions, before anything is stored, so a failed summary stores nothing). Each summary is stored as an artifact of its own, of kind `chunk_summary` (only the pipeline creates it), whose parent is the **chunk** (document, chunk, summary), with the session and times of the chunk. No entity or edge is extracted from it. A text that is not chunked has no chunk and so no summary.
- Each summary is embedded into a new vector index of its own, `dataset-<id>-summaries` (vector ID `summary-<artifact id>`, metadata `kind`, `artifact_id`, `chunk_id`, `parent_id`, `index`), in the same embeddings request as the chunk's. `VectorStats` (admin UI **View vec db**) lists it, and `DELETE /v1/gb/datasets/{name}` removes it.
- The model and the reasoning level that wrote each summary are tracked on its artifact: new columns `gb_artifacts.model_id` (→ `gb_models`, the model as the API reports it, registered if new) and `gb_artifacts.reasoning` (`none`, `low`, `medium` or `high`), both NULL for any other artifact.
- Reasoning levels. `POST /v1/gb/remember` takes `reasoning` (`{"summarize_chunk": "low"}`; levels `none`, `low`, `medium`, `high`; `400` for another stage, an unknown level, or with `image`). A level turns on extended thinking for the stage with a budget of 1024, 4096 or 16384 tokens on top of the answer's `max_tokens`. `summarize_chunk` is the only stage that takes one for now.
- The response of a chunked text gets `embedded_summaries` and, per entry of `chunks`, its `summary` (`artifact_id`, `text`, `model`, `reasoning`). Trajectories get a `summarize_chunk` step per chunk, and the `embed` step also embeds the summary.
- Go: `graph.ArtifactChunkSummary`, `graph.NewChunkSummaryArtifact`, `Tx.InsertChunkSummaryArtifact`, `Artifact.ModelID` / `Reasoning`, `Counts.Summaries`, `memory.VectorKindSummary`, `memory.SummaryVectorID`, `memory_core.IndexSummaries`, `memory_core.SummaryDataset`, `memory_core.StageSummarizeChunk`, `memory_core.Reasoning`, `WithReasoning`, `ResolveReasoning`, `ai.Request.Reasoning` and `ai.Reasoning*`, `Result.EmbeddedSummaries`, `ChunkResult.Summary`.
- The Python client's `remember` takes `reasoning` (client version 0.6.0).

### Changed
- `Artifact.IsDerived()` is also true for a chunk summary (so it is not a chunk), and a chunked text now stores two artifacts per chunk, so `Counts.Artifacts` is larger by the number of chunks. A chunked `remember` makes at least one more LLM call per chunk.
- **Schema change** (`gb_artifacts.model_id`, `gb_artifacts.reasoning`): existing database files are not migrated; delete the file and let the server recreate it. Chunks stored before this have no summary.

## [0.17.0] - 2026-10-18

### Added
- Admin UI, timeseries view: each series shows a small line plot (green/red/grey by trend) as a preview. `GET /admin/api/timeseries` returns a `preview` per series: at most 120 points as `[unix ms, value]`, downsampled keeping each bucket's min and max.

## [0.16.0] - 2026-10-15

### Added
- Eval runner: `-suite <name>` runs only the cases of that suite (unknown suite is an error listing the available ones) and `-list-suites` prints the suites with their cases. `run_evals.sh` takes `-s|--suite NAME` and `-h|--help`, which shows a formatted list of the eval suites.

## [0.15.1] - 2026-10-15

### Added
- Eval `timeseries_ingest_aapl` (suite `timeseries.json`): remembers a text with made-up Apple price and volume series and checks that they were stored (points, first/last values, anchors). The eval runner supports `timeseries` in a `remember` entry, `expect_series` in a case, and skips the recall for a case with no `query`.

## [0.15.0] - 2026-10-17

### Added
- Admin UI `timeseries` view, filled in: it lists the timeseries stored in each database (standard and eval), across the datasets of all users, newest update first. Per series it shows the period it covers (first to last point, UTC) and its length in minutes, hours or days (the largest unit that fits; hover for all three), the entities it belongs to (anchored directly, endpoints of an anchored fact, or mentioned by an anchored text), its name, key, field, unit, frequency, source and provider, point count, dataset and owner, last update, the change and min/max/mean of its values, and, folded, its anchors (with role and window) and the fetch definition.
- `GET /v1/gb/admin/api/timeseries?db=<name>[&include_deleted=true]` (admin UI only): the series of all datasets with their stats and labelled anchors, without points.

## [0.14.0] - 2026-10-16

### Added
- Admin UI views. Buttons stacked on the left of the page (always vertically centered on the screen, whatever the height of the view, and fixed while scrolling; their right edge on the left edge of the view) switch between the `main` view (the existing dashboard without evals), a new `evals` view (the evals block moved there from `main`) and a new `timeseries` view (an empty placeholder for now). The chosen view is kept in the URL hash. On narrow screens the buttons move into a row above the view.

## [0.13.0] - 2026-10-16

### Added
- `series_observation` artifacts. `POST /v1/gb/remember` takes `timeseries.observations: true` (with `series`, `fetch` or both; off by default). After the text, the web search and the fetch, `remember` derives a new kind of artifact from each series it stored (supplied or fetched, at most 10 per request): a text generated in Go, not by an LLM, from the series' stats (change, trend, high, low, average, largest move) over the period the request wrote, naming the entity the series is about. That text is extracted, reconciled and stored like any other, so what a series says reaches the graph as bi-temporal facts, deduplicated and embedded like all others. A failure never fails `remember`: it is reported per observation. The response gets `series_observations`. See [docs/timeseries.md](docs/timeseries.md#observations).
- An observation artifact has `kind` `series_observation`, the series it describes (`series_id`), a `query` saying which series and window, and as its parent the artifact that anchors the series (the call's artifact, or for a fetched series the artifact the entity came from). Its `valid_at` is the last point of the window. The provenance chain reads: fact, observation artifact, series, anchoring artifact. Like a web search it is not a chunk.
- Trajectories get the `timeseries_observations` input and an `observe_series` step per series.
- The Python client's `remember` takes `observe_timeseries` (client version 0.5.0).
- Go: `memory_core.WithSeriesObservations`, `Result.SeriesObservations`, `graph.ArtifactSeriesObservation`, `graph.Artifact.SeriesID` and `IsDerived`, `Tx.InsertSeriesObservationArtifact`, `memory_timeseries.ObservationText`.

### Changed
- Database schema: `gb_artifacts` gets a `series_id` column (→ `gb_series`, `ON DELETE SET NULL`). Existing SQLite database files are not migrated: delete the file (and the eval database) and the server recreates it. PostgreSQL adds the column by itself.
- `recall` is not changed: it does not follow observations to their series (on hold, see [docs/timeseries-design.md](docs/timeseries-design.md)).

## [0.12.0] - 2026-10-15

### Added
- Timeseries providers. A program embedding gligabao registers a list of providers when it starts the server (`gligabao.ServeOptions.TimeseriesProviders`). A provider (`gligabao.TimeseriesProvider`) has a `Name`, a natural-language `Description` of what it offers and **in which cases it should be used**, an optional `QueryFormat` (what identifies an entity to it, e.g. a ticker symbol) and a `Fetch` callback with the logic of fetching. The providers are validated at startup.
- `POST /v1/gb/remember` takes `timeseries.fetch` (`providers`, `before`, `after`; all optional; alone or with `series`). After the text is stored, an LLM (new stage `resolve_series`, choosable in `models`) reads the providers' descriptions and decides which provider fetches series for which entity of the text (for example a stock price provider for an entity that is a public company), and what to ask it. The callbacks then run in parallel for the period around the text's reference time (by default 30 days before it and nothing after it, so a memory never holds the future of its event), and the series are stored and anchored to the entity and to the artifact it came from, with the period as the window. Fetching again adds points to the same series. A failure in a provider, the LLM or the storage never fails `remember`: it is reported per fetch. The response gets `series_fetches`. `400` for an unknown provider or a malformed window, `503` if the server has no provider.
- Fetched series have `source` `fetched`, their `provider` and a `definition` (entity and query).
- Trajectories get the `timeseries_fetch` input and the steps `select_series_entities`, `resolve_series_providers` and `fetch_timeseries`.
- The Python client's `remember` takes `fetch_timeseries` (client version 0.4.0).
- Go: `memory_core.SeriesProvider`, `ResolveSeriesProviders`, `WithSeriesProviders`, `WithSeriesFetch`, `ResolveSeriesFetch`, `ParseFetchWindow`; `Result.SeriesFetches`.

### Changed
- `timeseries.fetch` is no longer a `400`; `timeseries` needs `series`, `fetch` or both.
- The stages that take a model (`models`) now include `resolve_series`.
- Go: `api.Register`, `api.RegisterEval` and `server.NewHandler` take the registered providers (nil for none).

## [0.11.0] - 2026-10-14

### Added
- Timeseries. `POST /v1/gb/remember` (and `/v1/gb/eval/remember`) takes an optional `timeseries` with `series`: a series has a `key` (and optional `field`, `name`, `unit`, `frequency`), `points` as `[time, value]` pairs and optional `anchors`. The series are stored next to the text, in the same transaction, and anchored to the artifact the request creates (the default) and to the entities and facts that their anchors select. The entities do not exist when the request is made, so an anchor is a selector (`entity: {name, type}`, `edge: {source, relation, target}`) that is looked up after the text has been extracted and stored; IDs of things already stored (`artifact_id`, `entity_id`, `edge_id`) work too. An anchor can have a `role`, a `window` and an `on_unmatched` (`create`, `ignore`, `error`). A series that cannot be anchored stores nothing (`422`). For a chunked text the series are stored after the chunks, anchored to the parent artifact. The response gets `series`. Not allowed with `image` (`400`); `timeseries.fetch` is reserved and is a `400`. See [docs/timeseries.md](docs/timeseries.md).
- `POST /v1/gb/timeseries` stores series in an existing dataset and anchors them to what it holds, for series that are known only after `remember` returned, that change later, or that are too big for a `remember` request (body up to 16 MiB). Sending a key again adds points to the stored series. `GET /v1/gb/timeseries` lists a dataset's series (stats and anchors, no points) and `GET /v1/gb/timeseries/{id}` reads one with its points between `from` and `to` (and the stats of that window).
- Every series has stats computed in Go whenever points are written: count, first, last, min, max, mean, change, percent change, trend and the largest move between two points.
- New tables `gb_series`, `gb_series_points` and `gb_series_anchors` (SQLite and PostgreSQL). Anchors are real foreign keys, so deleting an artifact, entity or edge removes only its anchors.
- Trajectories get the `timeseries` input and a `store_timeseries` step.
- The Python client's `remember` takes `timeseries`, and has `attach_timeseries` (client version 0.3.0).
- Go: `memory_core.WithSeries`, `ResolveSeries`, `SeriesSpec`; package `memory_timeseries` (`Store`, `Attach`, `Summarize`); `graph.ListSeries`, `GetSeries`, `SeriesPoints`, `ListSeriesAnchors`; new `Tx` methods (`UpsertSeries`, `UpsertSeriesPoints`, `ListSeriesPoints`, `UpdateSeriesSummary`, `AnchorSeries`, `FindEntity`, `FindEdgeID`, `CheckArtifact`); `Result.Series`.
- [docs/timeseries-design.md](docs/timeseries-design.md): the design, the changes made while building the first version, and what is left (fetching series from a data provider, derived observation artifacts, embedding, `recall`).

### Changed
- Database schema: three new tables (see above). Existing database files are not migrated: delete the file (and the eval database) and the server recreates it.
- `remember` returns `422` when a timeseries anchor that must match finds nothing.

## [0.10.0] - 2026-10-14

### Added
- Admin UI: web search. A new **Web search** card shows, per database, the totals of the web searches `remember` made: `web_search` artifacts, the datasets that hold them, sources, entities linked to a result (and those only known from the web), facts created or supported, and, for the eval database, the totals recorded in the eval trajectories (entity searches found / empty / failed, searches made by the search tool, entities and facts extracted, latency, cost). Each dataset with web searches has a **View** button.
- Admin UI: the runs of an experiment show how many web searches each eval made, and an eval that made some gets a **View web searches** button. The list shows, per search, its result, sources, the queries sent to the search engine, the entities and facts extracted from the result, its latency, model, tokens and cost (searches read from a dataset, not from a trajectory, lack the latency, cost and search-engine queries).
- Admin API: `GET /v1/gb/admin/api/web-searches`, `GET /v1/gb/admin/api/web-searches/dataset` and `GET /v1/gb/admin/api/evals/runs/{id}/web-searches`. See [docs/web-search.md](docs/web-search.md#admin-ui).

## [0.9.0] - 2026-10-14

### Added
- Web search for `remember`. `POST /v1/gb/remember` (and `/v1/gb/eval/remember`) takes an optional `web_search` flag (default `false`). When set, after the text is stored the key entities of the text (the extracted entities in the most facts, concepts excluded, at most 3) are searched for on the web, to get the latest world events about each. The search is Anthropic's cloud-based web search tool (`web_search_20250305`), so it is only available when an Anthropic API key is supplied: a request with `web_search` and no key returns `503` (and `400` together with an `image`). The server logs at startup whether web search is available. See [docs/web-search.md](docs/web-search.md).
- Each web search result is an artifact of its own, of the new kind `web_search` (a caller cannot ask for that kind). It saves the query that was issued (new column `gb_artifacts.query`) and its parent (`gb_artifacts.parent_id`) is the artifact from which the searched entity came (for a long text, the chunk). The graph is extracted from the result like from any text (reference time: now), reconciled with the stored facts, and stored: the entities discovered on the web are ordinary nodes, linked to the `web_search` artifact (and to the call's session), embedded when vectors are configured. The searched entities are not searched for in turn.
- The `remember` response gets `web_searches`: per entity searched, its query, the parent artifact, the `web_search` artifact, the sources and what the result yielded (`entities`, `edges`, `invalidated_edges`).
- New model stage `web_search` for `models` (the model that runs the web search tool; the results are extracted on `extract_graph`). The Python client's `remember` takes `web_search`.
- The web search is an enrichment that runs after the text is stored: a failure in it is logged, recorded in the trajectory and reported in the result, and never fails the request. It is billed by Anthropic ($0.01 per search, `ai.WebSearchRequestUSD`) on top of the tokens; that is included in the recorded cost (`gb_llm_calls`, trajectory totals). Trajectories get the steps `select_web_search_entities` and `web_search`.
- Eval: the suite `web_search.json` with one case, `web_search_companies`, that remembers a text about Apple, Microsoft and Nvidia with `web_search`; remember entries of a suite can set `"web_search": true`, and a case `expect_web_searches` (checks on the stored `web_search` artifacts).
- Go: `ai.Client.WebSearch`, `memory_core.WebSearcher`, `WithWebSearcher` (makes it available), `WithWebSearch` (asks for it per call), `ErrWebSearchUnavailable`, `StageWebSearch`, `graph.ArtifactWebSearch`, `Tx.InsertWebSearchArtifact`, `Artifact.Query` and `Artifact.IsChunk`, `Result.WebSearches`.

### Changed
- Database schema: `gb_artifacts` has a new column `query`. Existing database files are not migrated: delete the file (and the eval database) and the server recreates it.
- A child artifact is no longer always a chunk: `Counts.Chunks` counts the artifacts with a `chunk_index`, not those with a `parent_id`.
- Go: `server.NewHandler`, `api.Register` and `api.RegisterEval` take one more argument, the web searcher (nil for none; the server passes the Anthropic client).

## [0.8.0] - 2026-10-14

### Added
- Jev, TypeSafe's "system 1" model, as a small package (`internal/jev`) that wraps calls to its evaluation endpoint (`POST https://api.typesafe.ai/v1/systemone`, model `jev-latest`). It is only used when its API key is supplied: `jev_api_key` (default: the `JEV_API_KEY` environment variable). `jev_base_url` (`JEV_BASE_URL`) and `jev_model` are optional overrides. Without a key the server logs that Jev is off and nothing below runs. A call is one request and is not retried, so the measured latency is that of one request.
- First use, a new first stage of `remember`, `classify_complexity`: Jev answers one `choice` question that classifies the incoming text as `simple` (up to 5 entities), `medium` (6 to 20) or `complex` (more than 20) in the number of entities it is expected to yield, as a prediction of the compute needed per entity and edge. The `confidence` Jev returns with its answer, and the probability of each class, are tracked. For now the result is only logged (`jev: remember text complexity`, with `complexity`, `confidence`, `latency_ms`, text size). The stage is advisory: a Jev failure is logged and never fails `remember`. It always runs on Jev, so it is not part of `models`. At most the first 32 KiB of a text are sent to Jev.
- The stage is a step of the ATIF trajectory of the eval endpoints (`classify_complexity`) with its result (complexity, confidence, probabilities), the Jev model version and tokens, and `latency_ms` / `started_at` in the step's extra (also for a failed call), so Jev latency can be measured and compared across runs.
- Go: `jev.Client` (`Evaluate`, `Classify`), `memory_core.WithJev`, `memory_core.Classifier`, `Trace.TimedTool`.

### Changed
- Go: `server.NewHandler`, `api.Register` and `api.RegisterEval` take one more argument, the Jev classifier (nil for none).

## [0.7.0] - 2026-10-14

### Added
- Per-stage LLM models for `remember`. `POST /v1/gb/remember` (and `/v1/gb/eval/remember`) takes an optional `models` object that picks the model of each LLM-invoking stage: `extract_graph` (entities and edges), `resolve_edges` (duplicate/contradiction check) and `extract_image` (the image pipeline). It can be given with every call. A stage that is left out, or given as an empty string, uses the default model, so a call can change one stage and keep the others; without `models` nothing changes. An unknown stage or a malformed model name returns `400`.
- The model that runs the `extract_graph` stage is the one recorded as the `model_id` of the entities and edges it yields (the model name as the provider reports it), so the stored knowledge reflects the model chosen for that stage.
- Evals can vary the models too: `go run ./evals -models extract_graph=<model>,resolve_edges=<model>` sets them for a whole run (recorded in the experiment run's `params`), and a case of a suite can set `"models"` of its own, which wins stage by stage.
- Go: `memory_core.WithModels`, `memory_core.Models` (`ResolveModels`, `ParseModels`, `Merge`), the `memory_core.Stage*` constants and `CallOptions.ModelLLM`. Eval trajectories record the requested `models` in `extra.input`.
- The Python client's `remember` takes `models`.

## [0.6.1] - 2026-10-14

### Added
- New eval case `chunking_register` (suite `evals/suites/chunking.json`): remembers a ~3,700-word, 60-person register (`evals/suites/corpus/harbor_cooperative.txt`) that needs about 20 chunks, then checks that facts from the start, middle and end were extracted and recalled, and that the chunks are stored correctly (count, size, child artifacts, coverage and overlap of the text, provenance of the facts).
- Eval suites: a `remember` entry can take `text_file` (a file under the suites directory) instead of `text`; a case can set `chunk_size` and `expect_min_chunks` to turn on the chunking checks. See `evals/README.md`.

### Changed
- The eval runner waits up to 30 minutes (was 2) for a `remember` call, since a chunked text is extracted chunk by chunk in one request.

## [0.6.0] - 2026-10-14

### Added
- Chunking of long texts. `POST /v1/gb/remember` with a text of more than one chunk (`chunk_size`, 1024 tokens by default) no longer extracts it in one LLM call: the text is stored as an artifact (the parent), split into overlapping chunks, and every chunk is an artifact too, a child of the parent. See the new `docs/chunking.md`.
- The chunker (`internal/chunk`) sizes chunks in tokens (a pure-Go estimate of a BPE tokenizer by default; `chunk.Tokenizer` can be replaced from Go), cuts at paragraph boundaries (oversized paragraphs fall back to sentences, then words), and repeats the tail of each chunk at the start of the next one (`chunk_overlap_percent` of the chunk size, 20 by default, at most 50).
- Each chunk is processed individually: its own entity/edge extraction, candidate comparison against the facts the earlier chunks stored, storage, and embedding. Entities and edges link to the chunk they came from.
- Chunks are embedded into a dedicated vector dataset, `dataset-<id>-chunks` (vector ID `chunk-<chunk artifact id>`, metadata `kind`, `artifact_id`, `parent_id`, `index`), next to the entity and edge ones, using the same embeddings request as the chunk's entities and edges. It is listed by `VectorStats` (admin UI "View vec db") and removed with the dataset.
- Config: `chunk_size` (default 1024; 32 to 8000) and `chunk_overlap_percent` (default 20; 0 to 50), validated at startup. Go: `memory_core.WithChunking`, `gligabao.Config.ChunkSize` / `ChunkOverlapPercent`.
- The remember response gains `chunks` (artifact ID, index, tokens, overlap, byte range, number of entities and edges extracted per chunk) and `embedded_chunks`, only for a chunked text; `artifact_id` is then the parent's. Eval trajectories gain `chunk_text` and `store_parent_artifact` steps.
- Go: `graph.Tx.InsertChunkArtifact`, `graph.NewChunkArtifact`, `Artifact.ParentID` / `ChunkIndex` / `StartOffset` / `EndOffset` / `Tokens`, `Counts.Chunks`, `memory.ChunkVectorID`, `memory_core.ChunkDataset`, `memory_core.IndexChunks`.

### Changed
- DB schema: `gb_artifacts` gains `parent_id` (self-reference, `ON DELETE CASCADE`), `chunk_index`, `start_offset`, `end_offset` and `tokens`. There is no migration: existing databases (and vector directories) must be recreated.
- A text that is not larger than a chunk is processed exactly as before: no chunks and no chunk vector.
- A chunked text is not one transaction: the chunks go in one after the other (each chunk's candidates must see the previous ones), so a failure while storing chunk *k* leaves the parent and the chunks before it. An extraction failure still stores nothing, because every chunk is extracted before anything is written.
- `graph.Counts.Artifacts` counts chunk artifacts as well; `Counts.Chunks` says how many of them are chunks.
- Go: `graph.Tx` gained `InsertChunkArtifact` (implement it in custom backends); `memory_remember.Result` gained `Chunks` and `EmbeddedChunks`.

## [0.5.0] - 2026-10-14

### Added
- Optional `session_id` on `POST /v1/gb/remember` (text and image) and `POST /v1/gb/recall`: a string the caller chooses (at most 128 bytes, no control characters, else `400`) that labels one user/agent session. In Go it is the option `memory_core.WithSessionID`.
- `remember` attaches the session to the artifact (`gb_artifacts.session_id`, and `gb_image_artifacts.session_id` for images) and echoes it as `session_id` in the response.
- New tables `gb_entity_sessions` and `gb_edge_sessions` record which sessions each entity and each edge (fact) was discovered in. They are written for every entity and edge an artifact of a session mentions or states, including ones that already existed and were therefore not written again, so a session that only repeats known facts is still recorded. A call without a session adds no rows.
- `recall` echoes `session_id`, and sets it as the `session_id` of its eval trajectory (previously always the trajectory's own ID; `remember` trajectories do the same), so the calls of one session can be grouped. It does not change retrieval or the answer, and nothing about the recall is stored. The session ID is also in the `remember.*` / `recall.*` monitoring events and log lines.
- `docs/sessions.md`, and the session fields documented in `remember.md`, `recall.md`, `artifacts.md`, `images.md`, `evals.md` and the README.
- Go: `graph.ListEntitySessions`, `graph.ListEdgeSessions`, `Artifact.SessionID`, `ImageArtifact.SessionID`, `atif.Builder.SetSessionID`.

### Changed
- DB schema: new `session_id` columns and the two link tables. There is no migration: existing databases must be recreated.
- `graph.Tx.InsertArtifact` takes a `sessionID` argument; `Tx` gained `LinkEntitySession` and `LinkEdgeSession`.
- `docs/not-included.md`: the session memory entry now says that the `session_id` label is included and only the per-session cache of turns is left out.

## [0.4.5] - 2026-10-14

### Added
- `docs/not-included.md`: a list of things gligabao deliberately leaves out, starting with session memory (the Cognee-style per-conversation cache), why it is excluded, and the rationale around exact/keyword matching.

## [0.4.4] - 2026-10-14

### Changed
- Refactor, no behaviour change: the text pipeline moved out of `internal/memory` into two new subpackages. `internal/memory/memory_remember` has `Remember`, `Extract`, `Store`, `Result`, `ResolveKind`, `CallRemember` and the fact reconciliation, and `internal/memory/memory_recall` has `Recall`, `RecallResult`, the evidence types and `CallRecall`. What both share moved to `memory_core`: `Fact` (with `FactFromEdge`, `FactFromEvidence`), `Key`, `SynthStatement`, `DatasetID` and `ErrDatasetNotFound` (`memory.ErrDatasetNotFound` is still the same error). Go code that embeds gligabao must use the new import paths (e.g. `memory_remember.Remember`, `memory_recall.Recall`).

## [0.4.3] - 2026-10-14

### Changed
- Refactor, no behaviour change: the image pipeline (`RememberImage`, `LoadImage`, `ExtractImage`, `ImageDir`, `WithImageDir`, ...) moved from `internal/memory` to the new subpackage `internal/memory/memory_image`. What it shares with the text pipeline (the `LLM` interface and call recording, per-call options and the ATIF trace, user and dataset name validation, JSON extraction from LLM output) moved to the new subpackage `internal/memory/memory_core`. Go code that embeds gligabao must use the new import paths (e.g. `memory_core.LLM`, `memory_core.WithEval`, `memory_core.TrajectoryDir`, `memory_image.RememberImage`).

## [0.4.2] - 2026-10-14

### Changed
- Admin UI: the header is narrower and has no background or border; only the title and the health indicator are visible.

## [0.4.1] - 2026-10-14

### Added
- Admin UI: the lower right corner shows, in small text, the running gligabao version and a "github url" link to the commit the binary was built from (only when the binary was built inside a git checkout).
- `GET /version`: the `gligabao` object has a new `commit_url` field (GitHub URL of the full commit).

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
