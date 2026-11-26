package jev

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// The classes of ClassifyLanguage: the programming languages that have an extractor (the names of memory_code), and Unsupported for
// code in any other language (and for text that is not code).
const (
	LangPython     = "python"
	LangTypeScript = "typescript"
	LangJavaScript = "javascript"
	LangCSS        = "css"
	LangGo         = "go"
	Unsupported    = "unsupported"
)

// MaxLanguageLines is the most lines of code sent to Jev to tell its language: a small snippet from the start is enough. MaxLanguageBytes
// is the safety cap for code with very long lines (a minified file).
const (
	MaxLanguageLines = 30
	MaxLanguageBytes = 2048
)

// languageID is the key of the question in the request.
const languageID = "language"

// languageClasses are the options of the question, in a fixed order.
var languageClasses = []string{LangPython, LangTypeScript, LangJavaScript, LangCSS, LangGo, Unsupported}

var languageQuestion = Question{
	Type:         "choice",
	Instructions: "Which programming language is this code snippet written in? If it is code in any other language, or it is not code, answer unsupported.",
	Criteria: map[string]string{
		LangPython:     "Python (def, class, import, indentation based blocks)",
		LangTypeScript: "TypeScript or TSX (JavaScript with type annotations, interfaces, enums, generics)",
		LangJavaScript: "JavaScript or JSX, without type annotations",
		LangCSS:        "CSS (selectors with declaration blocks, @media, custom properties)",
		LangGo:         "Go (package clause, func, :=, struct, interface)",
		Unsupported:    "Any other programming or markup language (Rust, Java, C, SQL, HTML, shell, ...), or text that is not code",
	},
}

// LanguageClassification is Jev's judgment of the language of a code snippet.
type LanguageClassification struct {
	// Language is one of the Lang* constants, or Unsupported.
	Language string `json:"language"`
	// Confidence as in Classification; -1 if the reply had none.
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Model is the model version that answered; the tokens and timing are those of the call.
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	Call         Call   `json:"-"`
}

// ClassifyLanguage asks Jev (one choice question) which language a snippet of code is in: the first MaxLanguageLines lines of the code
// (at most MaxLanguageBytes) are sent. The Call is returned even on an error (see Evaluate).
func (c *Client) ClassifyLanguage(ctx context.Context,
	code string) (*LanguageClassification, Call, error) {
	input := snippet(code)
	resp, call, err := c.Evaluate(ctx,
		input,
		map[string]Question{
			languageID: languageQuestion,
		})
	if err != nil {
		return nil, call, err
	}
	lc, err := languageClassificationOf(resp)
	if err != nil {
		return nil, call, err
	}
	lc.Call = call
	return lc, call, nil
}

// languageClassificationOf reads the answer to the language question.
func languageClassificationOf(resp *Response) (*LanguageClassification, error) {
	a, ok := resp.Answers[languageID]
	if !ok || a.Type != "choice" {
		return nil, fmt.Errorf("jev: no choice answer %q in the response", languageID)
	}
	if !slices.Contains(languageClasses, a.Choice) {
		return nil, fmt.Errorf("jev: unknown language %q", a.Choice)
	}
	lc := &LanguageClassification{
		Language:      a.Choice,
		Confidence:    -1,
		Probabilities: a.Probabilities,
		Model:         resp.Model,
		InputTokens:   resp.InputTokens,
		OutputTokens:  resp.OutputTokens,
	}
	if a.Confidence != nil && *a.Confidence >= 0 && *a.Confidence <= 1 {
		lc.Confidence = *a.Confidence
	}
	return lc, nil
}

// snippet is the start of the code that is sent: its first MaxLanguageLines lines, cut to MaxLanguageBytes.
func snippet(code string) string {
	lines := strings.SplitAfterN(code, "\n", MaxLanguageLines+1)
	if len(lines) > MaxLanguageLines {
		lines = lines[:MaxLanguageLines]
	}
	s, _ := cut(strings.Join(lines, ""), MaxLanguageBytes)
	return s
}
