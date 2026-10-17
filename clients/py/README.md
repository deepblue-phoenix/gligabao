# gligabao (Python client)

Python client for the gligabao Go server.

```python
from gligabao import Client

gb = Client("http://127.0.0.1:7878")
res = gb.remember("u1", "Alice works at Acme and lives in Paris.", kind="episode")  # kind defaults to "knowledge"
print(res["artifact_id"], res["edges"])
```

Each LLM stage of `remember` can run on a model of your choice with `models` (stages `extract_graph`, `resolve_edges` and, for
images, `extract_image`); a stage you leave out uses the server's default model:

```python
res = gb.remember("u1", "Alice works at Acme.", models={"extract_graph": "claude-sonnet-4-5"})  # resolve_edges: default
```

No LLM stage thinks by default (`"none"`, so no thinking tokens are paid for); `reasoning` turns it on per stage (`"low"`,
`"medium"` or `"high"`). Each call is recorded by the server as an inference with its model and level, and each
chunk summary references the run that wrote it:

```python
res = gb.remember("u1", long_text, models={"summarize_chunk": "claude-haiku-5-5"}, reasoning={"summarize_chunk": "low"})
print(res["chunks"][0]["summary"])  # artifact_id, text, model, reasoning, inference_id
```

With `web_search=True` the server also searches the web for the latest world events about the key entities of the text and
remembers what it finds (needs an Anthropic API key on the server; see `docs/web-search.md` of gligabao):

```python
res = gb.remember("u1", "Apple and Microsoft are large companies.", web_search=True)
print([(w["entity"], w["query"], w["artifact_id"]) for w in res.get("web_searches", [])])
```

Timeseries can be stored with a text and anchored to what it yields (see `docs/timeseries.md` of gligabao). A series
without `anchors` is anchored to the artifact; an entity or edge anchor is matched after the text is extracted, so it may name
an entity the text will yield. `attach_timeseries` does the same for what is already stored:

```python
from datetime import datetime, timezone

closes = [(datetime(2025, 1, 30, tzinfo=timezone.utc), 237.6), (datetime(2025, 1, 31, tzinfo=timezone.utc), 222.0)]
res = gb.remember("u1", "Apple fell after the earnings miss.", kind="episode", timeseries=[
    {"key": "AAPL.close", "unit": "USD", "points": closes,
     "anchors": [{"entity": {"name": "Apple", "type": "organization"}}]},
])
print([(s["key"], s["stats"]["trend"], s["anchors"]) for s in res["series"]])
gb.attach_timeseries("u1", [{"key": "AAPL.close", "points": closes, "anchors": [{"entity_id": 12}]}])
```

If the server was started with timeseries providers (a Go program embedding gligabao registers them), it can fetch series itself:
an LLM reads the providers' descriptions and decides which provider is for which entity of the text.

```python
res = gb.remember("u1", "Apple fell after the earnings miss.", kind="episode", fetch_timeseries={"providers": ["prices"], "before": "30d"})
print([(f["entity"], f["provider"], f["query"], [s["key"] for s in f.get("series", [])]) for f in res["series_fetches"]])
```

Add `observe_timeseries=True` to also derive a `series_observation` artifact from each stored series (a text generated from
its stats, extracted into the graph like any text); they are listed under `series_observations`.

An image is remembered by the image pipeline: the server's LLM reads the text in it (title and text blocks) and
classifies the whole image by a theme (`plot`, `graph`, `chart`, `table`, `document`, ...). Pass the raw bytes, or the path of a JPEG,
PNG, GIF or WebP file (up to 5 MiB); `text` and `kind` do not apply:

```python
res = gb.remember("u1", image="revenue.png", dataset="charts")
print(res["theme"], res["title"], [b["text"] for b in res["text_blocks"]])
```

Install for development: `pip install -e ".[dev]"`, then `pytest`.
