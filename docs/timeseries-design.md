# Timeseries support in `remember` (design proposal)

> **Status: v1, v2 and the `series_observation` part of v3 are implemented; embedding series and `query` integration (the rest of v3) are not.** v2 (providers and the fetch stage) differs from the sketch below: see [Changes made while building v2](#changes-made-while-building-v2); the observations differ too: see [Changes made while building v3](#changes-made-while-building-v3).
>
> **v1:** What exists is described in [timeseries.md](timeseries.md): supplied series, anchors (artifact, entity, edge) resolved after `Store`, stats, the `POST /v1/gb/timeseries` endpoint and two read endpoints. This file keeps the reasoning behind it and the plan for what is left: the provider fetch stage (v2), and derived `series_observation` artifacts (v3, done), then embedding and `query` (v3, still to do). Where the text below differs from `timeseries.md`, `timeseries.md` is right (see [Changes made while building v1](#changes-made-while-building-v1)).

The goal is to let `POST /v1/gb/remember` associate **timeseries data** (not just text or images) with the events, entities and facts it remembers, so a memory such as "Apple fell sharply after the earnings miss" can be tied to Apple's price series around that date.

Related: [remember.md](remember.md), [artifacts.md](artifacts.md), [images.md](images.md) (a separate pipeline with its own tables, the model for this), [web-search.md](web-search.md) (a post-store enrichment stage, the model for fetching), [temporal.md](temporal.md).

## Summary

Support both ways of getting a series, as different mechanisms:

1. **Supplied series.** The caller sends points in the request. Anchoring is resolved *after* extraction, so it works although the entities do not exist yet when the request is made.
2. **Fetched series.** An opt-in enrichment stage after `Store` resolves entities to series through a provider (for example entity to ticker to price series). It follows the `web_search` pattern.
3. **A standalone attach endpoint.** It covers "I only know the entities after `remember` returns", refreshes live series, and takes large uploads. `remember` uses the same code path internally.

Supplied-only fails because the caller usually does not know which entities will come out of the text. Fetched-only fails because it cannot cover proprietary series, and entity-to-series mapping (name to ticker) is the hard part and sometimes fails.

## The timing problem

The entities of a request do not exist until extraction and `Store` have run. A series can still be anchored at three levels:

| Anchor | Known at request time? | How it is resolved |
| ------ | ---------------------- | ------------------ |
| **The artifact being created** | Yes, always | Implicit default. The series is attached to the artifact and its `reference_time`. Entities reach it through the existing `gb_entity_artifacts` link. |
| **An entity, by selector** (`{name, type}`) | Not as a row, but as a normalized `name_key` | After `Store`, with the same (`name_key`, `type`) rule as entity deduplication. |
| **An edge, by selector** (`source`, `relation`, `target`) | Same | After `Store`, by the endpoints' entities and the relation. |

If a selector matches nothing after extraction, the series field `on_unmatched` decides: `create` (default), `ignore` or `error`. `create` is the default because "this is AAPL's price series" implies the entity Apple even if the text never named it.

## Data model

Points are **not** stored in the graph. Following the image pipeline, series have their own tables and are linked to the graph; they are not rows of `gb_artifacts`.

```
gb_series          id, dataset_id, key, name, unit, frequency,
                   source ('supplied' | 'fetched'), provider,
                   definition (JSON: symbol, field, params; enough to refetch),
                   first_ts, last_ts, point_count, created_at
gb_series_points   series_id, ts, field, value          -- PK (series_id, ts, field)
gb_series_anchors  series_id, anchor_type ('artifact' | 'entity' | 'edge'), anchor_id,
                   role, window_start, window_end, created_at
gb_series_stats    series_id, window, min, max, mean, last, pct_change, trend,
                   change_points (JSON)
```

- **Many-to-many anchors**, like `gb_entity_artifacts`. One price series can anchor to an entity, to several news artifacts, and to the edges that describe events.
- **The window lives on the anchor, not on the series.** What matters for memory is "the price in the 30 days around this news item". The series is shared; each anchor says which slice matters.
- **Series identity** is (`dataset`, `provider`, `key`, `field`, `frequency`). Re-sending or re-fetching upserts points (as the image SHA-256 deduplicates images).
- **`field`** supports OHLCV and multi-metric series without a table per shape.
- **Bi-temporal points.** `ts` is event time. An optional `recorded_at` can be added later for restated data (macro revisions). It is not in v1, but nothing should be designed to exclude it.
- **`gb_series_stats`** holds deterministic summaries computed in Go: min, max, last, percent change, trend, change points. These are what extraction, embedding and `query` see. The LLM never sees raw points.
- **Embed the description plus the stats, never the points**, for example "AAPL daily close, Jan 2025, -6.2%, largest drop on 2025-01-31".

Datasets stay isolated: series are dataset-scoped like everything else, so a price series used by two datasets is stored once in each.

## Request

Additions to the `remember` request:

```json
{
  "user_id": "u1",
  "text": "Apple fell sharply after the earnings miss.",
  "kind": "episode",
  "reference_time": "2025-01-31T21:00:00Z",

  "timeseries": {
    "series": [{
      "key": "AAPL.close", "name": "Apple close", "unit": "USD", "frequency": "1d",
      "points": [["2025-01-29T00:00:00Z", 235.1], ["2025-01-30T00:00:00Z", 237.6]],
      "anchors": [{"entity": {"name": "Apple", "type": "organization"}, "on_unmatched": "create"}]
    }],
    "fetch": {"providers": ["prices"], "window": "30d"}
  }
}
```

- `timeseries.series[]` (supplied): points are `[ts, value]` pairs to keep the JSON small. A series without `anchors` is anchored to the artifact. The 1 MiB body limit applies, so there is a cap on points per series; large series go through the attach endpoint.
- `timeseries.fetch` (fetched): **opt-in**, like `web_search`. It costs money and time and can fail, so nothing is fetched by default.
- `models.resolve_series` (optional): the model for symbol resolution, if an LLM is used for it.
- Errors: `400` for malformed points, timestamps or anchors; `503` if `fetch` is asked for and no provider is configured.

## Pipeline placement

```
1 Extract -> 2 Resolve edges -> 3 Store (+ anchor supplied series)
          -> 4 Embed -> 5 web_search -> 6 timeseries fetch (new)
```

- **Supplied series are stored inside step 3's transaction.** Entity and edge selectors can only be resolved once the rows exist, and a bad series should fail the request before commit. Nothing is fetched, because the points are in the request.
- **Fetched series run after the commit**, like `web_search`. A provider failure never fails `remember`; it is reported in the response and the eval trajectory.

### The fetch stage

Modelled on `internal/memory/memory_remember/remember_websearch.go`:

1. **Pick key entities**, reusing the web-search heuristic: skip `concept`, rank by number of edges of the call, cap at about 3.
2. **Resolve**: ask each requested provider whether it supports the entity and which series it maps to.
3. **Fetch** the window around `reference_time` (an event-study shape: before, at and after the event), and optionally the latest window.
4. **Store**: upsert the series and points, add anchors, compute stats.
5. **Embed** the series description and stats.

### Providers

```go
type Provider interface {
    Name() string
    Resolve(ctx context.Context, e Entity) ([]SeriesRef, error) // nil: not applicable
    Fetch(ctx context.Context, ref SeriesRef, from, to time.Time) ([]Point, error)
}
```

For finance, `Resolve` maps an organization to a ticker and `Fetch` calls a price API (Polygon, Alpha Vantage, Yahoo) or FRED for macro series.

- **Entity to ticker is where this goes wrong.** A guessed ticker for "Apple" can be the wrong company. Prefer precision over recall: require an unambiguous match and skip otherwise.
- **Cache the resolved identifier** on the entity (an identifier attribute, or a `has_ticker` edge), so it is resolved once.
- **Providers by entity type.** Once [ontologies](ontologies.md) are used by `remember`, a class (`Company`) can imply a provider.

## The attach endpoint

```
POST /v1/gb/timeseries
{"user_id": "u1", "dataset": "main", "series": {...},
 "anchors": [{"entity_id": 12}, {"artifact_id": 1, "window": ["2025-01-01", "2025-02-01"]}]}
```

- Anchors by ID or selector to things that already exist, such as the entity names returned by an earlier `remember`.
- Has a fetch mode: `{"entity_id": 12, "provider": "prices"}`.
- Refreshes live series. `remember` should not own ongoing updates.
- Accepts bigger bodies (or a file upload) than `remember`.
- `remember` is defined as "extract, then attach", so there is one code path.

## Making series part of the graph

v1 writes no edges from raw points. A later step (**built in v3**, see [Changes made while building v3](#changes-made-while-building-v3)) adds a **derived artifact**, in the style of `web_search`:

- A new artifact kind, `series_observation`, with `parent_id` pointing at the anchoring artifact and a definition or query field.
- Its text is generated deterministically from the stats ("AAPL closed down 6.2% on 2025-01-31, its largest one-day drop in 90 days").
- That text goes through the normal extract, reconcile and store path, so it yields bi-temporal edges, deduplication and embeddings with no new logic.

Provenance then reads: fact, then observation artifact, then series, then the artifact that anchored it.

**(On hold.)** `query` would return the series anchored to the entities of the queried subgraph: summary and stats by default, downsampled points on request, through `GET /v1/gb/timeseries/{id}?from&to&resolution`.

## Changes made while building v1

The design above was simplified in these ways:

- **No `field` column on points.** `field` is a column of `gb_series` and part of the series identity (`provider`, `key`, `field`); an OHLCV series is several series with the same key. Points are `(series_id, ts, value)`. `frequency` is only a label and is not part of the identity.
- **Stats are a JSON column of `gb_series`** (`stats`), over all the points, instead of a `gb_series_stats` table with a window. A window's stats are computed on read (`GET /v1/gb/timeseries/{id}?from=&to=` returns `window_stats`). Change points are replaced by the single `largest_move`.
- **Anchors have one real foreign-key column per target type** (`artifact_id`, `entity_id`, `edge_id`, exactly one set) instead of `anchor_type` and `anchor_id`, so deleting a target cascades to its anchors.
- **`on_unmatched` defaults depend on the anchor**: `create` for an entity selector, `ignore` for an edge selector, `error` for an ID. `create` is not allowed for an edge.
- **The attach request takes `series` as a list, each with its own anchors**, like `remember`, rather than one series with top-level anchors. Its dataset must exist (`404`); a series without anchors is stored unanchored.
- **Not done in v1**: embedding the series description and stats (the entity that an anchor creates is not embedded either), and the `models.resolve_series` stage (there is no LLM step). `timeseries.fetch` is a `400`.
- **Chunked text**: the series are stored after the chunks, anchored to the parent artifact, in a transaction of their own, so they are not atomic with the chunks.
- **Lookahead leakage** (risk 1): series are stored as supplied and never clipped at `reference_time`; clipping is left to the caller until a fetch stage exists.

## Changes made while building v2

- **Providers are registered by the embedding program**, with `ServeOptions.TimeseriesProviders`, not configured: a provider is `{Name, Description, QueryFormat, Fetch}`. The sketched `Resolve` method is gone: **an LLM decides which provider is used for which entity** (the `resolve_series` stage), from the natural-language `Description` of each provider, and writes the provider-specific query (a ticker) from its `QueryFormat`. `Fetch` receives the entity, that query and the period.
- **No entity-to-identifier cache** yet: the decision is repeated for every request.
- **Entities offered**: up to 20, by number of facts, with no type excluded (a provider may be for concepts). At most 10 fetches per request.
- **Lookahead** (risk 1) is decided: a fetch reaches `before` (default 30 days) before the reference time and `after` (default none) after it, and what a provider returns is clipped to that period.
- **Anchors**: the fetched series is anchored to the entity and to the artifact the entity came from, both with the period as the window.
- The standalone endpoint has no fetch mode yet.

## Changes made while building v3

Only the derived `series_observation` artifacts are built; embedding the series and `query` are on hold (see the rollout table). What was built is described in [timeseries.md](timeseries.md#observations); where it differs from the sketch in [Making series part of the graph](#making-series-part-of-the-graph):

- **Opt-in** with `timeseries.observations`, at most 10 per request. The sketch did not say; each observation is an extraction call, as a web search is a search, so nothing is derived by default.
- **It describes what the request wrote** (the period of the supplied points, or the fetched period), not the whole stored series, and uses the stats of those points computed on read in Go (`Summarize`), not the stored `gb_series.stats`. "Its largest one-day drop in 90 days" is therefore not said: the text only knows the window it was given (largest move between consecutive points, high, low, average, change, trend).
- **The text names a subject**: the entity of the series' first entity selector (supplied), or the entity it was fetched for. Without it the extraction has nothing to attach the facts to.
- **A column for the series**: `gb_artifacts.series_id` (→ `gb_series`, `ON DELETE SET NULL`), so the chain fact → observation → series is a real link and not a convention; `query` says which series and window. The sketch's "definition or query field" is `query`. The observation is a child of the artifact that anchors the series: the call's artifact (the parent, for a chunked text) for a supplied series, the artifact the entity came from for a fetched one. `valid_at` is the last point of the window.
- **Last stage**, after the web search and the fetch (it needs the fetched series), and like them it never fails `remember`.
- **Not done**: a kind-specific treatment in `query` (it sees an observation's facts like any other fact), an observation by the standalone endpoint, deduplication of observations.

## Rollout

| Phase | Scope |
| ----- | ----- |
| **v1** (done) | Supplied series; artifact, entity and edge anchors resolved after `Store`; stats; the attach and read endpoints; trajectory steps; docs. No provider and no LLM. Not embedded (see above). |
| **v2** (done, except the cache) | The provider interface (registered by the embedding program, chosen by an LLM from their descriptions), the fetch stage. No built-in finance provider; no entity-to-identifier cache; no fetch mode in the attach endpoint. |
| **v3** (observations done) | `series_observation` artifacts feeding the graph: done (`timeseries.observations`). **`query` integration and embedding the series: on hold.** |
| **Later** | Digitizing `plot` images into series (the image pipeline already classifies `plot`), and a vintage (`recorded_at`) model. |

Files to add, following the existing layout (v3 added `memory_remember/remember_observations.go` and `memory_timeseries/observation.go`):

- `internal/memory/memory_timeseries/` (parse, anchor, stats, fetch)
- `internal/db/graph/{sqlite,postgres}/db_timeseries.go`
- `internal/timeseries/`
- `docs/timeseries.md`

## Risks and open questions

1. **Lookahead leakage.** If a series extends past `reference_time`, a memory of an event contains the future. Either clip fetched windows at `reference_time`, or label the post-event part explicitly. *Decision needed.*
2. **Scale.** Points in SQL tables are fine to millions of rows. A dedicated store (Timescale, Parquet files like the image directory) is an alternative. Proposed: start with SQL.
3. **Licensing and rate limits.** Market data providers restrict storing and redistributing data.
4. **Scope of v1.** Supplied-only first, or fetch immediately? Is there a specific finance provider or key? *Decision needed.*
5. **Deletion.** Deleting an anchor artifact or entity should remove its anchor rows. A series that other anchors still use should stay; one with no anchors can be removed.
6. **Time handling.** Timezones, irregular sampling and units need explicit rules (UTC RFC 3339 only, as everywhere else; `frequency` is a label, not a constraint).
