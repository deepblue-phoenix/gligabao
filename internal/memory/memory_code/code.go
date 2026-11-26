// Package memory_code extracts the knowledge graph of source code, for the remember pipeline: where a text goes through the LLM, code is
// parsed (tree-sitter) and its graph is read from the syntax tree, deterministically and without a model. Python is supported; Go is next
// (see Detect and ErrUnsupportedLanguage).
//
// The graph: entities are the code symbols (types "class", "function", "method", "module", "symbol" for an imported name), edges are the
// relations between them ("defines", "inherits", "calls", "imports"). Names are qualified by their scope ("Class.method"); the code
// itself is the entity "module".
package memory_code

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// Languages of the code (the Language of Detect and Extract).
const (
	LangPython = "python"
	LangGo     = "go"
)

// Types of the entities, and relations of the edges, of a code graph.
const (
	TypeModule   = "module"
	TypeClass    = "class"
	TypeFunction = "function"
	TypeMethod   = "method"
	TypeSymbol   = "symbol"

	RelDefines  = "defines"
	RelInherits = "inherits"
	RelCalls    = "calls"
	RelImports  = "imports"
)

// RootName is the name of the entity that stands for the code as a whole (the file that was remembered).
const RootName = "module"

// ErrUnsupportedLanguage means the language of the code has no extractor (yet).
var ErrUnsupportedLanguage = errors.New("memory_code: unsupported language")

// Graph is the graph extracted from code.
type Graph struct {
	Entities []graph.Entity
	Edges    []graph.Edge
}

var goPackageRe = regexp.MustCompile(`(?m)^package\s+\w+\s*$`)

// Detect guesses the language of the code: LangGo if it has a package clause, LangPython otherwise.
func Detect(text string) string {
	if goPackageRe.MatchString(text) {
		return LangGo
	}
	return LangPython
}

// Extract extracts the graph of the code in the given language. ErrUnsupportedLanguage (wrapped) for a language without extractor.
func Extract(lang,
	text string) (Graph, error) {
	switch lang {
	case LangPython:
		return extractPython(text)
	}
	return Graph{}, fmt.Errorf("%w: %q",
		ErrUnsupportedLanguage,
		lang)
}

// ExtractAuto is Extract of the language that Detect guesses.
func ExtractAuto(text string) (Graph, error) {
	return Extract(Detect(text),
		text)
}

// merge is the union of two graphs (neither is changed).
func merge(a,
	b Graph) Graph {
	return Graph{
		Entities: append(append([]graph.Entity{}, a.Entities...), b.Entities...),
		Edges:    append(append([]graph.Edge{}, a.Edges...), b.Edges...),
	}
}

// dedupe drops the repeated entities (by name; the first one wins, but one with a description replaces one without) and the repeated edges.
func dedupe(g Graph) Graph {
	type ekey struct{ s, r, t string }
	entities := map[string]int{}
	out := Graph{}
	for _, e := range g.Entities {
		if i, ok := entities[e.Name]; ok {
			if out.Entities[i].Description == "" {
				out.Entities[i].Description = e.Description
			}
			continue
		}
		entities[e.Name] = len(out.Entities)
		out.Entities = append(out.Entities, e)
	}
	seen := map[ekey]bool{}
	for _, e := range g.Edges {
		k := ekey{e.Source, e.Relation, e.Target}
		if seen[k] || e.Source == e.Target {
			continue
		}
		seen[k] = true
		out.Edges = append(out.Edges, e)
	}
	return out
}
