// Package postgres holds the Postgres storage of the training audits data model (package core). It has the schema (Schema) and the functions that write and read the records of its tables (create.go: create; read.go: update, finish, get, list, and Store).
package postgres

import _ "embed"

// Schema is the schema for Postgres: the same tables as the sqlite package (see there for what they hold), with native types
// (BIGSERIAL ids, JSONB for params, TIMESTAMPTZ for times). The statements are idempotent and in dependency order, so the whole
// schema can be executed in one call.
//
//go:embed sql/schema.sql
var Schema string
