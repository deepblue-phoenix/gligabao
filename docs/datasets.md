# Datasets

A dataset is a hard logical container, modelled on Cognee. Every memory (episode), entity and edge belongs to exactly one dataset (`dataset_id`), and datasets never share nodes: the same entity remembered into two datasets is two separate rows, and recall only sees the graph of the dataset it targets.

- `remember` and `recall` take an optional `dataset` (name). If omitted, the default dataset `main` is used. `main` exists from startup.
- `remember` creates the dataset on demand if it does not exist. `recall` on a dataset that does not exist (or was deleted) returns `404`.
- Names are 1-64 characters of letters, digits, `.`, `_`, `-`, starting with a letter or digit. Invalid names return `400`.
- A dataset has `id`, `name`, `description`, `tags` (list of strings), `created_at`, `deleted` and `deleted_at`.

## Soft delete

Deleting sets `deleted = true` (and `deleted_at`); no rows are removed. A deleted dataset is invisible to list, remember and recall. Its name can be reused: creating (or remembering into) that name makes a new dataset with a new id and an empty graph. The old rows stay in the database under the old id.

## Endpoints

`POST /v1/gb/datasets`: `{"name": "work", "tags": ["project"], "description": "optional"}` returns `201` with the dataset. `409` if a live dataset with that name exists, `400` for a bad name.

`GET /v1/gb/datasets`: `{"datasets": [{"id": 1, "name": "main", "tags": [], "created_at": "...", "deleted": false}, ...]}`, oldest first. `?include_deleted=true` also lists soft-deleted ones.

`DELETE /v1/gb/datasets/{name}`: soft-deletes the dataset. `204`, or `404` if there is no live dataset with that name.
