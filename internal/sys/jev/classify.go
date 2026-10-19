package jev

import (
	"context"
	"fmt"
	"unicode/utf8"
)

// Complexity is how many entities a text is expected to yield when the knowledge graph is extracted from it. It
// predicts the compute that extraction and reconciliation will take, per entity and edge.
type Complexity string

const (
	// Simple: up to SimpleMaxEntities entities.
	Simple Complexity = "simple"
	// Medium: more than SimpleMaxEntities, up to MediumMaxEntities.
	Medium Complexity = "medium"
	// Complex: more than MediumMaxEntities.
	Complex Complexity = "complex"
)

// The entity-count boundaries of the classes.
const (
	SimpleMaxEntities = 5
	MediumMaxEntities = 20
)

// MaxInputBytes is the most text sent to Jev; a longer text is cut (Classification.Truncated).
const MaxInputBytes = 32 << 10

// classifyID is the key of the question in the request.
const classifyID = "complexity"

var classifyQuestion = Question{
	Type: "choice",
	Instructions: "Estimate how many distinct entities (people, organizations, places, projects, products, concepts, events) " +
		"a careful reader would extract from this text to build a knowledge graph. How many are there?",
	Criteria: map[string]string{
		string(Simple):  fmt.Sprintf("At most %d entities", SimpleMaxEntities),
		string(Medium):  fmt.Sprintf("%d to %d entities", SimpleMaxEntities+1, MediumMaxEntities),
		string(Complex): fmt.Sprintf("More than %d entities", MediumMaxEntities),
	},
}

// Classification is Jev's judgment of a text.
type Classification struct {
	Complexity Complexity `json:"complexity"`
	// Confidence is Jev's confidence in the class, from 0 to 1, as it reports it (derived from the probabilities);
	// -1 if the reply had none.
	Confidence float64 `json:"confidence"`
	// Probabilities of each class.
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Truncated says the text was longer than MaxInputBytes, so only its start was judged.
	Truncated bool `json:"truncated,omitempty"`
	// Model is the model version that answered; the tokens and timing are those of the call.
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	Call         Call   `json:"-"`
}

// Classify asks Jev (one choice question) whether text is simple, medium or complex in the number of entities it will
// yield. The Call is returned even on an error (see Evaluate).
func (c *Client) Classify(ctx context.Context, text string) (*Classification, Call, error) {
	input, truncated := cut(text, MaxInputBytes)
	resp, call, err := c.Evaluate(ctx, input, map[string]Question{classifyID: classifyQuestion})
	if err != nil {
		return nil, call, err
	}
	cl, err := classificationOf(resp)
	if err != nil {
		return nil, call, err
	}
	cl.Truncated, cl.Call = truncated, call
	return cl, call, nil
}

// classificationOf reads the answer to the classify question.
func classificationOf(resp *Response) (*Classification, error) {
	a, ok := resp.Answers[classifyID]
	if !ok || a.Type != "choice" {
		return nil, fmt.Errorf("jev: no choice answer %q in the response", classifyID)
	}
	c := Complexity(a.Choice)
	switch c {
	case Simple, Medium, Complex:
	default:
		return nil, fmt.Errorf("jev: unknown complexity %q", a.Choice)
	}
	cl := &Classification{Complexity: c, Confidence: -1, Probabilities: a.Probabilities, Model: resp.Model,
		InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens}
	if a.Confidence != nil && *a.Confidence >= 0 && *a.Confidence <= 1 {
		cl.Confidence = *a.Confidence
	}
	return cl, nil
}

// cut shortens s to at most n bytes without splitting a rune.
func cut(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}
