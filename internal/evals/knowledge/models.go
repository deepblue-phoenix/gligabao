package knowledge

import (
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// The models of a run or of a case are one map, keyed by stage: the stages of remember (memory_core.Stages) and "query" for the
// LLM call that answers the question. "detect_geo" is a stage of both pipelines (the entities of a text, the places of a question):
// one setting is sent to both requests.

// ModelStages lists the keys a models map takes: the stages of remember, then the query.
func ModelStages() []string {
	return append(append([]string{}, memory_core.Stages...), stageQuery)
}

// splitModels validates m, a models map of the runner or of a case, and returns the part for the remember requests and the part
// for the query request.
func splitModels(m map[string]string) (remember, query memory_core.Models, err error) {
	rest, q := map[string]string{}, map[string]string{}
	for stage, model := range m {
		switch stage {
		case stageQuery:
			q[stage] = model
		case memory_core.StageDetectGeo:
			rest[stage], q[stage] = model, model
		default:
			rest[stage] = model
		}
	}
	if remember, err = memory_core.ResolveModels(rest); err != nil {
		return nil, nil, err
	}
	query, err = memory_core.ResolveQueryModels(q)
	return remember, query, err
}

// mergeModels combines the runner's -models with a case's models, stage by stage (the case wins, a case controls its own
// parameters), validates them and splits them into what the remember requests and the query request carry.
func mergeModels(flag, caseModels map[string]string) (remember, query memory_core.Models, err error) {
	merged := map[string]string{}
	for _, m := range []map[string]string{flag, caseModels} {
		for stage, model := range m {
			merged[stage] = model
		}
	}
	return splitModels(merged)
}

// ParseModels parses the -models flag, "extract_graph=claude-sonnet-4-5,query=claude-haiku-5-5", and validates it. An empty s
// gives nil.
func ParseModels(s string) (memory_core.Models, error) {
	m := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		stage, model, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("%q is not stage=model", part)
		}
		m[strings.TrimSpace(stage)] = strings.TrimSpace(model)
	}
	if _, _, err := splitModels(m); err != nil {
		return nil, err
	}
	// Dropped like memory_core.ResolveModels does: an empty model is the default.
	var out memory_core.Models
	for stage, model := range m {
		if model != "" {
			if out == nil {
				out = memory_core.Models{}
			}
			out[stage] = model
		}
	}
	return out, nil
}
