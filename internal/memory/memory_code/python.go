package memory_code

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/antlr4-go/antlr/v4"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/pyparser"
)

// scope is where a piece of Python code is: the qualified name of the enclosing module, class or function, and what it is.
type scope struct {
	name string
	kind string
}

var (
	dottedRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*(\.[A-Za-z_][A-Za-z_0-9]*)*$`)
	quoteRe  = regexp.MustCompile(`^[rRuUbBfF]{0,2}("""|'''|"|')`)
)

// silent drops the diagnostics: the lexer and the parser recover from a syntax error on their own, and what parsed is extracted.
type silent struct {
	*antlr.DefaultErrorListener
}

// extractPython parses the Python code (the ANTLR4 grammar of pyparser) and reads its graph from the parse tree. A syntax error does not
// fail it. The code must end with a newline, which is added if missing.
func extractPython(text string) (g Graph, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			g, err = Graph{}, fmt.Errorf("memory_code: parse python: %v", rec)
		}
	}()
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	lexer := pyparser.NewPython3Lexer(antlr.NewInputStream(text))
	lexer.RemoveErrorListeners()
	lexer.AddErrorListener(silent{antlr.NewDefaultErrorListener()})
	parser := pyparser.NewPython3Parser(antlr.NewCommonTokenStream(lexer, antlr.TokenDefaultChannel))
	parser.RemoveErrorListeners()
	parser.AddErrorListener(silent{antlr.NewDefaultErrorListener()})
	tree := parser.File_input()

	root := scope{
		name: RootName,
		kind: TypeModule,
	}
	g = merge(Graph{
		Entities: []graph.Entity{{Name: RootName, Type: TypeModule}},
	}, walkPython(tree, root))
	return resolveCalls(dedupe(g)), nil
}

// walkPython is the graph of a node of the parse tree and everything below it, in the given scope.
func walkPython(n antlr.Tree,
	sc scope) Graph {
	switch c := n.(type) {
	case *pyparser.ClassdefContext:
		return pyClass(c, sc)
	case *pyparser.FuncdefContext:
		return pyFunction(c, sc)
	case *pyparser.Import_nameContext:
		return pyImportName(c, sc)
	case *pyparser.Import_fromContext:
		return pyImportFrom(c, sc)
	case *pyparser.Atom_exprContext:
		return merge(pyCalls(c, sc), walkChildren(c, sc))
	}
	return walkChildren(n, sc)
}

// walkChildren is the merged graph of the children of the node.
func walkChildren(n antlr.Tree,
	sc scope) Graph {
	return foldGraphs(n.GetChildren(), func(c antlr.Tree) Graph {
		return walkPython(c, sc)
	})
}

// foldGraphs merges the graphs that f makes of the nodes.
func foldGraphs(nodes []antlr.Tree,
	f func(antlr.Tree) Graph) Graph {
	return foldFrom(nodes, Graph{}, f)
}

func foldFrom(nodes []antlr.Tree,
	acc Graph,
	f func(antlr.Tree) Graph) Graph {
	if len(nodes) == 0 {
		return acc
	}
	return foldFrom(nodes[1:], merge(acc, f(nodes[0])), f)
}

func qualify(sc scope,
	name string) string {
	if sc.kind == TypeModule {
		return name
	}
	return sc.name + "." + name
}

// definedBy is the entity and the "defines" edge of a symbol that a scope defines.
func definedBy(sc scope,
	name,
	typ,
	doc string) Graph {
	return Graph{
		Entities: []graph.Entity{{Name: name, Type: typ, Description: doc}},
		Edges: []graph.Edge{{
			Source:   sc.name,
			Target:   name,
			Relation: RelDefines,
		}},
	}
}

func pyClass(c *pyparser.ClassdefContext,
	sc scope) Graph {
	name := qualify(sc, c.Name().GetText())
	inner := scope{
		name: name,
		kind: TypeClass,
	}
	g := definedBy(sc, name, TypeClass, pyDocstring(c.Block()))
	if args := c.Arglist(); args != nil {
		g = merge(g, foldGraphs(args.GetChildren(), func(a antlr.Tree) Graph {
			base := a.(antlr.ParseTree).GetText() // a keyword (metaclass=X) or a call is not a base
			if _, ok := a.(*pyparser.ArgumentContext); !ok || !dottedRe.MatchString(base) {
				return Graph{}
			}
			return Graph{
				Entities: []graph.Entity{{Name: base, Type: TypeClass}},
				Edges:    []graph.Edge{{Source: name, Target: base, Relation: RelInherits}},
			}
		}))
	}
	return merge(g, walkChildren(c.Block(), inner))
}

func pyFunction(c *pyparser.FuncdefContext,
	sc scope) Graph {
	typ := TypeFunction
	if sc.kind == TypeClass {
		typ = TypeMethod
	}
	name := qualify(sc, c.Name().GetText())
	inner := scope{
		name: name,
		kind: TypeFunction,
	}
	g := definedBy(sc, name, typ, pyDocstring(c.Block()))
	return merge(g, walkChildren(c.Block(), inner))
}

func imported(sc scope,
	name,
	typ string) Graph {
	return Graph{
		Entities: []graph.Entity{{Name: name, Type: typ}},
		Edges:    []graph.Edge{{Source: sc.name, Target: name, Relation: RelImports}},
	}
}

// pyImportName is "import a.b, c as d": the scope imports the modules.
func pyImportName(c *pyparser.Import_nameContext,
	sc scope) Graph {
	return foldGraphs(c.Dotted_as_names().GetChildren(), func(a antlr.Tree) Graph {
		if d, ok := a.(*pyparser.Dotted_as_nameContext); ok {
			return imported(sc, d.Dotted_name().GetText(), TypeModule)
		}
		return Graph{}
	})
}

// pyImportFrom is "from m import a, b as c": the scope imports the module and each name of it (a relative module keeps its dots).
func pyImportFrom(c *pyparser.Import_fromContext,
	sc scope) Graph {
	mod := strings.Repeat(".", len(c.AllDOT())) + strings.Repeat("...", len(c.AllELLIPSIS()))
	if d := c.Dotted_name(); d != nil {
		mod += d.GetText()
	}
	g := Graph{}
	if mod != "" && strings.Trim(mod, ".") != "" {
		g = imported(sc, mod, TypeModule)
	}
	names := c.Import_as_names()
	if names == nil {
		return g
	}
	return merge(g, foldGraphs(names.GetChildren(), func(a antlr.Tree) Graph {
		n, ok := a.(*pyparser.Import_as_nameContext)
		if !ok {
			return Graph{}
		}
		sep := "."
		if strings.HasSuffix(mod, ".") {
			sep = ""
		}
		return imported(sc, mod+sep+n.Name(0).GetText(), TypeSymbol)
	}))
}

// pyCalls is the "calls" edges of the calls in `atom trailer*`: the callee is the name or dotted name before the "(" ; a call of anything
// else, like f()() or a[0](), is skipped. The graph of the arguments is found by the walk of the children.
func pyCalls(c *pyparser.Atom_exprContext,
	sc scope) Graph {
	atom := c.Atom().GetText()
	chain := ""
	if dottedRe.MatchString(atom) {
		chain = atom
	}
	g := Graph{}
	for _, t := range c.AllTrailer() {
		tr := t.(*pyparser.TrailerContext)
		switch {
		case tr.DOT() != nil && chain != "":
			chain += "." + tr.Name().GetText()
		case tr.OPEN_PAREN() != nil && chain != "":
			g = merge(g, Graph{
				Entities: []graph.Entity{{Name: chain, Type: TypeFunction}},
				Edges:    []graph.Edge{{Source: sc.name, Target: chain, Relation: RelCalls}},
			})
			chain = ""
		default:
			chain = ""
		}
	}
	return g
}

// pyDocstring is the docstring of a block (its first statement, if that is only a string), or "".
func pyDocstring(b pyparser.IBlockContext) string {
	var first antlr.Tree
	switch {
	case b.Simple_stmts() != nil:
		first = b.Simple_stmts()
	case len(b.AllStmt()) > 0:
		first = b.Stmt(0)
	default:
		return ""
	}
	return docOf(first)
}

// docOf follows the single-child chain of a statement down to its atom, and gives the string of it if it is one.
func docOf(n antlr.Tree) string {
	switch c := n.(type) {
	case *pyparser.AtomContext:
		tok := c.GetStart()
		if tok == nil || tok.GetTokenType() != pyparser.Python3ParserSTRING {
			return ""
		}
		raw := tok.GetText()
		loc := quoteRe.FindStringSubmatchIndex(raw)
		if loc == nil {
			return ""
		}
		q := raw[loc[2]:loc[3]]
		return strings.TrimSpace(strings.TrimSuffix(raw[loc[3]:], q))
	case *pyparser.Simple_stmtsContext:
		return docOf(c.GetChild(0))
	}
	if n.GetChildCount() != 1 {
		return ""
	}
	return docOf(n.GetChild(0))
}

// resolveCalls points the calls to a name the code defines at the defined entity: "self.run" and "run" are the "Class.run" or "run" that
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
		key := strings.TrimPrefix(strings.TrimPrefix(callee, "self."), "cls.")
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
