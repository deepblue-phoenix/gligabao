# sargo

A small model request router. A caller POSTs one provider-neutral inference request; sargo forwards it
to Anthropic or OpenAI and returns the reply in the same neutral shape. Tool calling is supported.
No streaming yet. The server is blocking, one thread per connection.

## Library and binary

The package is both: `src/lib.rs` exposes the modules (`config`, `router`, `providers`, ...) and `src/main.rs` is the `sargo` binary, which only does the start-up. Other crates can depend on it (`sargo = { path = "..." }`) and build their own `main`.

## Run

```sh
ANTHROPIC_API_KEY=... OPENAI_API_KEY=... cargo run      # reads ./config.toml (optional)
cargo run -- path/to/config.toml                        # or SARGO_CONFIG=path/to/config.toml
cargo test
```

## GCNII* graph classifier

sargo also holds GCNII* (the untied GCNII variant), ported from the deepblue engine of baby_thinker (`src/gcnii/`), with training and inference on the CPU.

```sh
sargo gcnii train DIR --save w.bin [--epochs 30] [--lr 0.01] [--batch 4] [--hidden 16] [--depth 16] [--seed N]
sargo gcnii eval DIR --load w.bin
sargo gcnii infer graph.json --load w.bin [--classes a,b]
sargo gcnii embed DIR|graph.json --load w.bin --out e.jsonl [--normalize]
# train also: [--grad-stats K] [--grad-map K] [--export-embeddings FILE] [--normalize]
```

`--grad-stats` / `--grad-map` print the gradient histograms and the per-element gradient map as `@grad` / `@gradmap` lines (format at the top of `src/gcnii/metrics.rs`). `embed` writes JSON lines `{"path", "class"?, "names"?, "embedding"}`.

DIR holds graph documents `{"n": N, "edges": [[a,b],...], "nodes": [{"name", "type"}, ...], "class": "..."}` as `.json` files or `.jsonl` lines (a `.json` without `class` takes its parent dir name). The weights file stores the size and node types and is compatible with the `gcnii_star` files of deepblue. To serve it, set `[gcnii] weights = "w.bin"` (and optionally `classes`) in `config.toml`, then `POST /v1/gcnii/infer` with `{"graph": {"n": 4, "edges": [[0,1],[1,2]]}}` answers `{"class", "index", "logits", "probabilities", "nodes", "edges"}`; `POST /v1/gcnii/embed` with `{"graph": {...}, "normalize": true}` answers `{"embedding", "dim"}`.

## Embedded in the gligabao binary

`./run_build.sh --sargo` (in the repo root) builds sargo as a static musl binary (`./run_build_rust.sh --embed`: needs cargo and
`musl-tools`), copies it to `bin/sargo` and builds gligabao, which embeds it with `embed.go` (package `sargoembed`) together with
this directory's `config.toml`, the default config (`gligabao sargo config` prints it). The binary is for the architecture of the
build machine; without `--sargo`, `bin/` only holds `.gitkeep` and the gligabao binary has no sargo.

- `gligabao sargo serve [--sargo-config FILE]` extracts both to a temporary directory and runs the server in the foreground until
  interrupted. With `--sargo-config FILE` (or `$SARGO_CONFIG`) that file is used instead of the embedded default config. The API
  keys come from the environment.
- `gligabao serve` starts it too, as a child process stopped with the server, when `sargo = true` in `gligabao.toml` (or `serve --sargo`);
  `sargo_config = "file.toml"` uses a config of yours instead of the embedded default (relative to `gligabao.toml`). The server does not
  start if the binary has no sargo or sargo fails to start. sargo gets the `anthropic_api_key` / `openai_api_key` of the config as
  `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` (so keys from a secret store work); it listens on `server.address` of its config
  (default `127.0.0.1:8431`). The same for `gligabao.Mount`.
- In Go, `sargo.StartServer(ctx, sargo.ServerOptions{ConfigPath: ...})` (`internal/sargo/server.go`) does the same in the
  background: it waits for `GET /health` and returns a `*Server` with a ready `Client`; `Stop` kills the child and removes the files.
  The address of the client is `server.address` of the config in use.

## Config

`config.toml` (all keys optional, the shipped file lists the defaults): `[server]` address, read timeout and
max body size; `[http_client]` provider timeout; `[providers.anthropic]` / `[providers.openai]` base URL and the
name of the env var holding the API key (keys are never in the file); `[gcnii]` the weights file and class names. Unknown keys are rejected.
It is loaded once in `src/main.rs` into the `Config` struct (`src/config.rs`) and passed by reference to the
server and the providers. If no config path is given and `./config.toml` does not exist, defaults are used.

## API

`GET /health` -> `{"status":"ok"}`

`POST /v1/infer`

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-4-5",
  "system": "You are helpful.",
  "messages": [{"role": "user", "content": [{"type": "text", "text": "Weather in Oslo?"}]}],
  "tools": [{"name": "weather", "description": "Get weather",
             "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}],
  "max_tokens": 1024
}
```

Reply:

```json
{
  "provider": "anthropic", "model": "claude-sonnet-4-5",
  "content": [{"type": "tool_use", "id": "tu_1", "name": "weather", "input": {"city": "Oslo"}}],
  "stop_reason": "tool_use",
  "usage": {"input_tokens": 50, "output_tokens": 20}
}
```

`stop_reason`: `end_turn`, `tool_use`, `max_tokens` (or the provider's raw value).

### Tool calling

sargo is stateless; the caller runs the loop. When `stop_reason` is `tool_use`, run the tools, then send the
same request again with two messages appended: an `assistant` message with the returned `content` unchanged,
and a `user` message with one `{"type": "tool_result", "tool_use_id": "<id>", "content": "<result>"}` block
per tool call (add `"is_error": true` if the tool failed). Repeat until `stop_reason` is `end_turn`.

Errors: `{"error": "..."}` with 400 (bad request), 502 (provider failed), 503 (provider not configured).

## JSON Schema

`inference.schema.json` (JSON Schema draft 2020-12) describes the whole API for clients: the root schema is the
`POST /v1/infer` request body; `$defs` holds `InferenceResponse`, `ErrorResponse` and `HealthResponse`, and the
top-level `x-endpoints` lists each endpoint with its request and reply schemas. Validate requests against it
before sending. A unit test in `src/types.rs` checks that the schema's property names match the Rust structs.

## Swapping parts

- HTTP server: implement `HttpServer` (`src/http_server.rs`), change one line in `src/main.rs`.
- HTTP client: only `src/http_client.rs` calls out.
- New provider: implement `Provider` in a new file in `src/providers/` (and register it in `providers/mod.rs`), add a field and a match arm in `src/router.rs`.
