package knowledge

import (
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// stageQuery is the key of a reasoning (or models) map for the LLM call of the query that answers the question; the other keys
// are the stages of remember (memory_core.ReasoningStages).
const stageQuery = memory_core.StageQuery

// ReasoningStages lists the keys a reasoning map takes: the stages of remember that think, then the query.
func ReasoningStages() []string {
	return append(append([]string{}, memory_core.ReasoningStages...), stageQuery)
}

// splitReasoning validates m, a reasoning map of the runner or of a case (levels per stage, and "query" for the query), and returns
// the part for the remember requests and the part for the query request. "detect_geo" is a stage of both pipelines (the entities
// of a text, the places of a question): its level is sent to both.
func splitReasoning(m map[string]string) (remember, query memory_core.Reasoning, err error) {
	rest, q := map[string]string{}, map[string]string{}
	for stage, level := range m {
		switch stage {
		case stageQuery:
			q[stage] = level
		case memory_core.StageDetectGeo:
			rest[stage], q[stage] = level, level
		default:
			rest[stage] = level
		}
	}
	if remember, err = memory_core.ResolveReasoning(rest); err != nil {
		return nil, nil, err
	}
	query, err = memory_core.ResolveQueryReasoningStages(q)
	return remember, query, err
}

// mergeReasoning combines the runner's -reasoning with a case's reasoning levels, stage by stage (the case wins, like its
// models: a case controls its own parameters), validates them and splits them into what the remember requests and the
// query request carry.
func mergeReasoning(flag, caseLevels map[string]string) (remember, query memory_core.Reasoning, err error) {
	merged := map[string]string{}
	for _, m := range []map[string]string{flag, caseLevels} {
		for stage, level := range m {
			merged[stage] = level
		}
	}
	return splitReasoning(merged)
}

// ParseReasoning parses the -reasoning flag, "extract_graph=low,query=medium", and validates it. An empty s gives nil.
func ParseReasoning(s string) (map[string]string, error) {
	m := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		stage, level, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("%q is not stage=level", part)
		}
		m[strings.TrimSpace(stage)] = strings.TrimSpace(level)
	}
	if _, _, err := splitReasoning(m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}
