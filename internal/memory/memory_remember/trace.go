package memory_remember

import (
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// Trace steps of the text pipeline that need its types (see memory_core.Trace for the generic ones).

// traceEmbed records an embedding request as a tool step with its tokens and cost, so they count in the trajectory's totals.
func traceEmbed(t *memory_core.Trace, texts []string, entities, edges int, chunk bool, usage *ai.EmbedUsage) {
	msg := "Embedded the entities and edges stored by this artifact."
	args := map[string]any{"entities": entities, "edges": edges, "texts": texts}
	if chunk {
		msg = "Embedded the entities and edges stored by this artifact, and the chunk's text."
		args["chunk"] = true
	}
	t.EmbedStep(msg, "embed", args, map[string]any{"vectors": len(texts)}, usage)
}

// traceSearchEntities records the similarity search that adds candidate facts: the embedded endpoints and what they matched.
func traceSearchEntities(t *memory_core.Trace, texts []string, found []similarEntity, facts []map[string]any, usage *ai.EmbedUsage, err error) {
	if t == nil || len(texts) == 0 {
		return
	}
	matches := make([]map[string]any, 0, len(found))
	for _, f := range found {
		matches = append(matches, map[string]any{"endpoint": f.endpoint, "entity": f.name, "type": f.typ, "score": f.score})
	}
	res := map[string]any{"matches": matches, "candidates": len(facts), "facts": facts}
	if err != nil {
		res["error"] = err.Error() // not fatal: the candidates then come from the exact names only
	}
	t.EmbedStep("Embedded the endpoints of the new facts and searched the dataset's entity index for similar stored "+
		"entities (aliases); their live facts are the candidates found by similarity.", "search_similar_entities",
		map[string]any{"texts": texts, "top_k": similarEntityTopK, "min_score": similarEntityMinScore}, res, usage)
}
