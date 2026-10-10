# Vector storage

Embeddings live in a vector store (`internal/db/vector`), selected with `vector_store` in `gligabao.toml`. It is separate from the main database (`db_backend`) and uses one namespace per dataset (see [Datasets](#datasets)). The `Store` interface has `Upsert`, `Query`, `Delete` and `DeleteAll`; the API handlers do not use it yet (the server only opens it at startup).

| `vector_store` | Backend | Needs |
|---|---|---|
| `"chromem"` (default) | [chromem-go](https://github.com/philippgille/chromem-go), embedded and pure Go | nothing (no CGO, no C compiler, no extension) |
| `"pinecone"` | Pinecone, hosted | `pinecone_api_key`, `pinecone_index_host` |

Pinecone is independent of the chromem store and is unchanged. The sqlite-vec extension (and with it CGO and `mattn/go-sqlite3`) is no longer used. The main SQLite database uses the pure-Go `modernc.org/sqlite` driver.

## Datasets

A dataset's embeddings live in the namespace `dataset-<dataset id>` (`memory.VectorNamespace`). It is keyed by the dataset's numeric ID, not its name or owner, so:

- datasets never share vectors, even if two users use the same dataset name, and a recall only ever searches its own dataset's namespace;
- a name reused after a soft delete is a new dataset with a new ID, so it starts with an empty namespace;
- there is nothing to do when a dataset is created: the store creates the namespace on the first `Upsert` (a chromem collection), and querying a namespace that does not exist returns no matches.

`DELETE /v1/gb/datasets/{name}` soft-deletes the dataset (the relational rows are kept) and removes its namespace with `DeleteAll`, since a deleted dataset can never be read again. If removing the vectors fails the request still succeeds, because the namespace is unreachable; the error is logged. This works the same for chromem and Pinecone. `remember` and `recall` do not read or write vectors yet.

## chromem store

- Each namespace (one per dataset) is a chromem collection, named after the namespace; on disk its directory is a hash of the name. The data is kept in memory for searching and every write is also persisted to a directory (one gob file per vector, plus `gligabao-dimensions`), which is loaded again on startup.
- The directory is `<db path without extension>.vectors` next to the main SQLite database (e.g. `bin/gligabao.vectors`, or `/data/gligabao.vectors` in the container). Override it with `serve --vector-dir <path>`, or `ServeOptions.VectorDir` when embedding. `:memory:` gives a throwaway in-memory store (used by tests).
- Search is an exhaustive cosine-similarity scan of the namespace; `Match.Score` is the cosine similarity (higher is closer), like a Pinecone cosine index. Vectors are normalized to unit length when stored, so `IncludeValues` returns normalized vectors. All-zero vectors are rejected.
- `embedding_dimensions` (default 1536) is fixed when the directory is created. Opening it with a different value fails with an error.
- Metadata is any JSON-compatible map. Filters support equality only (`{"type": "person"}` or `{"type": {"$eq": "person"}}`, all must match); other operators return an error. The filter is applied before the search, so `TopK` results are returned whenever that many vectors match.
- Upsert validates all vectors first, but is not atomic across a batch if the disk write fails part-way.
- The whole namespace is held in memory, which suits small and medium datasets. For larger ones use Pinecone.
- Do not point two server processes at the same directory.

Existing `*.vectors.db` files made by the sqlite-vec store are not read; the new store starts empty, and the old files can be deleted.

## Configuration

```toml
vector_store = "chromem"       # or "pinecone"
embedding_dimensions = 1536
```

The previous value `vector_store = "sqlite"` is no longer valid and fails config validation.

## Building without CGO

Everything builds with `CGO_ENABLED=0`; the Dockerfile does so and produces a static binary. See the README's Building section.
