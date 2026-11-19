# Ontologies

An ontology describes the concepts of a domain (its **classes**), how they relate (its **relations**) and what can be said about them (their **properties**). gligabao ontologies are written in a small declarative language in **YAML or JSON**: a typed property graph, not OWL. `remember` can ground the graph it extracts against them: the entities and edges are checked against the ontology, tagged with what they matched, and (in strict mode) what does not conform is dropped. See [Grounding in `remember`](#grounding-in-remember).

The design and the decisions behind it are in [ontology-grounding-plan.md](ontology-grounding-plan.md).

## The language

```yaml
id: market                      # optional, informational
version: "1"                    # optional label of the document (see "Versions")
description: A general ontology of markets.

classes:
  Market:
    description: A place where buyers and sellers meet.
  Exchange:
    subClassOf: Market          # one class or a list of classes
    aliases: [bourse]           # other names that resolve to this class
  Security:
    subClassOf: Asset
    aliases: [stock, share]
    properties:
      ticker: {type: string, pattern: "^[A-Z0-9.]{1,10}$", required: true}
  Order:
    properties:
      side: {type: string, enum: [buy, sell]}

relations:
  tradedOn:
    description: An asset is traded on a market.
    domain: Asset               # the class of the source (a class or a list of classes)
    range: Market               # the class of the target
    temporal: true              # the edge carries valid_at / invalid_at
    aliases: [listed_on]
    properties:
      since: {type: date}
```

| Part | Meaning |
| ---- | ------- |
| `classes` | A map from the class name to its definition (below). At least one class is required. |
| `classes.<C>.subClassOf` | The parent class(es). A class has the properties of its ancestors. |
| `classes.<C>.description`, `aliases` | Free text, and other names for the class. |
| `classes.<C>.properties` | A map from the property name to a [property](#properties). |
| `relations` | A map from the relation name to its definition. A relation is a type of edge. |
| `relations.<R>.domain`, `range` | The class (or classes: a union) the source and the target must be, or be a subclass of. Empty: any. |
| `relations.<R>.temporal` | The edge has a validity period (`valid_at`, `invalid_at`). |
| `relations.<R>.properties`, `aliases`, `description` | As for a class. |

**Names** are compared ignoring case and anything that is not a letter or a digit, so `MarketParticipant`, `market_participant` and `market participant` are the same name. That is what lets the ontology be written in `CamelCase` while the extraction writes lowercase `snake_case`. Classes and relations are two separate namespaces. An unknown field anywhere is an error.

### Properties

A property is `{type, description, enum, pattern, required}`:

| Field | Meaning |
| ----- | ------- |
| `type` | `string`, `number`, `integer`, `boolean`, or `date` (an ISO 8601 date, `2025-01-31`, or date-time, `2025-01-31T09:00:00Z`). |
| `enum` | `string` only: the allowed values. |
| `pattern` | `string` only: a regular expression ([Go syntax](https://pkg.go.dev/regexp/syntax)) the value must match (it is not anchored unless you anchor it). |
| `required` | The property must be there. |

A subclass may define an inherited property again only with the same `type`, `enum` and `pattern` (to make it `required`, say); anything else is a problem.

### What is checked in a document

A document that is not valid is a `400` with the list of its problems, each with the path of the place in the document (`classes.Buyer.subClassOf`):

- a `subClassOf` that names an unknown class, itself, or that makes a cycle;
- a `domain` or `range` that names an unknown class;
- an unknown property `type`; an `enum` or a `pattern` on a property that is not a `string`; a `pattern` that is not a regular expression;
- a name or alias that another class (or relation) already uses, and a property a subclass redefines differently;
- no classes at all, or an unknown field.

```json
{
  "error": "invalid ontology: classes.C.subClassOf: unknown class \"Z\"",
  "problems": [{"path": "classes.C.subClassOf", "message": "unknown class \"Z\""}]
}
```

## API

All the endpoints take the user from the `gb-user-id` header (see [identity.md](../identity.md)): ontologies are owned by a user, and names are unique per user. None of them makes an LLM call.

### POST /v1/gb/ontologies

Stores an ontology under a name, as its version 1.

```json
{
  "name": "market",
  "description": "optional",
  "format": "yaml",
  "content": "classes:\n  Market: {}\n..."
}
```

| Field | Required | Description |
| ----- | -------- | ----------- |
| `name` | yes | 1-64 characters of letters, digits, `.`, `_`, `-`, starting with a letter or digit. Unique per user. |
| `content` | one of `content`, `spec` and `file` | The ontology as a YAML or JSON document, as a string (at most 8 MiB). |
| `spec` | one of the three | The ontology as a JSON object, instead of a string. |
| `file` | one of the three | The name of a file of the [ontology directories](#ontology-directories) to load, e.g. `"market.yaml"`. A plain file name, no directory. |
| `format` | no | `yaml` or `json`. Default: detected from the content (it is JSON if it starts with `{`), or from the extension of the `file` (`.json`). |
| `description` | no | Free text. Defaults to the `description` of the document. |

Response `201` with the metadata:

```json
{
  "id": 1,
  "user_id": "u1",
  "name": "market",
  "description": "A general ontology of markets: ...",
  "format": "yaml",
  "version": 1,
  "size_bytes": 7210,
  "sha256": "9f2c...",
  "classes": 20,
  "relations": 26,
  "properties": 4,
  "created_at": "2025-06-01T10:00:00Z",
  "updated_at": "2025-06-01T10:00:00Z"
}
```

| Status | Meaning |
| ------ | ------- |
| `400` | Bad JSON, missing or invalid `gb-user-id` header or `name`, none (or more than one) of `content`, `spec` and `file`, a `file` that is not a plain file name or when no ontology directories are configured, an unsupported `format`, or a document that is not valid (with the `problems`). |
| `404` | The `file` is in none of the ontology directories. |
| `409` | The user already has an ontology with that name. |
| `500` | Database error. |

Errors are `{"error": "..."}`. Example, with the bundled ontology:

```sh
jq -n --rawfile c build/ontology/market.yaml '{name:"market", content:$c}' \
  | curl -s -H 'gb-user-id: u1' localhost:7878/v1/gb/ontologies -d @-
```

### PUT /v1/gb/ontologies/{name}

Saves a new version of the ontology: the document replaces the stored one entirely (there is no merge), the `version` goes up by one, and the compiled form in memory is swapped for the new one. The old documents stay in the history. The fields are those of `POST` (`name` comes from the path). Only the owner's ontology can be updated: `404` if the user has none with that name (an update never creates one), and no `409`. An invalid document changes nothing. The ontology keeps its `id`, `user_id`, `name` and `created_at`; `updated_at` is set. Response `200` with the new metadata.

```sh
jq -n --rawfile c build/ontology/market.yaml '{content:$c}' \
  | curl -s -H 'gb-user-id: u1' -X PUT localhost:7878/v1/gb/ontologies/market -d @-
```

### GET /v1/gb/ontologies, GET /v1/gb/ontologies/{name}

`GET /v1/gb/ontologies` lists the user's ontologies, oldest first, as `{"ontologies": [...]}` (the metadata above, without the spec). `GET /v1/gb/ontologies/{name}` returns one with its `spec` (the ontology as JSON); `404` if there is none.

### GET /v1/gb/ontologies/{name}/events

What grounding made of the nodes and edges of the `remember` calls that used the ontology, newest first, as `{"events": [...]}`. It is for debugging an ontology: which of the extracted entities and facts matched it, and why the others did not. Query parameters: `artifact_id` (only the events of that artifact), `outcome` (`matched`, `matched_via_alias`, `unmatched`, `violation` or `dropped`), `limit` (default 100, at most 1000). `400` for a bad parameter, `404` for an unknown ontology.

```json
{"events": [{
  "id": 41, "dataset_id": 1, "artifact_id": 7, "run_id": 12,
  "ontology_id": 1, "ontology_version": 2, "mode": "annotate",
  "subject_kind": "node", "name": "Max", "type": "dog", "class": "Dog",
  "outcome": "violation", "reason": "missing_required",
  "detail": {"issues": [{"property": "age", "reason": "missing_required", "message": "required property is missing"}]},
  "entity_id": 33, "created_at": "2025-06-01T10:00:00Z"
}]}
```

See [the events table](#the-events-table) for the fields and the reason codes.

## Ontology directories

`ontology_dirs` in `gligabao.toml` (relative paths are relative to the file; `serve --ontology-dir`, repeatable, and `ServeOptions.OntologyDirs` override it) lists directories of ontology documents. They are listed explicitly and **not scanned**: nothing is loaded at startup. A request with `"file": "market.yaml"` instead of `content` reads that file from the first directory that has it, and stores it like `content`, under the requesting user. The name must be a plain file name (no directory, not starting with `.`): it cannot reach outside the directories. The Docker image ships `build/ontology/` as `/app/ontology`.

## Grounding in `remember`

With `ontology_grounding: true`, `remember` checks the entities and edges the LLM extracted against the ontology before they are stored. Grounding is **off by default**; it only runs with the flag.

| Request field | Description |
| ------------- | ----------- |
| `ontology_grounding` | `true` to ground. Default `false`. |
| `ontologies` | The names of the ontologies to use. They are **merged into one** for the call. Default: all of the user's ontologies. |
| `ontology_grounding_mode` | `annotate` (default) or `strict`. |

`400` if `ontologies` or the mode is sent without `ontology_grounding: true`, for an unknown ontology name or mode, if the user has no ontology, if two of the selected ontologies define the same class or relation **differently** (the error names both), and for an image (grounding applies to a text). Merging is the union of the classes and relations; a name defined in two ontologies with the same definition is one definition.

**What is checked**, for every entity and edge of each text (or each chunk of a long one):

- an entity's `type` must resolve to a class (by its name or an alias; see [names](#the-language)), and its `properties` must be those of the class (and its ancestors'): no other, every `required` one, each with its type, `enum` and `pattern`;
- an edge's `relation` must resolve to a relation; the class of its source must be the `domain` or a subclass of it, the class of its target the `range` or a subclass; its `properties` must be those of the relation. An end that the extraction does not list as an entity (the store would create it with type `other`) cannot be judged: it is skipped in annotate mode and the edge is dropped in strict mode.

**The extraction prompt** describes the ontology (classes with their parents and properties, relations with their domain and range) and asks the LLM to give entities and edges optional `properties`. In `annotate` mode it is a guide: the LLM is told to use the ontology's types and relations when one fits, and its own when none does. In `strict` mode the LLM is told the output must conform, and is given the [JSON Schema](#extraction-schema) of the ontology. The LLM client has no structured-output mode (a `response_format`), so the schema is in the prompt, and **the guarantee in both modes is the validation afterwards**, not the model.

**`annotate`**: nothing is dropped. Entities and edges that resolve in the ontology are tagged as valid (also those that resolved through an alias); the others are kept, flagged, and recorded as events.

**`strict`**: whatever does not conform (an unknown type or relation, a domain or range violation, a property that is invalid or missing, an edge whose end was dropped or is not listed) is **dropped before the store stage**: it is not stored, and an edge never points at an entity that was dropped.

The grounding is the `ground_graph` stage of the [run log](../remember/remember.md#run-log), between the extraction of the text (or each chunk) and its reconciliation and storage. It runs on no model and never fails the call. Entities and edges of the enrichment stages (web search results, series observations) are not grounded.

### What is stored on the graph

The rows get three nullable columns, `NULL` when never grounded (the rows of a call without grounding, and rows made before): `ontology_version` (the version of the ontology that defines the class or relation, or of the one the call was grounded against, if it resolved to nothing), `ontology_class` (the canonical class name; for an edge, the relation; `NULL` if it resolved to nothing) and `ontology_valid` (it matched with no violation). An entity is shared between artifacts: a mention that was grounded replaces the tags of the stored entity, one that was not leaves them.

The `properties` of an entity or an edge are validated but **not stored** (the graph has no properties yet).

### The response

The `remember` response has `ontology_grounding`: the summary of the whole call (all the chunks of a document), by mode. Both have `mode`, `ontologies` (`id`, `name`, `version`), and `nodes` and `edges`, each `{"total", "matched", "via_alias", "violations", "dropped"}`.

```json
{
  "mode": "annotate",
  "ontologies": [{"id": 1, "name": "market", "version": 2}],
  "nodes": {"total": 5, "matched": 2, "via_alias": 1, "violations": 2, "dropped": 0},
  "edges": {"total": 3, "matched": 1, "via_alias": 0, "violations": 2, "dropped": 0},
  "matched_percent": 50.0,
  "matched_classes": [{"class": "Dog", "count": 2}, {"class": "keeps", "count": 1}],
  "violations": [{"reason": "unknown_class", "count": 1}, {"reason": "unknown_relation", "count": 1}]
}
```

- **annotate** adds `matched_percent` (the share, 0 to 100, of all nodes and edges that matched by name or alias), `matched_classes` (how many matched each class or relation) and `violations` (the count of each reason).
- **strict** adds `dropped_ratio` (0 to 1) and `violations` (the reasons the items were dropped for). **A high `dropped_ratio` is a signal that the ontology does not cover the vocabulary of the corpus** (or that the model does not follow it): look at the `dropped` [events](#the-events-table), and add the classes, relations and aliases the corpus needs.

The summary is also in the stage output of the trajectory of an eval call.

### The events table

`gb_ontology_events` (in the graph database) has a row for every node and edge of every grounded call, written in the transaction that stores the artifact (so it needs no other write after the store). Deleting the artifact, or the ontology, deletes its events.

| Column | Meaning |
| ------ | ------- |
| `id`, `dataset_id`, `artifact_id`, `run_id` | The event, the dataset and artifact of the call, and its [remember run](../remember/remember.md#run-log). |
| `ontology_id`, `ontology_version` | The ontology (and version) that defines the class or relation the item resolved to. An item that resolved to nothing is logged once for each ontology of the call, at its version. |
| `mode` | `annotate` or `strict`. |
| `subject_kind`, `name` | `node` or `edge`; the entity's name, or `source relation target`. |
| `type`, `class` | The type or relation as the LLM wrote it, and the class or relation it resolved to (empty if none). |
| `outcome` | `matched`, `matched_via_alias`, `unmatched` (annotate: the type or relation is not in the ontology), `violation` (annotate: it matched but breaks a rule) or `dropped` (strict). |
| `reason` | Empty for a match, else `unknown_class`, `unknown_relation`, `domain_violation`, `range_violation`, `property_invalid`, `missing_required`, `endpoint_dropped` (strict: an end of the edge was dropped) or `endpoint_unlisted` (strict: an end is not an entity of the extraction). |
| `detail` | JSON: the failing properties with the validator message, the domain or range that was violated, ... |
| `entity_id`, `edge_id` | The stored row; `NULL` for a dropped item. |
| `created_at` | |

Indexes: `artifact_id`, `ontology_id`, (`dataset_id`, `created_at`).

### Extraction schema

`Registry.ExtractionJSON()` (`internal/memory/memory_ontology/ontospec`) compiles the ontology into a JSON Schema (draft 2020-12) of the extraction output: `entities` is an array of `anyOf` branches, one per class (`type` a `const`, the properties of the class and its ancestors with their `enum` and `pattern`, `required`, `additionalProperties: false`), and `edges` one per relation (`relation` a `const`, its properties, and `valid_at` / `invalid_at` if it is `temporal`). The domain and range cannot be written in JSON Schema: they are in the description of the branch and checked by the grounding.

## Versions, storage and hot swap

- **`gb_ontologies`** (relational database): `id`, `user_id`, `name`, `description`, `format`, `version`, `spec` (the ontology as JSON: JSONB in PostgreSQL, TEXT in SQLite), `size_bytes`, `sha256` (of the latest document), `classes`, `relations`, `properties` (counts), `created_at`, `updated_at`; unique (`user_id`, `name`).
- **`gb_ontology_versions`**: the history, one row per save: `ontology_id` → `gb_ontologies` (cascade), `version`, `format`, `content` (the document as it was sent), `spec`, `sha256`, `created_at`.
- The **`version`** is an integer the server bumps on every save (the `version` field of a document is only a label the author gives it). It is what is stamped on the nodes and edges.
- In memory, a `Registry` per (user, ontology) is compiled from the spec and kept behind an atomic pointer (`memory_ontology.Registries`). A save compiles the new registry and swaps the pointer: the calls that are running keep the registry they started with, and the next ones get the new one. A registry that is not in memory (after a restart, or after a save by another server) is compiled from the database on first use.
- With `ontology_s3_bucket` (`ONTOLOGY_S3_BUCKET`, an existing bucket, with `ontology_s3_region`) set, the documents are also stored in S3, one directory per user: `<ontology_s3_prefix>/<user>/<name>/<timestamp>_<create|update>.<yaml|json>` (the prefix defaults to `ontologies`, also `ONTOLOGY_S3_PREFIX`). Documents are never overwritten; a failed S3 write is logged and does not fail the request. They are written by an S3 client dedicated to the ontologies (`memory_ontology/archive.go`).

## The market ontology

[`build/ontology/market.yaml`](../../build/ontology/market.yaml) is a basic, general ontology of markets with the 20 most important concepts: `Market`, `Exchange`, `MarketParticipant`, `Buyer`, `Seller`, `Intermediary`, `Regulator`, `Asset`, `Commodity`, `Security`, `Currency`, `Derivative`, `Order`, `Trade`, `Price`, `Supply`, `Demand`, `MarketIndex`, `Regulation` and `Risk`, and 26 relations such as `participatesIn`, `tradedOn`, `hasBuyer`, `executedOn`, `underlying`, `regulates` and `exposesTo`, with a few aliases and properties.

## Limits and what is not done

- OWL is not supported (the OWL parser, `gb_onto_classes` and `gb_onto_edges` were removed): there are no individuals, no reasoning and no consistency checking beyond the checks above.
- There is no endpoint to delete an ontology, and no merging of a fragment into a stored ontology: send the whole document.
- `query` does not use ontologies: **subclass expansion at query time** (`type IN descendants`; `Registry.Expand` is there for it) and a **background re-validation** of the stored graph when an ontology changes are a later phase. The tags on the rows say which version they were grounded against.
- An entity's `properties` are validated, not stored.
- Existing databases: the OWL ontologies (`gb_onto_*`, the old `gb_ontologies`) are dropped at startup, and the new tables and the three columns of `gb_entities` and `gb_edges` are created or added. Load ontologies again, in the new format.
