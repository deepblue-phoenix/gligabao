package investigate_core

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The gates are pure checks of the state: they return what is missing, and an empty list means pass. The exploration gate guards
// finish_exploration, the finalization gate guards finalize_report.

// ConclusionMarker starts the line of Scratch that states the conclusion of the run; the gate reads it and the conclusion challenge
// attacks it.
const ConclusionMarker = "Conclusion:"

// HasConclusion reports whether Scratch has a conclusion line.
func HasConclusion(n Notes) bool {
	return slices.ContainsFunc(strings.Split(n.Scratch, "\n"), func(l string) bool {
		return strings.HasPrefix(strings.TrimSpace(l), ConclusionMarker) && len(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), ConclusionMarker))) > 0
	})
}

func terminal(s Status) bool {
	return s == StatusResolved || s == StatusUnresolvable || s == StatusPruned
}

// ExplorationGate is what is missing before exploration may end (the design's exploration gate), in a fixed order.
func ExplorationGate(s State) []string {
	var missing []string
	f, lim := s.Frontier, s.Limits
	if root, ok := f.Root(); ok {
		switch {
		case root.Status == StatusResolved:
		case root.Status == StatusUnresolvable && strings.TrimSpace(root.Answer) != "":
		default:
			missing = append(missing, fmt.Sprintf("the root question %s is %s: resolve it (resolve_question), or make it unresolvable with a stated assumption", root.ID, root.Status))
		}
	}
	for _, q := range f.Questions {
		if f.Actionable(q) && q.Priority >= lim.GatePriority {
			missing = append(missing, fmt.Sprintf("%s is pending with priority %.2f (at or above %.2f): work on it, or prune it with a reason", q.ID, q.Priority, lim.GatePriority))
		}
	}
	domains := map[string]bool{}
	for _, src := range s.Notes.Sources {
		domains[src.Domain] = true
	}
	if len(s.Notes.Sources) < lim.MinSources {
		missing = append(missing, fmt.Sprintf("the notes have %d distinct sources, at least %d are needed", len(s.Notes.Sources), lim.MinSources))
	}
	if len(domains) < lim.MinDomains {
		missing = append(missing, fmt.Sprintf("the sources come from %d distinct domains, at least %d are needed", len(domains), lim.MinDomains))
	}
	for _, q := range f.Questions {
		if q.Status != StatusResolved {
			continue
		}
		cited := ClaimRefs(q.Answer)
		verified := slices.ContainsFunc(cited, func(id string) bool {
			c, ok := s.Notes.Claim(id)
			return ok && c.Status == ClaimVerified
		})
		if !verified && !strings.Contains(q.Answer, ReasonMarker) {
			missing = append(missing, fmt.Sprintf("%s is resolved without a VERIFIED claim: verify one of its claims, or resolve it again with the reason why UNVERIFIED is the best available", q.ID))
		}
	}
	for _, c := range s.Notes.Contradictions {
		if q, ok := f.Get(c.QuestionID); c.QuestionID == "" || !ok || !terminal(q.Status) {
			missing = append(missing, fmt.Sprintf("the contradiction %s vs %s is not addressed: resolve the question that settles it", c.A, c.B))
		}
	}
	for _, c := range OpenChallenges(s.Challenges) {
		missing = append(missing, fmt.Sprintf("challenge %s is open: run its search and call record_challenge_result", c.ID))
	}
	switch {
	case !HasConclusion(s.Notes):
		missing = append(missing, fmt.Sprintf("state the conclusion in a few sentences in Scratch (notes_scratch), on a line that starts with %q", ConclusionMarker))
	case !ConclusionSurvived(s.Challenges):
		missing = append(missing, "the conclusion has no closed challenge with outcome survived: challenge it (target_type conclusion), run the search and record the result")
	}
	return missing
}

var sourceRef = regexp.MustCompile(`\bs_\d+\b`)
var questionRef = regexp.MustCompile(`\bq_\d+\b`)

// SourceRefs are the source ids that text cites.
func SourceRefs(text string) []string { return dedupe(sourceRef.FindAllString(text, -1)) }

// FinalizationGate is what is wrong with the report (the design's finalization gate): it cites only existing source ids and existing
// claims, none of them REFUTED or RETRACTED, and every unresolvable question appears in it (under the gaps). Checked by ids in the text, not
// by reading the claims.
func FinalizationGate(s State, report string) []string {
	var problems []string
	if strings.TrimSpace(report) == "" {
		return []string{"the report is empty"}
	}
	for _, id := range SourceRefs(report) {
		if _, ok := findSource(s.Notes.Sources, id); !ok {
			problems = append(problems, fmt.Sprintf("the report cites %s, which is not a source in the notes", id))
		}
	}
	for _, id := range ClaimRefs(report) {
		c, ok := s.Notes.Claim(id)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("the report cites %s, which is not in the notes", id))
		case c.Status == ClaimRefuted || c.Status == ClaimRetracted:
			problems = append(problems, fmt.Sprintf("the report cites %s, which is %s", id, c.Status))
		}
	}
	cited := questionRef.FindAllString(report, -1)
	for _, q := range s.Frontier.Questions {
		if q.Status == StatusUnresolvable && !slices.Contains(cited, q.ID) {
			problems = append(problems, fmt.Sprintf("the unresolvable question %s (%s) is not in the report: list it under the gaps and assumptions", q.ID, truncate(q.Question, 80)))
		}
	}
	return problems
}

// RenderReport writes the source ids of the report as links to their URLs: "s_001" becomes "[s_001](url)" (an id that is already the
// text of a link is left alone). Unknown ids stay as they are (the gate refuses them before).
func RenderReport(report string, n Notes) string {
	idx := sourceRef.FindAllStringIndex(report, -1)
	var b strings.Builder
	last := 0
	for _, m := range idx {
		id := report[m[0]:m[1]]
		src, ok := findSource(n.Sources, id)
		inLink := m[0] > 0 && report[m[0]-1] == '[' && strings.HasPrefix(report[m[1]:], "](")
		b.WriteString(report[last:m[0]])
		if ok && !inLink {
			fmt.Fprintf(&b, "[%s](%s)", id, src.URL)
		} else {
			b.WriteString(id)
		}
		last = m[1]
	}
	b.WriteString(report[last:])
	return b.String()
}

// FinalCheck is the check at the end of an exhausted frontier: every UNVERIFIED claim of a resolved question gets a question to verify it
// (queued by the code, at priority 0.6). A claim that already has a verification question (same text) is not queued again, so the check
// ends. It returns the new state, the transitions and the ids of the questions made.
func FinalCheck(s State) (State, []Transition, []string) {
	var ts []Transition
	var ids []string
	for _, c := range s.Notes.Claims {
		q, ok := s.Frontier.Get(c.QuestionID)
		if c.Status != ClaimUnverified || !ok || q.Status != StatusResolved {
			continue
		}
		f, added, err := Add(s.Frontier, q.ID, AddInput{
			Question:           fmt.Sprintf("Verify claim %s: %s", c.ID, c.Text),
			Rationale:          "an UNVERIFIED claim behind a resolved question: the final check queues it for verification",
			Priority:           0.6,
			ValidationCriteria: fmt.Sprintf("%d independent sources and a survived challenge, or a reason why the claim cannot be verified", s.Limits.MinIndependentSources),
			Actor:              ActorCode,
		}, s.Limits)
		if err != nil {
			continue
		}
		s.Frontier, ts, ids = f, append(ts, added...), append(ids, added[0].QuestionID)
	}
	return s, ts, ids
}
