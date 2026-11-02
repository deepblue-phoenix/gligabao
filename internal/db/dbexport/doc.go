// Package dbexport dumps everything gligabao keeps locally (the SQLite tables, the Chromem vector store with its embeddings, and the
// image files) into one archive file, and reads such a file back. It is how a gligabao whose storage is going away (the research agent
// in a Modal container) hands its state to another one, which loads it into its own databases.
//
// Only what is local is exported: a remote database (PostgreSQL) or vector store (Pinecone) persists its data itself, so there is
// nothing to hand over. The archive then has no tables or no vectors, and Manifest.Skipped says why.
//
// # The archive
//
// The archive is a ZIP file (one standard format that every language reads, with random access, so that the manifest can be read
// without going through the data). Its entries are:
//
//	manifest.json          what the archive holds and how to read it (type Manifest). Always present.
//	tables/<table>.jsonl   one line per row of a table: a JSON object {"column": value, ...}, in the order of the primary key
//	vectors/<name>.jsonl   one line per vector of a vector store dataset: {"id": "...", "values": [...], "metadata": {...}}, in ID order
//	images/<path>          the files of the image directory (the path of gb_image_artifacts.path)
//	extra/<name>           files the caller added (the agent adds the ATIF trajectory of its research)
//
// Every table whose name starts with gb_ is in it, the empty ones too, so that the archive says what the schema was. The manifest lists
// the tables parents first (a table comes after the tables its foreign keys reference, except for the references to itself, which
// point at lower IDs because rows are written in primary-key order), with the columns, the primary key and the foreign keys of each,
// so that a loader can remap the IDs without knowing the schema. The IDs are those of the source database: they are only meant to be
// remapped when the rows are merged into another one.
//
// Values are JSON: integers and floats are numbers, text is a string, NULL is null and a timestamp is an RFC 3339 string in UTC (the
// same text SQLite stores). Bytes that are not text are {"$base64": "..."}.
//
// Vector datasets are named as in the vector store ("dataset-<id>-<index>", see memory_dataset.VectorDataset); for those the manifest
// also says which gligabao dataset and index they are. The embeddings are as the store has them (Chromem: normalized).
//
// Write writes an archive, Open (or NewReader) reads one: Reader.Rows, Reader.Vectors and Reader.Images iterate over the contents.
package dbexport
