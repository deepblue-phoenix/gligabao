package memory_remember

// What the stages of the pipeline file in the run log as outputs, for debugging (see runLog.Output and
// docs/remember.md#stage-outputs). The shapes are built here, next to each other, so that they stay consistent: the
// trajectory (see memory_core.Trace) shows the same steps, but only when the call was made with WithEval, and outside
// the database.

import (
	"sort"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// stepLabel says what the text that a step runs on is (see runLog.Output): "chunk 2 of 5", `web search for "Berlin"`,
// `observation of series "gdp"`; "" for the text the caller sent.
func (j *job) stepLabel(part *chunkPart) string {
	return strings.Trim(part.label()+j.web.label()+j.obs.label(), " ()")
}

// outputExtraction files the output of the extract_graph stage. ex may be the empty extraction that comes with a failure
// (its raw answer is what says why); nil if the call itself failed, which has nothing to file.
func (j *job) outputExtraction(label string, ex *Extraction) {
	if ex == nil {
		return
	}
	entities := ex.Entities
	if entities == nil {
		entities = []Entity{}
	}
	j.run.Output(memory_core.StageExtractGraph, "extract_graph", label,
		map[string]any{"model": ex.Model, "entities": entities, "edges": candidateFacts(ex.Edges, nil), "raw": ex.Raw})
}

// outputCandidates files the output of the load_candidates stage: what each lookup found, and the merge.
func (j *job) outputCandidates(label string, ex *Extraction, cs *candidates) {
	matches := make([]map[string]any, 0, len(cs.Similar))
	for _, f := range cs.Similar {
		matches = append(matches, map[string]any{"endpoint": f.endpoint, "entity": f.name, "type": f.typ, "score": f.score})
	}
	similar := map[string]any{"searched": cs.SearchTexts, "matches": matches, "facts": candidateFacts(cs.BySimilar, nil)}
	if cs.SearchErr != nil {
		similar["error"] = cs.SearchErr.Error() // not fatal: the candidates then come from the exact names only
	}
	j.run.Output(StageLoadCandidates, "load_candidates", label, map[string]any{
		"new_edges":     len(ex.Edges),
		"by_name":       map[string]any{"names": cs.Names, "facts": candidateFacts(cs.ByName, nil)},
		"by_similarity": similar,
		"candidates":    candidateFacts(cs.Edges, cs.Via),
	})
}

// outputResolveEdges files the output of the resolve_edges stage: the new facts (the numbers in the decisions are their
// positions), what was decided, and what the LLM answered (nothing if it was not asked: no candidates, or all repeats). plan
// is nil if the LLM call itself failed.
func (j *job) outputResolveEdges(label string, ex *Extraction, plan *EdgePlan) {
	out := map[string]any{"new_edges": candidateFacts(ex.Edges, nil)}
	if plan != nil {
		dups := make([]map[string]any, 0, len(plan.Duplicates))
		for n, id := range plan.Duplicates {
			dups = append(dups, map[string]any{"new": n, "existing": id})
		}
		sort.Slice(dups, func(a, b int) bool { return dups[a]["new"].(int) < dups[b]["new"].(int) })
		contradictions := make([]map[string]any, 0, len(plan.Contradictions))
		for _, c := range plan.Contradictions {
			contradictions = append(contradictions, map[string]any{"new": c.New, "existing": c.Existing})
		}
		out["duplicates"], out["contradictions"] = dups, contradictions
		out["llm_called"], out["raw"] = plan.Raw != "", plan.Raw
	}
	j.run.Output(memory_core.StageResolveEdges, "resolve_edges", label, out)
}
