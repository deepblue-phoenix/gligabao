// Package postgres holds the Postgres storage of the evals data model (package core). It has the schema (Schema) and the functions that create the records of its tables (create.go; for now only creation).
package postgres

import _ "embed"

// Schema is the schema for Postgres: the same tables as the sqlite package (see there for what they hold), with native types
// (BIGSERIAL ids, JSONB for params, tags and payloads, TIMESTAMPTZ for times). The statements are idempotent and in dependency
// order, so the whole schema can be executed in one call.
//
//go:embed sql/schema.sql
var Schema string
