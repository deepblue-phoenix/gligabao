package synth

import (
	"fmt"
	"slices"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
)

// Limits of the definition of a synthetic eval.
const (
	// MaxPromptBytes is the size of the prompt and of the corpus prompt each.
	MaxPromptBytes = 8 << 10
	// MaxBulkSize is the most cases a bulk call asks for.
	MaxBulkSize = 50
)

// Patch is a change of a definition, as the API takes it: a field that is nil is left as it is (on creation: takes its default).
type Patch struct {
	Description  *string         `json:"description"`
	Prompt       *string         `json:"prompt"`
	CorpusPrompt *string         `json:"corpus_prompt"`
	Model        *string         `json:"model"`
	NumExamples  *int            `json:"num_examples"`
	Mode         *core.SynthMode `json:"mode"`
	BulkSize     *int            `json:"bulk_size"`
}

// Defaults are the settings of a new definition before the user's fields are applied: one case per call, 10 per call in the bulk mode.
func Defaults() core.SynthSettings {
	return core.SynthSettings{
		Mode:     core.SynthSingle,
		BulkSize: core.DefaultBulkSize,
	}
}

// Apply returns base with the fields of p that are set.
func Apply(base core.SynthSettings,
	p Patch) core.SynthSettings {
	pick := func(cur string,
		v *string) string {
		if v == nil {
			return cur
		}
		return strings.TrimSpace(*v)
	}
	out := base
	out.Prompt = pick(base.Prompt, p.Prompt)
	out.CorpusPrompt = pick(base.CorpusPrompt, p.CorpusPrompt)
	out.Model = pick(base.Model, p.Model)
	if p.NumExamples != nil {
		out.NumExamples = *p.NumExamples
	}
	if p.Mode != nil {
		out.Mode = *p.Mode
	}
	if p.BulkSize != nil {
		out.BulkSize = *p.BulkSize
	}
	return out
}

// Validate returns the problems of the settings s: models are the LLM models that may be named, maxExamples the most cases an eval may have.
func Validate(s core.SynthSettings,
	models []string,
	maxExamples int) []evalfile.Problem {
	var out []evalfile.Problem
	add := func(format string,
		args ...any) {
		out = append(out, evalfile.Problem(fmt.Sprintf(format, args...)))
	}
	switch {
	case s.Prompt == "":
		add("prompt is required: describe the eval dataset")
	case len(s.Prompt) > MaxPromptBytes:
		add("prompt is %d bytes, the limit is %d", len(s.Prompt), MaxPromptBytes)
	}
	if len(s.CorpusPrompt) > MaxPromptBytes {
		add("corpus_prompt is %d bytes, the limit is %d", len(s.CorpusPrompt), MaxPromptBytes)
	}
	switch {
	case s.Model == "":
		add("model is required")
	case !slices.Contains(models, s.Model):
		add("model %q is not a supported model", s.Model)
	}
	switch {
	case s.NumExamples < 1:
		add("num_examples must be at least 1")
	case maxExamples > 0 && s.NumExamples > maxExamples:
		add("num_examples is %d, the limit is %d", s.NumExamples, maxExamples)
	}
	if !s.Mode.Valid() {
		add("mode %q must be %q (one case per call) or %q (several per call)", s.Mode, core.SynthSingle, core.SynthBulk)
	}
	if s.BulkSize < 1 || s.BulkSize > MaxBulkSize {
		add("bulk_size must be from 1 to %d", MaxBulkSize)
	}
	return out
}
