package investigate_core

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// ClaimStatus is the verification status of a claim or datapoint.
type ClaimStatus string

// The statuses of a claim.
const (
	ClaimUnverified ClaimStatus = "UNVERIFIED"
	ClaimVerified   ClaimStatus = "VERIFIED"
	ClaimRefuted    ClaimStatus = "REFUTED"
	ClaimRetracted  ClaimStatus = "RETRACTED"
)

// ClaimKind says whether a record is an atomic claim (c_ ids) or a numeric datapoint (d_ ids).
type ClaimKind string

// The kinds of claim.
const (
	KindClaim     ClaimKind = "claim"
	KindDatapoint ClaimKind = "datapoint"
)

// Stance is what a source says about a claim.
type Stance string

// The stances.
const (
	StanceSupports    Stance = "supports"
	StanceContradicts Stance = "contradicts"
)

// ErrNotVerifiable is returned by CanVerify, wrapped with what is missing.
var ErrNotVerifiable = errors.New("cannot be VERIFIED")

// Link ties a claim to a source. Independent is the agent's declaration that the source is not quoting another source already listed
// for the claim.
type Link struct {
	SourceID    string `json:"source_id"`
	Stance      Stance `json:"stance"`
	Independent bool   `json:"independent"`
}

// Claim is an atomic claim or a datapoint (a number: Value and Unit, Text is its context) with its sources.
type Claim struct {
	ID         string
	Kind       ClaimKind
	Text       string
	Value      string
	Unit       string
	Status     ClaimStatus
	QuestionID string
	Links      []Link
	// Check says what would verify it ("independent benchmark").
	Check string
	// Note holds the reason of a refutation, a retraction or a demotion.
	Note string
}

// Source is a page that was read: one per distinct URL.
type Source struct {
	ID     string
	URL    string
	Domain string
	Title  string
	Note   string
}

var secondLevel = []string{"co", "com", "org", "net", "gov", "edu", "ac", "or", "ne", "go", "gob", "nom", "ltd", "plc", "sch", "mil"}

// Domain is the registrable domain of a URL, the unit of the independence check. There is no public-suffix list in the repo, so the rule is
// simple: the host without a port and without a leading "www.", cut to its last two labels, or its last three when the last label is a
// two-letter country code and the one before is a generic second-level label (co.uk, com.au, ac.jp). An IP address or a single-label
// host is returned as it is. A text that is not a URL with a host gives "".
func Domain(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	labels := strings.Split(strings.TrimPrefix(host, "www."), ".")
	n := len(labels)
	isIP := strings.Contains(host, ":") || (n == 4 && labels[3] != "" && labels[3][0] >= '0' && labels[3][0] <= '9')
	switch {
	case isIP || n <= 2:
		return strings.Join(labels, ".")
	case len(labels[n-1]) == 2 && slices.Contains(secondLevel, labels[n-2]):
		return strings.Join(labels[n-3:], ".")
	}
	return strings.Join(labels[n-2:], ".")
}

// canonicalURL is the form of a URL under which two sources are the same: no fragment, no trailing slash.
func canonicalURL(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, "/")
}

// AddSource adds a source for a URL, or returns the existing one if the URL is known (one row per distinct URL). It returns the new list.
func AddSource(sources []Source, rawURL, title, note string) ([]Source, Source, error) {
	u := canonicalURL(rawURL)
	if u == "" || strings.ContainsAny(u, " \t\r\n|") {
		return sources, Source{}, fmt.Errorf("%w: %q is not a URL", ErrInvalid, rawURL)
	}
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return sources, Source{}, fmt.Errorf("%w: %q is not an http(s) URL", ErrInvalid, rawURL)
	}
	if i := slices.IndexFunc(sources, func(s Source) bool { return s.URL == u }); i >= 0 {
		return sources, sources[i], nil
	}
	ids := make([]string, len(sources))
	for i, s := range sources {
		ids[i] = s.ID
	}
	s := Source{ID: NextSourceID(ids), URL: u, Domain: Domain(u), Title: cleanTitle(title), Note: clean(note)}
	return append(slices.Clone(sources), s), s, nil
}

func findSource(sources []Source, id string) (Source, bool) {
	i := slices.IndexFunc(sources, func(s Source) bool { return s.ID == id })
	if i < 0 {
		return Source{}, false
	}
	return sources[i], true
}

// Support adds (or replaces) the link of a source to a claim, with the independence declaration.
func Support(c Claim, sourceID string, stance Stance, independent bool, sources []Source) (Claim, error) {
	if _, ok := findSource(sources, sourceID); !ok {
		return c, fmt.Errorf("%w: unknown source %q (add it to the Sources section first)", ErrInvalid, sourceID)
	}
	if stance == "" {
		stance = StanceSupports
	}
	if stance != StanceSupports && stance != StanceContradicts {
		return c, fmt.Errorf("%w: stance %q is not supports or contradicts", ErrInvalid, stance)
	}
	links := slices.DeleteFunc(slices.Clone(c.Links), func(l Link) bool { return l.SourceID == sourceID })
	c.Links = append(links, Link{SourceID: sourceID, Stance: stance, Independent: independent})
	return c, nil
}

// SupportingDomains are the distinct domains of the sources that support c with the independence declaration.
func SupportingDomains(c Claim, sources []Source) []string {
	var out []string
	for _, l := range c.Links {
		if l.Stance != StanceSupports || !l.Independent {
			continue
		}
		if s, ok := findSource(sources, l.SourceID); ok && s.Domain != "" && !slices.Contains(out, s.Domain) {
			out = append(out, s.Domain)
		}
	}
	return out
}

// CanVerify is the only check for VERIFIED, and every change of status to it goes through it: at least MinIndependentSources supporting
// sources, declared independent, with distinct domains, and at least MinChallenges closed challenges on the claim with outcome survived.
func CanVerify(c Claim, sources []Source, challenges []Challenge, limits Limits) error {
	if c.Status == ClaimRefuted || c.Status == ClaimRetracted {
		return fmt.Errorf("%s %w: it is %s", c.ID, ErrNotVerifiable, c.Status)
	}
	var problems []string
	if n := len(SupportingDomains(c, sources)); n < limits.MinIndependentSources {
		problems = append(problems, fmt.Sprintf("it has %d independent supporting source(s) with distinct domains, it needs %d (link sources with independent=true)", n, limits.MinIndependentSources))
	}
	survived := 0
	for _, ch := range challenges {
		if ch.TargetType == TargetClaim && ch.TargetID == c.ID && ch.Status == ChallengeClosed && ch.Outcome == OutcomeSurvived {
			survived++
		}
	}
	if survived < limits.MinChallenges {
		problems = append(problems, fmt.Sprintf("it has %d survived challenge(s), it needs %d (use the challenge tool on it)", survived, limits.MinChallenges))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s %w: %s", c.ID, ErrNotVerifiable, strings.Join(problems, "; "))
	}
	return nil
}

// Refute marks the claim REFUTED, with a contradicts link to the source that refutes it and the reason.
func Refute(c Claim, sourceID, reason string, sources []Source) (Claim, error) {
	if strings.TrimSpace(reason) == "" {
		return c, fmt.Errorf("%w: give the reason for refuting %s", ErrInvalid, c.ID)
	}
	if c.Status == ClaimRetracted {
		return c, fmt.Errorf("%w: %s is retracted", ErrBadStatus, c.ID)
	}
	c, err := Support(c, sourceID, StanceContradicts, true, sources)
	if err != nil {
		return c, err
	}
	c.Status = ClaimRefuted
	c.Note = clean(reason)
	return c, nil
}

// Retract marks the claim RETRACTED (the agent withdrew it), with the reason.
func Retract(c Claim, reason string) (Claim, error) {
	if strings.TrimSpace(reason) == "" {
		return c, fmt.Errorf("%w: give the reason for retracting %s", ErrInvalid, c.ID)
	}
	c.Status = ClaimRetracted
	c.Note = clean(reason)
	return c, nil
}
