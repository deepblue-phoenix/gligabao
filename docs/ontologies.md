# Ontologies

An ontology describes the concepts of a domain (its **classes**) and how they relate. gligabao can load ontologies written in OWL, update them (merge into or replace them), and keep them in the graph database next to the memory graph. This is initial support: ontologies are stored and tracked, but `remember` and `query` do not use them yet.

## POST /v1/gb/ontologies

Loads an ontology under a name.

```json
{
  "user_id": "u1",
  "name": "market",
  "description": "optional",
  "format": "rdfxml",
  "content": "<?xml version=\"1.0\"?><rdf:RDF ...>...</rdf:RDF>"
}
```

| Field | Required | Description |
| ----- | -------- | ----------- |
| `user_id` | yes | Owner of the ontology (see [identity.md](identity.md)). An ontology loaded from a file belongs to the user who sends the request too. |
| `name` | yes | Name of the ontology: 1-64 characters of letters, digits, `.`, `_`, `-`, starting with a letter or digit. Unique per user. |
| `content` | one of `content` and `file` | The ontology document, as a string (at most 8 MiB). |
| `file` | one of `content` and `file` | The name of a file of the [ontology directories](#ontology-directories) to load instead of sending `content`, e.g. `"market.owl"`. A plain file name, no directory. |
| `format` | no | Serialization of `content`. Only `rdfxml` (OWL in RDF/XML) is supported, and it is the default. |
| `description` | no | Free text. Defaults to the ontology's own `rdfs:comment`, if it has one. |

Response `201` with the metadata of the ontology:

```json
{
  "id": 1,
  "user_id": "u1",
  "name": "market",
  "description": "A general ontology of markets: ...",
  "iri": "https://gligabao.example/ontology/market",
  "title": "Market ontology",
  "format": "rdfxml",
  "size_bytes": 12920,
  "sha256": "9f2c...",
  "classes": 20,
  "properties": 26,
  "edges": 38,
  "created_at": "2025-06-01T10:00:00Z",
  "updated_at": "2025-06-01T10:00:00Z"
}
```

| Status | Meaning |
| ------ | ------- |
| `400` | Bad JSON, missing or invalid `user_id` or `name`, neither `content` nor `file` (or both), a `file` that is not a plain file name or when no ontology directories are configured, unsupported `format`, or a document that is not well-formed RDF/XML or defines no class. |
| `404` | The `file` is in none of the ontology directories. |
| `409` | The user already has an ontology with that name. Nothing is replaced. |
| `500` | Database error. |

Errors are `{"error": "..."}`. It makes no LLM call. Example, loading the bundled ontology:

```sh
jq -n --rawfile c build/ontology/market.owl '{user_id:"u1", name:"market", content:$c}' \
  | curl -s localhost:7878/v1/gb/ontologies -d @-
```

## Ontology directories

`ontology_dirs` in `gligabao.toml` (relative paths are relative to the file; `serve --ontology-dir`, repeatable, and `ServeOptions.OntologyDirs` override it) lists directories of ontology documents. They are listed explicitly and **not scanned**: nothing is loaded at startup, and an ontology is only loaded when a request names its file with `file` (in `POST` or `PUT`). The directories are searched in order and the first one that has the file is used, so a program that embeds gligabao can list its own directory first to override a file of the same name. The ontology is stored under the `user_id` of the request and the `name` it gives, exactly as if the file's text had been sent in `content`. Without `ontology_dirs`, `file` is rejected with `400`.

```sh
curl -s localhost:7878/v1/gb/ontologies -d '{"user_id":"u1","name":"market","file":"market.owl"}'
```

## PUT /v1/gb/ontologies/{name}

Updates the ontology stored under `name` with a new document. By default the document is **merged** into the stored ontology; with `"replace": true` it **replaces** it.

```json
{
  "user_id": "u1",
  "description": "optional",
  "format": "rdfxml",
  "replace": false,
  "content": "<?xml version=\"1.0\"?><rdf:RDF ...>...</rdf:RDF>"
}
```

`name` comes from the path. The other fields (`content` or `file`, ...) are as for `POST`, plus:

| Field | Required | Description |
| ----- | -------- | ----------- |
| `replace` | no | `false` (default): merge `content` into the stored ontology, see [Merging](#merging-the-default). `true`: replace the stored ontology entirely, see [Replacing](#replacing-replace-true). |

Only the owner's ontology can be updated. Response `200` with the new metadata. The ontology keeps its `id`, `user_id`, `name` and `created_at`; `updated_at` is set to now. Statuses are those of `POST`, except that there is no `409`, and `404` means the user has no ontology with that name. An update never creates an ontology; use `POST` for that.

```sh
# merge a fragment into the stored ontology
jq -n --rawfile c extension.owl '{user_id:"u1", content:$c}' \
  | curl -s -X PUT localhost:7878/v1/gb/ontologies/market -d @-

# replace it with a complete one
jq -n --rawfile c build/ontology/market.owl '{user_id:"u1", content:$c, replace:true}' \
  | curl -s -X PUT localhost:7878/v1/gb/ontologies/market -d @-
```

Either way, the update is all or nothing. The classes and edges are changed in one transaction, so readers never see a mix of old and new, and if the update fails (an unparseable document, a database error) the stored ontology is left exactly as it was.

### Merging (the default)

The document is merged into the stored ontology. A merge can add and correct things, but **never deletes** anything.

**Identity.** What counts as "the same" decides what gets overwritten:

- A **class** is identified by its IRI. Two classes with the same IRI are the same class, whatever their name, label or comment say.
- An **edge** is identified by its source class IRI, its target class IRI, its kind and its property IRI (for the `subclass_of`, `equivalent_class` and `disjoint_with` kinds, the RDFS/OWL predicate; see [How OWL is mapped](#how-owl-is-mapped)). So `Trade -hasBuyer-> Buyer` is one edge, and changing its label in the document does not make a second edge. Two properties with different IRIs between the same two classes are two edges.

**Rules**, applied to every class and edge of the document:

| In the document | Stored | Result |
| --------------- | ------ | ------ |
| A class or edge | not there | **Added.** |
| An edge | already there | **Overwritten.** `relation`, `label`, `comment` and `inverse_of` all take the document's values, *including empty ones*. Nothing of the old edge's metadata is kept: if the stored edge had an `inverse_of` and the document's has none, the result has none. |
| A class the document **declares** (types it as `owl:Class` or `rdfs:Class`) | already there | **Overwritten** in the same way: `name`, `label` and `comment` take the document's values, including empty ones. |
| A class the document only **mentions** (as the domain or range of a property, or in a `subClassOf` relationship, without declaring it) | already there | **Left untouched.** If it is not stored, it is added, with no label or comment. |
| (nothing) | a class or edge the document does not contain | **Kept as it is.** |

Why mentioned classes are not overwritten: a fragment such as "property `tradedOn` from `Asset` to `Market`" has to name its endpoints, but it says nothing about them. If naming a class overwrote it, such a fragment would erase the stored label and comment of `Asset` and `Market`. So a document describes a class only by declaring it.

Overwriting changes the stored row in place and keeps its ID, so everything attached to a class (the edges that start or end at it) stays attached.

**Metadata** after a merge:

- `classes`, `properties` and `edges` describe the ontology *after* the merge (the totals of what is stored), not the size of the document.
- `size_bytes` and `sha256` describe the document of this update, the last one applied, not the merged whole. They identify what was last sent. `updated_at` is set.
- `iri` and `title` (from the document's `owl:Ontology`, if it has one) overwrite the stored ones; if the document has none, the stored ones are kept, because a fragment usually has no header.
- `description`: the request's `description` if given; otherwise the document's `rdfs:comment`; otherwise the stored one is kept.

Merging the same document twice gives the same ontology as merging it once.

A merge cannot remove a class or an edge, nor clear one of its properties by omitting it (an omitted edge is kept as it is; to blank a label, send the edge with an empty label). To remove things, use `replace`.

Example. The stored ontology has the classes `Asset` and `Market` (with labels and comments) and the edge `Asset -tradedOn-> Market` (label "traded on", inverse `listsAsset`). Merging this fragment:

```xml
<owl:Class rdf:about="#Stock"><rdfs:subClassOf rdf:resource="#Security"/></owl:Class>
<owl:ObjectProperty rdf:about="#tradedOn">
  <rdfs:label>trades on</rdfs:label>
  <rdfs:domain rdf:resource="#Asset"/><rdfs:range rdf:resource="#Market"/>
</owl:ObjectProperty>
```

adds the class `Stock` and the edge `Stock -subclass_of-> Security`; overwrites the edge `Asset -tradedOn-> Market`, which now has the label "trades on", no comment and no inverse; leaves `Asset`, `Market` and `Security` as they were (they are only mentioned); and keeps everything else.

### Replacing (`replace: true`)

The stored ontology is thrown away and the document becomes the ontology. Every stored class and edge that is not in the document is **deleted**, and the metadata is recomputed from the document alone, as if it had just been loaded with `POST`: the counts are those of the document, and `description`, `iri` and `title` are the document's (the old ones are dropped, so an omitted `description` becomes the document's `rdfs:comment`, or empty). Only the identity (`id`, `user_id`, `name`, `created_at`) is kept. Use it to remove things, or to load a new version of the whole ontology.

## What is stored

Loading an ontology writes to two places. If either fails, nothing is kept and the name stays free. Updating writes to the same two places and, on failure, keeps the previous version. Within an ontology a class is identified by its IRI and an edge by (source, target, kind, IRI); this is what the unique constraints below enforce, and what a merge uses to decide what already exists.

**Metadata, in the relational database.** One row in `gb_ontologies` per ontology: `id`, `user_id`, `name`, `description`, `iri` and `title` (from the document's `owl:Ontology`, if any), `format`, `size_bytes`, `sha256` (of the loaded document, so you can tell which version was loaded), the counts `classes`, `properties` and `edges`, `created_at` and `updated_at` (equal until the first update). `UNIQUE (user_id, name)`.

**The ontology itself, in the graph database.** It is a small graph, separate from the dataset graph (`gb_entities`, `gb_edges`), and written in one transaction:

| Table | Content |
| ----- | ------- |
| `gb_onto_classes` | The nodes: `id`, `ontology_id` → `gb_ontologies`, `iri`, `name` (local name of the IRI, e.g. `Market`), `label`, `comment`; unique (`ontology_id`, `iri`) |
| `gb_onto_edges` | The relationships: `id`, `ontology_id`, `source_id` and `target_id` → `gb_onto_classes`, `relation`, `kind`, `iri`, `label`, `comment`, `inverse_of`; unique (`ontology_id`, `source_id`, `target_id`, `kind`, `iri`) |

The graph rows reference the metadata row with `ON DELETE CASCADE`. Like everything else, the SQL lives in the backends (`internal/db/relational/*/db_ontology.go`, `internal/db/graph/*/db_ontology.go`), for SQLite and PostgreSQL.

## How OWL is mapped

The document must be OWL in RDF/XML. The parser (`internal/memory/memory_ontology`) is a small RDF/XML reader plus an interpretation of the OWL terms below; it has no external dependencies.

| In the document | In the graph |
| --------------- | ------------ |
| A named `owl:Class` or `rdfs:Class` (or a class only mentioned by a relationship) | A class node. `rdfs:label` and `rdfs:comment` become `label` and `comment`. |
| `A rdfs:subClassOf B` | An edge `A -subclass_of-> B` (`kind` = `subclass_of`) |
| `A owl:equivalentClass B`, `A owl:disjointWith B` | An edge with relation and `kind` `equivalent_class` or `disjoint_with`. They are symmetric, so a pair is stored once even if stated both ways. |
| An `owl:ObjectProperty` (or a subtype such as `owl:SymmetricProperty`, `owl:TransitiveProperty`) with `rdfs:domain` D and `rdfs:range` R | An edge `D -name-> R` (`kind` = `property`), where `name` is the local name of the property IRI. `label`, `comment` and `inverse_of` (from `owl:inverseOf`, in either direction; the name of the inverse property) are kept on the edge. |
| A domain or range that is an `owl:unionOf` of named classes | One edge for each combination of domain and range class. |

In every edge, `iri` is the IRI of the property (or, for the first three rows, of the RDFS/OWL predicate). Within an ontology, classes are identified by IRI; `name` is only the readable local name.

Not stored: anything else, in particular restrictions and other anonymous class expressions, `owl:intersectionOf`, datatype and annotation properties, individuals, `rdfs:subPropertyOf`, properties missing a domain or a range, and the built-in classes (`owl:Thing`, ...). Documents using them still load; those parts are skipped. A document that yields no class at all is rejected with `400`.

## The market ontology

[`build/ontology/market.owl`](../build/ontology/market.owl) is a basic, general ontology of markets with the 20 most important concepts: `Market`, `Exchange`, `MarketParticipant`, `Buyer`, `Seller`, `Intermediary`, `Regulator`, `Asset`, `Commodity`, `Security`, `Currency`, `Derivative`, `Order`, `Trade`, `Price`, `Supply`, `Demand`, `MarketIndex`, `Regulation` and `Risk`. `Exchange` is a kind of `Market`; `Buyer`, `Seller` and `Intermediary` are kinds of `MarketParticipant`; `Commodity`, `Security`, `Currency` and `Derivative` are kinds of `Asset`. The other relationships are object properties, for example `Trade -hasBuyer-> Buyer`, `Order -placedBy-> MarketParticipant`, `Price -denominatedIn-> Currency`, `Derivative -underlying-> Asset`, `Regulator -regulates-> Market`, and `Asset -tradedOn-> Market` (inverse of `Market -listsAsset-> Asset`). Add further ontologies to the `build/ontology` directory the same way.

## Limits

- There is no endpoint yet to list, read or delete ontologies.
- A merge cannot delete a class or an edge; replace the ontology to do that.
- Updating changes the stored ontology in place; there is no version history beyond `sha256` (of the last document) and `updated_at`.
- Only RDF/XML is read (no Turtle, JSON-LD, ...), and there is no reasoning or consistency checking.
- Ontologies are not connected to datasets, and `remember` and `query` ignore them.
