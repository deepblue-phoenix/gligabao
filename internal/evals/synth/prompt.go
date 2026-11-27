package synth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
)

// CaseShape is the shape of a case, as the system prompt shows it to the LLM (the knowledge case of a user eval, without the name and the settings:
// the server names the cases).
const CaseShape = `{
  "remember": [
    {"text": "a text to remember, written like a real document, message or note", "reference_time": "2024-03-01T09:00:00Z"}
  ],
  "query": "a question whose answer is in the remembered texts",
  "expect_contains": ["a string the correct answer must contain", "another one"],
  "expect_edges": [
    {"source": "Entity A", "target": "Entity B", "valid_at": "2024-01-15T00:00:00Z", "ended": false}
  ]
}`

// caseRules are the rules of every case, in the system prompt.
const caseRules = `Rules of a case:
- "remember" has 1 to 5 texts, remembered in order. Each is a self-contained text of a few sentences up to a short document, with concrete names, dates, numbers and relations, so that facts can be extracted from it. "reference_time" (RFC 3339) is when the text was written; give it when the dates in the text are relative or the order of the texts matters.
- "query" is a question that needs the remembered texts to be answered. It must not repeat the answer.
- "expect_contains" lists the short strings (names, numbers, dates) that the correct answer must contain; case does not matter. Keep them to what the texts state unambiguously, and few (1 to 3).
- "expect_edges" (optional) lists facts that must be stored as edges of the knowledge graph between two entities: "source" and "target" are the names of the entities as in the text, "valid_at" / "invalid_at" are RFC 3339 and optional, "ended" says that the text says the fact ended. Only list the ones that the texts state clearly. Leave "expect_edges" out when unsure.
- Do not write any other key. Do not write the case name: it is assigned for you.
- Every case must be correct: the expected strings and edges must follow from the texts.`

// SystemPrompt is the system prompt of a generation call: the role, the dataset the user describes, the data corpus, the shape of a case
// and the form of the answer. perCall is the number of cases the call asks for (1: one object; more: an array).
func SystemPrompt(s core.SynthSettings,
	evalName,
	description string,
	perCall int) string {
	var b strings.Builder
	b.WriteString("You write test cases (an eval dataset) for \"gligabao\", a temporal knowledge-graph memory system. A case gives some texts to remember, then asks a question about them and says what a correct answer and a correct knowledge graph contain.\n\n")
	fmt.Fprintf(&b,
		"The eval is called %q.",
		evalName)
	if description != "" {
		fmt.Fprintf(&b,
			" Its description: %s",
			description)
	}
	b.WriteString("\n\nWhat the eval dataset is about (from the user):\n")
	b.WriteString(strings.TrimSpace(s.Prompt))
	if c := strings.TrimSpace(s.CorpusPrompt); c != "" {
		b.WriteString("\n\nThe data corpus: what the texts of the cases (the \"remember\" texts) are like (from the user):\n")
		b.WriteString(c)
	}
	b.WriteString("\n\nThe shape of a case (JSON; the values are examples):\n")
	b.WriteString(CaseShape)
	b.WriteString("\n\n")
	b.WriteString(caseRules)
	b.WriteString("\n\nAnswer with JSON only, no commentary and no code fence: ")
	if perCall <= 1 {
		b.WriteString("exactly one case object of that shape.")
	} else {
		fmt.Fprintf(&b,
			"a JSON array of exactly %d case objects of that shape, different from each other.",
			perCall)
	}
	return b.String()
}

// UserPrompt is the message of a generation call. avoid are one-line summaries of the cases that exist (the queries), which the new ones
// must not repeat. n is the number of cases asked for, index the number of the first of them (from 1).
func UserPrompt(n,
	index int,
	avoid []string) string {
	var b strings.Builder
	if n <= 1 {
		fmt.Fprintf(&b,
			"Write case number %d.",
			index)
	} else {
		fmt.Fprintf(&b,
			"Write %d cases, numbers %d to %d.",
			n,
			index,
			index+n-1)
	}
	if len(avoid) > 0 {
		b.WriteString("\n\nThe queries of the cases that already exist; do not repeat their topics:\n")
		for _, a := range avoid {
			fmt.Fprintf(&b,
				"- %s\n",
				a)
		}
	}
	return b.String()
}

// generated is a case as the LLM writes it (the shape of CaseShape).
type generated struct {
	core.KnowledgeInput
	core.KnowledgeExpected
}

// ErrNoCases means the answer of the LLM holds no case.
var ErrNoCases = errors.New("the answer has no case")

// ParseCases reads the cases from the answer of the LLM: a JSON object (one case), an array of them, or an object with a "cases"
// array, optionally inside a code fence or after some text. The cases have no name (the caller names them); keys the shape does not
// have are ignored.
func ParseCases(text string) ([]core.KnowledgeCase, error) {
	raw := extractJSON(text)
	if raw == "" {
		return nil, ErrNoCases
	}
	var items []json.RawMessage
	switch raw[0] {
	case '[':
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return nil, fmt.Errorf("the answer is not valid JSON: %w", err)
		}
	default:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return nil, fmt.Errorf("the answer is not valid JSON: %w", err)
		}
		if cs, ok := obj["cases"]; ok {
			if err := json.Unmarshal(cs, &items); err != nil {
				return nil, fmt.Errorf("\"cases\" is not an array: %w", err)
			}
		} else {
			items = []json.RawMessage{json.RawMessage(raw)}
		}
	}
	out := make([]core.KnowledgeCase, 0, len(items))
	for i, it := range items {
		var g generated
		if err := json.Unmarshal(it, &g); err != nil {
			return nil, fmt.Errorf("case %d: %w",
				i+1,
				err)
		}
		out = append(out, core.KnowledgeCase{
			KnowledgeInput:    g.KnowledgeInput,
			KnowledgeExpected: g.KnowledgeExpected,
		})
	}
	if len(out) == 0 {
		return nil, ErrNoCases
	}
	return out, nil
}

// extractJSON returns the first JSON value (object or array) in text, without a code fence around it; "" if there is none.
func extractJSON(text string) string {
	t := strings.TrimSpace(text)
	start := strings.IndexAny(t, "{[")
	if start < 0 {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(t[start:])))
	var v json.RawMessage
	if err := dec.Decode(&v); err != nil {
		return ""
	}
	return string(v)
}

// ParseCase reads one case from the body of an edit: strictly, like the cases of an eval file (an unknown key is an error, so a typo
// does not silently drop a check).
func ParseCase(body []byte) (core.KnowledgeCase, error) {
	var c core.KnowledgeCase
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return core.KnowledgeCase{}, fmt.Errorf("not a valid case: %w", err)
	}
	if dec.More() {
		return core.KnowledgeCase{}, errors.New("not a valid case: more than one JSON value")
	}
	return c, nil
}
