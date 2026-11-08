# S3 vector buckets (`vector_store = "s3vectors"`)

[Amazon S3 Vectors](https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-vectors.html) is an S3 bucket type for vectors. gligabao can use it as its [vector store](vectors.md), as an alternative to the local chromem store and Pinecone. It implements the same `vector.Store` interface (and `vector.Statter`), so nothing else changes: `remember` writes and `query` reads the same four indexes (entities, edges, chunks, summaries) per dataset.

Code: `internal/db/vector/s3vectors.go` (the wrapper `vector.S3Vectors`), wired in `internal/db/db.go`.

## Configuration

```toml
vector_store = "s3vectors"
embedding_dimensions = 1536

s3_vectors_bucket = "my-gligabao-vectors"   # required; the vector bucket must already exist
s3_vectors_region = "us-east-1"             # optional; default: AWS_REGION / shared AWS config
s3_vectors_prefix = "prod"                  # optional; the "subdirectory" of the bucket (see below)
```

| Setting | Environment variable | Meaning |
|---|---|---|
| `s3_vectors_bucket` | `S3_VECTORS_BUCKET` | Name of the vector bucket. Required with `vector_store = "s3vectors"` (the server refuses to start without it). |
| `s3_vectors_region` | `S3_VECTORS_REGION` | AWS region of the bucket. Empty: the AWS SDK default. |
| `s3_vectors_prefix` | `S3_VECTORS_PREFIX` | Prefix under which gligabao keeps its datasets. Empty: none. |

A value in the TOML file overrides the environment variable. Credentials are **not** in the config: they come from the standard AWS chain (`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`, `~/.aws`, an IAM role). The identity needs `s3vectors:CreateIndex`, `DeleteIndex`, `PutVectors`, `QueryVectors`, `DeleteVectors`, `ListVectors` and `GetVectors` on the bucket. gligabao does not create the bucket.

`embedding_dimensions` is the dimension every index is created with; it cannot change afterwards (an index has a fixed dimension), so changing it needs new indexes (a new prefix).

## How datasets are laid out

S3 Vectors has no directories: a vector bucket holds **indexes**, each with a fixed dimension and distance metric. To get the subdirectory structure, a gligabao vector dataset (see [vectors.md](vectors.md#datasets)) is one index of the bucket, named

```
<prefix>.<vector dataset>
```

```
bucket my-gligabao-vectors
├── prod.dataset-12-entities
├── prod.dataset-12-edges
├── prod.dataset-12-chunks
├── prod.dataset-12-summaries
├── prod.dataset-13-entities
├── ...
├── staging.dataset-1-entities
└── ...
```

- The **prefix** is the directory of a deployment: several gligabao deployments (prod, staging, a project) can share one bucket, each with its own prefix, and never see each other's vectors.
- The **dataset** is the subdirectory: vectors of different datasets live in different indexes, so they never mix and a query only searches the index it names (no metadata filter needed for isolation).
- Index names are 3-63 characters of `a-z`, `0-9`, `-` and `.`. The name is lower-cased and other characters become `-`; if the name had to be altered (or is too long) an 8-hex-digit hash of the original dataset name is appended, so two datasets never share an index. The names gligabao generates (`dataset-<id>-<index>`) are used as they are. The empty dataset is `default`. `vector.S3VectorsIndexName(prefix, dataset)` gives the name.

The limit of indexes per bucket (10,000 by default in AWS) is the limit of datasets x 4 indexes per bucket and prefix.

## Behaviour

| Operation | S3 Vectors call | Notes |
|---|---|---|
| `Upsert` | `PutVectors` (500 per call) | An unknown index is created (`CreateIndex`: `float32`, cosine, `embedding_dimensions`) and the call retried; an index that already exists is fine. A vector with the wrong number of values is rejected before any call. |
| `Query` | `QueryVectors` | `TopK` is capped at 100 (the S3 limit). `Score` is the cosine similarity (1 - distance). `Filter` is an S3 Vectors metadata filter (an equality map like `{"kind": "entity"}` works). Querying a dataset that does not exist returns no matches. `IncludeValues` is not supported (values are not returned). |
| `Delete` | `DeleteVectors` (500 per call) | Unknown keys and datasets are ignored. |
| `DeleteAll` | `DeleteIndex` | A dataset that does not exist is not an error. Used when a dataset is deleted. |
| `Stats` | `ListVectors` | S3 Vectors has no count call, so it pages through the keys (one call per 500 vectors). `disk_bytes` and `last_write` are not available; `bytes` is the estimate `count x dimensions x 4`. |

Metadata (`kind`, `entity_id`, `name`, ...) is stored as filterable S3 metadata (up to 2 KB per vector); it is returned with every match. S3 Vectors is eventually consistent for writes only in the sense of AWS's own guarantees: gligabao does nothing special.

The wrapper takes the AWS client through the small interface `vector.S3VectorsAPI`, so tests (`s3vectors_test.go`) use a fake and need no AWS account.

## Limits

- The store is write-through to AWS: there is no local copy, and no `vector.Exporter` (the `dbexport` archive does not include S3 vectors, as with Pinecone).
- One metric (cosine) and one data type (`float32`).
- Requests are billed by AWS per `PutVectors` / `QueryVectors` call and by stored data; `Stats` (listing) is the one to avoid calling in a loop on big datasets.
