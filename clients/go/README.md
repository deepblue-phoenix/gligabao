# gligabao (Go client)

A small Go client for the gligabao HTTP API (standard library only). For now it wraps `POST /v1/gb/remember` and `POST /v1/gb/query`.
It is its own Go module: `github.com/deepblue-phoenix/gligabao/clients/go` (package `gligabao`).

```go
gb := gligabao.NewFromEnv() // GLIGABAO_URL (default http://127.0.0.1:7878), GLIGABAO_API_KEY, GLIGABAO_USER
// or: gligabao.New("http://127.0.0.1:7878", gligabao.WithAPIKey(key), gligabao.WithUserID("u1"))

res, err := gb.Remember(ctx, gligabao.RememberRequest{Text: "Alice works at Acme.", Kind: "episode"})
fmt.Println(res.ArtifactID, res.Edges)

ans, err := gb.Query(ctx, gligabao.QueryRequest{Query: "Where does Alice work?"})
fmt.Println(ans.Answer, ans.Evidence.Edges)
```

- A server with auth maps the API key (`Authorization: Bearer`) to the user; a server without it needs `WithUserID`: the user ID is sent in the `gb-user-id` header of every call (never in the body or the query string, the server does not read it there).
- A non-2xx answer is a `*gligabao.Error` (`Status`, `Message`); a server that cannot be reached has `Status` 0.
- Fields not modelled by the request structs go in `Extra`; the whole JSON answer is in `Raw`.
- Fields: [docs/remember.md](../../docs/remember.md), [docs/query.md](../../docs/query.md).

Tests: `cd clients/go && go test ./...`.
