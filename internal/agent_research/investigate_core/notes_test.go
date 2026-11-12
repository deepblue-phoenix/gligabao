package investigate_core

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func richNotes(t *testing.T) Notes {
	t.Helper()
	n := Notes{
		Objective: "Does X fit in 16 GB?",
	}
	var err error
	n, _, err = Append(n, SectionSources, []Entry{
		{
			URL:   "https://arxiv.org/abs/1",
			Title: `A "quoted" title (v2)`,
		},
		{
			URL:   "https://www.example.co.uk/page/",
			Title: "Page",
			Note:  "read, nothing relevant",
		},
		{
			URL: "https://github.com/a/b",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, _, err = Append(n, SectionUnverified, []Entry{
		{
			Text:       "X needs 11.2 GB at fp16 | really",
			QuestionID: "q_002",
			Check:      "independent benchmark",
			Sources: []Link{{
				SourceID:    "s_001",
				Independent: true,
			}, {
				SourceID: "s_002",
				Stance:   StanceContradicts,
			}},
		},
		{
			Text: "Skip connections add 18%",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, _, err = Append(n, SectionDatapoints, []Entry{{
		Value: "16",
		Unit:  "GB of VRAM",
		Text:  "budget of the target GPU",
		Sources: []Link{{
			SourceID:    "s_003",
			Independent: true,
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	n, _, err = Append(n, SectionContradictions, []Entry{{
		A:          "c_001",
		B:          "c_002",
		QuestionID: "q_004",
	}, {
		A: "c_001",
		B: "d_001",
	}})
	if err != nil {
		t.Fatal(err)
	}
	n, _, err = Append(n, SectionAssumptions, []Entry{{
		QuestionID: "q_031",
		Text:       "assumed 2x",
	}, {
		Text: "no data on Y",
	}})
	if err != nil {
		t.Fatal(err)
	}
	n.Claims[0].Status = ClaimVerified
	n.Claims[1].Status, n.Claims[1].Note = ClaimRetracted, "misread"
	n, err = SetScratch(n, "plan:\r\n## not a section\nsecond line  ")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNotesRoundTrip(t *testing.T) {
	n := richNotes(t)
	text := Render(n)
	back, errs := Parse(text)
	if len(errs) > 0 {
		t.Fatalf("parse errors: %v\n%s", errs, text)
	}
	if !reflect.DeepEqual(back, n) {
		t.Fatalf("Parse(Render(n)) != n\n got: %+v\nwant: %+v\n%s", back, n, text)
	}
	if again := Render(back); again != text {
		t.Fatalf("render is not stable:\n%s\n---\n%s", again, text)
	}
	for _, want := range []string{"## Verified\n- [c_001] VERIFIED  X needs 11.2 GB at fp16 / really | src: s_001 (independent), s_002 (contradicts) | q: q_002 | check: independent benchmark",
		"- [d_001] UNVERIFIED  16 GB of VRAM | budget of the target GPU | src: s_003 (independent)", `- [s_001] https://arxiv.org/abs/1 "A 'quoted' title (v2)" (arxiv.org)`,
		"- [s_002] https://www.example.co.uk/page \"Page\" (example.co.uk) | note: read, nothing relevant", "- c_001 vs c_002 -> q_004", "- q_031 unresolvable: assumed 2x", "- gap: no data on Y"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered notes lack %q:\n%s", want, text)
		}
	}
}

func TestRenderParseCanonicalForm(t *testing.T) {
	messy := "# Notes:   obj \n\n\n## Unverified\n\n- [c_002] UNVERIFIED  second\n- [c_001] UNVERIFIED  first | src: s_001\n## Sources\n- [s_001] https://a.org/x \"T\" (a.org)   \n## Scratch\n\n  free\n\n"
	n, errs := Parse(messy)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	canon := Render(n)
	if m, _ := Parse(canon); Render(m) != canon {
		t.Fatalf("not canonical:\n%s", canon)
	}
	if n.Claims[0].ID != "c_001" || n.Scratch != "free" || n.Objective != "obj" {
		t.Fatalf("%+v", n)
	}
	if got, errs := Parse(Render(Notes{})); len(errs) != 0 || !reflect.DeepEqual(got, Notes{}) {
		t.Fatalf("empty notes: %+v %v", got, errs)
	}
	if got, errs := Parse(""); len(errs) != 0 || !reflect.DeepEqual(got, Notes{}) {
		t.Fatalf("empty text: %+v %v", got, errs)
	}
}

func TestParseRejectsUnknownLines(t *testing.T) {
	cases := map[string]string{
		"junk in claims":       "## Unverified\n- hello\n",
		"verified in wrong":    "## Unverified\n- [c_001] VERIFIED  x\n",
		"unverified in right":  "## Verified\n- [c_001] UNVERIFIED  x\n",
		"unknown section":      "## Mystery\n- x\n",
		"line outside section": "stray\n",
		"bad source":           "## Sources\n- [s_001] not a url line\n",
		"bad contradiction":    "## Contradictions\n- c_001 and c_002\n",
		"bad assumption":       "## Assumptions & gaps\n- something\n",
		"unknown field":        "## Unverified\n- [c_001] UNVERIFIED  x | color: red\n",
		"bad link":             "## Unverified\n- [c_001] UNVERIFIED  x | src: source1\n",
		"datapoint no context": "## Datapoints\n- [d_001] UNVERIFIED  16 GB\n",
		"datapoint in claims":  "## Unverified\n- [d_001] UNVERIFIED  16 GB | ctx\n",
	}
	for name, text := range cases {
		if _, errs := Parse(text); len(errs) == 0 {
			t.Errorf("%s: no error", name)
		}
	}
	n, errs := Parse("## Unverified\n- [c_001] UNVERIFIED  fine\n- junk\n")
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "line 3") || len(n.Claims) != 1 {
		t.Fatalf("%+v %v", n, errs)
	}
}

func TestAppend(t *testing.T) {
	n, ids, err := Append(Notes{}, SectionSources, []Entry{{
		URL: "https://a.org/x#frag",
	}, {
		URL: "https://a.org/x/",
	}, {
		URL: "https://b.com",
	}})
	if err != nil || len(ids) != 3 || ids[0] != "s_001" || ids[1] != "s_001" || ids[2] != "s_002" || len(n.Sources) != 2 {
		t.Fatalf("one row per distinct URL: %v %v %+v", err, ids, n.Sources)
	}
	cases := []struct {
		name    string
		section Section
		e       Entry
	}{
		{"verified takes no entries", SectionVerified, Entry{
			Text: "x",
		}},
		{"scratch takes no entries", SectionScratch, Entry{
			Text: "x",
		}},
		{"empty claim", SectionUnverified, Entry{
			Text: " ",
		}},
		{"unknown source", SectionUnverified, Entry{
			Text: "x",
			Sources: []Link{{
				SourceID: "s_009",
			}},
		}},
		{"bad stance", SectionUnverified, Entry{
			Text: "x",
			Sources: []Link{{
				SourceID: "s_001",
				Stance:   "maybe",
			}},
		}},
		{"datapoint without value", SectionDatapoints, Entry{
			Text: "ctx",
		}},
		{"datapoint value with space", SectionDatapoints, Entry{
			Text:  "ctx",
			Value: "1 2",
		}},
		{"datapoint without context", SectionDatapoints, Entry{
			Value: "1",
		}},
		{"bad url", SectionSources, Entry{
			URL: "not a url",
		}},
		{"ftp url", SectionSources, Entry{
			URL: "ftp://a.org/x",
		}},
		{"unknown claim in contradiction", SectionContradictions, Entry{
			A: "c_001",
			B: "c_002",
		}},
		{"empty assumption", SectionAssumptions, Entry{}},
	}
	for _, c := range cases {
		if _, _, err := Append(n, c.section, []Entry{c.e}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, _, err := Append(n, SectionUnverified, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("no entries: %v", err)
	}
	// All or nothing.
	if got, _, err := Append(n, SectionUnverified, []Entry{{
		Text: "ok",
	}, {
		Text: "",
	}}); err == nil || len(got.Claims) != 0 {
		t.Errorf("partial append: %v %+v", err, got.Claims)
	}
	// Ids are assigned by code, per kind.
	n, ids, _ = Append(n, SectionUnverified, []Entry{{
		Text: "a",
	}, {
		Text: "b",
	}})
	n, dids, _ := Append(n, SectionDatapoints, []Entry{{
		Value: "1",
		Text:  "ctx",
	}})
	if ids[0] != "c_001" || ids[1] != "c_002" || dids[0] != "d_001" {
		t.Fatalf("%v %v", ids, dids)
	}
	if got, ok := ParseSection("assumptions & gaps"); !ok || got != SectionAssumptions {
		t.Fatalf("section name %q", got)
	}
}

func TestUpdate(t *testing.T) {
	limits := DefaultLimits()
	n := richNotes(t)
	n.Claims[0].Status = ClaimUnverified
	n.Claims[0].Links = []Link{{
		SourceID:    "s_001",
		Stance:      StanceSupports,
		Independent: true,
	}}
	n.Claims[1].Status = ClaimUnverified

	// VERIFIED needs two independent sources with distinct domains and a survived challenge.
	if _, err := Update(n, "c_001", Change{
		Status: ClaimVerified,
	}, nil, limits); !errors.Is(err, ErrNotVerifiable) {
		t.Fatalf("one source: %v", err)
	}
	n2, err := Update(n, "c_001", Change{
		AddSource: &Link{
			SourceID:    "s_003",
			Independent: true,
		},
	}, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update(n2, "c_001", Change{
		Status: ClaimVerified,
	}, nil, limits); !errors.Is(err, ErrNotVerifiable) || !strings.Contains(err.Error(), "survived challenge") {
		t.Fatalf("no challenge: %v", err)
	}
	survived := []Challenge{{
		ID:         "ch_001",
		TargetType: TargetClaim,
		TargetID:   "c_001",
		Status:     ChallengeClosed,
		Outcome:    OutcomeSurvived,
	}}
	n3, err := Update(n2, "c_001", Change{
		Status: ClaimVerified,
	}, survived, limits)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := n3.Claim("c_001"); c.Status != ClaimVerified {
		t.Fatalf("%+v", c)
	}
	// The same domain twice does not count; undeclared independence does not count.
	same, _ := Update(n, "c_001", Change{
		AddSource: &Link{
			SourceID:    "s_001",
			Independent: true,
		},
	}, nil, limits)
	if _, err := Update(same, "c_001", Change{
		Status: ClaimVerified,
	}, survived, limits); !errors.Is(err, ErrNotVerifiable) {
		t.Fatalf("same domain: %v", err)
	}
	undeclared, _ := Update(n, "c_001", Change{
		AddSource: &Link{
			SourceID: "s_003",
		},
	}, nil, limits)
	if _, err := Update(undeclared, "c_001", Change{
		Status: ClaimVerified,
	}, survived, limits); !errors.Is(err, ErrNotVerifiable) {
		t.Fatalf("undeclared independence: %v", err)
	}
	// A VERIFIED claim keeps its text; it can be demoted with a reason.
	if _, err := Update(n3, "c_001", Change{
		Text: "other",
	}, nil, limits); !errors.Is(err, ErrBadStatus) {
		t.Fatalf("amend verified: %v", err)
	}
	if _, err := Update(n3, "c_001", Change{
		Status: ClaimUnverified,
	}, nil, limits); !errors.Is(err, ErrInvalid) {
		t.Fatalf("demote without reason: %v", err)
	}
	n4, err := Update(n3, "c_001", Change{
		Status: ClaimUnverified,
		Reason: "scope changed",
	}, nil, limits)
	if c, _ := n4.Claim("c_001"); err != nil || c.Status != ClaimUnverified || c.Note != "scope changed" {
		t.Fatalf("%v %+v", err, c)
	}
	// Refute and retract.
	if _, err := Update(n, "c_001", Change{
		Status:   ClaimRefuted,
		SourceID: "s_003",
	}, nil, limits); !errors.Is(err, ErrInvalid) {
		t.Fatalf("refute without reason: %v", err)
	}
	n5, err := Update(n, "c_001", Change{
		Status:   ClaimRefuted,
		SourceID: "s_003",
		Reason:   "newer data",
	}, nil, limits)
	c, _ := n5.Claim("c_001")
	if err != nil || c.Status != ClaimRefuted || !hasLink(c, "s_003", StanceContradicts) {
		t.Fatalf("%v %+v", err, c)
	}
	if _, err := Update(n5, "c_001", Change{
		Status: ClaimVerified,
	}, survived, limits); !errors.Is(err, ErrNotVerifiable) {
		t.Fatalf("a refuted claim cannot become verified: %v", err)
	}
	n6, err := Update(n, "c_002", Change{
		Status: ClaimRetracted,
		Reason: "misread",
	}, nil, limits)
	if c, _ := n6.Claim("c_002"); err != nil || c.Status != ClaimRetracted {
		t.Fatalf("%v %+v", err, c)
	}
	if _, err := Update(n, "c_002", Change{
		Status: ClaimRetracted,
	}, nil, limits); !errors.Is(err, ErrInvalid) {
		t.Fatalf("retract without reason: %v", err)
	}
	// Sources and unknown ids.
	n7, err := Update(n, "s_001", Change{
		Note: "paywalled",
	}, nil, limits)
	if s, _ := findSource(n7.Sources, "s_001"); err != nil || s.Note != "paywalled" {
		t.Fatalf("%v %+v", err, s)
	}
	if _, err := Update(n, "s_001", Change{
		Status: ClaimVerified,
	}, nil, limits); !errors.Is(err, ErrInvalid) {
		t.Fatalf("source status: %v", err)
	}
	for _, id := range []string{"c_099", "s_099", "zzz"} {
		if _, err := Update(n, id, Change{
			Note: "x",
		}, nil, limits); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if _, err := Update(n, "c_001", Change{
		Status: "MAYBE",
	}, nil, limits); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status: %v", err)
	}
	// The value of the notes is untouched by Update.
	if c, _ := n.Claim("c_001"); c.Status != ClaimUnverified || len(c.Links) != 1 {
		t.Fatalf("Update mutated its input: %+v", c)
	}
}

func hasLink(c Claim, source string, stance Stance) bool {
	for _, l := range c.Links {
		if l.SourceID == source && l.Stance == stance {
			return true
		}
	}
	return false
}

func TestSetScratchAndSize(t *testing.T) {
	n, err := SetScratch(Notes{}, "  a\r\n# b\n## c \n")
	if err != nil || n.Scratch != "a\n # b\n ## c" {
		t.Fatalf("%q %v", n.Scratch, err)
	}
	if _, err := SetScratch(Notes{}, strings.Repeat("x", ScratchMaxChars+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too long: %v", err)
	}
	if _, err := SetScratch(Notes{}, strings.Repeat("x", ScratchMaxChars)); err != nil {
		t.Fatal(err)
	}
	if got, want := TokenEstimate(Notes{
		Objective: "abcd",
	}), len([]rune(Render(Notes{
		Objective: "abcd",
	})))/4; got != want || got == 0 {
		t.Fatalf("estimate %d want %d", got, want)
	}
}
