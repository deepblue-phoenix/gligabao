# Vector storage

Embeddings live in a vector store (`internal/db/vector`), selected with `vector_store` in `gligabao.toml`. It is separate from the main database (`db_backend`) and isolates vectors by dataset (see [Datasets](#datasets)). The `Store` interface has `Upsert`, `Query`, `Delete` and `DeleteAll`; `remember` writes entity and edge embeddings to it (see [What `remember` writes](#what-remember-writes)); `recall` does not read it yet.

| `vector_store` | Backend | Needs |
|---|---|---|
| `"chromem"` (default) | [chromem-go](https://github.com/philippgille/chromem-go), embedded and pure Go | nothing (no CGO, no C compiler, no extension) |
| `"pinecone"` | Pinecone, hosted | `pinecone_api_key`, `pinecone_index_host` |

Pinecone is independent of the chromem store and is unchanged. The sqlite-vec extension (and with it CGO and `mattn/go-sqlite3`) is no longer used. The main SQLite database uses the pure-Go `modernc.org/sqlite` driver.

## Datasets

The vector store's unit of isolation is the **dataset**, the same abstraction as the dataset of the graph (see [datasets.md](datasets.md)). Every `Store` method takes the dataset to work on as its argument: vectors of different datasets never mix, and a query only sees the vectors of the dataset it names. Each backend maps a dataset to the isolation primitive it has:

| Backend | A dataset is | Created | `DeleteAll` |
|---|---|---|---|
| chromem | a collection, named after the dataset | on the first `Upsert` | deletes the collection |
| Pinecone | a namespace of the index, named after the dataset | on the first `Upsert` (Pinecone creates namespaces on write) | deletes all vectors of the namespace; a namespace that does not exist is not an error |

An empty dataset name is the backend's default one (chromem: a collection named `_default`; Pinecone: the default namespace). gligabao itself never uses it.

A gligabao dataset keeps its vectors in one vector dataset per purpose (an index), named `dataset-<dataset id>-<index>` (`memory.VectorDataset`), so the name says what the vectors are of:

| Index | Vector dataset | Vector ID | Metadata | Embedded text |
|---|---|---|---|---|
| entities | `dataset-<id>-entities` (`memory.EntityDataset`) | `entity-<entity id>` | `kind` = `entity`, `entity_id`, `name`, `type` | `Name (type): description` (the description is left out if the entity has none) |
| edges | `dataset-<id>-edges` (`memory.EdgeDataset`) | `edge-<edge id>` | `kind` = `edge`, `edge_id`, `source`, `target`, `relation` | the edge's description, e.g. `Alice works at Acme.` (or `source relation target.` if it has none) |

The IDs are the row IDs of `gb_entities` and `gb_edges` in that dataset, so a match is resolved to its row with the ID in the metadata. The metadata holds only what never changes; the validity of a fact (`valid_at`, `invalid_at`, ...) changes when it is superseded, so it is read from the database, not from the vector.

The vector dataset is keyed by the gligabao dataset's numeric ID, not its name or owner, so:

- datasets never share vectors, even if two users use the same dataset name, and a recall only ever searches its own dataset's vectors;
- a name reused after a soft delete is a new dataset with a new ID, so it starts with empty vector datasets;
- there is nothing to do when a dataset is created: the vector datasets appear with the first `Upsert`, and querying one that does not exist returns no matches. A dataset that has no entities or edges yet has none.

Further indexes are added the same way: another `memory.VectorIndex` and so another vector dataset; none of the existing ones change.

`DELETE /v1/gb/datasets/{name}` soft-deletes the dataset (the relational rows are kept) and removes all its vector datasets with `DeleteAll` (one call per index), since a deleted dataset can never be read again. If removing one fails the request still succeeds, because they are unreachable; the error is logged and the other vector datasets are still removed. This works the same for chromem and Pinecone.

## Admin UI: View vec db

Each live dataset in the admin UI has a **View vec db** button. It opens a modal with one row per index (`GET /v1/gb/admin/api/vectors?db=&user_id=&dataset=`, `memory.VectorStats`): the vector dataset name, the number of vectors in it, the number of rows that should have one (entities / edges), how many are missing, the dimension and the raw embedding size. The numbers come from the optional `vector.Statter` interface, which chromem (collection count) and Pinecone (`describe_index_stats`) implement.

## What `remember` writes

[`remember`](remember.md) embeds what it stores, in the same call:

- every entity the text mentions (listed by the extraction, or created from an edge that names it) goes to the entity index;
- every edge the text states goes to the edge index: a new edge, or the stored edge a repeated fact was merged into.

All the texts of one call are embedded in a single request to the OpenAI embeddings API (`text-embedding-3-small`, with `embedding_dimensions` as the output size), then written with one `Upsert` per vector dataset. Vectors are keyed by entity / edge ID, so an entity or fact that a later text mentions again is overwritten, not duplicated; an entity is re-embedded from its current stored name, type and description.

- The embeddings are computed before the database transaction commits: if embedding fails, nothing is stored and the request fails.
- The vectors are written right after the commit (the database and the vector store cannot share a transaction). If that write fails, the request fails with the relational data already saved; the vectors of those entities and edges are written again the next time they are mentioned.
- Embedding needs `openai_api_key` (or `OPENAI_API_KEY`). Without it the server still starts, logs a warning, and `remember` stores no vectors.
- The tokens and cost of each embedding request are stored in `gb_llm_calls` (type `remember`, with the embedding model and `output_tokens` 0), next to the LLM calls.
- The vectors have no effect yet on what `recall` returns: it does not read them.

If you change `embedding_dimensions`, or the embedding model, the stored vectors are no longer comparable with new ones (the chromem store rejects the new dimension outright); there is no re-embedding yet.

## chromem store

- Each dataset (one per gligabao dataset and index) is a chromem collection, named after the dataset; on disk its directory is a hash of the name. The data is kept in memory for searching and every write is also persisted to a directory (one gob file per vector, plus `gligabao-dimensions`), which is loaded again on startup.
- The directory is `<db path without extension>.vectors` next to the main SQLite database (e.g. `bin/gligabao.vectors`, or `/data/gligabao.vectors` in the container). Override it with `serve --vector-dir <path>`, or `ServeOptions.VectorDir` when embedding. `:memory:` gives a throwaway in-memory store (used by tests).
- Search is an exhaustive cosine-similarity scan of the dataset; `Match.Score` is the cosine similarity (higher is closer), like a Pinecone cosine index. Vectors are normalized to unit length when stored, so `IncludeValues` returns normalized vectors. All-zero vectors are rejected.
- `embedding_dimensions` (default 1536) is fixed when the directory is created. Opening it with a different value fails with an error.
- Metadata is any JSON-compatible map. Filters support equality only (`{"type": "person"}` or `{"type": {"$eq": "person"}}`, all must match); other operators return an error. The filter is applied before the search, so `TopK` results are returned whenever that many vectors match.
- Upsert validates all vectors first, but is not atomic across a batch if the disk write fails part-way.
- The whole dataset is held in memory, which suits small and medium datasets. For larger ones use Pinecone.
- Do not point two server processes at the same directory.

Existing `*.vectors.db` files made by the sqlite-vec store are not read; the new store starts empty, and the old files can be deleted.

## Configuration

```toml
vector_store = "chromem"       # or "pinecone"
embedding_dimensions = 1536
openai_api_key = ""            # embeds entities and edges in remember; default: OPENAI_API_KEY
```

A Pinecone index must be created with the same dimension as `embedding_dimensions`.

The previous value `vector_store = "sqlite"` is no longer valid and fails config validation.

## Building without CGO

Everything builds with `CGO_ENABLED=0`; the Dockerfile does so and produces a static binary. See the README's Building section.
