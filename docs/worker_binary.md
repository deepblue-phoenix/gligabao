# `gligabao_worker`: one pipeline per process

`gligabao_worker` runs one memory pipeline, `remember` or `query`, in process and exits. It is for places that start for one job and are thrown away afterwards (an AWS Lambda, a container job, a sub-process an orchestrator agent spawns): there is no server, HTTP API, admin UI, agents, training, evals or daemon, and unlike the [CLI](cli.md) it never starts a local server. It works **only on PostgreSQL and a remote vector store (Pinecone or S3 Vectors)**, the same ones the main gligabao instance uses, so both see every write at once and nothing is synced between them. There is no SQLite and no chromem. The binary is static (`CGO_ENABLED=0`), about the size of [`gligabao_agent`](deep-research-agent.md#the-standalone-binary-gligabao_agent), and `./run_build.sh` builds it as `build/gligabao_worker`. Code: `cmd/gligabao_worker/` (`main.go`: flags and commands; `app.go`: the databases and managers).

```
ANTHROPIC_API_KEY=... OPENAI_API_KEY=... gligabao_worker remember [flags] TEXT | -
ANTHROPIC_API_KEY=... OPENAI_API_KEY=... gligabao_worker query    [flags] QUESTION | -
gligabao_worker version
```

## How it runs

It opens the databases, runs the operation, stops the managers, closes the databases and exits:

- `remember` submits the text to the remember manager and **waits for the run to end**, so the text, its entities, facts and embeddings are stored when the process exits. It has the pipeline of [remember](remember/remember.md) with its defaults.
- `query` is the pipeline of [query](query/query.md) with its defaults. A user or dataset that never remembered anything gets "The knowledge base is empty: nothing was remembered yet." (exit `0`), not an error.

The output is the result on **stdout** (`remember`: a summary line; `query`: the answer; `--json`: the result as JSON), and logs and totals on **stderr**. Exit codes: `0` ok, `1` the operation failed (a model call failed, the timeout was reached, SIGINT/SIGTERM), `2` bad flags or input, a missing API key or a database that cannot be opened. The API keys are `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` (embeddings; required), from the environment or `--config`.

## The state

The worker keeps nothing: the data lives in the databases it is pointed at, which are those of the main gligabao instance (so a text remembered by the worker is at once visible to the main instance, and the other way round).

- **Relational and graph database: PostgreSQL only.** `--config gligabao.toml` with `db_backend = "postgres"` and `postgres_dsn`, or without a file the `POSTGRES_DSN` environment variable. A config file that says `sqlite` (or does not say `db_backend`, which defaults to `sqlite`) is refused with exit code `2`. SQLite is not supported: `remember` reads at the start of its pipeline and `query` reads all the way, so a local copy cannot be kept in sync with the main instance.
- **Vector store: Pinecone or S3 Vectors only** (`vector_store = "pinecone"` or `"s3vectors"`; remote, so the main instance sees the same vectors). `chromem` is refused. Without a config file the store is S3 Vectors if `S3_VECTORS_BUCKET` is set, else Pinecone (`PINECONE_API_KEY` and `PINECONE_INDEX_HOST`).
- The worker has no export: it does not write archives and there is nothing to download to the main instance. Several runs may work on one database at the same time.

## Flags

Every command: `--config FILE`, `--user` (default `worker`), `--dataset` (default `main`), `--timeout` (a duration like `90s`; default none), `--json`, `--verbose`.

- `remember`: `--kind` (`knowledge`, default, or `episode`), `--at` (the event time, RFC 3339; default now), `--session`.
- `query`: `--session`.

`-` as the argument reads it from stdin. For a Lambda, set `--timeout` a bit below the function's timeout, so the databases are closed before the platform kills the process.

## Small by construction

`TestMinimalDependencies` in `cmd/gligabao_worker` fails if the HTTP API, the server, the sidecar, the research agent, training, evals, `internal/agent_research`, `internal/db/dbfull`, `internal/db/dbimport`, the CLI or the admin UI get linked.
