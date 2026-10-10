# Datasets

A dataset is a hard logical container. Every memory (episode), entity and edge belongs to exactly one dataset (`dataset_id`), and datasets never share nodes: the same entity remembered into two datasets is two separate rows, and recall only sees the graph of the dataset it targets.

- Every dataset is owned by a `user_id`, and that ownership covers all its memories, entities and edges. All endpoints require `user_id` (see [identity.md](identity.md)) (a non-empty string of at most 128 bytes): in the JSON body for `POST`, in the query string for `GET` and `DELETE`. A missing or invalid one returns `400`. A user only sees and changes their own datasets; names are unique per user, so two users can each have a dataset called `work`.
- `remember` and `recall` take an optional `dataset` (name). If omitted, the default dataset `main` is used. There is no dataset until it is created: `main` is created for a user by their first `remember`.
- `remember` creates the dataset on demand if it does not exist. `recall` on a dataset that does not exist (or was deleted) returns `404`.
- Names are 1-64 characters of letters, digits, `.`, `_`, `-`, starting with a letter or digit. Invalid names return `400`.
- A dataset has `id`, `user_id`, `name`, `description`, `tags` (list of strings), `created_at`, `deleted` and `deleted_at`.

## Soft delete

Deleting sets `deleted = true` (and `deleted_at`); no rows are removed. A deleted dataset is invisible to list, remember and recall. Its name can be reused: creating (or remembering into) that name makes a new dataset with a new id and an empty graph. The old rows stay in the database under the old id.

## Endpoints

`POST /v1/gb/datasets`: `{"user_id": "u1", "name": "work", "tags": ["project"], "description": "optional"}` returns `201` with the dataset. `409` if a live dataset with that name exists, `400` for a bad name.

`GET /v1/gb/datasets?user_id=u1`: lists that user's datasets: `{"datasets": [{"id": 1, "user_id": "u1", "name": "main", "tags": [], "created_at": "...", "deleted": false}, ...]}`, oldest first. `?include_deleted=true` also lists soft-deleted ones.

`DELETE /v1/gb/datasets/{name}?user_id=u1`: soft-deletes the dataset. `204`, or `404` if the user has no live dataset with that name.

`GET /v1/gb/datasets/{name}/graph?user_id=u1`: exports the dataset's whole graph as JSON; see [graph.md](graph.md).
