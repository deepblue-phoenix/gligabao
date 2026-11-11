package investigate

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Section is a section of the notes file.
type Section string

// The fixed sections of the notes, in the order of the file.
const (
	SectionVerified       Section = "Verified"
	SectionUnverified     Section = "Unverified"
	SectionContradictions Section = "Contradictions"
	SectionDatapoints     Section = "Datapoints"
	SectionSources        Section = "Sources"
	SectionAssumptions    Section = "Assumptions & gaps"
	SectionScratch        Section = "Scratch"
)

var sectionOrder = []Section{SectionVerified, SectionUnverified, SectionContradictions, SectionDatapoints, SectionSources, SectionAssumptions, SectionScratch}

// ParseSection reads the name of a section as the model may write it (case and "&" free: "assumptions", "gaps").
func ParseSection(name string) (Section, bool) {
	switch Normalize(name) {
	case "verified":
		return SectionVerified, true
	case "unverified":
		return SectionUnverified, true
	case "contradictions":
		return SectionContradictions, true
	case "datapoints":
		return SectionDatapoints, true
	case "sources":
		return SectionSources, true
	case "assumptions gaps", "assumptions", "gaps":
		return SectionAssumptions, true
	case "scratch":
		return SectionScratch, true
	}
	return "", false
}

// Contradiction is a conflict between two claims, with the question spawned to resolve it ("" if none).
type Contradiction struct {
	A, B       string
	QuestionID string
}

// Assumption is an unresolvable question and the assumption made, or (QuestionID "") an open gap.
type Assumption struct {
	QuestionID string
	Text       string
}

// Notes is the value behind notes.md: the claims, datapoints and sources of the run and the rest of the fixed sections. Claims,
// Datapoints and Sources are in id order. It is immutable: the functions return new values and never touch the file (the effect layer does).
type Notes struct {
	Objective      string
	Claims         []Claim
	Datapoints     []Claim
	Sources        []Source
	Contradictions []Contradiction
	Assumptions    []Assumption
	Scratch        string
}

// Entry is one record that notes_append adds; which fields are used depends on the section.
type Entry struct {
	// Unverified (a claim) and Datapoints (Text is the context): Text, QuestionID, Sources, Check; Datapoints also Value and Unit.
	Text       string `json:"text"`
	Value      string `json:"value"`
	Unit       string `json:"unit"`
	QuestionID string `json:"question_id"`
	Sources    []Link `json:"sources"`
	Check      string `json:"check"`
	// Sources: URL, Title, Note.
	URL   string `json:"url"`
	Title string `json:"title"`
	Note  string `json:"note"`
	// Contradictions: the two claim ids (and QuestionID, the question spawned).
	A string `json:"a"`
	B string `json:"b"`
}

// Change is what notes_update changes in a record.
type Change struct {
	// Status is the new status of a claim: VERIFIED (checked by CanVerify), REFUTED (needs SourceID and Reason), RETRACTED (needs Reason), or
	// UNVERIFIED to demote a VERIFIED claim (needs Reason).
	Status   ClaimStatus `json:"status"`
	SourceID string      `json:"source_id"`
	Reason   string      `json:"reason"`
	// AddSource links a source to the claim.
	AddSource *Link `json:"add_source"`
	// Text amends the text of a claim that is not VERIFIED; Check and Note replace those fields; for a source, Title and Note.
	Text  string `json:"text"`
	Check string `json:"check"`
	Note  string `json:"note"`
	Title string `json:"title"`
}

// clean makes a text fit the one-line grammar of the notes: one line, single spaces, no "|" (it separates the fields).
func clean(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "|", "/")), " ")
}

func cleanTitle(s string) string { return clean(strings.ReplaceAll(s, `"`, "'")) }

// ClaimIDs are the ids of the claims and datapoints.
func (n Notes) ClaimIDs() []string {
	var ids []string
	for _, c := range n.Claims {
		ids = append(ids, c.ID)
	}
	for _, d := range n.Datapoints {
		ids = append(ids, d.ID)
	}
	return ids
}

// Claim finds a claim or datapoint by id.
func (n Notes) Claim(id string) (Claim, bool) {
	for _, list := range [][]Claim{n.Claims, n.Datapoints} {
		if i := slices.IndexFunc(list, func(c Claim) bool { return c.ID == id }); i >= 0 {
			return list[i], true
		}
	}
	return Claim{}, false
}

// withClaim returns n with c replacing the claim or datapoint of its id.
func (n Notes) withClaim(c Claim) Notes {
	replace := func(list []Claim) []Claim {
		i := slices.IndexFunc(list, func(x Claim) bool { return x.ID == c.ID })
		if i < 0 {
			return list
		}
		out := slices.Clone(list)
		out[i] = c
		return out
	}
	n.Claims, n.Datapoints = replace(n.Claims), replace(n.Datapoints)
	return n
}

func (n Notes) checkLinks(links []Link) error {
	for _, l := range links {
		if _, ok := findSource(n.Sources, l.SourceID); !ok {
			return fmt.Errorf("%w: unknown source %q (add it to the Sources section first)", ErrInvalid, l.SourceID)
		}
		if l.Stance != "" && l.Stance != StanceSupports && l.Stance != StanceContradicts {
			return fmt.Errorf("%w: stance %q is not supports or contradicts", ErrInvalid, l.Stance)
		}
	}
	return nil
}

// withLinks returns c with the links added (stance defaults to supports).
func withLinks(c Claim, links []Link, sources []Source) (Claim, error) {
	for _, l := range links {
		var err error
		if c, err = Support(c, l.SourceID, l.Stance, l.Independent, sources); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Append adds the entries to a section, assigning their ids (c_, d_, s_ with the functions of ids.go), and returns the new notes and the id
// of each entry (for Sources, the id of the existing source when the URL is known). It is all or nothing. Verified takes no entries (a claim
// starts UNVERIFIED and is promoted with Update) and Scratch is replaced with SetScratch.
func Append(n Notes, section Section, entries []Entry) (Notes, []string, error) {
	if len(entries) == 0 {
		return n, nil, fmt.Errorf("%w: no entries", ErrInvalid)
	}
	orig := n
	var ids []string
	for i, e := range entries {
		var id string
		var err error
		switch section {
		case SectionUnverified:
			n, id, err = appendClaim(n, e)
		case SectionDatapoints:
			n, id, err = appendDatapoint(n, e)
		case SectionSources:
			var s Source
			var sources []Source
			if sources, s, err = AddSource(n.Sources, e.URL, e.Title, e.Note); err == nil {
				n.Sources, id = sources, s.ID
			}
		case SectionContradictions:
			id, err = "", checkContradiction(n, e)
			if err == nil {
				n.Contradictions = append(slices.Clone(n.Contradictions), Contradiction{A: e.A, B: e.B, QuestionID: e.QuestionID})
				id = e.A + " vs " + e.B
			}
		case SectionAssumptions:
			text := clean(e.Text)
			if text == "" {
				err = fmt.Errorf("%w: the text is empty", ErrInvalid)
			} else {
				n.Assumptions = append(slices.Clone(n.Assumptions), Assumption{QuestionID: e.QuestionID, Text: text})
				id = "assumption"
			}
		case SectionVerified:
			err = fmt.Errorf("%w: nothing is appended to Verified: add the claim to Unverified, then notes_update it to VERIFIED once it has the sources and the challenge", ErrInvalid)
		default:
			err = fmt.Errorf("%w: section %q takes no entries (use notes_scratch for Scratch)", ErrInvalid, section)
		}
		if err != nil {
			return orig, nil, fmt.Errorf("entry %d: %w", i+1, err)
		}
		ids = append(ids, id)
	}
	return n, ids, nil
}

func appendClaim(n Notes, e Entry) (Notes, string, error) {
	text := clean(e.Text)
	if text == "" {
		return n, "", fmt.Errorf("%w: the claim text is empty", ErrInvalid)
	}
	if err := n.checkLinks(e.Sources); err != nil {
		return n, "", err
	}
	c := Claim{ID: NextClaimID(claimIDsOf(n.Claims)), Kind: KindClaim, Text: text, Status: ClaimUnverified, QuestionID: e.QuestionID, Check: clean(e.Check)}
	c, err := withLinks(c, e.Sources, n.Sources)
	if err != nil {
		return n, "", err
	}
	n.Claims = append(slices.Clone(n.Claims), c)
	return n, c.ID, nil
}

func appendDatapoint(n Notes, e Entry) (Notes, string, error) {
	value, context := strings.TrimSpace(e.Value), clean(e.Text)
	if value == "" || strings.ContainsAny(value, " \t\r\n|") {
		return n, "", fmt.Errorf("%w: the datapoint needs a value without spaces (put words in the unit)", ErrInvalid)
	}
	if context == "" {
		return n, "", fmt.Errorf("%w: the datapoint needs its context in text", ErrInvalid)
	}
	if err := n.checkLinks(e.Sources); err != nil {
		return n, "", err
	}
	d := Claim{ID: NextDatapointID(claimIDsOf(n.Datapoints)), Kind: KindDatapoint, Text: context, Value: value, Unit: clean(e.Unit),
		Status: ClaimUnverified, QuestionID: e.QuestionID, Check: clean(e.Check)}
	d, err := withLinks(d, e.Sources, n.Sources)
	if err != nil {
		return n, "", err
	}
	n.Datapoints = append(slices.Clone(n.Datapoints), d)
	return n, d.ID, nil
}

func claimIDsOf(list []Claim) []string {
	ids := make([]string, len(list))
	for i, c := range list {
		ids[i] = c.ID
	}
	return ids
}

func checkContradiction(n Notes, e Entry) error {
	for _, id := range []string{e.A, e.B} {
		if _, ok := n.Claim(id); !ok {
			return fmt.Errorf("%w: unknown claim %q", ErrInvalid, id)
		}
	}
	if e.A == e.B {
		return fmt.Errorf("%w: a claim cannot contradict itself", ErrInvalid)
	}
	return nil
}

// Update is the only way to change the status, the sources or the retraction of a record. A change to VERIFIED must pass CanVerify (with
// the challenges and limits it needs): the notes cannot hold a VERIFIED claim that does not meet the rule.
func Update(n Notes, id string, ch Change, challenges []Challenge, limits Limits) (Notes, error) {
	if strings.HasPrefix(id, prefixSource) {
		return updateSource(n, id, ch)
	}
	c, ok := n.Claim(id)
	if !ok {
		return n, fmt.Errorf("%w: unknown record %q", ErrInvalid, id)
	}
	if ch.AddSource != nil {
		var err error
		if c, err = Support(c, ch.AddSource.SourceID, ch.AddSource.Stance, ch.AddSource.Independent, n.Sources); err != nil {
			return n, err
		}
	}
	if t := clean(ch.Text); t != "" {
		if c.Status == ClaimVerified {
			return n, fmt.Errorf("%w: %s is VERIFIED; its text cannot be amended (demote it with status UNVERIFIED and a reason first)", ErrBadStatus, id)
		}
		c.Text = t
	}
	if ch.Check != "" {
		c.Check = clean(ch.Check)
	}
	if ch.Note != "" {
		c.Note = clean(ch.Note)
	}
	switch ch.Status {
	case "":
	case ClaimVerified:
		if err := CanVerify(c, n.Sources, challenges, limits); err != nil {
			return n, err
		}
		c.Status = ClaimVerified
	case ClaimRefuted:
		var err error
		if c, err = Refute(c, ch.SourceID, ch.Reason, n.Sources); err != nil {
			return n, err
		}
	case ClaimRetracted:
		var err error
		if c, err = Retract(c, ch.Reason); err != nil {
			return n, err
		}
	case ClaimUnverified:
		if c.Status != ClaimVerified && c.Status != ClaimUnverified {
			return n, fmt.Errorf("%w: %s is %s and stays so", ErrBadStatus, id, c.Status)
		}
		if c.Status == ClaimVerified {
			if strings.TrimSpace(ch.Reason) == "" {
				return n, fmt.Errorf("%w: give the reason for demoting %s", ErrInvalid, id)
			}
			c.Status, c.Note = ClaimUnverified, clean(ch.Reason)
		}
	default:
		return n, fmt.Errorf("%w: status %q is not VERIFIED, UNVERIFIED, REFUTED or RETRACTED", ErrInvalid, ch.Status)
	}
	return n.withClaim(c), nil
}

func updateSource(n Notes, id string, ch Change) (Notes, error) {
	i := slices.IndexFunc(n.Sources, func(s Source) bool { return s.ID == id })
	if i < 0 {
		return n, fmt.Errorf("%w: unknown source %q", ErrInvalid, id)
	}
	if ch.Status != "" || ch.AddSource != nil {
		return n, fmt.Errorf("%w: a source has no status; change its note or title", ErrInvalid)
	}
	s := n.Sources[i]
	if ch.Note != "" {
		s.Note = clean(ch.Note)
	}
	if ch.Title != "" {
		s.Title = cleanTitle(ch.Title)
	}
	n.Sources = slices.Clone(n.Sources)
	n.Sources[i] = s
	return n, nil
}

// SetScratch replaces the Scratch section. Line ends are normalized, a line that would look like a section header is indented, and a text
// above ScratchMaxChars is rejected.
func SetScratch(n Notes, text string) (Notes, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "# ") || strings.HasPrefix(l, "## ") {
			lines[i] = " " + l
		}
	}
	text = strings.TrimSpace(strings.Join(lines, "\n"))
	if r := len([]rune(text)); r > ScratchMaxChars {
		return n, fmt.Errorf("%w: the scratch text has %d characters, the most is %d: shorten it", ErrInvalid, r, ScratchMaxChars)
	}
	n.Scratch = text
	return n, nil
}

// TokenEstimate estimates the tokens of the rendered notes as characters / 4 (a rough rule of thumb, enough for the size check).
func TokenEstimate(n Notes) int { return len([]rune(Render(n))) / 4 }

// Render is the text of the notes file: the fixed sections, one record per line.
func Render(n Notes) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Notes: %s\n", n.Objective)
	var verified, unverified []Claim
	for _, c := range n.Claims {
		if c.Status == ClaimVerified {
			verified = append(verified, c)
		} else {
			unverified = append(unverified, c)
		}
	}
	section := func(s Section, lines []string) {
		fmt.Fprintf(&b, "\n## %s\n", s)
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
	}
	section(SectionVerified, mapSlice(verified, claimLine))
	section(SectionUnverified, mapSlice(unverified, claimLine))
	section(SectionContradictions, mapSlice(n.Contradictions, func(c Contradiction) string {
		l := fmt.Sprintf("- %s vs %s", c.A, c.B)
		if c.QuestionID != "" {
			l += " -> " + c.QuestionID
		}
		return l
	}))
	section(SectionDatapoints, mapSlice(n.Datapoints, claimLine))
	section(SectionSources, mapSlice(n.Sources, func(s Source) string {
		l := fmt.Sprintf(`- [%s] %s "%s" (%s)`, s.ID, s.URL, s.Title, s.Domain)
		if s.Note != "" {
			l += " | note: " + s.Note
		}
		return l
	}))
	section(SectionAssumptions, mapSlice(n.Assumptions, func(a Assumption) string {
		if a.QuestionID == "" {
			return "- gap: " + a.Text
		}
		return fmt.Sprintf("- %s unresolvable: %s", a.QuestionID, a.Text)
	}))
	section(SectionScratch, nil)
	if n.Scratch != "" {
		b.WriteString(n.Scratch + "\n")
	}
	return b.String()
}

func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, x := range in {
		out[i] = f(x)
	}
	return out
}

func claimLine(c Claim) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s] %s  ", c.ID, c.Status)
	if c.Kind == KindDatapoint {
		b.WriteString(strings.TrimSpace(c.Value + " " + c.Unit))
		b.WriteString(" | " + c.Text)
	} else {
		b.WriteString(c.Text)
	}
	if len(c.Links) > 0 {
		b.WriteString(" | src: " + strings.Join(mapSlice(c.Links, linkText), ", "))
	}
	if c.QuestionID != "" {
		b.WriteString(" | q: " + c.QuestionID)
	}
	if c.Check != "" {
		b.WriteString(" | check: " + c.Check)
	}
	if c.Note != "" {
		b.WriteString(" | note: " + c.Note)
	}
	return b.String()
}

func linkText(l Link) string {
	switch {
	case l.Stance == StanceContradicts:
		return l.SourceID + " (contradicts)"
	case l.Independent:
		return l.SourceID + " (independent)"
	}
	return l.SourceID
}

var (
	titleLine     = regexp.MustCompile(`^# Notes:\s*(.*)$`)
	sectionLine   = regexp.MustCompile(`^## (.+?)\s*$`)
	claimHead     = regexp.MustCompile(`^- \[([cd]_\d+)\] (UNVERIFIED|VERIFIED|REFUTED|RETRACTED)  (.+)$`)
	sourceLine    = regexp.MustCompile(`^- \[(s_\d+)\] (\S+) "(.*)" \((.*)\)$`)
	contradLine   = regexp.MustCompile(`^- ([cd]_\d+) vs ([cd]_\d+)(?: -> (q_\d+))?$`)
	assumpLine    = regexp.MustCompile(`^- (q_\d+) unresolvable: (.+)$`)
	gapLine       = regexp.MustCompile(`^- gap: (.+)$`)
	linkTokenLine = regexp.MustCompile(`^(s_\d+)(?: \((independent|contradicts)\))?$`)
)

// Parse reads the notes text in the fixed grammar of Render. A line that does not fit the grammar of its section is an error (with its line
// number), never silently dropped; the notes of the lines that do fit are returned with the errors. The text of Scratch is kept as it is.
// An empty text is empty notes.
func Parse(text string) (Notes, []error) {
	var n Notes
	var errs []error
	fail := func(no int, format string, args ...any) {
		errs = append(errs, fmt.Errorf("notes line %d: %s", no, fmt.Sprintf(format, args...)))
	}
	var section Section
	var scratch []string
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		no := i + 1
		if section == SectionScratch {
			scratch = append(scratch, line)
			continue
		}
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := titleLine.FindStringSubmatch(line); m != nil && section == "" && no >= 1 {
			n.Objective = strings.TrimSpace(m[1])
			continue
		}
		if m := sectionLine.FindStringSubmatch(line); m != nil {
			s, ok := ParseSection(m[1])
			if !ok || string(s) != m[1] {
				fail(no, "unknown section %q", m[1])
				section = "?"
				continue
			}
			section = s
			continue
		}
		var err error
		switch section {
		case SectionVerified, SectionUnverified, SectionDatapoints:
			var c Claim
			if c, err = parseClaim(line, section); err == nil {
				if c.Kind == KindDatapoint {
					n.Datapoints = append(n.Datapoints, c)
				} else {
					n.Claims = append(n.Claims, c)
				}
			}
		case SectionContradictions:
			if m := contradLine.FindStringSubmatch(line); m != nil {
				n.Contradictions = append(n.Contradictions, Contradiction{A: m[1], B: m[2], QuestionID: m[3]})
			} else {
				err = fmt.Errorf("not a contradiction line (- c_001 vs c_002 -> q_003)")
			}
		case SectionSources:
			err = parseSource(line, &n)
		case SectionAssumptions:
			if m := assumpLine.FindStringSubmatch(line); m != nil {
				n.Assumptions = append(n.Assumptions, Assumption{QuestionID: m[1], Text: m[2]})
			} else if m := gapLine.FindStringSubmatch(line); m != nil {
				n.Assumptions = append(n.Assumptions, Assumption{Text: m[1]})
			} else {
				err = fmt.Errorf("not an assumption line (- q_001 unresolvable: text, or - gap: text)")
			}
		default:
			err = fmt.Errorf("outside a known section")
		}
		if err != nil {
			fail(no, "%v: %q", err, line)
		}
	}
	n.Scratch = strings.TrimSpace(strings.Join(scratch, "\n"))
	byID := func(a, b Claim) int { return trailingNumber(a.ID) - trailingNumber(b.ID) }
	slices.SortStableFunc(n.Claims, byID)
	slices.SortStableFunc(n.Datapoints, byID)
	slices.SortStableFunc(n.Sources, func(a, b Source) int { return trailingNumber(a.ID) - trailingNumber(b.ID) })
	return n, errs
}

func parseClaim(line string, section Section) (Claim, error) {
	segs := strings.Split(line, " | ")
	m := claimHead.FindStringSubmatch(segs[0])
	if m == nil {
		return Claim{}, fmt.Errorf("not a claim line (- [c_001] UNVERIFIED  text | src: s_1 | q: q_1)")
	}
	c := Claim{ID: m[1], Status: ClaimStatus(m[2]), Kind: KindClaim}
	rest := segs[1:]
	if c.ID[0] == 'd' {
		c.Kind = KindDatapoint
		value, unit, _ := strings.Cut(m[3], " ")
		c.Value, c.Unit = value, strings.TrimSpace(unit)
		if len(rest) == 0 {
			return Claim{}, fmt.Errorf("a datapoint needs its context after the value")
		}
		c.Text, rest = rest[0], rest[1:]
	} else {
		c.Text = m[3]
	}
	switch {
	case section == SectionVerified && c.Status != ClaimVerified:
		return Claim{}, fmt.Errorf("only VERIFIED claims belong to Verified")
	case section == SectionUnverified && (c.Status == ClaimVerified || c.Kind != KindClaim):
		return Claim{}, fmt.Errorf("Unverified holds the claims that are not VERIFIED")
	case section == SectionDatapoints && c.Kind != KindDatapoint:
		return Claim{}, fmt.Errorf("Datapoints holds d_ records")
	}
	for _, seg := range rest {
		key, val, ok := strings.Cut(seg, ": ")
		if !ok {
			return Claim{}, fmt.Errorf("unknown field %q", seg)
		}
		switch key {
		case "src":
			for _, tok := range strings.Split(val, ", ") {
				lm := linkTokenLine.FindStringSubmatch(tok)
				if lm == nil {
					return Claim{}, fmt.Errorf("bad source link %q", tok)
				}
				l := Link{SourceID: lm[1], Stance: StanceSupports, Independent: lm[2] == "independent"}
				if lm[2] == "contradicts" {
					l = Link{SourceID: lm[1], Stance: StanceContradicts}
				}
				c.Links = append(c.Links, l)
			}
		case "q":
			c.QuestionID = val
		case "check":
			c.Check = val
		case "note":
			c.Note = val
		default:
			return Claim{}, fmt.Errorf("unknown field %q", key)
		}
	}
	return c, nil
}

func parseSource(line string, n *Notes) error {
	head, note, hasNote := strings.Cut(line, " | note: ")
	m := sourceLine.FindStringSubmatch(head)
	if m == nil {
		return fmt.Errorf(`not a source line (- [s_001] https://... "Title" (domain))`)
	}
	s := Source{ID: m[1], URL: m[2], Title: m[3], Domain: m[4]}
	if hasNote {
		s.Note = note
	}
	n.Sources = append(n.Sources, s)
	return nil
}
