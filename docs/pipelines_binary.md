# `gligabao_pipelines`: one pipeline per process

`gligabao_pipelines` runs one memory pipeline, `remember` or `query`, in process and exits. It is for places that start for one job and are thrown away afterwards (an AWS Lambda, a container job, a sub-process an orchestrator agent spawns): there is no server, HTTP API, admin UI, agents, training, evals or daemon, and unlike the [CLI](cli.md) it never starts a local server. The binary is static (`CGO_ENABLED=0`), about the size of [`gligabao_agent`](deep-research-agent.md#the-standalone-binary-gligabao_agent), and `./run_build.sh` builds it as `build/gligabao_pipelines`. Code: `cmd/gligabao_pipelines/` (`main.go`: flags and commands; `app.go`: the databases and managers).

```
ANTHROPIC_API_KEY=... OPENAI_API_KEY=... gligabao_pipelines remember [flags] TEXT | -
ANTHROPIC_API_KEY=... OPENAI_API_KEY=... gligabao_pipelines query    [flags] QUESTION | -
gligabao_pipelines version
```

## How it runs

It opens the databases, runs the operation, stops the managers, closes the databases and exits:

- `remember` submits the text to the remember manager and **waits for the run to end**, so the text, its entities, facts and embeddings are stored when the process exits. It has the pipeline of [remember](remember.md) with its defaults.
- `query` is the pipeline of [query](query.md) with its defaults. A user or dataset that never remembered anything gets "The knowledge base is empty: nothing was remembered yet." (exit `0`), not an error.

The output is the result on **stdout** (`remember`: a summary line; `query`: the answer; `--json`: the result as JSON), and logs and totals on **stderr**. Exit codes: `0` ok, `1` the operation failed (a model call failed, the timeout was reached, SIGINT/SIGTERM), `2` bad flags or input, a missing API key or a database that cannot be opened. The API keys are `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` (embeddings; required), from the environment or `--config`.

## The state

The state is what the databases hold, and the process is thrown away, so keeping it is the caller's job:

- **SQLite** (the default): the file is `--db`, else `$GLIGABAO_DB`, else `build/gligabao.db`; the chromem vector store (`<name>.vectors/`) and the image directory (`<name>.images`) are next to it, or `--vector-dir`. The directory of the file is created if it does not exist, so on a Lambda use `--db /tmp/gligabao.db`. After the process exits the files are complete (the journal mode is SQLite's default, one file with no `-wal`), so the caller can upload `<name>.db` and `<name>.vectors/`, and download them to the same paths before the next run.
- **`--export FILE`** writes the whole local state to one ZIP archive when the operation ends (also if it failed), in the format of [`gligabao_agent --export`](deep-research-agent.md#the-export-gligabao_agent---export), with `gligabao_pipelines` as its source. This binary does not import archives: to start from a previous state, restore the SQLite file and the vector directory.
- **PostgreSQL / Pinecone** (`--config gligabao.toml` with `db_backend = "postgres"` and `vector_store = "pinecone"`): the data persists in those servers and the process keeps nothing, so no file needs to travel. Several runs may work on one database at the same time; with SQLite files, concurrent runs from the same snapshot diverge, so run one at a time per dataset (for a Lambda: reserved concurrency of 1, or a FIFO queue).

## Flags

Every command: `--config FILE`, `--db FILE`, `--vector-dir DIR`, `--user` (default `pipelines`), `--dataset` (default `main`), `--export FILE`, `--timeout` (a duration like `90s`; default none), `--json`, `--verbose`.

- `remember`: `--kind` (`knowledge`, default, or `episode`), `--at` (the event time, RFC 3339; default now), `--session`.
- `query`: `--session`.

`-` as the argument reads it from stdin. For a Lambda, set `--timeout` a bit below the function's timeout, so the databases are closed (and `--export` is written) before the platform kills the process.

## Small by construction

`TestMinimalDependencies` in `cmd/gligabao_pipelines` fails if the HTTP API, the server, the sidecar, the research agent, training, evals, `internal/agent_research`, `internal/db/dbfull`, `internal/db/dbimport`, the CLI or the admin UI get linked.
