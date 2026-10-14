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

An image is remembered by the image pipeline: the server's LLM reads the text in it (title and text blocks) and
classifies the whole image by a theme (`plot`, `graph`, `chart`, `table`, `document`, ...). Pass the raw bytes, or the path of a JPEG,
PNG, GIF or WebP file (up to 5 MiB); `text` and `kind` do not apply:

```python
res = gb.remember("u1", image="revenue.png", dataset="charts")
print(res["theme"], res["title"], [b["text"] for b in res["text_blocks"]])
```

Install for development: `pip install -e ".[dev]"`, then `pytest`.
