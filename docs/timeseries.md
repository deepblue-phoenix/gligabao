# Timeseries

gligabao can store **timeseries** (a value over time, such as a price) and **anchor** them to what it remembers: the artifact a text was stored as, the entities and the facts (edges) extracted from it. A memory such as "Apple fell sharply after the earnings miss" can then be tied to Apple's price series around that date. The points are not part of the graph: a series has tables of its own (like [images](images.md)) and is linked to the graph by anchors.

A series is supplied by the caller, in a `remember` request or with the standalone endpoint. Fetching series from a data provider, embedding them and using them in `recall` are not implemented yet: see [timeseries-design.md](timeseries-design.md) for the plan.

Code: `internal/memory/memory_core/series.go` (the request types and their validation), `internal/memory/memory_timeseries/` (storing, anchoring, stats), `internal/memory/memory_remember/remember_series.go` (the part in `remember`), `internal/api/timeseries/` (the endpoints), `internal/db/graph/{sqlite,postgres}/db_series.go` (SQL).

## A series

```json
{
  "key": "AAPL.close", "field": "value", "name": "Apple close", "unit": "USD", "frequency": "1d",
  "points": [["2025-01-29T00:00:00Z", 235.1], ["2025-01-30T00:00:00Z", 237.6], ["2025-01-31T00:00:00Z", 222.0]],
  "anchors": [{"entity": {"name": "Apple", "type": "organization"}, "role": "price"}]
}
```

- `key` (required): your identifier of the series, at most 256 bytes. A series is identified in its [dataset](datasets.md) by `key` and `field`: sending the same key again **adds points to the stored series**, and a point at a time the series already has **replaces its value**.
- `field` (optional, default `value`, at most 64 bytes): which value of the key the points are. The open, high, low and close of a price are four series with the same key and different fields.
- `name` (default: the key), `unit`, `frequency` (a label such as `1d`, not a constraint: points may be irregular).
- `points` (required): `[time, value]` pairs, at least one and at most 100,000 per series. The time is RFC 3339 (stored in UTC, with the precision of the database: seconds in SQLite, microseconds in PostgreSQL), the value a finite number. They may come in any order; they are sorted, and if a time is given twice the last value wins.
- `anchors` (optional): what the series is anchored to, see below.

A request has at most 20 series, and a series at most 20 anchors. Strings are trimmed and must not have control characters. Anything malformed is a `400` and nothing is stored.

## Anchors

An anchor links a series to one **artifact**, **entity** or **edge** of the dataset (many-to-many: a price series can be anchored to a company and to each news item about it). Exactly one of these says what:

| Field | Anchors to | Known when? |
| ----- | ---------- | ----------- |
| `artifact: true` | the artifact the request creates | `remember` only. This is the default for a series with no anchors. |
| `artifact_id` | an artifact that is already stored | |
| `entity: {"name", "type"}` | the entity with that normalized name (lowercased, whitespace collapsed) and type (any type if `type` is left out) | selector: resolved after the text is stored |
| `entity_id` | an entity that is already stored | |
| `edge: {"source", "relation", "target"}` | the fact between the entities with those normalized names, with that relation (normalized as relations are: lowercased, spaces to `_`); a live fact is preferred to an expired one, then the newest | selector: resolved after the text is stored |
| `edge_id` | an edge that is already stored | |

Besides that an anchor takes:

- `role` (optional): what the series is to the anchor, e.g. `price`.
- `window` (optional): `[start, end]`, RFC 3339: the slice of the series that matters to the anchor (for a news item, the days around it). The window belongs to the anchor, not to the series, so the same series can have another window for another anchor. Without it the whole series is meant.
- `on_unmatched` (optional): what to do when the selector or ID matches nothing.

| `on_unmatched` | Meaning | Default for |
| -------------- | ------- | ----------- |
| `create` | create the entity (type `other` if the selector has none). Only for an entity selector. | an entity selector |
| `ignore` | skip the anchor and report it in `skipped_anchors` | an edge selector |
| `error` | fail with `422`; nothing is stored | an ID |

**Why selectors.** With `remember` the entities do not exist when the request is made: they are what the extraction finds in the text. So an anchor names the entity (or fact) it expects, and it is looked up once the extraction has been stored, in the same transaction. `create` is the default for an entity because "this is Apple's series" implies the entity Apple even if the text never named it. An entity created that way is not linked to the artifact (no text mentioned it) and, for now, not embedded; it is `created: true` in the response.

Deleting an artifact, entity or edge deletes only its anchors: the series stays, and so do its other anchors. An anchor is unique per series and target: anchoring again replaces `role` and `window`.

## With `remember`

`POST /v1/gb/remember` takes `timeseries`:

```json
{
  "user_id": "u1",
  "text": "Apple fell sharply after the earnings miss.",
  "kind": "episode",
  "reference_time": "2025-01-31T21:00:00Z",
  "timeseries": {
    "series": [
      {"key": "AAPL.close", "unit": "USD", "points": [["2025-01-30T00:00:00Z", 237.6], ["2025-01-31T00:00:00Z", 222.0]]},
      {"key": "AAPL.volume", "points": [["2025-01-31T00:00:00Z", 9000000]],
       "anchors": [{"entity": {"name": "Apple", "type": "organization"}, "role": "volume"}]}
    ]
  }
}
```

- `timeseries.series`: the series. The first has no anchors, so it is anchored to the artifact (and reaches Apple through the artifact's entities); the second is anchored to the entity Apple.
- `timeseries.fetch`: reserved for the pipeline fetching series itself; giving it is a `400` for now.
- Not allowed with `image` (`400`).
- The series are validated before the first LLM call, so a malformed one costs nothing.

**When it is stored.** For a text that fits one chunk, the series are written in the same transaction as the artifact, entities and edges, after them, so a series that cannot be anchored (an anchor with `on_unmatched: "error"`, or an ID that does not exist: `422`) stores **nothing**, not even the text. A [chunked](chunking.md) text cannot be one transaction (each chunk has its own, and a selector may name an entity that a later chunk yields), so its series are stored after all the chunks, in a transaction of their own, anchored to the **parent** artifact. If they fail there, the text and its chunks stay (the error says so), and the request returns an error. The result of a web search (see [web-search.md](web-search.md)) never carries series.

The response gets `series` (omitted without `timeseries`), one item per series:

```json
{
  "series": [{
    "id": 1, "key": "AAPL.close", "field": "value", "name": "AAPL.close", "unit": "USD",
    "created": true, "points": 2, "point_count": 2,
    "first_ts": "2025-01-30T00:00:00Z", "last_ts": "2025-01-31T00:00:00Z",
    "stats": {"count": 2, "first": 237.6, "last": 222, "min": 222, "max": 237.6, "mean": 229.8, "change": -15.6, "pct_change": -6.57, "trend": "down", "largest_move": {"pct": -6.57}},
    "anchors": [{"type": "artifact", "id": 1}]
  }]
}
```

`created` says the series is new; `points` is how many the request carried and `point_count` how many the series has now; `anchors` are the stored anchors (`type`, `id`, `role`, `window` as `start/end`, `created` for an entity the anchor created); `skipped_anchors` lists the ignored ones.

In an eval [trajectory](evals.md) the input has `timeseries` (key, field, number of points and anchors per series; never the points) and a `store_timeseries` step records the result.

## Stats

Every time points are written, a summary of **all** the points of the series (not only those just sent) is computed in Go, never by an LLM, and stored with the series (`gb_series.stats`, JSON): `count`, `first_ts`, `last_ts`, `first`, `last`, `min` and `min_ts`, `max` and `max_ts`, `mean`, `change` (last minus first), `pct_change` (relative to the first value; omitted when that is 0), `trend` and `largest_move`.

- `trend` is `up` or `down` when `pct_change` is more than 1% (`TrendThresholdPct`) in that direction, else `flat` (a single point is flat; a series that starts at 0 is `up` or `down` by the sign of the change).
- `largest_move` is the biggest relative change between two consecutive points (`from`, `to`, `from_value`, `to_value`, `pct`); omitted for fewer than two points, and steps from 0 are not considered.

The stats are what the rest of the system is meant to look at instead of the raw points.

## POST /v1/gb/timeseries

Stores series in an **existing** dataset and anchors them, for what `remember` could not do: the entities are only known after it returned, a series changes later, or the points are too many for a `remember` request.

```json
{
  "user_id": "u1", "dataset": "main",
  "series": [{
    "key": "AAPL.close",
    "points": [["2025-02-03T00:00:00Z", 225.0]],
    "anchors": [{"entity_id": 12}, {"artifact_id": 1, "window": ["2025-01-30T00:00:00Z", "2025-02-03T00:00:00Z"], "role": "event"}]
  }]
}
```

- `user_id` (required) and `dataset` (default `main`): the dataset must exist (`404`; unlike `remember`, this never creates one).
- `series`: as above, except that there is no artifact, so `artifact: true` is a `400`, and a series **without anchors is stored unanchored** (anchor it later by sending its key again with anchors; its points are kept).
- The body can be up to 16 MiB (`remember` takes 1 MiB of text).
- Everything is one transaction.
- Response `200`: `{"user_id", "dataset", "series": [...]}`, the items as in the `remember` response.

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON; no `series`; a malformed series, anchor, window or point; `artifact: true` |
| 404 | The dataset does not exist |
| 422 | An anchor with `on_unmatched: "error"` (or an ID) matched nothing in the dataset; nothing is stored |
| 500 | Storing failed |

## Reading

- `GET /v1/gb/timeseries?user_id=...&dataset=...`: the dataset's series (`id`, `key`, `field`, `name`, `unit`, `frequency`, `source`, `point_count`, `first_ts`, `last_ts`, `stats`, `anchors`, ...), without points. `anchors` are `type`, `id`, `role`, `window_start`, `window_end`.
- `GET /v1/gb/timeseries/{id}?user_id=...&dataset=...&from=...&to=...&limit=...`: one series with the same fields plus its `points` (`[time, value]` pairs, oldest first) between `from` and `to` (RFC 3339, both optional, inclusive), at most `limit` (default 1000, at most 100,000). `truncated` says there were more. With `from` or `to` (and not truncated) `window_stats` has the [stats](#stats) of the points returned. `404` for an unknown series, dataset or user, `400` for a malformed `from`, `to` or `limit`.

`recall` does not use series yet.

## Schema

All tables are created with the other graph tables (`internal/db/graph/{sqlite,postgres}/db_schema.go`).

| Table | Columns |
| ----- | ------- |
| `gb_series` | `id`, `dataset_id` → `gb_datasets`, `key`, `field`, `name`, `unit`, `frequency`, `source` (`supplied`; `fetched` is reserved), `provider` (`''` for a supplied one), `definition` (JSON to fetch it again; `''`), `first_ts`, `last_ts`, `point_count`, `stats` (JSON), `created_at`, `updated_at`; unique (`dataset_id`, `provider`, `key`, `field`) |
| `gb_series_points` | `series_id` → `gb_series` (cascade), `ts`, `value`; primary key (`series_id`, `ts`) |
| `gb_series_anchors` | `id`, `series_id` → `gb_series` (cascade), `artifact_id` → `gb_artifacts`, `entity_id` → `gb_entities`, `edge_id` → `gb_edges` (each cascade; exactly one is set, by a CHECK), `role`, `window_start`, `window_end`, `created_at`; unique per (`series_id`, target) |

The anchor targets are real foreign keys, one column per type, so deleting an artifact, entity or edge removes its anchors without any cleanup code. Existing database files are not migrated when the schema changes: delete the file and the server recreates it.

Go access, in package `graph`: `ListSeries`, `GetSeries`, `SeriesPoints`, `ListSeriesAnchors`; writing goes through `Tx` (`UpsertSeries`, `UpsertSeriesPoints`, `ListSeriesPoints`, `UpdateSeriesSummary`, `AnchorSeries`, and the lookups `FindEntity`, `FindEdgeID`, `CheckArtifact`). `memory_core.WithSeries` supplies series to `memory_remember.Remember` and `Store`; `memory_timeseries.Attach` is the standalone path.

## Limitations

- Supplied series only: nothing fetches series from a data provider (`fetch` is a `400`), and the `source` and `provider` columns are there for it.
- Series, and the entities an anchor created, are not embedded, and `recall` does not return them; there is no derived `series_observation` artifact yet. The stats are not yet used by anything but the endpoints and the response.
- A series is never clipped at the `reference_time` of the text: the points are stored as given, so a series that runs past the event is stored whole (as memory it knows the future of the event). Clip it before sending if that matters.
- A chunked text's series are stored after its chunks, not atomically with them (see [With `remember`](#with-remember)).
- At most 100,000 points per series and request; send a bigger series in several requests (the key adds to it).
- There is no endpoint to delete a series or its points, or to read a series' anchors other than through the two `GET`s above.
- `frequency` and `unit` are labels, and the same key sent with another `unit` renames the stored one. No timezone handling: times are UTC.
