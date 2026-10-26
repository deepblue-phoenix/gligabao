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
| Ontologies used by `remember` and `query` | Planned | Ontologies are loaded and stored as a graph of their own, but neither endpoint uses them yet. See [ontologies.md](../ontologies.md). |
| `query` uses timeseries | Planned | `query` does not read stored series, and treats `series_observation` artifacts like any other. See [timeseries.md](../timeseries.md). |
| Fetch timeseries from the standalone endpoint | Planned | Fetching from providers is in `remember` only; a series is not refreshed automatically. See [timeseries.md](../timeseries.md). |
| Lookahead protection for timeseries | Planned | A series that runs past a text's `reference_time` is stored whole, so a memory of an event can contain its future. Clip fetched windows, or label the post-event part. See [timeseries-design.md](../timeseries-design.md). |
| Web search for image requests | Planned | `web_search` is rejected (`400`) together with `image`, since the image pipeline extracts no entities yet. Depends on grounding image extractions. See [web-search.md](../web-search.md). |
| OpenAI as a main reasoning provider | Planned | `remember` and `query` use only Anthropic for the main reasoning; OpenAI is used for embeddings only. Add OpenAI as a supported reasoning provider too. |

## Considering

| Feature | Status | Notes |
| ------- | ------ | ----- |
| Session memory (per-session turn cache) | Considering | Left out for now; the reasoning, and the least invasive design if revisited, are in [not-included.md](../not-included.md#session-memory). |

## Adding to the roadmap

Add a row to the right table (or a new section), keep the notes short, and link to the doc that holds the details. A larger
plan gets its own file in this directory, linked from its row.
