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

An image is remembered by the image pipeline: the server's LLM reads the text in it (title and text blocks) and
classifies the whole image by a theme (`plot`, `graph`, `chart`, `table`, `document`, ...). Pass the raw bytes, or the path of a JPEG,
PNG, GIF or WebP file (up to 5 MiB); `text` and `kind` do not apply:

```python
res = gb.remember("u1", image="revenue.png", dataset="charts")
print(res["theme"], res["title"], [b["text"] for b in res["text_blocks"]])
```

Install for development: `pip install -e ".[dev]"`, then `pytest`.
