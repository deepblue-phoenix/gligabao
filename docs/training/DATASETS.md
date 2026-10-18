# Training datasets: the training datasets directory and its manifests

A training dataset is a JSONL file (format: [TRAINING_DATA_FORMAT.md](TRAINING_DATA_FORMAT.md)) that lives in a subdirectory of the **training datasets directory** and is described by a **manifest**, a `dataset.toml` in that subdirectory. gligabao scans the directory for manifests, reads each one to learn the dataset's name and where its data file is, and offers the datasets for training. A training dataset is separate from a memory [dataset](../datasets.md) (a container of artifacts, entities and edges in the database).

## Layout

```
bin/datasets_training/                         the training datasets directory (serve --training-dataset-dir; default: "datasets_training" next to --db)
  world-knowledge-base/               one subdirectory per dataset: anything the dataset needs can live here
    dataset.toml                      the manifest: its presence makes the subdirectory a dataset
    generate_dataset.py               the script that generates the data (gligabao ignores it)
    world_knowledge_base_v1.jsonl     the data file the manifest names
    world_knowledge_base_triples.json raw sources (ignored)
  my-other-dataset/
    dataset.toml
    data/rows.jsonl
```

- The generator scripts and their output belong to the dataset's own subdirectory, so a dataset is one self-contained directory.
- **Discovery:** the server looks at the direct subdirectories of the training datasets directory and reads the `dataset.toml` of each one (a subdirectory without one is not a dataset; names starting with `.` are skipped). It does this every time it lists the datasets or starts a job, so a dataset added to the directory (or a regenerated data file) is available at once, with no restart. A directory that does not exist yet has no datasets.
- **The manifest decides where the data is:** gligabao does not look for `*.jsonl` files; it reads the path in the manifest's `file`. Other files in the subdirectory (a second `.jsonl`, a `.json`) are not part of the dataset. For now only the JSONL format is supported.
- **In the repository:** `bin/datasets_training/` is tracked (the rest of `bin/` is ignored); the generated data files of a dataset are ignored by a `.gitignore` in its subdirectory (run the generator to create them).

## The manifest (`dataset.toml`)

```toml
name = "world-knowledge-base"
description = "Memorization of a hand-curated knowledge base ..."
format = "jsonl"
file = "world_knowledge_base_v1.jsonl"
version = "1"
generator = "generate_dataset.py"
```

| Key | | Meaning |
|---|---|---|
| `file` | required | The data file, a relative path inside the dataset's subdirectory (no `..`, not absolute), ending in `.jsonl`. |
| `name` | optional | The dataset's name, what a training job asks for. 1-122 characters of letters, digits, `.`, `_`, `-`, starting with a letter or digit, without `..`. Default: the subdirectory's name. Names are unique: a second dataset with a name already taken is a problem. |
| `description` | optional | Free text, shown in the admin UI (tooltip of the dataset's row). |
| `format` | optional | The data format; only `jsonl` (the default). |
| `version` | optional | Informational. |
| `generator` | optional | Informational: the script in the subdirectory that creates `file`. It is named in the message when `file` is missing. |

The data file is valid if it is UTF-8 and every non-blank line is a JSON object with string `instruction` and `output` (and, if present, a string `input`), and it has at least one line and is at most 64 MB.

**Problems:** a subdirectory with a manifest that cannot be used (a TOML error, no `file`, a bad `name`, an unsupported `format`, a `file` outside the subdirectory, a missing or invalid data file, a name already taken) is not a dataset. It is skipped, and listed with its message in the `problems` of `GET /v1/gb/training/datasets` (the admin UI shows them under the datasets table). Asking for it in a job is a `400` like for any unknown dataset.

## Names

A dataset has a **name** (the manifest's `name`) and a **file name on the sidecar**, `<name>.jsonl`: the file the sidecar stores on its data volume, the `filename` of the record, and the `dataset` the sidecar reports for a job. A training job asks for the dataset by its name.

## The server (Go)

| Endpoint | Behaviour |
|---|---|
| `GET /v1/gb/training/datasets[?db=]` | `{"datasets": [...], "problems": [...]}`. `datasets` are the usable datasets by name: the record (below) plus the manifest's `name`, `description`, `format`, `file`, `version`, `generator`, and `dir`, the subdirectory's name. It never asks the sidecar. |
| `GET /v1/gb/training/datasets/preview?name=<name>[&lines=][&db=]` | The first lines (default 100, at most 1000) of the dataset's data file: `{"name", "lines", "num_records", "truncated"}`. `404` for an unknown dataset. |
| `POST /v1/gb/training/datasets[?db=]` | Uploads a JSONL file (multipart form, field `file`, optional `filename`). It must be valid (as above) and have a plain `*.jsonl` name; the server saves it as the dataset named like the file without `.jsonl`: in the subdirectory of that name, as `<name>.jsonl`, next to a manifest it writes if there is none. Uploading the name again replaces the file. `201` with the dataset, `400` invalid file, `409` if a dataset of that name exists with a manifest of its own naming another data file (or the name belongs to a dataset in another subdirectory), `413` over 64 MB. |
| `POST /v1/gb/training/jobs[?db=]` | The job's `dataset` is a dataset's name. It is required: a missing or unknown name is a `400`. |

Only the main database has a training datasets directory; other databases (the eval one) list no datasets.

### The `gb_training_datasets` record

One row per dataset (Go: `relational.TrainingDataset`). The directory is the source of truth; the row is made when the server first sees the data file, and replaced (status `stored` again) when it sees the file changed:

| Field | Meaning |
|---|---|
| `id` | The record. |
| `filename` | `<name>.jsonl`, unique. |
| `size_bytes`, `num_records`, `sha256` | Size, number of non-blank lines, and hex SHA-256 of the data file. |
| `status` | `stored` (only on the Go server) or `synced` (a job has taken this version of the file to the sidecar). |
| `created_at`, `updated_at` | When the row was made; when it last described a new version of the file. |
| `synced_at` | When the sidecar last took the current file; null while it has not. |

Rows of datasets that are no longer in the directory stay in the table and are not listed.

## How a dataset reaches the sidecar

Only through a job start. `POST /v1/gb/training/jobs` finds the dataset by name (reading the manifests), reads its data file, validates it, brings the record up to date and sends the file to the sidecar in `POST /training/job_start` as `dataset_content` (base64) next to `dataset` (`<name>.jsonl`). The sidecar stores it on its data volume as `/data/<name>.jsonl` before it spawns the job. When the sidecar has taken the job the record becomes `synced`. The file is sent with every job that uses the dataset. The sidecar has no default dataset and no dataset endpoints.

## The Modal CLI

`modal run app.py` (in `sidecar/ai_train`) reads the same manifests, from `bin/datasets_training` (override: `GLIGABAO_DATASETS_TRAINING_DIR`), in `training_data.py` (`tomllib`, Python 3.11+):

```sh
modal run app.py --action datasets                          # list the local datasets and their problems
modal run app.py --action sync --dataset <name>             # upload the dataset to the data volume, as <name>.jsonl, if it changed
modal run app.py --action validate --dataset <name>         # check the file on the data volume
modal run app.py --action train --dataset <name> --train-steps 100 --model-size 4b   # syncs, then trains
```

## Adding a dataset

1. Make `bin/datasets_training/<dir>/`, put (or generate) the JSONL file there.
2. Write `bin/datasets_training/<dir>/dataset.toml` with at least `file`.
3. It shows up in `GET /v1/gb/training/datasets` and in the admin UI's training view; pick it there or pass its name as `dataset` to `POST /v1/gb/training/jobs`.
