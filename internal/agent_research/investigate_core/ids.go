// Package investigate is the state of the investigative deep-research loop (docs/agents/deep-research-loop.md), stage 1: the question
// frontier, the notes (claims, datapoints, sources, contradictions, assumptions), the challenges, their pure transitions, the tools
// the model will call, and the effect layer that stores them. The loop that uses them is not connected yet (stage 2).
//
// The files ids.go, limits.go, frontier.go, notes.go, claims.go, challenges.go, state.go, diff.go and tools.go are the pure core: values and
// functions of values, with no database, model or file system (a test checks their imports). effects.go, store.go and notes_file.go are the edge.
package investigate_core

import (
	"fmt"
	"strconv"
	"strings"
)

// The prefixes of the ids that code assigns (the model never does).
const (
	prefixQuestion  = "q_"
	prefixClaim     = "c_"
	prefixDatapoint = "d_"
	prefixSource    = "s_"
	prefixChallenge = "ch_"
)

// nextID is prefix and the number after the highest of the ids that have that prefix, zero-padded to 3 digits.
func nextID(prefix string, ids []string) string {
	highest := 0
	for _, id := range ids {
		if n := idNumber(prefix, id); n > highest {
			highest = n
		}
	}
	return fmt.Sprintf("%s%03d", prefix, highest+1)
}

// idNumber is the number of an id of the given prefix (q_007 is 7), or 0 if id does not have the form.
func idNumber(prefix, id string) int {
	rest, ok := strings.CutPrefix(id, prefix)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// NextQuestionID is the id of the next question of a run, given the ids it has: q_001 for the first (the root).
func NextQuestionID(ids []string) string { return nextID(prefixQuestion, ids) }

// NextClaimID is the id of the next claim (c_001, ...).
func NextClaimID(ids []string) string { return nextID(prefixClaim, ids) }

// NextDatapointID is the id of the next datapoint (d_001, ...).
func NextDatapointID(ids []string) string { return nextID(prefixDatapoint, ids) }

// NextSourceID is the id of the next source (s_001, ...).
func NextSourceID(ids []string) string { return nextID(prefixSource, ids) }

// NextChallengeID is the id of the next challenge (ch_001, ...).
func NextChallengeID(ids []string) string { return nextID(prefixChallenge, ids) }

// idLess orders ids of the same prefix by number (then by text), so that q_010 comes after q_009 and also past q_999.
func idLess(a, b string) bool {
	na, nb := trailingNumber(a), trailingNumber(b)
	if na != nb {
		return na < nb
	}
	return a < b
}

func idCompare(a, b string) int {
	switch {
	case idLess(a, b):
		return -1
	case idLess(b, a):
		return 1
	}
	return 0
}

func trailingNumber(id string) int {
	i := strings.LastIndexByte(id, '_')
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil {
		return 0
	}
	return n
}
