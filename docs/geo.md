# Geo

Some entities are places: a city, a country, a region, a landmark. `remember` finds them and stores their coordinates, so the
graph knows where things are. The code is the package `internal/geo` (the resolvers) and `memory_remember/remember_geo.go` (the
stage of the pipeline); the data is the table `gb_entity_geo`.

`query` uses it too, with the same switch: when the request asks for it, it finds the geo entities near the places of a question and gives them to the LLM that answers, within a radius you can set, and stores the places of the question it resolves (see [In `query`](#in-query)).

## Turning it on

Off by default: it costs one more LLM call per text (an entity list is sent to the `detect_geo` stage) and it sends the names of
places to a geocoder.

- `geo = true` in `gligabao.toml`, or `serve --geo`: uses the public Nominatim geocoder of OpenStreetMap (cached; one request per
  second, as its [usage policy](https://operations.osmfoundation.org/policies/nominatim/) asks).
- A program that embeds gligabao sets `ServeOptions.GeoResolver` (any `gligabao.GeoResolver`); that turns geo on by itself.
  For Go code that builds the call itself: `memory_core.WithGeoResolver(r)`, and `api.WithGeoResolver(r)` for `api.Register`.

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
3. **Resolve**: the geo resolver is asked for each place found (at most 25), one at a time. "Not found" is not a failure.
4. **Store**: one transaction writes the rows of `gb_entity_geo`, which are then in the geo index too ([The geo index](#the-geo-index)).

`detect_geo` takes a model (`models`) and a reasoning level (`reasoning`) like the other LLM stages
([remember.md](remember.md#choosing-the-model-of-each-stage)). In the run log the stage is `resolve_geo` and holds the
`detect_geo` inference.

## In the trajectory

An [eval trajectory](evals/knowledge.md) of a call with a geo resolver has, after the steps of the text itself, a `select_geo_entities` step (the candidates), the `detect_geo` LLM call and a `detect_geo` step (the places it found, with `kind` and `query`), one `resolve_geo` step per place (found or not, the coordinates, `latency_ms`), and a `store_geo` step (how many places were stored). When there are no candidates, the stage stops after `select_geo_entities`.

## In `query`

With geo on (the same switch as for `remember`), `query` makes one more LLM call, separate from the one that answers: the `detect_geo` stage reads the question (only the question, not the graph) and lists the places it mentions, each with the `text` as written, its `kind` (`country`, `region`, `city` or `place`) and the `query` a geocoder should be asked ("Paris, France"). It runs after the graph was retrieved, and not at all if the dataset is empty (the fixed "no information" answer is given without any LLM call). Entities of the graph are not involved.

An LLM that names something that is not in the question is ignored, at most 10 places are kept, and a failure of the stage is logged and recorded in the trajectory but never fails the query.

### Nearby entities

The search for nearby entities is **off by default**: the places are found (above), but nothing else happens, and the geo resolver is not asked, unless the request turns it on with `geo_nearby: true` or by setting `geo_radius_km` ([query request](query.md#request); setting a radius with `geo_nearby: false` is a `400`, and so is asking for it on a server without geo). The user decides when nearby entities are involved. Then the proximity step (`memory_query/query_geo_nearby.go`) gives the answering LLM the geo entities that lie near the places of the question:

1. **Anchors** (at most 5), the places the search is centered on: first the entities that matched the question and have coordinates in `gb_entity_geo`; then the places the LLM found in the question that are not the name of one of those. For such a place, an entity of the dataset with that name that has coordinates is used as it is (no resolver call); otherwise the geo resolver is asked, and a place it does not know is skipped.
2. **Storing the places of the question.** A place the resolver finds is stored, in one transaction, so that it is in the database for the next question (and for the nearby searches of later questions, which only look at stored entities): if the dataset has an entity with that name whose type can be a place (anything but `person`, `concept`, `product` or `event`), it gets the coordinates; otherwise a new entity of the type `place` is made with them (the name as the question writes it, `kind` and `query` from the LLM, the rest from the resolver, as `remember` stores it). The next question with the same place finds it in the database and does not ask the resolver again. This is the one write of `query`, and only happens with the nearby search on; an entity made this way has no artifact (no text it came from) and no description. A failure to store is logged and recorded, and the place is still used as an anchor.
3. **Nearby**: for each anchor, `graph.NearbyEntityGeo` returns the geo entities of the dataset within the radius (the 20 nearest per anchor, at most 30 in all; an entity near several anchors is listed once, with the nearest). The anchors that are entities are left out.
4. **Context**: the nearby entities and the edges around them (at most 50, with the entities at their other end) are added to the graph the LLM reads, so it can cite them as evidence, and a `<nearby radius_km="50">` section says how far each is from which anchor (`[entity 7] Versailles is 17.9 km from Paris`). Without anchors, or when nothing is near, there is no `<nearby>` section.

The result has `nearby` (omitted when the search was off or there was no anchor): `{"radius_km": 50, "anchors": [{"name": "Lyon", "entity_id": 9, "stored": true, "latitude": ..., "longitude": ...}], "nearby": [{"entity_id": 7, "name": "Villeurbanne", "type": "place", "latitude": ..., "longitude": ..., "distance_km": 3.4, "anchor": "Lyon"}]}` (`stored` is only there for a place stored by this call). The trajectory has a `nearby_geo` step. A failure of the step (the resolver, the database) is logged and recorded, and never fails the query.

**The radius** is 50 km by default. The server's default is `geo_radius_km` in `gligabao.toml` (it does not turn the search on; Go: `server.Options.GeoRadiusKm`, `api.WithGeoRadius`, `memory_core.WithGeoRadius`); a request overrides it with `geo_radius_km`. It must be greater than 0 and at most 20015 km (half the circumference of the Earth; `400` otherwise). Go callers of `memory_query.Query` turn the search on with `memory_core.WithGeoNearby(true)`.

**How it is searched.** The index prefilters (SQLite: the R*Tree, or the index on latitude and longitude when geo was never on for the database; PostgreSQL: PostGIS `ST_DWithin` on the GiST index, or a latitude/longitude box without it), and the candidates are refined with the haversine distance on a sphere, so the result is the same with or without an index. The box handles the antimeridian (a search near 180° also finds places at -179°) and the poles. Distances are straight lines (great circle), not travel distances.

Like the answering call, it is recorded as an inference (type `query`, stage `detect_geo`). Its model is chosen with `models` in the [query request](query.md#request); its reasoning level with `reasoning` (the string form of `reasoning` is the `query` stage only, so use the object form: `{"detect_geo": "low"}`); a stage left out runs without thinking.

### Nearby entities without a question

`GET /v1/gb/geo/nearby` runs that same search (`memory_query.NearbyEntityOf` / `NearbyPoint`, which call the function `query` uses for its proximity step, so the two always select the same entities, with the same limits: the 20 nearest per anchor, at most 30, nearest first) for one anchor you name, with no question and no LLM:

- `user_id`, `dataset` (default `main`), and either `entity_id` (a geo entity of the dataset: the anchor is its stored coordinates, and it is left out of the result) or `latitude` and `longitude` (a point; every geo entity in the radius is listed);
- `radius_km` (optional): as for `query`, greater than 0 and at most 20015; default the server's `geo_radius_km`, else 50.

The response is the `nearby` object of the query result: `{"radius_km": 50, "anchors": [{"name": "Paris", "entity_id": 3, "latitude": 48.85, "longitude": 2.35}], "nearby": [{"entity_id": 7, "name": "Versailles", "type": "place", "latitude": ..., "longitude": ..., "distance_km": 17.9, "anchor": "Paris"}]}`. `400` for a bad parameter, `404` for an unknown dataset, or an entity that is not in the dataset or has no coordinates. Like the other routes of users it needs an API key when the server has authentication (the `user_id` is then the key's owner). Unlike `query` it never asks the geo resolver and stores nothing, and works whether or not geo is on (it only reads what is stored). The admin UI calls the same handler as `GET /v1/gb/admin/api/geo/nearby?db=<name>&user_id=&dataset=&entity_id=[&radius_km=]`.

## The result

`geo` in the response of `remember` has one element per entity the LLM judged to be a place:

```json
{"entity": "Paris", "entity_type": "place", "entity_id": 12, "kind": "city", "query": "Paris, France", "resolved": true,
 "latitude": 48.8588897, "longitude": 2.320041, "display_name": "Paris, Île-de-France, France", "country": "France",
 "country_code": "fr", "region": "Île-de-France", "provider": "nominatim"}
```

`resolved: false` with no `error` means the resolver does not know the place; with an `error` the resolver or the storage failed.

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

## Writing a resolver

```go
type Resolver interface {
	Name() string
	Resolve(ctx context.Context, q geo.Query) (*geo.Place, error) // geo.ErrNotFound if it knows no such place
}
```

`geo.Cached(r)` adds a cache, `geo.NewNominatim(geo.NominatimOptions{BaseURL: ..., MinInterval: -1})` points at your own Nominatim
instance, and `geo.Func` adapts a function. A resolver must be safe for concurrent use and return coordinates in range
(`Place.Validate`), or the answer is treated as a failure.
