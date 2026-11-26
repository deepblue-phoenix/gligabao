//go:build tsjs_css

// This file is compiled ONLY with the build tag "tsjs_css" (go build -tags tsjs_css ./...). Without the tag, typescript_stub.go is
// compiled instead, and TypeScript/JavaScript/CSS code is reported as unsupported. The tag covers all the vendored parsers: esparser/
// (esbuild, made by gen_esbuild) for code and CSS, and tsparser/ (typescript-go, made by gen_tsgo) for TypeScript.
//
// WHY A BUILD TAG. This file imports ./esparser, a copy of the parser of esbuild (github.com/evanw/esbuild). That copy is NOT in git:
// it is about 2 MB of third party code, and committing it would put it in the history of this repo for good (and in every upgrade of
// esbuild after it). It is generated at build time instead, by
//
//	go run ./internal/memory/memory_code/gen_esbuild
//
// which downloads the pinned esbuild release and copies its parser packages into esparser/ (see that command for why a copy is needed
// at all: esbuild keeps its parser in internal/ packages, which Go does not let another module import).
//
// Without the tag, nothing here refers to esparser/, so a plain `go build ./...` or `go test ./...` of a fresh checkout, and a program
// that imports gligabao as a library (go get), compile with no generate step; they only lack TypeScript/JavaScript/CSS. With the tag the
// generate step MUST have run first, or the import of esparser/... fails to compile ("package ... is not in std" / "no required module").
//
// The shipped builds always pass the tag after generating: ops/build.py (all commands, so also ops/pypi.py), the Dockerfile,
// run_tests.sh and, for the gligabao_int binary, its run_build.sh. By hand:
//
//	go run ./internal/memory/memory_code/gen_esbuild && go run ./internal/memory/memory_code/gen_tsgo && go build -tags tsjs_css ./cmd/gligabao

package memory_code

import (
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/ast"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/config"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/helpers"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/js_ast"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/js_parser"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/logger"
)

// parsersBuilt tells whether TypeScript and JavaScript are compiled in (see the stub for the build without the tag).
const parsersBuilt = true

// tsFile is what the walk of a parsed JavaScript file needs: the symbol names (identifiers are symbol references), the import records (the module
// paths) and, for each imported local name, the qualified name of what it stands for ("a.b.c" for "import {c} from 'a.b'").
type tsFile struct {
	symbols []ast.Symbol
	records []ast.ImportRecord
	aliases map[string]string
}

// extractJavaScript parses the code with the parser of esbuild (vendored in esparser, see gen_esbuild) and reads the graph from its syntax
// tree. A syntax error fails it. (TypeScript is not parsed here: esbuild's tree has no type syntax, so TypeScript goes through typescript-go
// alone, see tsgo.go.) Imports are kept even when only types use them.
func extractJavaScript(text string) (g Graph, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			g, err = Graph{}, fmt.Errorf("memory_code: parse javascript: %v", rec)
		}
	}()
	defines := config.ProcessDefines(nil)
	opts := &config.Options{
		Defines: &defines,
		JSX:     config.JSXOptions{Parse: true},
	}
	log := logger.NewDeferLog(logger.DeferLogNoVerboseOrDebug, nil)
	tree, ok := js_parser.Parse(log,
		logger.Source{
			Contents: text,
			KeyPath:  logger.Path{Text: "input"},
		},
		js_parser.OptionsFromConfig(opts))
	if !ok || log.HasErrors() {
		return Graph{}, fmt.Errorf("memory_code: parse javascript: %s", firstError(log.Done()))
	}
	f := tsFile{
		symbols: tree.Symbols,
		records: tree.ImportRecords,
		aliases: map[string]string{},
	}
	f.aliases = importAliases(f,
		tree.Parts)
	stmts := []js_ast.Stmt{}
	for _, p := range tree.Parts {
		stmts = append(stmts, p.Stmts...)
	}
	root := scope{
		name: RootName,
		kind: TypeModule,
	}
	return resolveCalls(dedupe(unalias(concat(Graph{
		Entities: []graph.Entity{{Name: RootName, Type: TypeModule}},
	}, f.stmts(stmts,
		root))))), nil
}

// firstError is the text of the first error message of the log.
func firstError(msgs []logger.Msg) string {
	for _, m := range msgs {
		if m.Kind == logger.Error {
			loc := ""
			if m.Data.Location != nil {
				loc = fmt.Sprintf("line %d: ", m.Data.Location.Line)
			}
			return loc + m.Data.Text
		}
	}
	return "syntax error"
}

// importAliases maps each local name an import statement binds to what it stands for: the name of the module plus the imported name
// ("mod.name"; "mod.default" for a default import; just "mod" for a namespace import, so that "ns.f" is "mod.f").
func importAliases(f tsFile,
	parts []js_ast.Part) map[string]string {
	out := map[string]string{}
	for _, p := range parts {
		for _, s := range p.Stmts {
			imp, ok := s.Data.(*js_ast.SImport)
			if !ok {
				continue
			}
			mod := f.modulePath(imp.ImportRecordIndex)
			if imp.DefaultName != nil {
				out[f.name(imp.DefaultName.Ref)] = mod + ".default"
			}
			if imp.StarNameLoc != nil {
				out[f.name(imp.NamespaceRef)] = mod
			}
			if imp.Items != nil {
				for _, it := range *imp.Items {
					out[f.name(it.Name.Ref)] = mod + "." + it.Alias
				}
			}
		}
	}
	return out
}

func (f tsFile) name(ref ast.Ref) string {
	if int(ref.InnerIndex) >= len(f.symbols) {
		return ""
	}
	return f.symbols[ref.InnerIndex].OriginalName
}

func (f tsFile) modulePath(i uint32) string {
	if int(i) >= len(f.records) {
		return ""
	}
	return f.records[i].Path.Text
}

// stmts is the graph of statements, in a scope.
func (f tsFile) stmts(ss []js_ast.Stmt,
	sc scope) Graph {
	return mapGraphs(ss, func(s js_ast.Stmt) Graph {
		return f.stmt(s,
			sc)
	})
}

// stmt is the graph of one statement and everything inside it.
func (f tsFile) stmt(s js_ast.Stmt,
	sc scope) Graph {
	switch d := s.Data.(type) {
	case *js_ast.SClass:
		return f.class(d.Class,
			f.name(d.Class.Name.Ref),
			sc)
	case *js_ast.SFunction:
		return f.function(d.Fn,
			f.name(d.Fn.Name.Ref),
			sc)
	case *js_ast.SLocal:
		return mapGraphs(d.Decls, func(dc js_ast.Decl) Graph {
			return f.decl(dc,
				sc)
		})
	case *js_ast.SImport:
		return f.importStmt(d,
			sc)
	case *js_ast.SExportFrom:
		return f.moduleImport(d.ImportRecordIndex,
			sc)
	case *js_ast.SExportStar:
		return f.moduleImport(d.ImportRecordIndex,
			sc)
	case *js_ast.SExportDefault:
		return f.exportDefault(d,
			sc)
	case *js_ast.SExpr:
		return f.expr(d.Value,
			sc)
	case *js_ast.SBlock:
		return f.stmts(d.Stmts,
			sc)
	case *js_ast.SIf:
		return concat(f.expr(d.Test,
			sc), f.stmt(d.Yes,
			sc), f.optStmt(d.NoOrNil,
			sc))
	case *js_ast.SFor:
		return concat(f.optStmt(d.InitOrNil,
			sc), f.expr(d.TestOrNil,
			sc), f.expr(d.UpdateOrNil,
			sc), f.stmt(d.Body,
			sc))
	case *js_ast.SForIn:
		return concat(f.stmt(d.Init,
			sc), f.expr(d.Value,
			sc), f.stmt(d.Body,
			sc))
	case *js_ast.SForOf:
		return concat(f.stmt(d.Init,
			sc), f.expr(d.Value,
			sc), f.stmt(d.Body,
			sc))
	case *js_ast.SWhile:
		return concat(f.expr(d.Test,
			sc), f.stmt(d.Body,
			sc))
	case *js_ast.SDoWhile:
		return concat(f.stmt(d.Body,
			sc), f.expr(d.Test,
			sc))
	case *js_ast.SLabel:
		return f.stmt(d.Stmt,
			sc)
	case *js_ast.SWith:
		return concat(f.expr(d.Value,
			sc), f.stmt(d.Body,
			sc))
	case *js_ast.SReturn:
		return f.expr(d.ValueOrNil,
			sc)
	case *js_ast.SThrow:
		return f.expr(d.Value,
			sc)
	case *js_ast.STry:
		return concat(f.stmts(d.Block.Stmts,
			sc), f.catch(d.Catch,
			sc), f.finally(d.Finally,
			sc))
	case *js_ast.SSwitch:
		return concat(f.expr(d.Test,
			sc), mapGraphs(d.Cases, func(c js_ast.Case) Graph {
			return concat(f.expr(c.ValueOrNil,
				sc), f.stmts(c.Body,
				sc))
		}))
	}
	return Graph{}
}

func (f tsFile) optStmt(s js_ast.Stmt,
	sc scope) Graph {
	if s.Data == nil {
		return Graph{}
	}
	return f.stmt(s,
		sc)
}

func (f tsFile) catch(c *js_ast.Catch,
	sc scope) Graph {
	if c == nil {
		return Graph{}
	}
	return f.stmts(c.Block.Stmts,
		sc)
}

func (f tsFile) finally(c *js_ast.Finally,
	sc scope) Graph {
	if c == nil {
		return Graph{}
	}
	return f.stmts(c.Block.Stmts,
		sc)
}

// moduleImport is the "imports" edge of the scope to the module of an import record.
func (f tsFile) moduleImport(i uint32,
	sc scope) Graph {
	mod := f.modulePath(i)
	if mod == "" {
		return Graph{}
	}
	return imported(sc,
		mod,
		TypeModule)
}

// importStmt is "import d, {a as b} from 'm'": the scope imports the module and each name of it.
func (f tsFile) importStmt(d *js_ast.SImport,
	sc scope) Graph {
	mod := f.modulePath(d.ImportRecordIndex)
	g := f.moduleImport(d.ImportRecordIndex,
		sc)
	if mod == "" {
		return g
	}
	names := []string{}
	if d.DefaultName != nil {
		names = append(names, "default")
	}
	if d.Items != nil {
		for _, it := range *d.Items {
			names = append(names, it.Alias)
		}
	}
	return concat(g, mapGraphs(names, func(n string) Graph {
		return imported(sc,
			mod+"."+n,
			TypeSymbol)
	}))
}

// exportDefault is "export default <class | function | expression>".
func (f tsFile) exportDefault(d *js_ast.SExportDefault,
	sc scope) Graph {
	switch v := d.Value.Data.(type) {
	case *js_ast.SClass:
		return f.class(v.Class,
			f.defaultName(v.Class.Name),
			sc)
	case *js_ast.SFunction:
		return f.function(v.Fn,
			f.defaultName(v.Fn.Name),
			sc)
	}
	return f.stmt(d.Value,
		sc)
}

// defaultName is the name of a default export: its own name, or the one esbuild gives it (like "input_default"), which is "default".
func (f tsFile) defaultName(own *ast.LocRef) string {
	if own != nil {
		return f.name(own.Ref)
	}
	return "default"
}

// decl is a "const|let|var" declarator: a function, arrow function or class bound to a name is a symbol of that name; any other value is
// walked as code of the scope.
func (f tsFile) decl(d js_ast.Decl,
	sc scope) Graph {
	id, ok := d.Binding.Data.(*js_ast.BIdentifier)
	if !ok {
		return f.expr(d.ValueOrNil,
			sc)
	}
	return f.named(f.name(id.Ref),
		d.ValueOrNil,
		sc)
}

// named is the graph of the value of a name: a function value is the function (a method in a class), a class value is the class.
func (f tsFile) named(name string,
	value js_ast.Expr,
	sc scope) Graph {
	switch v := value.Data.(type) {
	case *js_ast.EArrow:
		return f.callable(name,
			v.Args,
			v.Body,
			sc)
	case *js_ast.EFunction:
		return f.callable(name,
			v.Fn.Args,
			v.Fn.Body,
			sc)
	case *js_ast.EClass:
		return f.class(v.Class,
			name,
			sc)
	}
	return f.expr(value,
		sc)
}

func (f tsFile) function(fn js_ast.Fn,
	name string,
	sc scope) Graph {
	return f.callable(name,
		fn.Args,
		fn.Body,
		sc)
}

// loweredDecl is what esbuild makes of "namespace N {...}" and "enum E {...}": "var N; (function (N) {...})(N || (N = {}))". It
// is found by its shape: an immediately called function of one parameter, called with "N ||= {}". The body of an enum is only
// assignments of its members, a namespace is the members it declares.
type loweredDecl struct {
	name string
	body []js_ast.Stmt
}

func (f tsFile) lowered(c *js_ast.ECall) (loweredDecl, bool) {
	args, body, ok := funcParts(c.Target)
	if !ok || len(args) != 1 || len(c.Args) != 1 {
		return loweredDecl{}, false
	}
	bin, ok := c.Args[0].Data.(*js_ast.EBinary)
	if !ok || (bin.Op != js_ast.BinOpLogicalOrAssign && bin.Op != js_ast.BinOpLogicalOr) {
		return loweredDecl{}, false
	}
	id, ok := bin.Left.Data.(*js_ast.EIdentifier)
	if !ok {
		return loweredDecl{}, false
	}
	return loweredDecl{
		name: f.name(id.Ref),
		body: body,
	}, true
}

// funcParts are the parameters and the statements of a function or arrow function expression.
func funcParts(e js_ast.Expr) ([]js_ast.Arg, []js_ast.Stmt, bool) {
	switch d := e.Data.(type) {
	case *js_ast.EFunction:
		return d.Fn.Args, d.Fn.Body.Block.Stmts, true
	case *js_ast.EArrow:
		return d.Args, d.Body.Block.Stmts, true
	}
	return nil, nil, false
}

func (f tsFile) loweredDecl(d loweredDecl,
	sc scope) Graph {
	name := qualify(sc,
		d.name)
	if isEnumBody(d.body) {
		return definedBy(sc,
			name,
			TypeEnum,
			"")
	}
	return concat(definedBy(sc,
		name,
		TypeNamespace,
		""), f.stmts(d.body,
		scope{
			name: name,
			kind: TypeNamespace,
		}))
}

// isEnumBody tells the body of a lowered enum: it has statements, and each is "E[E["A"] = 0] = "A"" (or "E["A"] = 0").
func isEnumBody(ss []js_ast.Stmt) bool {
	if len(ss) == 0 {
		return false
	}
	for _, s := range ss {
		e, ok := s.Data.(*js_ast.SExpr)
		if !ok {
			return false
		}
		bin, ok := e.Value.Data.(*js_ast.EBinary)
		if !ok || bin.Op != js_ast.BinOpAssign {
			return false
		}
		if _, ok := bin.Left.Data.(*js_ast.EIndex); !ok {
			return false
		}
	}
	return true
}

// callable is a function or method that a scope defines, with the code of its parameters and body in its own scope.
func (f tsFile) callable(name string,
	args []js_ast.Arg,
	body js_ast.FnBody,
	sc scope) Graph {
	typ := TypeFunction
	if sc.kind == TypeClass {
		typ = TypeMethod
	}
	q := qualify(sc,
		name)
	inner := scope{
		name: q,
		kind: TypeFunction,
	}
	return concat(definedBy(sc,
		q,
		typ,
		""), argumentsOf(q,
		f.argNames(args)), mapGraphs(args, func(a js_ast.Arg) Graph {
		return f.expr(a.DefaultOrNil,
			inner)
	}), f.stmts(body.Block.Stmts,
		inner))
}

// argNames are the names of the parameters that are plain identifiers (a destructured parameter has no single name).
func (f tsFile) argNames(args []js_ast.Arg) []string {
	names := []string{}
	for _, a := range args {
		if id, ok := a.Binding.Data.(*js_ast.BIdentifier); ok {
			names = append(names, f.name(id.Ref))
		}
	}
	return names
}

// class is a class that a scope defines: its base class, and its members (methods and fields that hold functions are methods).
func (f tsFile) class(c js_ast.Class,
	name string,
	sc scope) Graph {
	q := qualify(sc,
		name)
	inner := scope{
		name: q,
		kind: TypeClass,
	}
	g := definedBy(sc,
		q,
		TypeClass,
		"")
	if base, ok := f.chain(c.ExtendsOrNil); ok {
		g = concat(g, Graph{
			Entities: []graph.Entity{{Name: base, Type: TypeClass}},
			Edges:    []graph.Edge{{Source: q, Target: base, Relation: RelInherits}},
		})
	}
	return concat(g, f.expr(c.ExtendsOrNil,
		sc), mapGraphs(c.Properties, func(p js_ast.Property) Graph {
		return f.member(p,
			inner)
	}))
}

// member is a property of a class: a method, getter or setter, a field (that is a method if it holds a function) or a static block.
func (f tsFile) member(p js_ast.Property,
	sc scope) Graph {
	if p.ClassStaticBlock != nil {
		return f.stmts(p.ClassStaticBlock.Block.Stmts,
			sc)
	}
	name, named := f.key(p)
	if !named {
		return concat(f.expr(p.ValueOrNil,
			sc), f.expr(p.InitializerOrNil,
			sc))
	}
	value := p.ValueOrNil
	if value.Data == nil {
		value = p.InitializerOrNil
	}
	return f.named(name,
		value,
		sc)
}

// key is the name of a property whose key is an identifier or a string (not a computed one).
func (f tsFile) key(p js_ast.Property) (string, bool) {
	if p.Flags.Has(js_ast.PropertyIsComputed) {
		return "", false
	}
	switch k := p.Key.Data.(type) {
	case *js_ast.EString:
		return helpers.UTF16ToString(k.Value), true
	case *js_ast.EPrivateIdentifier:
		return f.name(k.Ref), true
	}
	return "", false
}

// chain is the dotted name of an expression that is a name or a member access of names ("a", "a.b.c", "this.x"); an imported name is
// replaced by what it stands for. false for anything else (a call, an index, ...).
func (f tsFile) chain(e js_ast.Expr) (string, bool) {
	switch d := e.Data.(type) {
	case *js_ast.EIdentifier:
		return f.qualified(f.name(d.Ref)), true
	case *js_ast.EImportIdentifier:
		return f.qualified(f.name(d.Ref)), true
	case *js_ast.EThis:
		return "this", true
	case *js_ast.EDot:
		base, ok := f.chain(d.Target)
		return base + "." + d.Name, ok
	}
	return "", false
}

// qualified replaces an imported local name with the name it stands for.
func (f tsFile) qualified(name string) string {
	if full, ok := f.aliases[name]; ok {
		return full
	}
	return name
}

// call is the graph of the call (or "new") of a target with arguments: the "calls" edge, if the callee has a name, and the graph of the
// callee expression and the arguments.
func (f tsFile) call(target js_ast.Expr,
	args []js_ast.Expr,
	sc scope) Graph {
	g := f.exprs(args,
		sc)
	callee, ok := f.chain(target)
	if !ok {
		return concat(g, f.expr(target,
			sc))
	}
	return concat(g, Graph{
		Entities: []graph.Entity{{Name: callee, Type: TypeFunction}},
		Edges:    []graph.Edge{{Source: sc.name, Target: callee, Relation: RelCalls}},
	})
}

func (f tsFile) exprs(es []js_ast.Expr,
	sc scope) Graph {
	return mapGraphs(es, func(e js_ast.Expr) Graph {
		return f.expr(e,
			sc)
	})
}

// expr is the graph of an expression: its calls, imports and the functions and classes inside it (those are code of the scope).
func (f tsFile) expr(e js_ast.Expr,
	sc scope) Graph {
	switch d := e.Data.(type) {
	case nil:
		return Graph{}
	case *js_ast.ECall:
		if ns, ok := f.lowered(d); ok {
			return f.loweredDecl(ns,
				sc)
		}
		return f.call(d.Target,
			d.Args,
			sc)
	case *js_ast.ENew:
		return f.call(d.Target,
			d.Args,
			sc)
	case *js_ast.EImportString:
		return f.moduleImport(d.ImportRecordIndex,
			sc)
	case *js_ast.ERequireString:
		return f.moduleImport(d.ImportRecordIndex,
			sc)
	case *js_ast.EImportCall:
		return f.exprs([]js_ast.Expr{d.Expr, d.OptionsOrNil},
			sc)
	case *js_ast.EArrow:
		return concat(mapGraphs(d.Args, func(a js_ast.Arg) Graph {
			return f.expr(a.DefaultOrNil,
				sc)
		}), f.stmts(d.Body.Block.Stmts,
			sc))
	case *js_ast.EFunction:
		return concat(mapGraphs(d.Fn.Args, func(a js_ast.Arg) Graph {
			return f.expr(a.DefaultOrNil,
				sc)
		}), f.stmts(d.Fn.Body.Block.Stmts,
			sc))
	case *js_ast.EClass:
		if d.Class.Name == nil { // no name, no symbol: its code belongs to the scope
			return concat(f.expr(d.Class.ExtendsOrNil,
				sc), mapGraphs(d.Class.Properties, func(p js_ast.Property) Graph {
				return f.member(p,
					sc)
			}))
		}
		return f.class(d.Class,
			f.name(d.Class.Name.Ref),
			sc)
	}
	return f.exprs(subExprs(e),
		sc)
}

// subExprs are the direct sub-expressions of the expressions that have no meaning of their own for the graph.
func subExprs(e js_ast.Expr) []js_ast.Expr {
	props := func(ps []js_ast.Property) []js_ast.Expr {
		out := []js_ast.Expr{}
		for _, p := range ps {
			out = append(out, p.ValueOrNil, p.InitializerOrNil)
			if p.Flags.Has(js_ast.PropertyIsComputed) {
				out = append(out, p.Key)
			}
		}
		return out
	}
	switch d := e.Data.(type) {
	case *js_ast.EArray:
		return d.Items
	case *js_ast.EUnary:
		return []js_ast.Expr{d.Value}
	case *js_ast.EBinary:
		return []js_ast.Expr{d.Left, d.Right}
	case *js_ast.EDot:
		return []js_ast.Expr{d.Target}
	case *js_ast.EIndex:
		return []js_ast.Expr{d.Target, d.Index}
	case *js_ast.EIf:
		return []js_ast.Expr{d.Test, d.Yes, d.No}
	case *js_ast.EObject:
		return props(d.Properties)
	case *js_ast.ESpread:
		return []js_ast.Expr{d.Value}
	case *js_ast.EAwait:
		return []js_ast.Expr{d.Value}
	case *js_ast.EYield:
		return []js_ast.Expr{d.ValueOrNil}
	case *js_ast.ETemplate:
		out := []js_ast.Expr{d.TagOrNil}
		for _, p := range d.Parts {
			out = append(out, p.Value)
		}
		return out
	case *js_ast.EJSXElement:
		return append(append([]js_ast.Expr{d.TagOrNil}, props(d.Properties)...), d.NullableChildren...)
	}
	return nil
}

// unalias undoes the alias that esbuild gives a class inside its own body when it needs one (a class "Foo" that is used in its body is
// "_Foo" there): a "calls" or "inherits" target "_X" is "X" if the code defines "X".
func unalias(g Graph) Graph {
	defined := map[string]bool{}
	for _, e := range g.Edges {
		if e.Relation == RelDefines {
			defined[e.Target] = true
		}
	}
	fix := func(n string) string {
		if t, ok := strings.CutPrefix(n, "_"); ok && defined[t] {
			return t
		}
		return n
	}
	edges := make([]graph.Edge, len(g.Edges))
	for i, e := range g.Edges {
		if e.Relation == RelCalls || e.Relation == RelInherits {
			e.Target = fix(e.Target)
		}
		edges[i] = e
	}
	entities := make([]graph.Entity, len(g.Entities))
	for i, e := range g.Entities {
		e.Name = fix(e.Name)
		entities[i] = e
	}
	return Graph{
		Entities: entities,
		Edges:    edges,
	}
}

// parseTypeScript is the Result of TypeScript, from the tree of typescript-go alone (see tsgo.go): the graph of the code and the types.
func parseTypeScript(text string) (Result, error) {
	out, err := tsParse(text)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Language:    LangTypeScript,
		Graph:       resolveCalls(dedupe(out.Graph)),
		Types:       out.Types,
		Annotations: out.Annotations,
	}, nil
}

// parseJavaScript is the Result of JavaScript: the graph of esbuild's tree (JavaScript has no type syntax).
func parseJavaScript(text string) (Result, error) {
	g, err := extractJavaScript(text)
	return Result{
		Language: LangJavaScript,
		Graph:    g,
	}, err
}
