package synth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
)

func TestParseCases(t *testing.T) {
	one := `{"remember":[{"text":"a"}],"query":"q","expect_contains":["x"],"extra":1}`
	for name, text := range map[string]string{
		"object":  one,
		"fence":   "Here:\n```json\n" + one + "\n```\n",
		"array":   "[" + one + "," + one + "]",
		"wrapped": `{"cases":[` + one + `]}`,
	} {
		cs, err := ParseCases(text)
		if err != nil || len(cs) == 0 || cs[0].Query != "q" || cs[0].ExpectContains[0] != "x" {
			t.Errorf("%s: %+v %v", name, cs, err)
		}
	}
	if cs, _ := ParseCases("[" + one + "," + one + "]"); len(cs) != 2 {
		t.Errorf("array: %d", len(cs))
	}
	for _, text := range []string{"", "no json", `{"cases":[]}`, "[]"} {
		if _, err := ParseCases(text); !errors.Is(err, ErrNoCases) {
			t.Errorf("%q: %v", text, err)
		}
	}
	if _, err := ParseCases(`{"remember": 5}`); err == nil {
		t.Error("a case of the wrong shape was accepted")
	}
}

func TestPrompts(t *testing.T) {
	s := Defaults()
	s.Prompt, s.CorpusPrompt = "About cats", "Vet notes"
	sys := SystemPrompt(s, "e", "desc", 1)
	for _, want := range []string{"About cats", "Vet notes", `"expect_contains"`, "exactly one case object"} {
		if !strings.Contains(sys, want) {
			t.Errorf("the system prompt lacks %q", want)
		}
	}
	if !strings.Contains(SystemPrompt(s, "e", "", 5), "array of exactly 5") {
		t.Error("bulk prompt")
	}
	if strings.Contains(SystemPrompt(core.SynthSettings{Prompt: "p"}, "e", "", 1), "data corpus") {
		t.Error("no corpus prompt, but a corpus section")
	}
	u := UserPrompt(1, 2, []string{"Earlier question?"})
	if !strings.Contains(u, "case number 2") || !strings.Contains(u, "Earlier question?") || strings.Contains(u, "Anchor") {
		t.Errorf("user prompt: %s", u)
	}
	if strings.Contains(UserPrompt(3, 1, nil), "already exist") {
		t.Error("an avoid list without cases")
	}
}

func TestValidate(t *testing.T) {
	ok := core.SynthSettings{Prompt: "p", Model: "m", NumExamples: 5, Mode: core.SynthBulk, BulkSize: 5}
	if p := Validate(ok, []string{"m"}, 10); len(p) != 0 {
		t.Errorf("valid settings: %v", p)
	}
	for name, mod := range map[string]func(*core.SynthSettings){
		"prompt": func(s *core.SynthSettings) { s.Prompt = "" },
		"model":  func(s *core.SynthSettings) { s.Model = "x" },
		"num":    func(s *core.SynthSettings) { s.NumExamples = 11 },
		"zero":   func(s *core.SynthSettings) { s.NumExamples = 0 },
		"mode":   func(s *core.SynthSettings) { s.Mode = "x" },
		"bulk":   func(s *core.SynthSettings) { s.BulkSize = 0 },
		"big":    func(s *core.SynthSettings) { s.Prompt = strings.Repeat("x", MaxPromptBytes+1) },
	} {
		s := ok
		mod(&s)
		if p := Validate(s, []string{"m"}, 10); len(p) != 1 {
			t.Errorf("%s: %v", name, p)
		}
	}
	two := 2
	if got := Apply(ok, Patch{NumExamples: &two}); got.NumExamples != 2 || got.Prompt != "p" {
		t.Errorf("apply: %+v", got)
	}
}

func TestPlanClassifyProvenance(t *testing.T) {
	mk := func(name, q string) core.KnowledgeCase {
		c := core.KnowledgeCase{Name: name}
		c.Query = q
		return c
	}
	a, b, c := mk("case-001", "a"), mk("case-002", "b"), mk("case-003", "c")
	rows := []core.SynthCase{
		{CaseName: "case-001", ContentHash: core.CaseHash(a)},
		{CaseName: "case-002", ContentHash: core.CaseHash(b)},
		{CaseName: "case-003", ContentHash: core.CaseHash(mk("case-003", "old"))}, // edited since
	}
	user := mk("mine", "u") // no row: added by the user
	have := []core.KnowledgeCase{a, b, c, user}
	if Classify(a, rows) != CaseGenerated || Classify(c, rows) != CaseEdited || Classify(user, rows) != CaseUser {
		t.Error("classify")
	}
	kept, n := Plan(have, rows, 6, true)
	if len(kept) != 2 || kept[0].Name != "case-003" || kept[1].Name != "mine" || n != 4 {
		t.Errorf("plan keep: %v %d", kept, n)
	}
	if kept, n := Plan(have, rows, 1, true); len(kept) != 2 || n != 0 {
		t.Errorf("plan over: %v %d", kept, n)
	}
	if kept, n := Plan(have, rows, 6, false); len(kept) != 0 || n != 6 {
		t.Errorf("plan replace: %v %d", kept, n)
	}
	if got := freeNames(map[string]bool{"case-001": true, "case-003": true}, 3); strings.Join(got, ",") != "case-002,case-004,case-005" {
		t.Errorf("names: %v", got)
	}
	merged := Merge(kept[:0:0], nil)
	_ = merged
	prov := Provenance(rows, []core.KnowledgeCase{c, user}, []core.KnowledgeCase{a}, 7, 3, rows[0].CreatedAt)
	if len(prov) != 2 || prov[0].CaseName != "case-003" || prov[1].CaseName != "case-001" || prov[1].GenerationID != 7 || prov[1].ContentHash != core.CaseHash(a) {
		t.Errorf("provenance: %+v", prov)
	}
}

// scriptLLM answers with the next text of its script.
type scriptLLM struct {
	texts []string
	err   error
	i     int
}

func (l *scriptLLM) Complete(context.Context, llm.Request) (*llm.Response, error) {
	if l.i >= len(l.texts) {
		return nil, l.err
	}
	l.i++
	return &llm.Response{Text: l.texts[l.i-1], CostUSD: 1, InputTokens: 2, OutputTokens: 3}, nil
}

func TestGenerateRetriesInvalid(t *testing.T) {
	good := `{"remember":[{"text":"a"}],"query":"q","expect_contains":["x"]}`
	bad := `{"remember":[{"text":"a","reference_time":"yesterday"}],"query":"q"}`
	lm := &scriptLLM{texts: []string{"not json", bad, good, good}}
	var progress int
	res, err := Generate(context.Background(), lm, Request{
		Settings: core.SynthSettings{Mode: core.SynthSingle},
		EvalName: "e",
		Count:    2,
		Limits:   evalfile.DefaultLimits(nil, nil, nil),
	}, func(Result) { progress++ })
	if err != nil || len(res.Cases) != 2 || res.Calls != 4 || res.Rejected != 2 || res.CostUSD != 4 || progress != 4 {
		t.Fatalf("%+v %v progress %d", res, err, progress)
	}
	if res.Cases[0].Name != "case-001" || res.Cases[1].Name != "case-002" {
		t.Errorf("names: %v", res.Cases)
	}
}

func TestGenerateGivesUpAndFails(t *testing.T) {
	good := `{"remember":[{"text":"a"}],"query":"q"}`
	lm := &scriptLLM{texts: []string{good}, err: errors.New("boom")}
	res, err := Generate(context.Background(), lm, Request{
		Settings: core.SynthSettings{Mode: core.SynthSingle},
		EvalName: "e",
		Count:    3,
		Limits:   evalfile.DefaultLimits(nil, nil, nil),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") || len(res.Cases) != 1 {
		t.Errorf("llm error: %+v %v", res, err)
	}
	lm = &scriptLLM{texts: []string{"x", "x", "x", "x", "x", "x", "x"}}
	res, err = Generate(context.Background(), lm, Request{
		Settings: core.SynthSettings{Mode: core.SynthBulk, BulkSize: 2},
		EvalName: "e",
		Count:    2,
		Limits:   evalfile.DefaultLimits(nil, nil, nil),
	}, nil)
	if err == nil || res.Calls != 1+extraCalls {
		t.Errorf("no cases: %+v %v", res, err)
	}
	if _, err := Generate(context.Background(), nil, Request{}, nil); !errors.Is(err, core.ErrNoLLM) {
		t.Errorf("no llm: %v", err)
	}
}
