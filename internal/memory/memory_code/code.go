// Package memory_code extracts the knowledge graph of source code, for the remember pipeline: where a text goes through the LLM, code is
// parsed and its graph is read from the syntax tree, deterministically and without a model. Python (ANTLR4), TypeScript/JavaScript (esbuild for the code, typescript-go for the types) and CSS (esbuild) are supported; Go is next
// (see Detect and ErrUnsupportedLanguage).
//
// The graph: entities are the code symbols (types "class", "function", "method", "module", "symbol" for an imported name), edges are the
// relations between them ("defines", "inherits", "calls", "imports"). Names are qualified by their scope ("Class.method"); the code
// itself is the entity "module".
package memory_code

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// Languages of the code (the Language of Detect and Extract).
const (
	LangPython     = "python"
	LangTypeScript = "typescript"
	LangJavaScript = "javascript"
	LangCSS        = "css"
	LangGo         = "go"
)

// Types of the entities, and relations of the edges, of a code graph.
const (
	TypeModule   = "module"
	TypeClass    = "class"
	TypeFunction = "function"
	TypeMethod   = "method"
	TypeSymbol   = "symbol"

	TypeNamespace = "namespace"
	TypeEnum      = "enum"

	// TypeScript types (from the parser of typescript-go) and CSS (from the parser of esbuild)
	TypeInterface = "interface"
	TypeAlias     = "type_alias"
	TypeSelector  = "selector"
	TypeVariable  = "variable"
	TypeKeyframes = "keyframes"

	RelDefines  = "defines"
	RelInherits = "inherits"
	RelCalls    = "calls"
	RelImports  = "imports"

	RelImplements = "implements"
	RelUsesType   = "uses_type"
	RelUses       = "uses"
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

// Language is a language that has an extractor: its name, the file extensions (lowercase, with the dot) that tell code is in it, and the
// function that reads the graph of its code.
type Language struct {
	Name       string
	Extensions []string
	parse      func(text string) (Result, error)
	// built is false when the extractor is not compiled in (TypeScript/JavaScript without the build tag "tsjs_css").
	built bool
}

// languages are the supported languages. To add one: write its extractor (a file of this package) and add it here.
var languages = []Language{
	{
		Name:       LangPython,
		Extensions: []string{".py", ".pyi", ".pyw"},
		parse:      parsePython,
		built:      true,
	},
	{
		Name:       LangTypeScript,
		Extensions: []string{".ts", ".tsx", ".mts", ".cts"},
		parse:      parseTypeScript,
		built:      parsersBuilt,
	},
	{
		Name:       LangJavaScript,
		Extensions: []string{".js", ".jsx", ".mjs", ".cjs"},
		parse:      parseJavaScript,
		built:      parsersBuilt,
	},
	{
		Name:       LangCSS,
		Extensions: []string{".css"},
		parse:      parseCSS,
		built:      parsersBuilt,
	},
}

var goPackageRe = regexp.MustCompile(`(?m)^package\s+\w+\s*$`)

// Languages are the languages that can be extracted.
func Languages() []Language {
	return slices.Clone(languages)
}

// languageOf is the language of a file name, by its extension (case insensitive).
func languageOf(filename string) (string, bool) {
	ext := strings.ToLower(path.Ext(filename))
	i := slices.IndexFunc(languages, func(l Language) bool {
		return slices.Contains(l.Extensions, ext)
	})
	if i < 0 {
		return "", false
	}
	return languages[i].Name, true
}

// Detect tells the language of the code. With a file name it is the language of the extension of the name, and ErrUnsupportedLanguage
// (wrapped) if no language has that extension (or none). Without one the language is guessed from the code: LangGo if it has a package
// clause, LangPython otherwise.
func Detect(filename,
	text string) (string, error) {
	if strings.TrimSpace(filename) != "" {
		if lang, ok := languageOf(strings.TrimSpace(filename)); ok {
			return lang, builtOrError(lang)
		}
		return "", fmt.Errorf("%w: file %q",
			ErrUnsupportedLanguage,
			filename)
	}
	if goPackageRe.MatchString(text) {
		return LangGo, nil
	}
	return LangPython, nil
}

// builtOrError is the language, or ErrUnsupportedLanguage (wrapped) if its extractor is not compiled in (see typescript.go).
func builtOrError(lang string) error {
	i := slices.IndexFunc(languages, func(l Language) bool {
		return l.Name == lang
	})
	if i >= 0 && !languages[i].built {
		return fmt.Errorf("%w: %s is not built in: build with -tags tsjs_css after `go run` of gen_esbuild and gen_tsgo (internal/memory/memory_code)",
			ErrUnsupportedLanguage,
			lang)
	}
	return nil
}

// Parse parses the code in the given language into the one Result (graph, types, annotations, styles). ErrUnsupportedLanguage
// (wrapped) for a language without a parser.
func Parse(lang,
	text string) (Result, error) {
	i := slices.IndexFunc(languages, func(l Language) bool {
		return l.Name == lang
	})
	if i < 0 {
		return Result{}, fmt.Errorf("%w: %q",
			ErrUnsupportedLanguage,
			lang)
	}
	return languages[i].parse(text)
}

// ParseAuto is Parse of the language that Detect finds, from the file name (may be empty) or the code.
func ParseAuto(filename,
	text string) (Result, error) {
	lang, err := Detect(filename,
		text)
	if err != nil {
		return Result{}, err
	}
	return Parse(lang,
		text)
}

// Extract is the graph of Parse.
func Extract(lang,
	text string) (Graph, error) {
	r, err := Parse(lang,
		text)
	return r.Graph, err
}

// ExtractAuto is Extract of the language that Detect finds, from the file name (may be empty) or the code.
func ExtractAuto(filename,
	text string) (Graph, error) {
	r, err := ParseAuto(filename,
		text)
	return r.Graph, err
}

// concat is the union of any number of graphs (none is changed).
func concat(gs ...Graph) Graph {
	out := Graph{}
	for _, g := range gs {
		out.Entities = append(out.Entities, g.Entities...)
		out.Edges = append(out.Edges, g.Edges...)
	}
	return out
}

// mapGraphs is the union of the graphs that f makes of the items.
func mapGraphs[T any](items []T,
	f func(T) Graph) Graph {
	gs := make([]Graph, len(items))
	for i, it := range items {
		gs[i] = f(it)
	}
	return concat(gs...)
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

// resolveCalls points the calls to a name the code defines at the defined entity: "self.run" or "this.run" and "run" are the "Class.run" or "run" that
// the code defines, when exactly one symbol has that name. The entities of unresolved calls that a resolved call replaced are dropped.
func resolveCalls(g Graph) Graph {
	defined := map[string]bool{}
	for _, e := range g.Edges {
		if e.Relation == RelDefines {
			defined[e.Target] = true
		}
	}
	bySimple := map[string][]string{}
	for _, e := range g.Entities {
		if defined[e.Name] {
			simple := e.Name[strings.LastIndex(e.Name, ".")+1:]
			bySimple[simple] = append(bySimple[simple], e.Name)
		}
	}
	resolve := func(callee string) string {
		key := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(callee, "self."), "cls."), "this.")
		if strings.Contains(key, ".") {
			return callee
		}
		if c := bySimple[key]; len(c) == 1 {
			return c[0]
		}
		return callee
	}
	edges := make([]graph.Edge, 0, len(g.Edges))
	for _, e := range g.Edges {
		if e.Relation == RelCalls {
			e.Target = resolve(e.Target)
		}
		edges = append(edges, e)
	}
	used := map[string]bool{}
	for _, e := range edges {
		used[e.Source], used[e.Target] = true, true
	}
	entities := make([]graph.Entity, 0, len(g.Entities))
	for _, e := range g.Entities {
		if used[e.Name] || e.Name == RootName {
			entities = append(entities, e)
		}
	}
	return dedupe(Graph{Entities: entities, Edges: edges})
}
