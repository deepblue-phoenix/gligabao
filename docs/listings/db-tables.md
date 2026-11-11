# Database tables

> **Note:** keep this file up to date. Whenever a table is added, renamed or removed (in any `schema*.sql` file), update the list and its description here.

## Tables

- `gb_datasets`
- `gb_ontologies`
- `gb_models`
- `gb_inferences`
- `gb_remember_runs`
- `gb_remember_run_stages`
- `gb_remember_run_stage_outputs`
- `gb_query_runs`
- `gb_artifacts`
- `gb_entities`
- `gb_edges`
- `gb_edge_artifacts`
- `gb_entity_artifacts`
- `gb_entity_sessions`
- `gb_edge_sessions`
- `gb_edge_events`
- `gb_artifact_classes`
- `gb_graph_in_ctx_counters`
- `gb_image_artifacts`
- `gb_image_extractions`
- `gb_image_text_blocks`
- `gb_entity_geo`
- `gb_series`
- `gb_series_points`
- `gb_series_anchors`
- `gb_onto_classes`
- `gb_onto_edges`
- `gb_training_job_runs`
- `gb_training_job_events`
- `gb_trained_models`
- `gb_trained_model_inferences`
- `gb_trained_model_deployments`
- `gb_training_datasets`

## What each table is about

| Table                         | Description                                                                                                                                     |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `gb_datasets`                 | hard logical containers (owned by one user) that every artifact, entity and edge belongs to; deletion is soft.                                  |
| `gb_ontologies`               | metadata of a loaded ontology (name, IRI, format, digest, counts); its classes and edges are in `gb_onto_classes` and `gb_onto_edges`.          |
| `gb_models`                   | the static list of AI models gligabao knows, referenced by entities, edges and inferences.                                                      |
| `gb_inferences`               | one row per model invocation (LLM call, embedding, web search, Jev classification) with tokens, cost and reasoning level.                       |
| `gb_remember_runs`            | one row per remember call, tracking its user, dataset, parameters, status, error and times.                                                    |
| `gb_remember_run_stages`      | one row per stage of a remember run (`stage_type`): its status, error, times and the IDs of its inferences.                                    |
| `gb_remember_run_stage_outputs` | what each stage of a remember run produced (extracted entities and facts, candidates, decisions, raw LLM answers), one row per output, kept apart from the run log for debugging. |
| `gb_query_runs`               | one row per query call, tracking its question, session, status and the status, times, errors and inferences of each pipeline stage. |
| `gb_artifacts`                | ingested pieces of data (notes, episodes, chunks, web search results, series observations, chunk summaries), the provenance of the graph.       |
| `gb_entities`                 | graph nodes extracted from artifacts, deduplicated per dataset by name and type.                                                                |
| `gb_edges`                    | bi-temporal facts relating two entities, with event time (valid/invalid) and system time (created/expired).                                     |
| `gb_edge_artifacts`           | many-to-many link of which artifacts an edge (fact) was inferred from.                                                                          |
| `gb_entity_artifacts`         | many-to-many link of which artifacts an entity was inferred from.                                                                               |
| `gb_entity_sessions`          | many-to-many link of which sessions an entity was discovered in.                                                                                |
| `gb_edge_sessions`            | many-to-many link of which sessions an edge was discovered in.                                                                                  |
| `gb_edge_events`              | append-only log of what each remember call did to the temporal state of a fact (created, confirmed, ended, invalidated, superseded), with its `invalid_at` before and after. |
| `gb_artifact_classes`         | the classes an LLM gave to the graph of an artifact (`classify_graph` stage), one row per class, with the inference that gave it. |
| `gb_graph_in_ctx_counters`    | the in-context log: one row every time an entity or edge went into the context of an LLM call (or was returned by a retrieval-only query), by pipeline, stage and run. |
| `gb_image_artifacts`          | ingested images (media type, SHA-256, size, file path), kept apart from the graph; the bytes live on the file system.                           |
| `gb_image_extractions`        | what an LLM read from an image (theme, title, text), with the raw output, tokens and cost.                                                      |
| `gb_image_text_blocks`        | the text regions of an image extraction in reading order, each with a role (title, caption, label, ...).                                        |
| `gb_entity_geo`               | geo metadata of an entity that is a place (coordinates, kind, display name, country, bounding box, provider), one row per entity.                |
| `gb_series`                   | timeseries identified by (provider, key, field) in a dataset, with a summary and stats of all their points.                                     |
| `gb_series_points`            | the timestamped values of a series.                                                                                                             |
| `gb_series_anchors`           | many-to-many link anchoring a series (optionally a window of it) to an artifact, entity or edge.                                                |
| `gb_onto_classes`             | the classes (nodes) of a loaded ontology.                                                                                                       |
| `gb_onto_edges`               | the relationships between ontology classes (subclass, equivalence, disjointness, properties).                                                   |
| `gb_training_job_runs`        | training jobs started on the sidecar, with status, settings, loss and timings.                                                                  |
| `gb_training_job_events`      | the status and progress events (with loss) the sidecar reported for a training job, in order.                                                   |
| `gb_trained_models`           | one row per completed training job, the saved weights (LoRA adapter) that inference, evals and audits run on.                                   |
| `gb_trained_model_inferences` | one row per prompt run through a trained model, with response, status, tokens and cold-start timing.                                            |
| `gb_trained_model_deployments` | one row per deployment of a trained model through a deployer hook, with status, local directory, the deployer's ref and error.                     |
| `gb_training_datasets`        | one row per JSONL training dataset the server has seen (file name, size, record count, digest, sync status with the sidecar).                   |

## Where each table is defined

| Table                         | SQLite                                             | Postgres                                             |
| ----------------------------- | -------------------------------------------------- | ---------------------------------------------------- |
| `gb_datasets`                 | `internal/db/relational/sqlite/schema.sql`       | `internal/db/relational/postgres/schema.sql`       |
| `gb_ontologies`               | `internal/db/relational/sqlite/schema.sql`       | `internal/db/relational/postgres/schema.sql`       |
| `gb_models`                   | `internal/db/relational/sqlite/schema.sql`       | `internal/db/relational/postgres/schema.sql`       |
| `gb_inferences`               | `internal/db/relational/sqlite/schema.sql`       | `internal/db/relational/postgres/schema.sql`       |
| `gb_remember_runs`            | `internal/db/relational/sqlite/schema_remember_runs.sql` | `internal/db/relational/postgres/schema_remember_runs.sql` |
| `gb_remember_run_stages`      | `internal/db/relational/sqlite/schema_remember_runs.sql` | `internal/db/relational/postgres/schema_remember_runs.sql` |
| `gb_remember_run_stage_outputs` | `internal/db/relational/sqlite/schema_remember_runs.sql` | `internal/db/relational/postgres/schema_remember_runs.sql` |
| `gb_query_runs`               | `internal/db/relational/sqlite/schema_query_runs.sql`    | `internal/db/relational/postgres/schema_query_runs.sql`    |
| `gb_artifacts`                | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_entities`                 | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_edges`                    | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_edge_artifacts`           | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_entity_artifacts`         | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_entity_sessions`          | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_edge_sessions`            | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_edge_events`              | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_artifact_classes`         | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_graph_in_ctx_counters`    | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_image_artifacts`          | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_image_extractions`        | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_image_text_blocks`        | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_entity_geo`               | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_series`                   | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_series_points`            | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_series_anchors`           | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_onto_classes`             | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_onto_edges`               | `internal/db/graph/sqlite/schema.sql`            | `internal/db/graph/postgres/schema.sql`            |
| `gb_training_job_runs`        | `internal/training/db/sqlite/schema.sql`         | `internal/training/db/postgres/schema.sql`         |
| `gb_training_job_events`      | `internal/training/db/sqlite/schema.sql`         | `internal/training/db/postgres/schema.sql`         |
| `gb_trained_models`           | `internal/training/db/sqlite/schema.sql`         | `internal/training/db/postgres/schema.sql`         |
| `gb_trained_model_inferences` | `internal/training/db/sqlite/schema.sql`         | `internal/training/db/postgres/schema.sql`         |
| `gb_trained_model_deployments` | `internal/training/db/sqlite/schema.sql`         | `internal/training/db/postgres/schema.sql`         |
| `gb_training_datasets`        | `internal/training/db/sqlite/schema.sql`         | `internal/training/db/postgres/schema.sql`         |

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
| `gb_training_audit_runs`  | `internal/training_audits/db/sqlite/schema.sql`     | `internal/training_audits/db/postgres/schema.sql`     |
| `gb_training_audit_case_runs` | `internal/training_audits/db/sqlite/schema.sql`     | `internal/training_audits/db/postgres/schema.sql`     |

## EVALS

Tables of the evals (`internal/evals`, see its `README.md`; applied by `dbfull.Open` (`internal/db/dbfull`)).

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
- `gb_case_run_commentaries`
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
| `gb_eval_runs_meta_knowledge`             | the statistics of a knowledge eval run as a whole: chunks, steps, remember and query calls and cost, checks held and failed.     |
| `gb_eval_runs_meta_training`              | the statistics of a training eval run: the trained model, the judge, the split of the scores, the judge's calls, tokens and cost. |
| `gb_case_runs`                            | one row per execution of a case in an eval run: status (lifecycle), verdict (pass or fail), score, cost and times.                |
| `gb_case_runs_meta_knowledge`             | what a knowledge case run was given, its answer, the checks that held and failed, and the dataset its graph is in.               |
| `gb_case_runs_meta_knowledge_trajectories` | the ATIF trajectories of a knowledge case run (remember calls, then the query), by reference.                                    |
| `gb_case_run_commentaries`                | the LLM commentaries on a failed case run, written on request from the admin UI (several if asked again).                        |
| `gb_case_runs_meta_training`              | the question, the expected answer, the model's answer and the judge's score and reason of a training case run.                    |

### Where each table is defined

All of them are in `internal/evals/db/sqlite/schema.sql` (SQLite) and `internal/evals/db/postgres/schema.sql` (Postgres).

## AGENT_RESEARCH

Tables of the research agents (`internal/agent_research`, see `docs/agents/research.md`; applied by `dbfull.Open` (`internal/db/dbfull`)).

- `gb_agent_research_runs`
- `gb_agent_research_states`
- `gb_agent_frontier_questions`
- `gb_agent_frontier_deps`
- `gb_agent_frontier_attempts`
- `gb_agent_frontier_transitions`
- `gb_agent_sources`
- `gb_agent_claims`
- `gb_agent_claim_sources`
- `gb_agent_challenges`
- `gb_agent_challenge_sources`
- `gb_agent_notes_versions`

### What each table is about

| Table                          | Description                                                                                                                                      |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `gb_agent_research_runs`   | one row per research agent started on a topic, with its status, the state it is in, steps, tokens and cost so far, and the report or the error. |
| `gb_agent_research_states` | one row per state the agent went through while working (starting, thinking, searching, reading, ...), append-only, with its detail, error and times. |
| `gb_agent_frontier_questions` | one row per question of a run's deep-research frontier (a node of the question graph), with its parent, depth, priority, current status and answer. |
| `gb_agent_frontier_deps` | the edges "question depends on question" of the frontier (a DAG). |
| `gb_agent_frontier_attempts` | append-only log of the queries tried on a question (drives the dead-end rule and the "already attempted" list). |
| `gb_agent_frontier_transitions` | append-only: one row per status (or priority) change of a question, with trigger, actor, reason and the research state it happened in. |
| `gb_agent_sources` | the sources (one row per distinct URL, with its registrable domain) read in a run. |
| `gb_agent_claims` | the claims and numeric datapoints of a run with their verification status (`UNVERIFIED`, `VERIFIED`, `REFUTED`, `RETRACTED`). |
| `gb_agent_claim_sources` | links between claims and sources, with the stance (`supports` or `contradicts`). |
| `gb_agent_challenges` | one row per adversarial query (strategy, negation, query, outcome, evidence) with which the agent attacks its own claims, answers and conclusion. |
| `gb_agent_challenge_sources` | the sources that backed the verdict of a challenge. |
| `gb_agent_notes_versions` | append-only: the whole `notes.md` of a run after each write, for debugging and export. |

### Where each table is defined

| Table                          | SQLite                                          | Postgres                                          |
| ------------------------------ | ----------------------------------------------- | ------------------------------------------------- |
| `gb_agent_research_runs`   | `internal/agent_research/db/sqlite/schema.sql`   | `internal/agent_research/db/postgres/schema.sql`   |
| `gb_agent_research_states` | `internal/agent_research/db/sqlite/schema.sql`   | `internal/agent_research/db/postgres/schema.sql`   |
| `gb_agent_frontier_questions` | `internal/agent_research/db/sqlite/schema.sql` | `internal/agent_research/db/postgres/schema.sql` |
| `gb_agent_frontier_deps` | `internal/agent_research/db/sqlite/schema.sql` | `internal/agent_research/db/postgres/schema.sql` |
| `gb_agent_frontier_attempts` | `internal/agent_research/db/sqlite/schema.sql` | `internal/agent_research/db/postgres/schema.sql` |
| `gb_agent_frontier_transitions` | `internal/agent_research/db/sqlite/schema.sql` | `internal/agent_research/db/postgres/schema.sql` |
| `gb_agent_sources` | `internal/agent_research/db/sqlite/schema.sql` | `internal/agent_research/db/postgres/schema.sql` |
| `gb_agent_claims` | `internal/agent_research/db/sqlite/schema.sql` | `internal/agent_research/db/postgres/schema.sql` |
| `gb_agent_claim_sources` | `internal/agent_research/db/sqlite/schema.sql` | `internal/agent_research/db/postgres/schema.sql` |
| `gb_agent_challenges` | `internal/agent_research/db/sqlite/schema.sql` | `internal/agent_research/db/postgres/schema.sql` |
| `gb_agent_challenge_sources` | `internal/agent_research/db/sqlite/schema.sql` | `internal/agent_research/db/postgres/schema.sql` |
| `gb_agent_notes_versions` | `internal/agent_research/db/sqlite/schema.sql` | `internal/agent_research/db/postgres/schema.sql` |
