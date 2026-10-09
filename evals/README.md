# Evals

End-to-end checks that run against a real gligabao server (and the real LLM), so `ANTHROPIC_API_KEY` is required.

Run from the repo root: `./run_evals.sh` (optionally pass case names, e.g. `./run_evals.sh remember_recall`).

The runner is pure Go (`evals/main.go`; `run_evals.sh` just invokes `go run ./evals`). Each case gets a fresh temporary SQLite DB and an in-process HTTP server using the real handlers, so cases are independent.

## Case format

```json
{
  "name": "remember_recall",
  "remember": "Alice works at Acme and lives in Paris.",
  "query": "Where does Alice work?",
  "expect_contains": ["Acme"]
}
```

1. `POST /v1/gb/remember` with `remember` as the text.
2. `POST /v1/gb/recall` with `query`.
3. Passes if the answer contains every `expect_contains` string (case-insensitive).
