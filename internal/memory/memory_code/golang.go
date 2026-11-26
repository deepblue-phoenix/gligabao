package memory_code

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// parseGo is the Result of Go code. It is parsed by the standard library (go/parser, go/ast), so it needs no build tag. A syntax error does
// not fail it when the parser still gives a tree (what parsed is extracted).
//
// The graph: structs are "class", interfaces "interface", other named types "type_alias", package level var/const "variable", functions
// "function" and methods "method" named "Recv.Method" (the pointer is dropped). The module "defines" them. A struct "inherits" the
// types it embeds, an interface "inherits" the interfaces it embeds. Imports are "imports" edges of the module (the import path).
// Calls are "calls" edges of the enclosing function, method or module; a call through the receiver ("s.run()") is a call of "Recv.run".
func parseGo(text string) (res Result, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			res, err = Result{}, fmt.Errorf("memory_code: parse go: %v", rec)
		}
	}()
	file, parseErr := parser.ParseFile(token.NewFileSet(),
		"",
		text,
		parser.ParseComments|parser.SkipObjectResolution)
	if file == nil {
		return Result{}, fmt.Errorf("memory_code: parse go: %w", parseErr)
	}
	root := scope{
		name: RootName,
		kind: TypeModule,
	}
	g := merge(Graph{
		Entities: []graph.Entity{{Name: RootName, Type: TypeModule}},
	}, mapGraphs(file.Decls, func(d ast.Decl) Graph {
		return goDecl(d, root)
	}))
	return Result{
		Language: LangGo,
		Graph:    resolveCalls(dedupe(g)),
	}, nil
}

// goDecl is the graph of a top level declaration.
func goDecl(d ast.Decl,
	sc scope) Graph {
	switch c := d.(type) {
	case *ast.FuncDecl:
		return goFunc(c, sc)
	case *ast.GenDecl:
		return mapGraphs(c.Specs, func(s ast.Spec) Graph {
			return goSpec(s, c, sc)
		})
	}
	return Graph{}
}

// goSpec is the graph of an import, type, var or const spec.
func goSpec(s ast.Spec,
	gd *ast.GenDecl,
	sc scope) Graph {
	switch c := s.(type) {
	case *ast.ImportSpec:
		path, unquoteErr := strconv.Unquote(c.Path.Value)
		if unquoteErr != nil {
			return Graph{}
		}
		return imported(sc, path, TypeModule)
	case *ast.TypeSpec:
		return goType(c, gd, sc)
	case *ast.ValueSpec:
		return merge(mapGraphs(c.Names, func(n *ast.Ident) Graph {
			if n.Name == "_" {
				return Graph{}
			}
			return definedBy(sc,
				n.Name,
				TypeVariable,
				goDoc(c.Doc, gd.Doc))
		}), mapGraphs(c.Values, func(v ast.Expr) Graph {
			return goCalls(v, sc, "")
		}))
	}
	return Graph{}
}

// goType is a named type: its entity, and the embedded types of a struct or interface.
func goType(t *ast.TypeSpec,
	gd *ast.GenDecl,
	sc scope) Graph {
	typ, fields := TypeAlias, (*ast.FieldList)(nil)
	switch x := t.Type.(type) {
	case *ast.StructType:
		typ, fields = TypeClass, x.Fields
	case *ast.InterfaceType:
		typ, fields = TypeInterface, x.Methods
	}
	g := definedBy(sc,
		t.Name.Name,
		typ,
		goDoc(t.Doc, gd.Doc))
	if fields == nil {
		return g
	}
	return merge(g, mapGraphs(fields.List, func(f *ast.Field) Graph {
		base := goTypeName(f.Type)
		if len(f.Names) > 0 || base == "" {
			return Graph{}
		}
		return Graph{
			Entities: []graph.Entity{{Name: base, Type: typ}},
			Edges:    []graph.Edge{{Source: t.Name.Name, Target: base, Relation: RelInherits}},
		}
	}))
}

// goFunc is a function or method, with the calls in its body.
func goFunc(f *ast.FuncDecl,
	sc scope) Graph {
	recvType, recvName := goReceiver(f)
	name, typ := f.Name.Name, TypeFunction
	if recvType != "" {
		name, typ = recvType+"."+name, TypeMethod
	}
	g := definedBy(sc,
		name,
		typ,
		goDoc(f.Doc))
	if f.Body == nil {
		return g
	}
	inner := scope{
		name: name,
		kind: TypeFunction,
	}
	return merge(g, goCalls(f.Body, inner, goRecvPrefix(recvName, recvType)))
}

// goReceiver is the type name and the variable name of the receiver of a method (both "" for a function).
func goReceiver(f *ast.FuncDecl) (string, string) {
	if f.Recv == nil || len(f.Recv.List) == 0 {
		return "", ""
	}
	r := f.Recv.List[0]
	recvName := ""
	if len(r.Names) > 0 {
		recvName = r.Names[0].Name
	}
	return goTypeName(r.Type), recvName
}

// goRecvPrefix is "recv=Type", what goCalls needs to point a call through the receiver at the method of its type ("" if no receiver).
func goRecvPrefix(recvName,
	recvType string) string {
	if recvName == "" || recvName == "_" || recvType == "" {
		return ""
	}
	return recvName + "=" + recvType
}

// goCalls is the "calls" edges of every call in the node (function literals included): the callee is a name or a dotted name (pkg.Func,
// x.Method); a call of anything else is skipped. recv is the result of goRecvPrefix.
func goCalls(n ast.Node,
	sc scope,
	recv string) Graph {
	recvName, recvType, _ := strings.Cut(recv, "=")
	g := Graph{}
	ast.Inspect(n, func(x ast.Node) bool {
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		callee := goDotted(call.Fun)
		calleeType := TypeFunction
		if recv != "" && strings.HasPrefix(callee, recvName+".") && strings.Count(callee, ".") == 1 {
			callee, calleeType = recvType+callee[len(recvName):], TypeMethod
		}
		if callee != "" {
			g = merge(g, Graph{
				Entities: []graph.Entity{{Name: callee, Type: calleeType}},
				Edges:    []graph.Edge{{Source: sc.name, Target: callee, Relation: RelCalls}},
			})
		}
		return true
	})
	return g
}

// goDotted is the name of an identifier or selector chain ("a.b.c"), or "" for anything else (also a generic instantiation is read through).
func goDotted(e ast.Expr) string {
	switch c := e.(type) {
	case *ast.Ident:
		return c.Name
	case *ast.SelectorExpr:
		if base := goDotted(c.X); base != "" {
			return base + "." + c.Sel.Name
		}
	case *ast.ParenExpr:
		return goDotted(c.X)
	case *ast.IndexExpr:
		return goDotted(c.X)
	case *ast.IndexListExpr:
		return goDotted(c.X)
	}
	return ""
}

// goTypeName is the name of a type expression without pointer and type arguments ("*pkg.T[int]" is "pkg.T"), or "".
func goTypeName(e ast.Expr) string {
	if s, ok := e.(*ast.StarExpr); ok {
		return goTypeName(s.X)
	}
	return goDotted(e)
}

// goDoc is the first doc comment that is not empty, as one trimmed text.
func goDoc(groups ...*ast.CommentGroup) string {
	for _, c := range groups {
		if c != nil {
			return strings.TrimSpace(c.Text())
		}
	}
	return ""
}
