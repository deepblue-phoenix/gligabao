package investigate_core

import (
	"fmt"
	"math"
	"strings"
)

// Breaker is a limit of the run that was reached.
type Breaker struct {
	// Name is "iterations", "tokens" or "cost"; Limit and Used are in the unit of the breaker (turns, tokens, USD), Limit being the limit
	// that applied (the reserve already taken off in exploration).
	Name        string
	Limit, Used float64
}

// StopReason is the stop reason the breaker gives ("breaker:cost").
func (b Breaker) StopReason() string { return "breaker:" + b.Name }

func (b Breaker) String() string {
	return fmt.Sprintf("breaker: %s (%.4g of %.4g)", b.Name, b.Used, b.Limit)
}

// Reserve arithmetic: exploration may spend the limit minus the synthesis reserve, synthesis the whole limit.

// ExplorationLimit is what exploration may spend of limit; a limit of 0 (none) stays 0.
func ExplorationLimit(limit, reserve float64) float64 { return limit * (1 - reserve) }

// ExplorationTurns is the number of turns exploration may take of the limit (rounded down, at least 1 when there is a limit).
func ExplorationTurns(maxIterations int, reserve float64) int {
	if maxIterations <= 0 {
		return 0
	}
	return max(1, int(math.Floor(ExplorationLimit(float64(maxIterations), reserve))))
}

// CheckBreakers returns the breakers that fired, checked before every model call: the turns made, the tokens and the cost against the
// limits (those of exploration leave the synthesis reserve; those of synthesis are the whole limits). A limit of 0 is no limit. The
// per-question attempt cap, the question cap and the depth cap are not here: the frontier enforces them (stage 1) and they are not stops.
func CheckBreakers(ls LoopState, limits Limits) []Breaker {
	reserve := limits.SynthesisReserve
	if ls.Phase == PhaseSynthesis {
		reserve = 0
	}
	var out []Breaker
	if limits.MaxIterations > 0 {
		limit := float64(limits.MaxIterations)
		if reserve > 0 {
			limit = float64(ExplorationTurns(limits.MaxIterations, reserve))
		}
		if float64(ls.Turns) >= limit {
			out = append(out, Breaker{"iterations", limit, float64(ls.Turns)})
		}
	}
	if limits.MaxTotalTokens > 0 {
		if limit := ExplorationLimit(float64(limits.MaxTotalTokens), reserve); float64(ls.Tokens) >= limit {
			out = append(out, Breaker{"tokens", limit, float64(ls.Tokens)})
		}
	}
	if limits.MaxCostUSD > 0 {
		if limit := ExplorationLimit(limits.MaxCostUSD, reserve); ls.CostUSD >= limit {
			out = append(out, Breaker{"cost", limit, ls.CostUSD})
		}
	}
	return out
}

// IsLastTurn reports whether the next model call is the last the run may make (the turn limit is reached by it): in synthesis it is forced
// to be finalize_report.
func IsLastTurn(ls LoopState, limits Limits) bool {
	return limits.MaxIterations > 0 && ls.Turns+1 >= limits.MaxIterations
}

// CutShort is what the loop writes when a breaker ends exploration: a line in Scratch saying so, and the questions still open as gaps in
// the notes, so that the synthesis (and the report) says what was not covered. The questions that are pending, blocked or in progress are
// listed; it never fails the run: a note that does not fit is cut.
func CutShort(s State, b Breaker) State {
	line := fmt.Sprintf("The investigation was cut short by %s.", b)
	scratch := strings.TrimSpace(s.Notes.Scratch + "\n" + line)
	n, err := SetScratch(s.Notes, scratch)
	if err != nil { // too long: keep the end, which has the line
		r := []rune(scratch)
		n, err = SetScratch(s.Notes, string(r[max(0, len(r)-ScratchMaxChars/2):]))
	}
	if err == nil {
		s.Notes = n
	}
	for _, q := range s.Frontier.Questions {
		switch q.Status {
		case StatusPending, StatusBlocked, StatusInProgress:
			if q.ParentID == "" {
				continue
			}
			if n, _, err := Append(s.Notes, SectionAssumptions, []Entry{{Text: fmt.Sprintf("open gap (%s), the investigation was cut short: %s", q.ID, q.Question)}}); err == nil {
				s.Notes = n
			}
		}
	}
	return s
}
