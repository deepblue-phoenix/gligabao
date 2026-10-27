# Geo

Some entities are places: a city, a country, a region, a landmark. `remember` finds them and stores their coordinates, so the
graph knows where things are. The code is the package `internal/geo` (the resolvers) and `memory_remember/remember_geo.go` (the
stage of the pipeline); the data is the table `gb_entity_geo`.

`query` finds the places a question mentions with the same switch, but does not use them yet: see [In `query`](#in-query) and the [roadmap](roadmap/ROADMAP.md).

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
It only reports; gligabao does not create the index or enable the extension itself yet.

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
4. **Store**: one transaction writes the rows of `gb_entity_geo`.

`detect_geo` takes a model (`models`) and a reasoning level (`reasoning`) like the other LLM stages
([remember.md](remember.md#choosing-the-model-of-each-stage)). In the run log the stage is `resolve_geo` and holds the
`detect_geo` inference.

## In the trajectory

An [eval trajectory](evals/knowledge.md) of a call with a geo resolver has, after the steps of the text itself, a `select_geo_entities` step (the candidates), the `detect_geo` LLM call and a `detect_geo` step (the places it found, with `kind` and `query`), one `resolve_geo` step per place (found or not, the coordinates, `latency_ms`), and a `store_geo` step (how many places were stored). When there are no candidates, the stage stops after `select_geo_entities`.

## In `query`

With geo on (the same switch as for `remember`), `query` makes one more LLM call, separate from the one that answers: the `detect_geo` stage reads the question (only the question, not the graph) and lists the places it mentions, each with the `text` as written, its `kind` (`country`, `region`, `city` or `place`) and the `query` a geocoder should be asked ("Paris, France"). It runs after the graph was retrieved, and not at all if the dataset is empty (the fixed "no information" answer is given without any LLM call). Entities of the graph are not involved.

For now the places are only extracted: they are not used for retrieval or for the answer, the resolver is not asked for their coordinates, and they are not in the response. They are in the [eval trajectory](evals/knowledge.md) (a `detect_geo` step after the retrieval) and in `QueryResult.Geo` for Go callers. An LLM that names something that is not in the question is ignored, at most 10 places are kept, and a failure of the stage is logged and recorded in the trajectory but never fails the query.

Like the answering call, it is recorded as an inference (type `query`, stage `detect_geo`). Its model is chosen with `models` in the [query request](query.md#request); its reasoning level with `reasoning` (the string form of `reasoning` is the `query` stage only, so use the object form: `{"detect_geo": "low"}`); a stage left out runs without thinking.

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
