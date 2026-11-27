package evalfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
)

// Limits are the limits of a user eval file (see ValidateUser). A zero numeric limit means "no limit".
type Limits struct {
	// MaxFileBytes is the size of the eval file.
	MaxFileBytes int
	// MaxCases is the number of cases of an eval.
	MaxCases int
	// MaxRemember is the number of remember items of a case.
	MaxRemember int
	// MaxTextBytes is the size of one text (the text file is checked by its size when it is uploaded, see MaxTextBytes of usereval).
	MaxTextBytes int
	// MaxQueryBytes is the size of a query.
	MaxQueryBytes int
	// Models are the models a "models" map may name (llm.SupportedModels); empty: every model is refused.
	Models []string
	// ModelStages and ReasoningStages are the keys a "models" and a "reasoning" map take; empty: the keys are not checked.
	ModelStages     []string
	ReasoningStages []string
}

// DefaultLimits are the limits of section 5.3 of the design (config keys evals_max_*): 1 MiB per file, 100 cases, 20 remember items
// per case, 256 KiB per text, 4 KiB per query. models and the stage lists are the caller's.
func DefaultLimits(models,
	modelStages,
	reasoningStages []string) Limits {
	return Limits{
		MaxFileBytes:    1 << 20,
		MaxCases:        100,
		MaxRemember:     20,
		MaxTextBytes:    256 << 10,
		MaxQueryBytes:   4 << 10,
		Models:          models,
		ModelStages:     modelStages,
		ReasoningStages: reasoningStages,
	}
}

// Problem is one reason a user eval is not valid, in words for the user.
type Problem string

// UserEval is a user eval file once parsed: the cases are the knowledge cases.
type UserEval = Eval[core.KnowledgeCase]

// ReservedNames are the words that cannot be the name of an eval: they are path segments of the API (/v1/gb/evals/runs/{id}, ...).
var ReservedNames = []string{"runs", "case_runs", "trajectories", "synth"}

var (
	nameRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	textFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)
)

// maxTextFileLen is the longest text_file path.
const maxTextFileLen = 200

// ValidName reports whether s can be the name of an eval or of a case: 1-64 characters of letters, digits, ".", "_" and "-", starting
// with a letter or a digit, and without "..".
func ValidName(s string) bool {
	return nameRe.MatchString(s) && !strings.Contains(s, "..")
}

// ValidTextFile reports whether s is a valid text_file: a relative path of at most 200 bytes, segments of letters, digits, ".", "_"
// and "-" that start with a letter or a digit, separated by single "/"; no "..", no leading "/", no "//". A path of one segment that
// ends in ".json" is refused too: it would be the file of an eval.
func ValidTextFile(s string) bool {
	return len(s) <= maxTextFileLen &&
		!strings.Contains(s, "..") &&
		textFileRe.MatchString(s) &&
		!(strings.HasSuffix(s, ".json") && !strings.Contains(s, "/"))
}

// ParseUser parses the file of a user eval, strictly (an unknown key is a problem, so a typo does not silently drop a check). The
// cases are returned even when ValidateUser finds problems in them; the error is only for a file that is not JSON of the right shape.
func ParseUser(data []byte) (UserEval, error) {
	var f struct {
		Name        string               `json:"name"`
		Description string               `json:"description"`
		Cases       []core.KnowledgeCase `json:"cases"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return UserEval{}, fmt.Errorf("not a valid eval file: %w", err)
	}
	if dec.More() {
		return UserEval{}, fmt.Errorf("not a valid eval file: more than one JSON value")
	}
	return UserEval{
		Name:        f.Name,
		Description: f.Description,
		Cases:       f.Cases,
	}, nil
}

// ValidateUser checks the file of a user eval that is to be stored as "<name>.json" and returns every problem found (none: it is
// valid). It is pure: whether the text_file files exist is checked by the caller (TextFiles gives their paths). The rules:
//   - the file is at most limits.MaxFileBytes, valid JSON of the shape of section 5.2, and its "name" equals name;
//   - the eval name and the case names match ValidName, the eval name is not one of ReservedNames, case names are unique, and there are
//     1 to limits.MaxCases cases;
//   - a case has at most limits.MaxRemember remember items, each with exactly one of "text" and "text_file" (a valid ValidTextFile path),
//     a text of at most limits.MaxTextBytes, a "kind" that is empty, "knowledge" or "episode", and a "reference_time" in RFC 3339;
//   - a query is at most limits.MaxQueryBytes; a case has something to do (a remember item or a query);
//   - "models" values are in limits.Models and "reasoning" values are reasoning levels, with keys from the stage lists of limits;
//   - "chunk_size" is valid for memory_chunk.Config.
func ValidateUser(data []byte,
	name string,
	limits Limits) []Problem {
	if limits.MaxFileBytes > 0 && len(data) > limits.MaxFileBytes {
		return []Problem{Problem(fmt.Sprintf("the eval file is %d bytes, the limit is %d", len(data), limits.MaxFileBytes))}
	}
	var problems []Problem
	add := func(format string,
		args ...any) {
		problems = append(problems, Problem(fmt.Sprintf(format, args...)))
	}
	if !ValidName(name) {
		add("the eval name %q is not valid (1-64 characters: letters, digits, . _ -, starting with a letter or digit, no \"..\")", name)
	}
	if slices.Contains(ReservedNames, name) {
		add("the eval name %q is reserved (reserved: %s)", name, strings.Join(ReservedNames, ", "))
	}
	ev, err := ParseUser(data)
	if err != nil {
		return append(problems, Problem(err.Error()))
	}
	if ev.Name != name {
		add("the name in the file (%q) must be the name of the eval (%q)", ev.Name, name)
	}
	switch {
	case len(ev.Cases) == 0:
		add("the eval has no cases")
	case limits.MaxCases > 0 && len(ev.Cases) > limits.MaxCases:
		add("the eval has %d cases, the limit is %d", len(ev.Cases), limits.MaxCases)
	}
	seen := map[string]bool{}
	for i, c := range ev.Cases {
		label := fmt.Sprintf("case #%d", i+1)
		switch {
		case c.Name == "":
			add("%s has no name", label)
		case !ValidName(c.Name):
			add("%s: the name %q is not valid (1-64 characters: letters, digits, . _ -, starting with a letter or digit, no \"..\")", label, c.Name)
			label = fmt.Sprintf("case %q", c.Name)
		case seen[c.Name]:
			add("%s: duplicate case name %q", label, c.Name)
			label = fmt.Sprintf("case %q", c.Name)
		default:
			label = fmt.Sprintf("case %q", c.Name)
		}
		seen[c.Name] = true
		problems = append(problems, validateCase(label, c, limits)...)
	}
	return problems
}

// validateCase returns the problems of one case.
func validateCase(label string,
	c core.KnowledgeCase,
	limits Limits) []Problem {
	var problems []Problem
	add := func(format string,
		args ...any) {
		problems = append(problems, Problem(label+": "+fmt.Sprintf(format, args...)))
	}
	if len(c.Remember) == 0 && c.Query == "" {
		add("nothing to do: it has no remember item and no query")
	}
	if limits.MaxRemember > 0 && len(c.Remember) > limits.MaxRemember {
		add("%d remember items, the limit is %d", len(c.Remember), limits.MaxRemember)
	}
	if limits.MaxQueryBytes > 0 && len(c.Query) > limits.MaxQueryBytes {
		add("the query is %d bytes, the limit is %d", len(c.Query), limits.MaxQueryBytes)
	}
	for i, m := range c.Remember {
		item := fmt.Sprintf("remember #%d", i+1)
		switch {
		case m.Text != "" && m.TextFile != "":
			add("%s has both text and text_file", item)
		case m.Text == "" && m.TextFile == "":
			add("%s needs text or text_file", item)
		}
		if limits.MaxTextBytes > 0 && len(m.Text) > limits.MaxTextBytes {
			add("%s: the text is %d bytes, the limit is %d", item, len(m.Text), limits.MaxTextBytes)
		}
		if m.TextFile != "" && !ValidTextFile(m.TextFile) {
			add("%s: text_file %q is not a valid relative path (letters, digits, . _ -, segments separated by /, at most %d bytes, no \"..\")", item, m.TextFile, maxTextFileLen)
		}
		if m.Kind != "" && m.Kind != "knowledge" && m.Kind != "episode" {
			add("%s: kind %q must be knowledge or episode", item, m.Kind)
		}
		if m.ReferenceTime != "" {
			if _, err := time.Parse(time.RFC3339, m.ReferenceTime); err != nil {
				add("%s: reference_time %q is not RFC 3339", item, m.ReferenceTime)
			}
		}
	}
	if c.ChunkSize != 0 {
		if err := (memory_chunk.Config{Size: c.ChunkSize}).Validate(); err != nil {
			add("chunk_size: %v", err)
		}
	}
	for _, stage := range sortedKeys(c.Models) {
		model := c.Models[stage]
		if len(limits.ModelStages) > 0 && !slices.Contains(limits.ModelStages, stage) {
			add("models: %q is not a stage (stages: %s)", stage, strings.Join(limits.ModelStages, ", "))
		}
		if !slices.Contains(limits.Models, model) {
			add("models: %q for %s is not a supported model", model, stage)
		}
	}
	for _, stage := range sortedKeys(c.Reasoning) {
		level := c.Reasoning[stage]
		if len(limits.ReasoningStages) > 0 && !slices.Contains(limits.ReasoningStages, stage) {
			add("reasoning: %q is not a stage (stages: %s)", stage, strings.Join(limits.ReasoningStages, ", "))
		}
		if _, ok := llm.NormalizeReasoning(level); !ok || strings.TrimSpace(level) == "" {
			add("reasoning: %q for %s is not a level (levels: %s)", level, stage, strings.Join(llm.ReasoningLevels, ", "))
		}
	}
	return problems
}

// TextFiles returns the distinct text_file paths the cases name, sorted.
func TextFiles(ev UserEval) []string {
	set := map[string]bool{}
	for _, c := range ev.Cases {
		for _, m := range c.Remember {
			if m.TextFile != "" {
				set[m.TextFile] = true
			}
		}
	}
	return sortedKeys(set)
}

// sortedKeys returns the keys of m, sorted.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
