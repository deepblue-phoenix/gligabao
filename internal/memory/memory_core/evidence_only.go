package memory_core

// WithEvidenceOnly makes Query a retrieval-only call: it retrieves the subgraph and the passages close to the question as usual, but does
// not ask the LLM to write the answer (the query stage is skipped, so that call is not made nor paid for). The result has an empty
// answer, and as evidence everything that was retrieved (every entity, edge and passage of the context), not only what an LLM cited.
// It is for callers that have an LLM of their own. With geo on, the places of the question are only looked for (one LLM call) when the
// call also asks for the proximity search (WithGeoNearby).
func WithEvidenceOnly(on bool) Option { return func(o *CallOptions) { o.EvidenceOnly = on } }
