# The gligabao CLI

`gligabao` is also a client: `remember`, `query`, `dataset`, `api`, `status` and `stop` let an agent (or a person) use gligabao from the command line, without writing HTTP calls. The commands are thin: each one builds the JSON body of the matching REST endpoint, sends it and prints the response, so fields, validation and errors are exactly those of the API ([remember](remember.md), [query](query.md), [datasets](datasets.md)). The commands live in the public `cli` package (`cli/commands.go`), so a program that embeds gligabao with `cli.New` gets them too (its own `serve`, with its hooks, is what the CLI starts as the local daemon).

## The local daemon

The commands always talk to a server. By default that is a **local daemon**: `gligabao serve --host 127.0.0.1` of this same binary, which the first command starts in the background (its own session, so it outlives the terminal) when nothing answers on the port. If a gligabao server already answers there, the command uses it and starts nothing. So there is one process on the database, however many agents call the CLI at once, and the remember manager still runs one remember at a time.

- Port: `port` of the config (default 7878). If another program holds it, the command fails and tells so.
- No authentication: the daemon listens on `127.0.0.1` only (`serve --host`, or `host` in the config), and the CLI sends no API key to it.
- Keys: the daemon is a normal server, so it needs `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` (environment or config). If it cannot start, the command exits with code 5 and prints the end of the daemon's log.
- Files: the log is `daemon-<port>.log` and the pid `daemon-<port>.pid`, in `~/.gligabao` (`GLIGABAO_HOME` changes the directory). `gligabao stop` shuts the daemon down (the next command starts it again); `gligabao status` shows whether it runs, without starting it.

### Where the config and the data are

- If the current directory has a `gligabao.toml`, or `--config` is given, the daemon runs as `gligabao serve` would: that config and the default paths of `serve` (`build/gligabao.db`, relative to the directory).
- Otherwise it uses a stable directory, so an agent gets the same memory whatever directory it runs in: `~/.gligabao/gligabao.toml` if it exists (else the defaults) and the database `~/.gligabao/gligabao.db` (the vectors and images next to it).

### Another server

`--server URL` (or `GLIGABAO_URL`) sends the commands to that server instead; nothing is started, and `GLIGABAO_API_KEY` is sent as `Authorization: Bearer`. (Remote servers as targets are not finished: how the CLI authenticates to them is still to be designed.)

## Commands

```
gligabao remember [TEXT | -] [-f FILE] [-d DATASET] [--kind episode] [--at TIME] [--session S]
                  [--web-search] [--series FILE.json] [--image FILE] [--model STAGE=NAME] [--reasoning STAGE=LEVEL] [--json BODY]
gligabao query QUESTION [-d DATASET] [--session S] [--evidence-only] [--geo-nearby] [--geo-radius KM]
                  [--model STAGE=NAME] [--reasoning STAGE=LEVEL] [--raw] [--json BODY]
gligabao dataset ls | create NAME [--description D] [--tag T] | rm NAME | graph NAME
gligabao research start TOPIC [--max-steps N] [--model M] [-d DATASET] [--wait [--raw]] [--json BODY]
gligabao research ls [--active] [--status running|completed|failed] [--limit N]
gligabao research get ID [--raw]
gligabao api METHOD PATH [JSON | @file | -]      # any other endpoint (timeseries, ontologies, ...)
gligabao status                                  # the target, and whether it runs
gligabao stop                                    # stop the local daemon
```

Global flags: `--user` (env `GLIGABAO_USER`, default `local_user`: the `user_id` of every request), `--server`, `--config`.

- `remember` takes the text as an argument, from `--file`, or from stdin with `-`; `--image` remembers an image instead. The flags map to the request fields (`--at` is `reference_time`, `--series` is `timeseries.series`, `--model` and `--reasoning` are `models` and `reasoning`, repeatable as `STAGE=VALUE`).
- `--json` (on `remember` and `query`) is the whole request body as JSON text, `@file` or `-` (stdin), so the exact REST body can be passed and a new request field never needs a new flag. The flags that are given override its fields; `user_id` defaults to `--user` only if the body has none.
- `query --evidence-only` is a retrieval-only call, see [query.md](query.md#retrieval-only-evidence_only). `query --raw` prints just the answer, as text.
- `research` starts and follows [research agents](research.md): `start` prints the run right away (`-d` picks the dataset of your knowledge base the agent queries and remembers into, `main` by default) (the agent goes on in the background, it costs money), `--wait` waits for it to end and prints the finished run (exit code 1 if it failed; `--raw` prints only the report), `ls --active` lists the agents that are working, and `get ID` shows a run with every state it went through (`--raw`: only the report).
- `api` sends the request as it is (it adds no `user_id`), for what has no command.

## Output and exit codes

The response of the server is printed to stdout as indented JSON. An error is printed to stderr as `{"error": "...", "status": 404}` (`status` is the HTTP status, if there was one), and the exit code says what kind it is:

| Code | Meaning |
| ---- | ------- |
| 0 | ok |
| 1 | any other error |
| 2 | bad usage, or the server answered `400` |
| 3 | not found (`404`) |
| 4 | conflict (`409`) |
| 5 | not available: a `5xx` (no LLM key, a failed LLM call, ...), the server cannot be reached, or the daemon could not start |

## Not covered

Training, evals, the sidecar and the admin API are not commands (they need the sidecar and long-running event streams); use `api` for the plain request/response ones, or the server's endpoints. Forgetting a single artifact or fact, and reading the status of a run, are not available yet.
