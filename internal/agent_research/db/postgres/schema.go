// Package postgres holds the Postgres storage of the research agents' data model (package core): the schema (Schema) and the
// functions that write and read its tables (Store).
package postgres

import _ "embed"

// Schema is the schema for Postgres: the same tables as the sqlite package (see there for what they hold), with native types
// (BIGSERIAL ids, TIMESTAMPTZ for times). The statements are idempotent and in dependency order, so the whole schema can be executed
// in one call.
//
//go:embed schema.sql
var Schema string

// Upgrade is what an older database lacks: the columns parent_id, name and reason of gb_agent_research_runs (before sub-agents, 0.129.0)
// and runner (before the Modal runner, 0.165.0), and agent_name, agent_version and agent_hash (before the agent definitions, 0.168.0), and notes_id (before the deep-research loop state, 0.180.0), and phase and stop_reason (before the investigative loop, 0.181.0).
//
//go:embed upgrade_columns.sql
var Upgrade string

// SubagentIndexes are the indexes on the columns of Upgrade, so that they are made after it (see the sqlite package).
//
//go:embed schema_subagent_indexes.sql
var SubagentIndexes string

// StatusMigration is what a database made before the status 'stopped' (0.145.0) lacks: its CHECK on gb_agent_research_runs.status does not
// allow it. The constraint (named by Postgres after the table and column) is replaced; it is run at every start, like the rest.
//
//go:embed upgrade_status.sql
var StatusMigration string
