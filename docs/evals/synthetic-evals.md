# Synthetic evals

A **synthetic eval** is a knowledge eval whose cases an LLM writes, from a **definition** the user gives: a prompt that describes the eval dataset, optionally a prompt that describes the **data corpus** the cases run on (the texts they remember), the model, how many cases, and whether to ask for one case per call or several. The definition is **versioned**, the cases can be **generated again** from it, and each case can be **edited** by the user.

A synthetic eval is otherwise an ordinary [user eval](user-evals.md): its cases are in the user's eval file (`evals/<user>/<name>.json`), its def and case defs are in `gb_eval_defs` / `gb_case_defs` with the type `knowledge`, and it runs, is listed, and is deleted like any other (`POST /v1/gb/evals/{name}/runs`, `DELETE /v1/gb/evals/{name}`, ...). It differs by its **origin**: `human` (written by the user) or `synthetic`. The origin is not a `core.Type`: the type is that of the cases (which runner executes them), the origin says who wrote them. An eval def is synthetic when it has a row in `gb_synth_defs`.

The code is `internal/evals/synth` (prompts, parsing, the generator, the `Manager`), `internal/evals/db/reads_synth.go` (the tables), `internal/api/evals/synth.go` (the routes), the types in `internal/evals/core/synth.go`.

## The definition

| field | meaning |
|---|---|
| `name`, `description` | the name of the eval (as every eval's, see [user-evals.md](user-evals.md#the-files)) and a short description |
| `prompt` | what the eval dataset is about: what the cases test (required, <= 8 KiB) |
| `corpus_prompt` | what the synthetic data corpus looks like: the texts of the `remember` items the cases run on (optional, <= 8 KiB; empty leaves it to the model) |
| `model` | the LLM model that writes the cases (from the supported models) |
| `num_examples` | the number of cases, 1 to `evals_max_cases` (100) |
| `mode` | `single` (default): one case per LLM call; `bulk`: `bulk_size` cases per call |
| `bulk_size` | cases per call in the `bulk` mode (1 to 50, default 10) |

**Version.** The definition has a `version`, from 1. Every change of the settings (any of the fields above except the name and the description) adds 1; saving the same settings again does not. Every version is kept (`gb_synth_def_versions`), and each generation records the version it used. A change of the definition does not touch the cases; they are written again by a generation.

## Generating

The system prompt gives the LLM the **shape of the expected case** (JSON: `remember` items with `text` and `reference_time`, `query`, `expect_contains`, optional `expect_edges`), the rules a case follows, the dataset prompt and the corpus prompt, and asks for JSON only. The server names the cases `case-001`, `case-002`, ... (the model does not). Every generated case is validated like a case of a user eval (the same rules and limits); a case that is not valid, or an answer that is not JSON, is dropped and asked for again, up to 3 calls more than the minimum. If the model delivers fewer than asked, the cases that were written are saved and the generation ends `errored` (`generated 7 of 10 cases`).

- **One case per call** (`mode: single`). The calls are made one after the other. Every call is given the queries of the cases so far, to avoid repeats.
- **Bulk** (`mode: bulk`). Each call asks for `bulk_size` cases as a JSON array (the last call for the rest). Faster and cheaper, with less consistency between the cases.

A generation runs **in the background** (`gb_synth_generations`: `pending`, `running`, `completed` or `errored`, with `requested`, `generated`, `kept`, `calls`, tokens, `cost_usd`; the detail of the eval is how it is followed). At most one generation of an eval is active (`409` otherwise). Its progress is stored after every call; one that makes no progress for 10 minutes (its server died) is ended `errored` by the sweep at startup and when the detail is read. When the cases are ready the file is written and the def synced as `PUT /v1/gb/evals/{name}` does (so the eval has no file, and no cases, until its first generation ends; a failed first generation leaves it that way, and it can be generated again or deleted).

## Editing and regenerating

The user edits one case with `PUT .../cases/{name}/{case}`; the whole eval file may also be replaced with `PUT /v1/gb/evals/{name}`. Where each case comes from is tracked in `gb_synth_cases` (the generation and version that wrote it, and the hash of its content as generated), so a case is, by comparing the hash with its content now:

- `generated`: as the model wrote it;
- `edited`: generated, then changed by the user;
- `user`: added by the user (no row).

A regeneration (`POST .../generate/{name}`, or `regenerate` on a definition change) with `keep_edited` (the default) **keeps the `edited` and `user` cases** and generates only as many new ones as are missing to reach `num_examples` (none if the user's cases are already that many); the new ones take the free names, so the cases that were not edited are rewritten in place (the case defs and their run history stay). With `"keep_edited": false` every case is replaced. Case defs of names that no longer are in the file stay in the database (as when a case is removed from any eval), and are listed after the others.

## API

All routes are under `/v1/gb/evals` and use the same `gb-user-id` rule as the others. Errors are `{"error": "..."}`; an invalid definition or case is `400 {"error":"invalid eval","problems":[...]}`. `synth` is a reserved eval name. The routes put the verb before the name because Go's mux rejects `/evals/synth/{name}/generate` next to `/evals/{name}/files/{path...}`.

| Route | Body | Success | Errors |
|---|---|---|---|
| `GET /v1/gb/evals/synth` | | `200 {"evals":[EvalView], "models", "can_generate", "max_examples", "defaults"}`: the synthetic evals (as in the list, with `origin` and `synth: {version, model, num_examples, mode, generation, generation_error}`) and what a form needs | `503` |
| `POST /v1/gb/evals/synth` | `{name, description?, prompt, corpus_prompt?, model, num_examples, mode?, bulk_size?}` | `202 {name, version, generation}`: created, generating | `400`, `409` the name is taken, `429` cost cap of the day, `503` no LLM |
| `GET /v1/gb/evals/synth/{name}` | | `200` the definition (`version`, the settings), `versions`, the latest `generations`, and `cases` (each with `origin`: `generated`, `edited` or `user`, its `generation` and `version`, and its content) | `404` (also for a human eval) |
| `PUT /v1/gb/evals/synth/{name}` | the fields to change (and `regenerate`, `keep_edited`) | `200 {name, version}` (the cases stay), or `202` with the `generation` when `regenerate` is true | `400`, `404`, `409`, `429`, `503` |
| `POST /v1/gb/evals/synth/generate/{name}` | `{"keep_edited": true}` (optional) | `202 {name, version, generation}` | `404`, `409` a generation is going, `429`, `503` |
| `PUT /v1/gb/evals/synth/cases/{name}/{case}` | the case (JSON of a case of an eval file; the name is the path's) | `200` the case with its `origin` (`edited`) | `400`, `404` |

`GET /v1/gb/evals` lists every eval with `origin` (`human` or `synthetic`) and, for a synthetic one, `synth`. `DELETE /v1/gb/evals/{name}` deletes a synthetic eval with its definition, versions, generations and case records (`409` while a generation is going). The generation's LLM cost is on its record (`cost_usd`); it is not part of the cost of the eval runs, and is not counted in the per-user daily cap (the cap is only checked before a generation starts).

## Database

Four tables next to the eval ones, in `internal/evals/db/{sqlite,postgres}/sql/schema.sql`: `gb_synth_defs` (the current definition and `version`, keyed by the eval def), `gb_synth_def_versions` (every version), `gb_synth_generations` (the runs of the generator; a unique partial index keeps one active per eval) and `gb_synth_cases` (provenance by case name). They hang off the eval def with `ON DELETE CASCADE` and the same owner check (`(eval_def_id, user_id)`). The universal eval tables do not change. See [../listings/db-tables.md](../listings/db-tables.md).
