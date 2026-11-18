# Geo

Some entities are places: a city, a country, a region, a landmark. `remember` finds them and stores their coordinates, so the
graph knows where things are. The code is the package `internal/memory/memory_geo` (the resolvers) and `memory_remember/remember_geo.go` (the
stage of the pipeline); the data is the table `gb_entity_geo`.

`remember` does not wait for the geocoder: the places are resolved later by the [geo manager](#the-geo-manager) of the server (or at once with `geo_sync`). `query` uses it too, with the same switch: when the request asks for it, it finds the geo entities near the places of a question and gives them to the LLM that answers, within a radius you can set, and stores the places of the question it resolves (see [In `query`](#in-query)).

## Turning it on

Off by default: it costs one more LLM call per text (an entity list is sent to the `detect_geo` stage) and it sends the names of
places to a geocoder.

- `geo = true` in `gligabao.toml`, or `serve --geo`: uses the public Nominatim geocoder of OpenStreetMap (cached; one request per
  second, as its [usage policy](https://operations.osmfoundation.org/policies/nominatim/) asks).
- A program that embeds gligabao sets `ServeOptions.GeoResolver` (any `gligabao.GeoResolver`); that turns geo on by itself.
  For Go code that builds the call itself: `memory_core.WithGeoResolver(r)`, and `api.WithGeoResolver(r)` for `api.Register`.
  The server puts the resolver behind its [geo manager](#the-geo-manager); a plain resolver given straight to `memory_core.WithGeoResolver` (not a manager) is asked inline, as before.

## Geo index in the admin UI

The top bar of the admin UI has a `geo index` badge: green (`geo index: rtree` or `geo index: postgis`) when the database can index
coordinates, red (`geo index: off`) when it cannot; hover for the version or the reason. Geo queries (proximity) need it:
SQLite needs the R*Tree module (compiled into the gligabao binary, so always on), PostgreSQL needs the PostGIS extension
(`CREATE EXTENSION postgis` in the database; the badge says if PostGIS is installed on the server but not enabled).
The badge is a live probe by `GET /v1/gb/admin/api/geo-index[?db=<name>]`:
`{"backend":"sqlite","enabled":true,"index":"rtree","version":"3.53.4"}` (`available` and `detail` explain a disabled state).
The probe only reports. gligabao creates and fills the index itself (see [The geo index](#the-geo-index)), but never enables the
PostGIS extension.

## The geo index

When geo is on, every row of `gb_entity_geo` is also in the geo index of the database, so that the proximity queries of
`query` ("what is within 50 km of Paris") can fetch the nearby entities without scanning the table. The index is created at
startup **only when geo is on** (`geo = true`, `--geo` or a `GeoResolver`; Go: `db.Config.Geo`, `graph.EnableGeoIndex`); with geo
off nothing is created.

- **SQLite**: the R*Tree table `gb_entity_geo_rtree(entity_id, min_lat, max_lat, min_lon, max_lon)`, one point (min = max) per geo
  entity. Triggers on `gb_entity_geo` keep it in step: a place stored by `remember`, a place moved by a later upsert, and a place
  removed with its entity (the cascade) are all reflected. Query it for a bounding box first, then compute exact distances.
- **PostgreSQL**: when PostGIS is enabled in the database (`CREATE EXTENSION postgis`), `gb_entity_geo` gets the generated column
  `geo geography(Point, 4326)` (always the point of `latitude`/`longitude`) with the GiST index `idx_gb_entity_geo_gist`; query it
  with `ST_DWithin(geo, ST_MakePoint(lon, lat)::geography, meters)`. Without PostGIS startup logs a warning and creates neither;
  the places are stored all the same. After enabling the extension, restart with geo on.

There is no backfill: entities stored while geo was off have no place in `gb_entity_geo` (they are not looked up) and so are not
indexed; finding and indexing them would be a pipeline of its own. (On PostgreSQL the generated column does cover rows that
already existed in `gb_entity_geo`, because adding it cannot skip them; on SQLite those rows stay out of the R*Tree.)

## The geo view of the admin UI

The `geo` view of the admin UI shows, per database, how many entities are geo entities (have a row in `gb_entity_geo`, in live
datasets) out of all entities, and a table of the 100 most connected ones. The connections of an entity are its live (not
expired) edges, as source or as target (a self-loop counts once). Click an entity to open a modal with an
[OpenStreetMap](https://www.openstreetmap.org) map (Leaflet on the tiles of openstreetmap.org, so the browser needs internet access):
the entity (red, with the bounding box of its place), a circle of the search radius, and the geo entities near it (blue, with their
distance), chosen by the same code as the proximity step of `query` ([Nearby entities without a question](#nearby-entities-without-a-question)).
The side panel has the details of the place, a radius field (default: the server's `geo_radius_km`) and the list of the nearby
entities (click one to center the map on it; click its marker to search around it). The list is read by
`GET /v1/gb/admin/api/geo/nearby`, the same handler as the public endpoint, with `db`. The list of the entities is read by `GET /v1/gb/admin/api/geo[?db=<name>]`:
`{"entities":12,"all_entities":340,"top":[{"entity_id":7,"name":"Paris","connections":9,"kind":"city","latitude":48.85,...}]}`
(Go: `admin.GeoOf`, `graph.GeoStatistics`).

## What the stage does

`resolve_geo` runs after the text (every chunk, for a long text) is stored and before the web search. It never fails the call:
errors are logged, recorded in the trajectory and run log, and returned in `geo` of the result.

1. **Candidates**: the extracted entities that may be places (not a `person`, `concept`, `product` or `event`), at most 50,
   minus those that already have a row in `gb_entity_geo`: an entity is shared by every artifact that mentions it, so it is
   looked up once. A place the resolver did not know has no row, and is asked again the next time it is mentioned.
2. **Detect** (LLM, stage `detect_geo`): which candidates are places, their `kind` (`country`, `region`, `city` or `place`) and
   what to ask the resolver: the name plus what disambiguates it (`"Paris, France"`). It prefers precision: an entity it is
   not sure is a place is left out ("Bank of America" and "Paris Hilton" are not places).
3. **Queue** (with the geo manager, the default of a server): each place found (at most 25) is recorded in `gb_geo_pending` and queued in the manager, and the call goes on without waiting for the geocoder. The manager resolves it later ([The geo manager](#the-geo-manager)), so the places come back in `geo` as `pending` and without coordinates, unless the request sends `geo_sync: true` (below). A plain resolver is asked here instead, one place at a time ("not found" is not a failure).
4. **Store**: one transaction writes the rows of `gb_entity_geo`, which are then in the geo index too ([The geo index](#the-geo-index)). With the manager this is done by the manager when it has the answer, and the pending record is removed in the same transaction.

`detect_geo` takes a model (`models`) and a reasoning level (`reasoning`) like the other LLM stages
([remember/remember.md](remember/remember.md#choosing-the-model-of-each-stage)). In the run log the stage is `resolve_geo` and holds the
`detect_geo` inference.

## The geo manager

A server with geo on has ONE geo manager (`memory_geo.Manager`, `internal/memory/memory_geo/manager.go`): a single goroutine, started with the server and stopped with its context (not with a request's), that owns the resolver and asks it for one place at a time. A public geocoder allows one request per second, so the manager is also where that is kept, however many pipelines run. The server wraps its resolver in it (`ServeOptions.GeoResolver` or Nominatim); nothing else has to be set.

It has two queues, and **the query queue always wins**:

- **Query queue** (1000 requests): `Manager.Resolve`, the `Resolver` interface of the manager, is what `query` calls (the proximity search, [Nearby entities](#nearby-entities); it only asks when the request asked for that, which is off by default). The caller blocks on the answer. The manager takes from this queue first and only looks at the remember queue when it is empty; a request being resolved is not interrupted, so a query can wait for the one remember job in progress (at most the 30 s timeout of a resolver call). A request whose caller gave up is skipped.
- **Remember queue** (10000 jobs): `Manager.Enqueue` takes a job (`memory_geo.Job`: the key, which is the entity ID, the query for the resolver and a `Done` function) and returns at once. A key that is already queued or being resolved is refused (`ErrDuplicate`: an entity is resolved once however many calls mention it), and a full queue refuses too (`ErrQueueFull`; the place stays pending). `Done` is called by the manager with the answer and stores it: the coordinates go to `gb_entity_geo` and the pending record is removed; "not found" removes the pending record (a new mention asks again); a failure of the resolver counts an attempt on it. An entity that was deleted meanwhile is not an error. `Manager.Flush` waits for the jobs queued before it.

**Pending places** are rows of `gb_geo_pending` (`entity_id`, `kind`, `query`, `attempts`, `last_error`, timestamps; the row goes with the entity): every place the LLM found gets one when `remember` queues it, and it is removed when it is resolved. They make the work durable. The **geo sweeper** (`memory_remember.RunGeoSweeper`, started with the manager) queues the pending places at startup and every two minutes (500 at a time, those with fewer than 5 failed attempts), so a place whose resolving was lost (a restart, a full queue) or whose resolver was down is asked again. A new mention of the entity starts its attempts again.

**`geo_sync`**: a `remember` request with `"geo_sync": true` (Go: `memory_core.WithGeoSync(true)`, `RememberRequest.GeoSync`) records and queues the places as usual, then waits for the manager (`Flush`) before it returns, so the coordinates are in `geo` as with a plain resolver. It is for callers that read the coordinates right after: the knowledge evals send it, so they need no change. A place that another call had already queued is read from the database after the wait.

**Reading the coordinates.** Without `geo_sync`, call `GET /v1/gb/geo` later: `dataset` (default `main`; the user is the `gb-user-id` header) and optionally `entity_id` give `{"dataset": "main", "entities": [{"entity_id": 12, "name": "Paris", "type": "place", "kind": "city", "latitude": 48.85, "longitude": 2.35, "display_name": "...", "country": "France", "country_code": "fr", "region": "...", "bbox": [..], "provider": "nominatim"}], "pending": 1}`. `pending` is how many places of the dataset are still to be resolved: call again until it is 0. `404` for an unknown dataset, or an `entity_id` that has no coordinates yet; `400` for a bad parameter. It needs no LLM and no resolver. Go: `query.GeoHandler`.

**The worker** (`gligabao_worker`) has no manager (see [worker_binary.md](worker_binary.md)): with geo on its `remember` only detects the places and records them as pending (`memory_core.WithGeoDeferred(true)`), and the server that shares the database resolves them through its sweeper. A server with geo off never does. The worker's `query` asks the resolver directly, so it does not get the priority of the server's query queue, and shares the geocoder's rate limit with the server.

## In the trajectory

An [eval trajectory](evals/knowledge.md) of a call with a geo resolver has, after the steps of the text itself, a `select_geo_entities` step (the candidates), the `detect_geo` LLM call and a `detect_geo` step (the places it found, with `kind` and `query`), then, with the geo manager, a `queue_geo` step (`mode`: `async`, `sync` or `deferred`; how many places were recorded as pending, queued, or already queued by another call). Without `geo_sync` that is the end of the stage: the resolving happens later and is not in the trajectory. With `geo_sync` (or a plain resolver) there is one `resolve_geo` step per place (found or not, the coordinates, `latency_ms`), and with a plain resolver a `store_geo` step (how many places were stored). When there are no candidates, the stage stops after `select_geo_entities`.

## In `query`

With geo on (the same switch as for `remember`), `query` makes one more LLM call, separate from the one that answers: the `detect_geo` stage reads the question (only the question, not the graph) and lists the places it mentions, each with the `text` as written, its `kind` (`country`, `region`, `city` or `place`) and the `query` a geocoder should be asked ("Paris, France"). It runs after the graph was retrieved, and not at all if the dataset is empty (the fixed "no information" answer is given without any LLM call). Entities of the graph are not involved.

An LLM that names something that is not in the question is ignored, at most 10 places are kept, and a failure of the stage is logged and recorded in the trajectory but never fails the query.

### Nearby entities

The search for nearby entities is **off by default**: the places are found (above), but nothing else happens, and the geo resolver is not asked, unless the request turns it on with `geo_nearby: true` or by setting `geo_radius_km` ([query request](query/query.md#request); setting a radius with `geo_nearby: false` is a `400`, and so is asking for it on a server without geo). The user decides when nearby entities are involved. Then the proximity step (`memory_query/query_geo_nearby.go`) gives the answering LLM the geo entities that lie near the places of the question:

1. **Anchors** (at most 5), the places the search is centered on: first the entities that matched the question and have coordinates in `gb_entity_geo`; then the places the LLM found in the question that are not the name of one of those. For such a place, an entity of the dataset with that name that has coordinates is used as it is (no resolver call); otherwise the geo resolver is asked, and a place it does not know is skipped.
2. **Storing the places of the question.** A place the resolver finds is stored, in one transaction, so that it is in the database for the next question (and for the nearby searches of later questions, which only look at stored entities): if the dataset has an entity with that name whose type can be a place (anything but `person`, `concept`, `product` or `event`), it gets the coordinates; otherwise a new entity of the type `place` is made with them (the name as the question writes it, `kind` and `query` from the LLM, the rest from the resolver, as `remember` stores it). The next question with the same place finds it in the database and does not ask the resolver again. This is the one write of `query`, and only happens with the nearby search on; an entity made this way has no artifact (no text it came from) and no description. A failure to store is logged and recorded, and the place is still used as an anchor.
3. **Nearby**: for each anchor, `graph.NearbyEntityGeo` returns the geo entities of the dataset within the radius (the 20 nearest per anchor, at most 30 in all; an entity near several anchors is listed once, with the nearest). The anchors that are entities are left out.
4. **Context**: the nearby entities and the edges around them (at most 50, with the entities at their other end) are added to the graph the LLM reads, so it can cite them as evidence, and a `<nearby radius_km="50">` section says how far each is from which anchor (`[entity 7] Versailles is 17.9 km from Paris`). Without anchors, or when nothing is near, there is no `<nearby>` section.

The result has `nearby` (omitted when the search was off or there was no anchor): `{"radius_km": 50, "anchors": [{"name": "Lyon", "entity_id": 9, "stored": true, "latitude": ..., "longitude": ...}], "nearby": [{"entity_id": 7, "name": "Villeurbanne", "type": "place", "latitude": ..., "longitude": ..., "distance_km": 3.4, "anchor": "Lyon"}]}` (`stored` is only there for a place stored by this call). The trajectory has a `nearby_geo` step. A failure of the step (the resolver, the database) is logged and recorded, and never fails the query.

**The radius** is 50 km by default. The server's default is `geo_radius_km` in `gligabao.toml` (it does not turn the search on; Go: `server.Options.GeoRadiusKm`, `api.WithGeoRadius`, `memory_core.WithGeoRadius`); a request overrides it with `geo_radius_km`. It must be greater than 0 and at most 20015 km (half the circumference of the Earth; `400` otherwise). Go callers of `memory_query.Query` turn the search on with `memory_core.WithGeoNearby(true)`.

**How it is searched.** The index prefilters (SQLite: the R*Tree, or the index on latitude and longitude when geo was never on for the database; PostgreSQL: PostGIS `ST_DWithin` on the GiST index, or a latitude/longitude box without it), and the candidates are refined with the haversine distance on a sphere, so the result is the same with or without an index. The box handles the antimeridian (a search near 180° also finds places at -179°) and the poles. Distances are straight lines (great circle), not travel distances.

Like the answering call, it is recorded as an inference (type `query`, stage `detect_geo`). Its model is chosen with `models` in the [query request](query/query.md#request); its reasoning level with `reasoning` (the string form of `reasoning` is the `query` stage only, so use the object form: `{"detect_geo": "low"}`); a stage left out runs without thinking.

### Nearby entities without a question

`GET /v1/gb/geo/nearby` runs that same search (`memory_query.NearbyEntityOf` / `NearbyPoint`, which call the function `query` uses for its proximity step, so the two always select the same entities, with the same limits: the 20 nearest per anchor, at most 30, nearest first) for one anchor you name, with no question and no LLM:

- `dataset` (default `main`) in the query string (the user is the `gb-user-id` header, as everywhere), and either `entity_id` (a geo entity of the dataset: the anchor is its stored coordinates, and it is left out of the result) or `latitude` and `longitude` (a point; every geo entity in the radius is listed);
- `radius_km` (optional): as for `query`, greater than 0 and at most 20015; default the server's `geo_radius_km`, else 50.

The response is the `nearby` object of the query result: `{"radius_km": 50, "anchors": [{"name": "Paris", "entity_id": 3, "latitude": 48.85, "longitude": 2.35}], "nearby": [{"entity_id": 7, "name": "Versailles", "type": "place", "latitude": ..., "longitude": ..., "distance_km": 17.9, "anchor": "Paris"}]}`. `400` for a bad parameter, `404` for an unknown dataset, or an entity that is not in the dataset or has no coordinates. Like the other routes of users it needs an API key when the server has authentication (the `user_id` is then the key's owner). Unlike `query` it never asks the geo resolver and stores nothing, and works whether or not geo is on (it only reads what is stored). The admin UI calls the same handler as `GET /v1/gb/admin/api/geo/nearby?db=<name>&user_id=&dataset=&entity_id=[&radius_km=]`.

## The result

`geo` in the response of `remember` has one element per entity the LLM judged to be a place. By default (the geo manager) the coordinates are not known yet: the element has `"pending": true` and `resolved` is false; `geo_sync: true` waits for them. The element of a resolved place:

```json
{"entity": "Paris", "entity_type": "place", "entity_id": 12, "kind": "city", "query": "Paris, France", "resolved": true,
 "latitude": 48.8588897, "longitude": 2.320041, "display_name": "Paris, Île-de-France, France", "country": "France",
 "country_code": "fr", "region": "Île-de-France", "provider": "nominatim"}
```

`pending: true` means the place is recorded and waiting for the manager (read it with [`GET /v1/gb/geo`](#the-geo-manager)); it is also set with `geo_sync` when the resolver failed, as the sweeper asks again. `resolved: false` with no `error` and no `pending` means the resolver does not know the place; with an `error` the resolver, the queue or the storage failed.

## The table

`gb_entity_geo` has one row per geo entity, keyed by `entity_id` (a foreign key to `gb_entities`; the row goes with the entity):

| Column | |
| ------ | - |
| `entity_id` | the entity (primary key) |
| `kind` | `country`, `region`, `city` or `place`, as the LLM judged it |
| `query` | what the resolver was asked for |
| `latitude`, `longitude` | degrees, WGS 84 (checked: -90..90, -180..180) |
| `display_name` | the full name the resolver gives |
| `country`, `country_code`, `region` | where the place lies (`country_code`: ISO 3166-1 alpha-2, lowercase); `''` if unknown |
| `place_type` | the resolver's own type (`city`, `administrative`, `peak`, ...) |
| `bbox_south`, `bbox_west`, `bbox_north`, `bbox_east` | bounding box; `NULL` if unknown |
| `importance` | how prominent the place is, 0..1; 0 if the resolver does not say |
| `provider`, `provider_ref` | the resolver and the place's ID there (`relation/7444`) |
| `created_at`, `updated_at` | |

Go: `graph.GetEntityGeo`, `graph.ListEntityGeo`, and `Tx.UpsertEntityGeo` / `Tx.GetEntityGeo` inside a transaction.

`gb_geo_pending` has one row per place waiting for the [geo manager](#the-geo-manager), keyed by `entity_id` (a foreign key to `gb_entities`; the row goes with the entity): `kind` and `query` (what the resolver is to be asked), `attempts` (failed attempts of the resolver) and `last_error`, `created_at`, `updated_at`. Go: `Tx.UpsertGeoPending`, `DeleteGeoPending`, `FailGeoPending`, `ListGeoPending`, `CountGeoPending`.

## Writing a resolver

```go
type Resolver interface {
	Name() string
	Resolve(ctx context.Context, q memory_geo.Query) (*memory_geo.Place, error) // memory_geo.ErrNotFound if it knows no such place
}
```

`memory_geo.Cached(r)` adds a cache, `memory_geo.NewManager(ctx, r)` is the [geo manager](#the-geo-manager) (a `Resolver` itself, and a `memory_geo.Queue` for `remember`), `memory_geo.NewNominatim(memory_geo.NominatimOptions{BaseURL: ..., MinInterval: -1})` points at your own Nominatim
instance, and `memory_geo.Func` adapts a function. A resolver must be safe for concurrent use and return coordinates in range
(`Place.Validate`), or the answer is treated as a failure. The manager calls it from one goroutine, one place at a time, with a 30 s timeout.
