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
- `gb_experiments_defs`
- `gb_experiment_cases_defs`
- `gb_experiment_runs`
- `gb_experiment_case_results`
- `gb_experiment_trajectories`
- `gb_knowledge_case_meta`
- `gb_training_eval_run_meta`
- `gb_training_case_meta`
- `gb_training_audit_runs`
- `gb_training_audit_cases`
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
| `gb_experiments_defs`         | a named series of eval runs (knowledge or training) with free-form config.                                                                      |
| `gb_experiment_cases_defs`    | the cases (eval + case name) an experiment has executed.                                                                                        |
| `gb_experiment_runs`          | one execution of an experiment over one or more cases, with status, params and counts.                                                          |
| `gb_experiment_case_results`  | the result of one case in one run, for both knowledge and training evals.                                                                       |
| `gb_experiment_trajectories`  | references to the ATIF trajectory files of a case's remember and recall calls.                                                                  |
| `gb_knowledge_case_meta`      | knowledge-eval specific data of a case result (answer, failed and passed checks, dataset, steps).                                               |
| `gb_training_eval_run_meta`   | training-eval specific data of a run (trained model, eval name, judge model, progress counters and judge usage).                                |
| `gb_training_case_meta`       | training-eval specific data of a case result (question, expected answer, model response, judge score and reason).                               |
| `gb_training_audit_runs`      | one row per training data audit, a trained model run against its own fine-tuning dataset (not an eval), with progress counters and judge usage. |
| `gb_training_audit_cases`     | one row per example of an audit, with the question, dataset answer, model response and judge score.                                             |
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
| `gb_experiments_defs`         | `internal/evals/db/sqlite/db_schema.go`            | `internal/evals/db/postgres/db_schema.go`            |
| `gb_experiment_cases_defs`    | `internal/evals/db/sqlite/db_schema.go`            | `internal/evals/db/postgres/db_schema.go`            |
| `gb_experiment_runs`          | `internal/evals/db/sqlite/db_schema.go`            | `internal/evals/db/postgres/db_schema.go`            |
| `gb_experiment_case_results`  | `internal/evals/db/sqlite/db_schema.go`            | `internal/evals/db/postgres/db_schema.go`            |
| `gb_experiment_trajectories`  | `internal/evals/db/sqlite/db_schema.go`            | `internal/evals/db/postgres/db_schema.go`            |
| `gb_knowledge_case_meta`      | `internal/evals/db/sqlite/db_schema.go`            | `internal/evals/db/postgres/db_schema.go`            |
| `gb_training_eval_run_meta`   | `internal/evals/db/sqlite/db_schema.go`            | `internal/evals/db/postgres/db_schema.go`            |
| `gb_training_case_meta`       | `internal/evals/db/sqlite/db_schema.go`            | `internal/evals/db/postgres/db_schema.go`            |
| `gb_training_audit_runs`      | `internal/evals/db/sqlite/db_schema.go`            | `internal/evals/db/postgres/db_schema.go`            |
| `gb_training_audit_cases`     | `internal/evals/db/sqlite/db_schema.go`            | `internal/evals/db/postgres/db_schema.go`            |
| `gb_training_job_runs`        | `internal/training/db/sqlite/db_schema.go`         | `internal/training/db/postgres/db_schema.go`         |
| `gb_training_job_events`      | `internal/training/db/sqlite/db_schema.go`         | `internal/training/db/postgres/db_schema.go`         |
| `gb_trained_models`           | `internal/training/db/sqlite/db_schema.go`         | `internal/training/db/postgres/db_schema.go`         |
| `gb_trained_model_inferences` | `internal/training/db/sqlite/db_schema.go`         | `internal/training/db/postgres/db_schema.go`         |
| `gb_training_datasets`        | `internal/training/db/sqlite/db_schema.go`         | `internal/training/db/postgres/db_schema.go`         |
