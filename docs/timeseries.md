# Timeseries

gligabao can store **timeseries** (a value over time, such as a price) and **anchor** them to what it remembers: the artifact a text was stored as, the entities and the facts (edges) extracted from it. A memory such as "Apple fell sharply after the earnings miss" can then be tied to Apple's price series around that date. The points are not part of the graph: a series has tables of its own (like [images](images.md)) and is linked to the graph by anchors.

A series is supplied by the caller, in a `remember` request or with the standalone endpoint, or **fetched** by gligabao from a [provider](#providers) that the program embedding it registered. A series can also be turned into text that goes through the normal extraction, so it yields facts in the graph: the [`series_observation` artifact](#observations). Embedding series and using them in `query` are not implemented yet: see [timeseries-design.md](timeseries-design.md) for the plan.

Code: `internal/memory/memory_core/series.go` (the request types and their validation), `internal/memory/memory_timeseries/` (storing, anchoring, stats), `internal/memory/memory_remember/remember_series.go`, `remember_seriesfetch.go` and `remember_observations.go` (the parts in `remember`), `internal/memory/memory_timeseries/observation.go` (the text of an observation), `internal/timeseries/` (the endpoints), `internal/db/graph/{sqlite,postgres}/db_series.go` (SQL).

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
- `timeseries.fetch`: fetch series from the registered [providers](#providers). It can be given with or without `series`.
- `timeseries.observations` (default `false`): derive a [`series_observation` artifact](#observations) from each series the request stores (supplied or fetched). It needs `series` or `fetch`.
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

In an eval [trajectory](evals/knowledge.md) the input has `timeseries` (key, field, number of points and anchors per series; never the points) and a `store_timeseries` step records the result.

## Providers

A **provider** is a source of timeseries that the program embedding gligabao registers when it starts the server. It has a callback that does the fetching, and a description in natural language of **when it should be used**. With `timeseries.fetch` in a `remember` request, an LLM reads the descriptions and decides which provider fetches series for which entity of the text: a stock price provider for an entity that is a public company, a macro-economic provider for "inflation", and nothing for an entity no provider covers. gligabao holds no provider logic of its own (credentials, symbols, rate limits are the embedding program's).

### Registering providers (Go)

```go
err := gligabao.Serve(ctx, cfg, gligabao.ServeOptions{
    TimeseriesProviders: []gligabao.TimeseriesProvider{{
        Name: "prices",
        Description: "Daily open, high, low, close and volume of stocks. Use for entities that are companies whose shares " +
            "are publicly traded on a stock exchange (e.g. Apple, Toyota). Not for private companies, people, places or concepts.",
        QueryFormat: "the ticker symbol on the company's main exchange, e.g. AAPL",
        Fetch: func(ctx context.Context, req gligabao.TimeseriesFetchRequest) ([]gligabao.TimeseriesData, error) {
            bars, err := myPriceAPI.Daily(ctx, req.Query, req.From, req.To) // req.Query is "AAPL"
            if err != nil {
                return nil, err // the fetch failed
            }
            closes := make([]gligabao.TimeseriesPoint, 0, len(bars))
            for _, b := range bars {
                closes = append(closes, gligabao.TimeseriesPoint{TS: b.Day, Value: b.Close})
            }
            return []gligabao.TimeseriesData{{Key: req.Query, Field: "close", Name: req.Entity.Name + " close", Unit: "USD", Frequency: "1d", Points: closes}}, nil
        },
    }},
})
```

- `Name` (required): letters, digits, `_`, `-` and `.`, at most 64 bytes, unique. It is what a request names in `fetch.providers` and what is stored as the series' `provider`.
- `Description` (required, at most 4000 bytes): **the whole decision rests on it.** Say what the provider offers and in which cases it applies, and when it does not. The LLM is told to prefer precision to recall: it skips an entity it is not sure the provider covers.
- `QueryFormat` (optional): what the callback needs to identify an entity, which the LLM writes as `req.Query` ("the ticker symbol, e.g. AAPL"). Without it the query is the entity's name. The query is a model's suggestion: validate it in the callback and return nothing if it is unusable.
- `Fetch` (required): called with the entity (`Name`, `Type`, `Description`), the `Query`, the period (`From`, `To`) and the `ReferenceTime` of the text. It returns the series (`Key`, `Field`, `Name`, `Unit`, `Frequency`, `Points`, as for a [supplied series](#a-series); several if the source has several fields), **no series and no error** if it has nothing for the entity, or an error. It must honour `ctx` (each call has a 60 s timeout), may be called concurrently, and a panic in it is caught and reported as an error. Points outside the period are dropped.
- The providers are validated at startup: `Serve` returns an error for a malformed one.

### Fetching in a `remember` request

```json
{"user_id": "u1", "text": "Apple fell sharply after the earnings miss.", "kind": "episode", "reference_time": "2025-01-31T21:00:00Z",
 "timeseries": {"fetch": {"providers": ["prices"], "before": "30d", "after": "0"}},
 "models": {"resolve_series": "claude-sonnet-4-5"}}
```

- `fetch` is opt-in: nothing is fetched by default. `providers` (default: all registered) limits which may be used. `before` and `after` (`"30d"`, `"2w"`, `"6mo"`, `"1y"`, or a Go duration; at most 20 years) set the period around the `reference_time` of the text: by default **30 days before and nothing after**, so a memory never contains the future of its event. Ask for `after` explicitly to get an event-study window.
- `400` for an unknown provider or a malformed window; `503` if the server has no provider.
- `models.resolve_series` picks the model of the decision (default model otherwise).

What happens, after the text has been stored:

1. **Candidates**: the entities the extraction found **in the text itself** (all its chunks, for a long text), those in the most facts first, at most 20. No type is excluded; the descriptions decide. Entities that only a [web search](web-search.md) found (from the briefings it stored) are **never offered** to providers, whether or not `web_search` is on.
2. **Resolve** (1 LLM call, the `resolve_series` stage): the LLM sees the providers (name, description, query format) and the entities (name, type, description) and answers which provider is for which entity, with the query and a reason. An entity can get several providers; unknown providers, entities and repeats are dropped; at most 10 fetches are made.
3. **Fetch**: the callbacks run in parallel for the period.
4. **Store**: for each fetch, in a transaction of its own: the series (identified in the dataset by `provider`, `key` and `field`, so fetching again **adds points** to the same series), their [stats](#stats), and two anchors with the period as `window` and the provider's name as `role`: one to the **entity**, one to the **artifact** the entity came from (a chunk, for a long text). `source` is `fetched` and `definition` records the entity and query.

A failure here (the LLM, a provider, the storage) never fails `remember` and never removes what was stored: it is in the item's `error`, the log and the trajectory.

The response gets `series_fetches` (omitted without `fetch`), one item per (entity, provider) chosen:

```json
{"series_fetches": [{
  "entity": "Apple", "entity_type": "organization", "parent_artifact_id": 1,
  "provider": "prices", "query": "AAPL", "reason": "Apple is a public company",
  "from": "2024-12-31T21:00:00Z", "to": "2025-01-31T21:00:00Z",
  "series": [{"id": 1, "key": "AAPL", "field": "close", "provider": "prices", "created": true, "points": 21, "point_count": 21, "stats": {"...": "..."},
              "anchors": [{"type": "entity", "id": 3, "role": "prices", "window": "..."}, {"type": "artifact", "id": 1, "role": "prices", "window": "..."}]}]
}]}
```

In an eval [trajectory](evals/knowledge.md) the input has `timeseries_fetch`, and the steps are `select_series_entities`, the LLM call, `resolve_series_providers` (the assignments) and one `fetch_timeseries` per fetch (with its latency).

## Stats

Every time points are written, a summary of **all** the points of the series (not only those just sent) is computed in Go, never by an LLM, and stored with the series (`gb_series.stats`, JSON): `count`, `first_ts`, `last_ts`, `first`, `last`, `min` and `min_ts`, `max` and `max_ts`, `mean`, `change` (last minus first), `pct_change` (relative to the first value; omitted when that is 0), `trend` and `largest_move`.

- `trend` is `up` or `down` when `pct_change` is more than 1% (`TrendThresholdPct`) in that direction, else `flat` (a single point is flat; a series that starts at 0 is `up` or `down` by the sign of the change).
- `largest_move` is the biggest relative change between two consecutive points (`from`, `to`, `from_value`, `to_value`, `pct`); omitted for fewer than two points, and steps from 0 are not considered.

The stats are what the rest of the system is meant to look at instead of the raw points.

## Observations

A series is not part of the graph: its points live in tables of their own. With `timeseries.observations: true`, `remember` also **derives an artifact from each series it stored**, so what the series says reaches the graph as facts, with bi-temporal edges, deduplication and embeddings and no logic of its own.

```json
{"user_id": "u1", "text": "Apple fell sharply after the earnings miss.", "kind": "episode", "reference_time": "2025-01-31T21:00:00Z",
 "timeseries": {"observations": true, "series": [{"key": "AAPL.close", "name": "Apple close", "unit": "USD",
   "points": [["2025-01-29T00:00:00Z", 235.1], ["2025-01-30T00:00:00Z", 237.6], ["2025-01-31T00:00:00Z", 222.0]],
   "anchors": [{"entity": {"name": "Apple", "type": "organization"}}]}]}}
```

It is opt-in, like `web_search` and `fetch`, because each observation costs an extraction call (and a resolution call when there are stored facts to compare with). It is the last stage of `remember`, after the text, the web search and the fetch, so a failure in it never fails the request and never removes what was stored.

For each series (at most **10** per request, in the order stored: the supplied ones, then the fetched ones):

1. **The window.** The points that this request wrote: the period from the first to the last point of a supplied series, or the period asked of the provider for a fetched one (points a series already had from earlier requests are not described). They are read back from the database.
2. **The text** is generated in Go from the [stats](#stats) of those points. **No LLM writes it**, so it can only say what the numbers say. For the example above:

   ```
   Observation of the timeseries "Apple close" (key AAPL.close, unit USD) for Apple (organization), from 2025-01-29 to 2025-01-31 (3 points).
   Apple close fell 5.57% in this period, from 235.1 USD on 2025-01-29 to 222 USD on 2025-01-31.
   Its highest value was 237.6 USD on 2025-01-30, its lowest 222 USD on 2025-01-31, and its average 231.5667 USD.
   The largest move between two consecutive points was -6.57%, from 237.6 USD on 2025-01-30 to 222 USD on 2025-01-31.
   ```

   It names the series (`name`, `key`, and `field`, `unit`, `frequency` if set), the provider of a fetched series, and the **entity the series is about**: the one its first entity selector names (for a supplied series), or the one it was fetched for. The latter is what lets the extraction attach the facts to the right node; a series anchored only to the artifact has no subject. Times are dates when all the points are at midnight UTC, else RFC 3339. A "flat" series (change under 1%) says so; a series that starts at 0 has no percentage.
3. **Extract, reconcile, store.** The text is processed like any other: the graph is extracted from it (the `extract_graph` model; the `extract_graph` step of the trajectory), compared with the stored facts (so a newer observation can close an older one that it contradicts) and stored, with its entities and edges linked to the artifact, embedded when vectors are configured. The reference time is that of the **last point of the window**, so facts without a date of their own are valid from then.

### The `series_observation` artifact

Each observation is **an artifact of its own** (`gb_artifacts`), with `kind = 'series_observation'`. A `remember` request cannot ask for that kind; only this stage creates it (as for `web_search`). Besides the usual columns:

| Column | Value |
| ------ | ----- |
| `series_id` | The series it describes (`gb_series.id`). NULL for any other kind of artifact, and set to NULL if the series is deleted (the observation stays: it is a memory). |
| `query` | What it is of: the series and the window, e.g. `AAPL.close from 2025-01-29 to 2025-01-31` (the field is added in parentheses when it is not `value`). |
| `parent_id` | The artifact that anchors the series: the artifact of the request (the **parent** artifact, for a [chunked](chunking.md) text) for a supplied series, the artifact the entity came from for a fetched one. |
| `text` | The generated text above. |
| `valid_at` | The last point of the window. `created_at` is the time of the request. |
| `session_id` | That of the `remember` call. |

So the provenance chain of a fact that came from a series reads: **the fact** (`gb_edges.artifact_id`, `gb_edge_artifacts`) → **the observation artifact** → **the series** (`series_id`) → **the artifact that anchored it** (`parent_id`, and the series' own anchors). Like a web search or a chunk, an observation is deleted with its parent (`ON DELETE CASCADE`). It is neither a chunk (`Artifact.IsChunk()` is false; it has no `chunk_index`) nor counted as one by the dataset's counts; it counts as an artifact.

Observations are not deduplicated: remembering again, or sending more points for the same key, makes a new observation of the new window.

### The response

The response gets `series_observations` (omitted without `observations`), one item per series observed. `entities` and `edges` of the response stay those of the text itself:

```json
{"series_observations": [{
  "series_id": 1, "key": "AAPL.close", "field": "value", "parent_artifact_id": 1,
  "query": "AAPL.close from 2025-01-29 to 2025-01-31", "from": "2025-01-29T00:00:00Z", "to": "2025-01-31T00:00:00Z",
  "text": "Observation of the timeseries ...", "artifact_id": 2,
  "entities": [{"name": "Apple close", "type": "concept"}], "edges": [{"id": 5, "source": "Apple", "target": "Apple close", "relation": "..."}],
  "invalidated_edges": []
}]}
```

`artifact_id` is 0 (omitted) when nothing was stored, and then `error` says why (the points could not be read, the extraction or the storage failed). A series with no point in its window is skipped without an error.

In an eval [trajectory](evals/knowledge.md) the input has `timeseries_observations: true`, and each series adds an `observe_series` step (its text and number of points), followed by the usual steps of a text (`extract_graph`, `find_candidates_by_name`, `load_candidates`, `store_artifact`, ...), labelled ` (observation of series "<key>")`.

From Go: `memory_core.WithSeriesObservations(true)`, `Result.SeriesObservations`; the text itself is `memory_timeseries.ObservationText(memory_timeseries.Observed{...}, points)`.

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

`query` does not use series yet, and neither the endpoints nor `query` treat a `series_observation` artifact any differently from other artifacts.

## Schema

All tables are created with the other graph tables (`internal/db/graph/{sqlite,postgres}/db_schema.go`).

| Table | Columns |
| ----- | ------- |
| `gb_series` | `id`, `dataset_id` → `gb_datasets`, `key`, `field`, `name`, `unit`, `frequency`, `source` (`supplied` or `fetched`), `provider` (`''` for a supplied one), `definition` (JSON: the entity and query of a fetched one; `''`), `first_ts`, `last_ts`, `point_count`, `stats` (JSON), `created_at`, `updated_at`; unique (`dataset_id`, `provider`, `key`, `field`) |
| `gb_series_points` | `series_id` → `gb_series` (cascade), `ts`, `value`; primary key (`series_id`, `ts`) |
| `gb_artifacts.series_id` | `series_id` → `gb_series` (ON DELETE SET NULL): only for a [`series_observation` artifact](#observations). In SQLite it is a column of `gb_artifacts`; in PostgreSQL it is added by `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` after `gb_series` is created, so an existing PostgreSQL database gets it by itself |
| `gb_series_anchors` | `id`, `series_id` → `gb_series` (cascade), `artifact_id` → `gb_artifacts`, `entity_id` → `gb_entities`, `edge_id` → `gb_edges` (each cascade; exactly one is set, by a CHECK), `role`, `window_start`, `window_end`, `created_at`; unique per (`series_id`, target) |

The anchor targets are real foreign keys, one column per type, so deleting an artifact, entity or edge removes its anchors without any cleanup code. Existing database files are not migrated when the schema changes: delete the file and the server recreates it.

Go access, in package `graph`: `ListSeries`, `GetSeries`, `SeriesPoints`, `ListSeriesAnchors`; writing goes through `Tx` (`UpsertSeries`, `UpsertSeriesPoints`, `ListSeriesPoints`, `UpdateSeriesSummary`, `AnchorSeries`, and the lookups `FindEntity`, `FindEdgeID`, `CheckArtifact`). `memory_core.WithSeries` supplies series to `memory_remember.Remember` and `Store`; `memory_timeseries.Attach` is the standalone path.

## Limitations

- Fetching is in `remember` only: the standalone endpoint cannot fetch (yet), and a series is not refreshed automatically: remember again, or call the provider yourself and send the points.
- The entity-to-identifier decision is made again for every request: the ticker an LLM chose for Apple is not cached on the entity.
- Entities found by a [web search](web-search.md) are not offered to the providers, only the entities of the text itself are (see [Fetching in a `remember` request](#fetching-in-a-remember-request)).
- Series, and the entities an anchor created, are not embedded, and `query` does not return them. The facts of an [observation](#observations) are queried like any fact (they are in the graph), but `query` does not follow them to the series or return its stats or points. The stats are used by the endpoints, the response and the observations.
- Observations are opt-in, at most 10 per request, made only by `remember` (not by the standalone endpoint), only of what the request wrote (never of a whole stored series), and not deduplicated. Their text is a fixed template from the stats: it does not know why the series moved. A supplied series that runs past the `reference_time` is described whole, future included (see the previous point on clipping).
- A supplied series is never clipped at the `reference_time` of the text: the points are stored as given, so a series that runs past the event is stored whole (as memory it knows the future of the event). Clip it before sending if that matters. A fetched series is clipped to its period, which ends at the `reference_time` unless `fetch.after` says otherwise.
- A chunked text's series are stored after its chunks, not atomically with them (see [With `remember`](#with-remember)).
- At most 100,000 points per series and request; send a bigger series in several requests (the key adds to it).
- There is no endpoint to delete a series or its points, or to read a series' anchors other than through the two `GET`s above.
- `frequency` and `unit` are labels, and the same key sent with another `unit` renames the stored one. No timezone handling: times are UTC.
