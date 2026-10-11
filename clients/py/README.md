# gligabao (Python client)

Python client for the gligabao Go server.

```python
from gligabao import Client

gb = Client("http://127.0.0.1:7878")
res = gb.remember("u1", "Alice works at Acme and lives in Paris.", kind="episode")  # kind defaults to "knowledge"
print(res["artifact_id"], res["edges"])
```

Install for development: `pip install -e ".[dev]"`, then `pytest`.
