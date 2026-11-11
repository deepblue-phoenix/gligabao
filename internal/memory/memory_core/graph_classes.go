package memory_core

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// The graph classes: the classify_graph stage of Remember (see docs/remember/remember.md#graph-classes) asks an LLM which class or classes the graph
// of each artifact belongs to, and stores every answer as a record of gb_artifact_classes with the run of the model call. Those classes are
// what the classifying training hook (see training_hook.go) gets as the classes of the graphs.

const (
	// MaxGraphClasses is the most classes a request can restrict the choice to (graph_classes).
	MaxGraphClasses = 50
	// MaxGraphClassLen is the longest class name, in characters (a longer one is cut).
	MaxGraphClassLen = 64
	// MaxClassesPerGraph is the most classes one call gives to a graph (the first ones the LLM names).
	MaxClassesPerGraph = 3
)

// ErrInvalidGraphClasses means the classes a call may choose from are malformed (see ResolveGraphClasses).
var ErrInvalidGraphClasses = errors.New("invalid graph_classes")

// NormalizeGraphClass makes the stored form of a class name: lowercase, every run of characters that are not letters or digits turned into
// one "_", no "_" at either end, at most MaxGraphClassLen characters. "" if nothing is left (it is no class).
func NormalizeGraphClass(name string) string {
	var b strings.Builder
	pending := false
	n := 0
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if pending && b.Len() > 0 {
				b.WriteByte('_')
				n++
			}
			pending = false
			if n >= MaxGraphClassLen {
				break
			}
			b.WriteRune(r)
			n++
			continue
		}
		pending = true
	}
	return strings.Trim(b.String(), "_")
}

// ResolveGraphClasses validates the classes that a call restricts the choice of the LLM to (the graph_classes of a remember request) and
// returns them normalized (NormalizeGraphClass), without repeats, in order. It returns ErrInvalidGraphClasses for a class that is empty once
// normalized, or for more than MaxGraphClasses. A nil or empty list gives nil: the LLM chooses freely.
func ResolveGraphClasses(classes []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, c := range classes {
		n := NormalizeGraphClass(c)
		if n == "" {
			return nil, fmt.Errorf("%w: %q is not a class name (it needs letters or digits)", ErrInvalidGraphClasses, c)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) > MaxGraphClasses {
		return nil, fmt.Errorf("%w: at most %d classes", ErrInvalidGraphClasses, MaxGraphClasses)
	}
	return out, nil
}

// WithClassifyGraph sets whether Remember classifies the graph of each artifact it stores with an LLM (the classify_graph stage; default
// false, as it costs a call per artifact). With a non-empty classes (validated with ResolveGraphClasses first) the LLM can only choose
// among them; without, it chooses freely, being told the classes the dataset already uses so that it reuses them.
func WithClassifyGraph(on bool, classes []string) Option {
	return func(o *CallOptions) { o.ClassifyGraph, o.GraphClasses = on, classes }
}
