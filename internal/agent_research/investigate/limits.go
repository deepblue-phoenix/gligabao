package investigate

// Limits are the knobs of the investigation that stage 1 uses (the defaults of the design's Configuration section). Stage 2 passes
// run options in through the same struct.
type Limits struct {
	// MaxDepth is the deepest a question may be (the root is 0).
	MaxDepth int
	// MaxAttempts is the number of query formulations after which a question is unresolvable.
	MaxAttempts int
	// MaxQuestions is the most questions of a run.
	MaxQuestions int
	// NotesMaxTokens is the budget of the notes file (used by the size check only; the consolidation is stage 2).
	NotesMaxTokens int
	// MinSources and MinDomains are for the exploration gate (stage 2).
	MinSources, MinDomains int
	// MinIndependentSources is the number of supporting sources with distinct domains that VERIFIED needs.
	MinIndependentSources int
	// MinChallenges is the number of survived challenges that VERIFIED needs.
	MinChallenges int
	// MaxChallengesPerTarget is the most challenges on one target.
	MaxChallengesPerTarget int
	// MaxReopens is for finalize_report (stage 2).
	MaxReopens int
}

// DefaultLimits are the defaults of the design.
func DefaultLimits() Limits {
	return Limits{
		MaxDepth:               3,
		MaxAttempts:            4,
		MaxQuestions:           40,
		NotesMaxTokens:         6000,
		MinSources:             5,
		MinDomains:             3,
		MinIndependentSources:  2,
		MinChallenges:          1,
		MaxChallengesPerTarget: 3,
		MaxReopens:             1,
	}
}

// ScratchMaxChars is the bound on the Scratch section of the notes (characters); stage 2 uses the same bound for the consolidation.
const ScratchMaxChars = 8000
