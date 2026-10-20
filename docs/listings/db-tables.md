# Database tables

> **Note:** keep this file up to date. Whenever a table is added, renamed or removed (in any `db_schema.go` or similar), update the list and its description here.

## Tables

- `gb_datasets`
- `gb_ontologies`
- `gb_models`
- `gb_inferences`
- `gb_remember_runs`
- `gb_artifacts`
- `gb_entities`
- `gb_edges`
- `gb_edge_artifacts`
- `gb_entity_artifacts`
- `gb_entity_sessions`
- `gb_edge_sessions`
- `gb_image_artifacts`
- `gb_image_extractions`
- `gb_image_text_blocks`
- `gb_series`
- `gb_series_points`
- `gb_series_anchors`
- `gb_onto_classes`
- `gb_onto_edges`
- `gb_training_job_runs`
- `gb_training_job_events`
- `gb_trained_models`
- `gb_trained_model_inferences`
- `gb_training_datasets`

## What each table is about

| Table                         | Description                                                                                                                                     |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `gb_datasets`                 | hard logical containers (owned by one user) that every artifact, entity and edge belongs to; deletion is soft.                                  |
| `gb_ontologies`               | metadata of a loaded ontology (name, IRI, format, digest, counts); its classes and edges are in `gb_onto_classes` and `gb_onto_edges`.          |
| `gb_models`                   | the static list of AI models gligabao knows, referenced by entities, edges and inferences.                                                      |
| `gb_inferences`               | one row per model invocation (LLM call, embedding, web search, Jev classification) with tokens, cost and reasoning level.                       |
| `gb_remember_runs`            | one row per remember call, tracking its status and the status, times and errors of each pipeline stage.                                         |
| `gb_artifacts`                | ingested pieces of data (notes, episodes, chunks, web search results, series observations, chunk summaries), the provenance of the graph.       |
| `gb_entities`                 | graph nodes extracted from artifacts, deduplicated per dataset by name and type.                                                                |
| `gb_edges`                    | bi-temporal facts relating two entities, with event time (valid/invalid) and system time (created/expired).                                     |
| `gb_edge_artifacts`           | many-to-many link of which artifacts an edge (fact) was inferred from.                                                                          |
| `gb_entity_artifacts`         | many-to-many link of which artifacts an entity was inferred from.                                                                               |
| `gb_entity_sessions`          | many-to-many link of which sessions an entity was discovered in.                                                                                |
| `gb_edge_sessions`            | many-to-many link of which sessions an edge was discovered in.                                                                                  |
| `gb_image_artifacts`          | ingested images (media type, SHA-256, size, file path), kept apart from the graph; the bytes live on the file system.                           |
| `gb_image_extractions`        | what an LLM read from an image (theme, title, text), with the raw output, tokens and cost.                                                      |
| `gb_image_text_blocks`        | the text regions of an image extraction in reading order, each with a role (title, caption, label, ...).                                        |
| `gb_series`                   | timeseries identified by (provider, key, field) in a dataset, with a summary and stats of all their points.                                     |
| `gb_series_points`            | the timestamped values of a series.                                                                                                             |
| `gb_series_anchors`           | many-to-many link anchoring a series (optionally a window of it) to an artifact, entity or edge.                                                |
| `gb_onto_classes`             | the classes (nodes) of a loaded ontology.                                                                                                       |
| `gb_onto_edges`               | the relationships between ontology classes (subclass, equivalence, disjointness, properties).                                                   |
| `gb_training_job_runs`        | training jobs started on the sidecar, with status, settings, loss and timings.                                                                  |
| `gb_training_job_events`      | the status and progress events (with loss) the sidecar reported for a training job, in order.                                                   |
| `gb_trained_models`           | one row per completed training job, the saved weights (LoRA adapter) that inference, evals and audits run on.                                   |
| `gb_trained_model_inferences` | one row per prompt run through a trained model, with response, status, tokens and cold-start timing.                                            |
| `gb_training_datasets`        | one row per JSONL training dataset the server has seen (file name, size, record count, digest, sync status with the sidecar).                   |

## Where each table is defined

| Table                         | SQLite                                             | Postgres                                             |
| ----------------------------- | -------------------------------------------------- | ---------------------------------------------------- |
| `gb_datasets`                 | `internal/db/relational/sqlite/db_schema.go`       | `internal/db/relational/postgres/db_schema.go`       |
| `gb_ontologies`               | `internal/db/relational/sqlite/db_schema.go`       | `internal/db/relational/postgres/db_schema.go`       |
| `gb_models`                   | `internal/db/relational/sqlite/db_schema.go`       | `internal/db/relational/postgres/db_schema.go`       |
| `gb_inferences`               | `internal/db/relational/sqlite/db_schema.go`       | `internal/db/relational/postgres/db_schema.go`       |
| `gb_remember_runs`            | `internal/db/relational/sqlite/db_remember_run.go` | `internal/db/relational/postgres/db_remember_run.go` |
| `gb_artifacts`                | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_entities`                 | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_edges`                    | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_edge_artifacts`           | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_entity_artifacts`         | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_entity_sessions`          | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_edge_sessions`            | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_image_artifacts`          | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_image_extractions`        | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_image_text_blocks`        | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_series`                   | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_series_points`            | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_series_anchors`           | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_onto_classes`             | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_onto_edges`               | `internal/db/graph/sqlite/db_schema.go`            | `internal/db/graph/postgres/db_schema.go`            |
| `gb_training_job_runs`        | `internal/training/db/sqlite/db_schema.go`         | `internal/training/db/postgres/db_schema.go`         |
| `gb_training_job_events`      | `internal/training/db/sqlite/db_schema.go`         | `internal/training/db/postgres/db_schema.go`         |
| `gb_trained_models`           | `internal/training/db/sqlite/db_schema.go`         | `internal/training/db/postgres/db_schema.go`         |
| `gb_trained_model_inferences` | `internal/training/db/sqlite/db_schema.go`         | `internal/training/db/postgres/db_schema.go`         |
| `gb_training_datasets`        | `internal/training/db/sqlite/db_schema.go`         | `internal/training/db/postgres/db_schema.go`         |

## TRAINING_AUDIT

Tables of the training data audits (`internal/training_audits`, see `docs/training/audit.md`).

- `gb_training_audit_runs`
- `gb_training_audit_case_runs`

### What each table is about

| Table                         | Description                                                                                                                                     |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `gb_training_audit_runs`  | one row per training data audit, a trained model run against its own fine-tuning dataset (not an eval), with counts, judge statistics and cost. |
| `gb_training_audit_case_runs` | one row per example of an audit, with the question, dataset answer, model response and judge score and reason.                                  |

### Where each table is defined

| Table                         | SQLite                                             | Postgres                                             |
| ----------------------------- | -------------------------------------------------- | ---------------------------------------------------- |
| `gb_training_audit_runs`  | `internal/training_audits/db/sqlite/schema.go`     | `internal/training_audits/db/postgres/schema.go`     |
| `gb_training_audit_case_runs` | `internal/training_audits/db/sqlite/schema.go`     | `internal/training_audits/db/postgres/schema.go`     |

## EVALS

Tables of the evals (`internal/evals`, see its `README.md`; applied by `internal/db.Open`).

- `gb_eval_defs`
- `gb_case_defs`
- `gb_case_def_meta_knowledge`
- `gb_case_def_meta_training`
- `gb_experiment_defs`
- `gb_experiment_def_evals`
- `gb_experiment_runs`
- `gb_eval_runs`
- `gb_eval_runs_meta_knowledge`
- `gb_eval_runs_meta_training`
- `gb_case_runs`
- `gb_case_runs_meta_knowledge`
- `gb_case_runs_meta_knowledge_trajectories`
- `gb_case_runs_meta_training`

### What each table is about

| Table                                         | Description                                                                                                                       |
| --------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `gb_eval_defs`                            | one row per eval definition: a named set of cases of one type (`knowledge`, `training`).                                          |
| `gb_case_defs`                            | one row per case definition of an eval: its name and position; the type-specific part is in the `gb_case_def_meta_*` table.   |
| `gb_case_def_meta_knowledge`              | the texts to remember, the question, the checks and the settings of a knowledge case.                                             |
| `gb_case_def_meta_training`               | the question and the expected answer of a training case.                                                                          |
| `gb_experiment_defs`                      | one row per experiment definition: a named group of evals run together, with the settings under test.                             |
| `gb_experiment_def_evals`                 | the evals of an experiment, in the order they run.                                                                                |
| `gb_experiment_runs`                      | one row per execution of an experiment: status, counts and cost over the case runs of its eval runs.                              |
| `gb_eval_runs`                            | one row per execution of an eval (on its own or in an experiment run): status, params, counts, cost and times.                    |
| `gb_eval_runs_meta_knowledge`             | the statistics of a knowledge eval run as a whole: chunks, steps, remember and recall calls and cost, checks held and failed.     |
| `gb_eval_runs_meta_training`              | the statistics of a training eval run: the trained model, the judge, the split of the scores, the judge's calls, tokens and cost. |
| `gb_case_runs`                            | one row per execution of a case in an eval run: status (lifecycle), verdict (pass or fail), score, cost and times.                |
| `gb_case_runs_meta_knowledge`             | what a knowledge case run was given, its answer, the checks that held and failed, and the dataset its graph is in.               |
| `gb_case_runs_meta_knowledge_trajectories` | the ATIF trajectories of a knowledge case run (remember calls, then the recall), by reference.                                    |
| `gb_case_runs_meta_training`              | the question, the expected answer, the model's answer and the judge's score and reason of a training case run.                    |

### Where each table is defined

All of them are in `internal/evals/db/sqlite/schema.go` (SQLite) and `internal/evals/db/postgres/schema.go` (Postgres).
