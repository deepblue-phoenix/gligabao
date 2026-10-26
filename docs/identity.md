# Identity

Every request to gligabao says which user it acts for, with a `user_id`. without the `Auth` hook gligabao does not authenticate users: `user_id` is an opaque string that the calling application supplies and is trusted to get right. Put gligabao behind your own auth layer and pass the authenticated user's ID.

## With the `Auth` hook

A program that embeds gligabao can set `ServeOptions.Auth` (see [hooks.md](hooks.md#auth)). The caller then sends an API key (`Authorization: Bearer <key>`) instead of a `user_id`: the hook maps the key to a user, and the request acts as that user, replacing any `user_id` it carries. A missing or rejected key is a `401`. Everything below about `user_id` (format, ownership) is unchanged, it is just not the caller that chooses it any more. The Python client works this way: it reads the key from `GLIGABAO_API_KEY`.

## Passing `user_id`

| Endpoint | Where |
| -------- | ----- |
| `POST /v1/gb/remember` | JSON body: `"user_id"` |
| `POST /v1/gb/query` | JSON body: `"user_id"` |
| `POST /v1/gb/datasets` | JSON body: `"user_id"` |
| `GET /v1/gb/datasets` | query string: `?user_id=u1` |
| `DELETE /v1/gb/datasets/{name}` | query string: `?user_id=u1` |
| `POST /v1/gb/ontologies`, `PUT /v1/gb/ontologies/{name}` | JSON body: `"user_id"` (ontologies are owned by a user; names are unique per user, see [ontologies.md](ontologies.md)) |

`GET /health` takes none.

```
curl -s localhost:7878/v1/gb/remember -d '{"user_id":"u1","text":"Alice works at Acme."}'
curl -s "localhost:7878/v1/gb/datasets?user_id=u1"
```

## Format

`user_id` is required. Leading and trailing whitespace is trimmed, and what remains must be:

- not empty,
- at most 128 bytes,
- free of control characters.

Anything else is allowed (UUIDs, emails, numeric IDs as strings, ...). IDs are compared exactly, so `Alice` and `alice` are different users. A missing or invalid `user_id` returns `400` with `{"error": "invalid user_id: ..."}`.

## Ownership

A [dataset](datasets.md) is owned by the user who created it, and that ownership covers everything in it. Artifacts, entities and edges belong to a dataset (`dataset_id`), so they have no `user_id` of their own: they belong to whoever owns the dataset. The owner is shown as `user_id` on dataset objects and in the `remember` response.

- Dataset names are unique per user. Two users can each have a dataset called `work`, and they are unrelated: separate ids, separate graphs and separate vector datasets.
- `list` returns only the caller's datasets.
- `remember` and `query` look the dataset up by `(user_id, name)`. `remember` creates it for that user if missing. `query` returns `404` if the user has no live dataset with that name, even when another user has one.
- `delete` soft-deletes only the caller's dataset, and returns `404` for another user's.
- There is no cross-user sharing. Nothing is read or written across users.
- The default dataset `main` is per user, and is created by that user's first `remember`.

Because a user's dataset is looked up by `(user_id, name)`, a user cannot reach another user's data by guessing a dataset name or id. A caller that supplies someone else's `user_id` can, since there is no authentication.

## Storage

```
gb_datasets (id, user_id, name, ...)   UNIQUE (user_id, name) WHERE deleted = 0
  └─ gb_artifacts, gb_entities, gb_edges (dataset_id); gb_edge_artifacts and gb_entity_artifacts link edges and entities to artifacts
```

The schema has no migration. A database created before `user_id` was added must be recreated.

`gb_inferences` (token and cost accounting) is not tied to a user.
