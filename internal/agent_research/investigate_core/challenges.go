package investigate_core

import (
	"fmt"
	"slices"
	"strings"
)

// TargetType is what a challenge attacks.
type TargetType string

// The targets of a challenge.
const (
	TargetClaim          TargetType = "claim"
	TargetQuestionAnswer TargetType = "question_answer"
	TargetConclusion     TargetType = "conclusion"
)

// Strategy is how a challenge attacks: the closed vocabulary of the design.
type Strategy string

// The strategies.
const (
	StrategyNegation          Strategy = "negation"
	StrategyDiscrepancy       Strategy = "discrepancy"
	StrategyCriticism         Strategy = "criticism"
	StrategyAlternativeMethod Strategy = "alternative_method"
	StrategyScopeLimit        Strategy = "scope_limit"
	StrategyPrimarySource     Strategy = "primary_source"
	StrategyStaleness         Strategy = "staleness"
)

// Strategies lists the seven strategies.
var Strategies = []Strategy{StrategyNegation, StrategyDiscrepancy, StrategyCriticism, StrategyAlternativeMethod, StrategyScopeLimit, StrategyPrimarySource, StrategyStaleness}

// strategyTerms are words a query of each strategy is expected to carry: they count as new terms against the text of the target.
var strategyTerms = map[Strategy][]string{
	StrategyNegation:          {"not", "no", "without", "false", "incorrect"},
	StrategyDiscrepancy:       {"conflicting", "discrepancy", "different", "inconsistent", "mismatch"},
	StrategyCriticism:         {"criticism", "problem", "issue", "errata", "retraction", "flaw", "bug"},
	StrategyAlternativeMethod: {"independent", "reproduction", "alternative", "replication", "benchmark"},
	StrategyScopeLimit:        {"only", "limitations", "unless", "fails", "limit"},
	StrategyPrimarySource:     {"original", "primary", "source", "official"},
	StrategyStaleness:         {"deprecated", "superseded", "outdated", "newer", "latest", "updated"},
}

// ChallengeStatus is open until a verdict is recorded, then closed.
type ChallengeStatus string

// The statuses of a challenge.
const (
	ChallengeOpen   ChallengeStatus = "open"
	ChallengeClosed ChallengeStatus = "closed"
)

// Outcome is the verdict of a challenge ("" while it is open).
type Outcome string

// The outcomes.
const (
	OutcomeSurvived     Outcome = "survived"
	OutcomeWeakened     Outcome = "weakened"
	OutcomeRefuted      Outcome = "refuted"
	OutcomeInconclusive Outcome = "inconclusive"
)

// Challenge is one adversarial query on a target (a row of gb_agent_challenges).
type Challenge struct {
	ID         string
	QuestionID string
	TargetType TargetType
	// TargetID is the claim id or the question id; "" for the conclusion.
	TargetID        string
	Negation        string
	Strategy        Strategy
	ExpectedIfFalse string
	Query           string
	// AttemptSeq is the seq, in the attempt log of QuestionID, of the attempt that executed it (0 until it runs); the effect layer links it.
	AttemptSeq    int
	Status        ChallengeStatus
	Outcome       Outcome
	Evidence      string
	SourceIDs     []string
	ResultClaimID string
}

// OpenInput is what the model gives to the challenge tool.
type OpenInput struct {
	TargetType      TargetType
	TargetID        string
	Negation        string
	Strategy        Strategy
	ExpectedIfFalse string
	Query           string
}

// conclusionQuestion is the question a challenge of the conclusion is filed under: the root.
func conclusionQuestion(f Frontier) string {
	if r, ok := f.Root(); ok {
		return r.ID
	}
	return ""
}

// Open validates a new challenge and returns it, open, with the next id. It runs nothing. The rules, in this order: the target exists; the
// strategy is one of the seven; negation, expected_if_false and the query are not empty; the normalized query differs from every earlier
// query of the question and of earlier challenges on the same target; the strategy was not used on the target before; the target has fewer
// than MaxChallengesPerTarget challenges; the query has at least one term of the negation or the strategy that the text of the target does
// not have.
func Open(challenges []Challenge, in OpenInput, f Frontier, n Notes, limits Limits) ([]Challenge, Challenge, error) {
	var targetText, questionID string
	switch in.TargetType {
	case TargetClaim:
		c, ok := n.Claim(in.TargetID)
		if !ok {
			return challenges, Challenge{}, fmt.Errorf("%w: unknown claim %q", ErrInvalid, in.TargetID)
		}
		targetText, questionID = c.Text+" "+c.Value+" "+c.Unit, c.QuestionID
		if _, ok := f.Get(questionID); !ok {
			questionID = currentQuestionID(f)
		}
	case TargetQuestionAnswer:
		q, ok := f.Get(in.TargetID)
		if !ok {
			return challenges, Challenge{}, fmt.Errorf("%w: unknown question %q", ErrUnknownQuestion, in.TargetID)
		}
		targetText, questionID = q.Question+" "+q.Answer, q.ID
	case TargetConclusion:
		if strings.TrimSpace(n.Scratch) == "" {
			return challenges, Challenge{}, fmt.Errorf("%w: there is no conclusion to challenge: state it in a few sentences with notes_scratch first", ErrInvalid)
		}
		in.TargetID = ""
		targetText, questionID = n.Scratch, conclusionQuestion(f)
	default:
		return challenges, Challenge{}, fmt.Errorf("%w: target_type %q is not claim, question_answer or conclusion", ErrInvalid, in.TargetType)
	}
	if !slices.Contains(Strategies, in.Strategy) {
		return challenges, Challenge{}, fmt.Errorf("%w: strategy %q is not one of %v", ErrInvalid, in.Strategy, Strategies)
	}
	if strings.TrimSpace(in.Negation) == "" || strings.TrimSpace(in.ExpectedIfFalse) == "" || strings.TrimSpace(in.Query) == "" {
		return challenges, Challenge{}, fmt.Errorf("%w: negation, expected_if_false and query must not be empty", ErrInvalid)
	}
	norm := Normalize(in.Query)
	if q, ok := f.Get(questionID); ok {
		for _, a := range q.Attempts {
			if Normalize(a.Query) == norm {
				return challenges, Challenge{}, fmt.Errorf("%w: the query repeats attempt %d of %s: an adversarial query must be different", ErrDuplicateQuestion, a.Seq, q.ID)
			}
		}
	}
	same := ChallengesOn(challenges, in.TargetType, in.TargetID)
	for _, c := range same {
		if Normalize(c.Query) == norm {
			return challenges, Challenge{}, fmt.Errorf("%w: the query repeats challenge %s on the same target", ErrDuplicateQuestion, c.ID)
		}
		if c.Strategy == in.Strategy {
			return challenges, Challenge{}, fmt.Errorf("%w: strategy %s was already used on this target (%s): use another", ErrInvalid, in.Strategy, c.ID)
		}
	}
	if len(same) >= limits.MaxChallengesPerTarget {
		return challenges, Challenge{}, fmt.Errorf("%w: the target already has %d challenges (the most is %d)", ErrInvalid, len(same), limits.MaxChallengesPerTarget)
	}
	if !hasNewTerm(in, targetText) {
		return challenges, Challenge{}, fmt.Errorf("%w: the query is not adversarial: it has no term of the negation or of the %s strategy that the target does not already have", ErrInvalid, in.Strategy)
	}
	ids := make([]string, len(challenges))
	for i, c := range challenges {
		ids[i] = c.ID
	}
	ch := Challenge{
		ID:              NextChallengeID(ids),
		QuestionID:      questionID,
		TargetType:      in.TargetType,
		TargetID:        in.TargetID,
		Negation:        strings.TrimSpace(in.Negation),
		Strategy:        in.Strategy,
		ExpectedIfFalse: strings.TrimSpace(in.ExpectedIfFalse),
		Query:           strings.TrimSpace(in.Query),
		Status:          ChallengeOpen,
	}
	return append(slices.Clone(challenges), ch), ch, nil
}

// hasNewTerm is the cheap string test of rule 4: some word of the query is a word of the negation or a word of the strategy, and not a
// word of the target's text.
func hasNewTerm(in OpenInput, targetText string) bool {
	target := strings.Fields(Normalize(targetText))
	wanted := append(strings.Fields(Normalize(in.Negation)), strategyTerms[in.Strategy]...)
	return slices.ContainsFunc(strings.Fields(Normalize(in.Query)), func(w string) bool {
		return slices.Contains(wanted, w) && !slices.Contains(target, w)
	})
}

func currentQuestionID(f Frontier) string {
	if q, ok := f.Current(); ok {
		return q.ID
	}
	return conclusionQuestion(f)
}

// Close records the verdict of an open challenge. weakened and refuted need evidence and at least one source id; survived needs the
// challenge to have been executed (AttemptSeq set); a challenge closes once.
func Close(challenges []Challenge, id string, outcome Outcome, evidence string, sourceIDs []string, resultClaimID string) ([]Challenge, Challenge, error) {
	i := slices.IndexFunc(challenges, func(c Challenge) bool { return c.ID == id })
	if i < 0 {
		return challenges, Challenge{}, fmt.Errorf("%w: unknown challenge %q", ErrInvalid, id)
	}
	c := challenges[i]
	if c.Status == ChallengeClosed {
		return challenges, Challenge{}, fmt.Errorf("%w: challenge %s is already closed (%s)", ErrBadStatus, id, c.Outcome)
	}
	evidence = strings.TrimSpace(evidence)
	switch outcome {
	case OutcomeSurvived:
		if c.AttemptSeq == 0 {
			return challenges, Challenge{}, fmt.Errorf("%w: challenge %s was never executed: run its query with web_search first, a challenge cannot survive without a search", ErrInvalid, id)
		}
	case OutcomeWeakened, OutcomeRefuted:
		if evidence == "" || len(sourceIDs) == 0 {
			return challenges, Challenge{}, fmt.Errorf("%w: %s needs evidence and at least one source id", ErrInvalid, outcome)
		}
	case OutcomeInconclusive:
	default:
		return challenges, Challenge{}, fmt.Errorf("%w: outcome %q is not survived, weakened, refuted or inconclusive", ErrInvalid, outcome)
	}
	c.Status, c.Outcome, c.Evidence, c.SourceIDs, c.ResultClaimID = ChallengeClosed, outcome, evidence, dedupe(sourceIDs), resultClaimID
	out := slices.Clone(challenges)
	out[i] = c
	return out, c, nil
}

// LinkAttempt records that the search of an open challenge ran: its query is added to the attempt log of its question (with the tool and a
// one-line outcome) and the challenge points to that attempt. The effect layer calls it after the search; Open only validates.
func LinkAttempt(f Frontier, challenges []Challenge, id, tool, outcome string) (Frontier, []Challenge, error) {
	i := slices.IndexFunc(challenges, func(c Challenge) bool { return c.ID == id })
	if i < 0 {
		return f, challenges, fmt.Errorf("%w: unknown challenge %q", ErrInvalid, id)
	}
	c := challenges[i]
	if c.Status != ChallengeOpen || c.AttemptSeq != 0 {
		return f, challenges, fmt.Errorf("%w: challenge %s is not open and unexecuted", ErrBadStatus, id)
	}
	f, a, err := AddAttempt(f, c.QuestionID, Attempt{
		Query:   c.Query,
		Tool:    tool,
		Outcome: outcome,
	})
	if err != nil {
		return f, challenges, err
	}
	c.AttemptSeq = a.Seq
	out := slices.Clone(challenges)
	out[i] = c
	return f, out, nil
}

// ChallengesOn are the challenges on a target (the conclusion has target id "").
func ChallengesOn(challenges []Challenge, t TargetType, targetID string) []Challenge {
	return slices.DeleteFunc(slices.Clone(challenges), func(c Challenge) bool { return c.TargetType != t || c.TargetID != targetID })
}

// OpenChallenges are the challenges still waiting for a verdict.
func OpenChallenges(challenges []Challenge) []Challenge {
	return slices.DeleteFunc(slices.Clone(challenges), func(c Challenge) bool { return c.Status != ChallengeOpen })
}

// RequiresAnswerChallenge reports whether resolving the question needs a challenge of its answer: the questions of depth 0 and 1, which carry
// the conclusion.
func RequiresAnswerChallenge(q Question) bool { return q.Depth <= 1 }

// AnswerChallengeClosed reports whether a question_answer challenge on the question is closed and none is open.
func AnswerChallengeClosed(challenges []Challenge, questionID string) bool {
	on := ChallengesOn(challenges, TargetQuestionAnswer, questionID)
	return slices.ContainsFunc(on, func(c Challenge) bool { return c.Status == ChallengeClosed }) &&
		!slices.ContainsFunc(on, func(c Challenge) bool { return c.Status == ChallengeOpen })
}

// ConclusionSurvived reports whether the conclusion has a closed challenge with outcome survived (for the exploration gate of stage 2).
func ConclusionSurvived(challenges []Challenge) bool {
	return slices.ContainsFunc(ChallengesOn(challenges, TargetConclusion, ""), func(c Challenge) bool { return c.Outcome == OutcomeSurvived })
}

// ChallengeSummary is a compact text of the open challenges, for the prompt (so the agent cannot forget to close one).
func ChallengeSummary(challenges []Challenge) string {
	var b strings.Builder
	for _, c := range OpenChallenges(challenges) {
		target := string(c.TargetType)
		if c.TargetID != "" {
			target += " " + c.TargetID
		}
		fmt.Fprintf(&b, "Open challenge [%s] on %s (%s): %q\n", c.ID, target, c.Strategy, c.Query)
	}
	return b.String()
}

// SortChallenges orders challenges by id number (for the store that loads them).
func SortChallenges(challenges []Challenge) []Challenge {
	out := slices.Clone(challenges)
	slices.SortFunc(out, func(a, b Challenge) int { return idCompare(a.ID, b.ID) })
	return out
}
