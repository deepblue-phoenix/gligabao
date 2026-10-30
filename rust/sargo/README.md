# sargo

A small model request router. A caller POSTs one provider-neutral inference request; sargo forwards it
to Anthropic or OpenAI and returns the reply in the same neutral shape. Tool calling is supported.
No streaming yet. The server is blocking, one thread per connection.

## Run

```sh
ANTHROPIC_API_KEY=... OPENAI_API_KEY=... cargo run      # SARGO_ADDR defaults to 127.0.0.1:8080
cargo test
```

Optional: `ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL`, `SARGO_ADDR`.

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

## Swapping parts

- HTTP server: implement `HttpServer` (`src/http_server.rs`), change one line in `main.rs`.
- HTTP client: only `src/http_client.rs` calls out.
- New provider: implement `Provider` in a new file in `src/providers/` (and register it in `providers/mod.rs`), add a field and a match arm in `src/router.rs`.
