# gligabao (Python client)

Python client for the gligabao Go server.

```python
from gligabao import Client

gb = Client("http://127.0.0.1:7878")
res = gb.remember("u1", "Alice works at Acme and lives in Paris.", kind="episode")  # kind defaults to "knowledge"
print(res["artifact_id"], res["edges"])
```

An image is remembered by the image pipeline: the server's LLM reads the text in it (title and text blocks) and
classifies the whole image by a theme (`plot`, `graph`, `chart`, `table`, `document`, ...). Pass the raw bytes, or the path of a JPEG,
PNG, GIF or WebP file (up to 5 MiB); `text` and `kind` do not apply:

```python
res = gb.remember("u1", image="revenue.png", dataset="charts")
print(res["theme"], res["title"], [b["text"] for b in res["text_blocks"]])
```

Install for development: `pip install -e ".[dev]"`, then `pytest`.
