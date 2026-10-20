package knowledge

import (
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// stageRecall is the key of a reasoning map for the LLM call of the recall that answers the question; the other keys
// are the stages of remember (memory_core.ReasoningStages).
const stageRecall = memory_core.StageRecall

// ReasoningStages lists the keys a reasoning map takes: the stages of remember that think, then the recall.
func ReasoningStages() []string {
	return append(append([]string{}, memory_core.ReasoningStages...), stageRecall)
}

// splitReasoning validates m, a reasoning map of the runner or of a case (levels per stage, and "recall" for the recall),
// and returns the part for the remember requests and the level for the recall request ("" if m names none).
func splitReasoning(m map[string]string) (remember memory_core.Reasoning, recall string, err error) {
	rest := map[string]string{}
	for stage, level := range m {
		if stage == stageRecall {
			r, err := memory_core.ResolveRecallReasoning(level)
			if err != nil {
				return nil, "", err
			}
			recall = r[stageRecall]
			continue
		}
		rest[stage] = level
	}
	remember, err = memory_core.ResolveReasoning(rest)
	return remember, recall, err
}

// mergeReasoning combines the runner's -reasoning with a case's reasoning levels, stage by stage (the case wins, like its
// models: a case controls its own parameters), validates them and splits them into what the remember requests and the
// recall request carry.
func mergeReasoning(flag, caseLevels map[string]string) (remember memory_core.Reasoning, recall string, err error) {
	merged := map[string]string{}
	for _, m := range []map[string]string{flag, caseLevels} {
		for stage, level := range m {
			merged[stage] = level
		}
	}
	return splitReasoning(merged)
}

// ParseReasoning parses the -reasoning flag, "extract_graph=low,recall=medium", and validates it. An empty s gives nil.
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
