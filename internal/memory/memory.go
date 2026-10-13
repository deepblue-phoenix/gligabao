// Package memory turns free text into a bi-temporal knowledge graph (modelled on Graphiti) with an LLM and stores it in the configured database (see internal/db/graph).
//
// Every input is an artifact (a row in gb_artifacts): a piece of data ingested to be remembered. Remember stores
// artifacts of kind episode, which have an event time (ValidAt: when the text was written or observed) and a system
// time (CreatedAt: when it was ingested). The facts extracted from it are edges between entities; each edge is
// anchored to its artifact (artifact_id) and carries its own event time (ValidAt / InvalidAt) and system time
// (CreatedAt / ExpiredAt). Entities are shared between artifacts, so they are linked to every artifact that
// mentions them (gb_entity_artifacts).
// See temporal.go for how facts are reconciled with what is already stored.
package memory

import "strings"

// Call types stored in gb_llm_calls.type.
const (
	CallRemember = "remember"
	CallRecall   = "recall"
)

//---------------------------------------------------------------------------------------------

func key(name string) string { return strings.ToLower(strings.Join(strings.Fields(name), " ")) }
