//go:build tsjs_css

// TypeScript, parsed only with typescript-go, the official TypeScript parser (vendored in tsparser by gen_tsgo; see typescript.go for the build
// tag). One walk of its syntax tree gives both halves of the Result: the code graph (what is defined, called, imported, inherited: the same
// graph esbuild's tree gives for JavaScript) and the types (interfaces, type aliases, enums, annotations, "implements"), which esbuild's
// parser drops. Symbols are named by their scope names joined by ".".

package memory_code

import (
	"fmt"
	"slices"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/tsparser/ast"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/tsparser/core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/tsparser/parser"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/tsparser/scanner"
)

// tsSyntax is a parsed file: the text (nodes are slices of it) and, for each local name an import binds, what it stands for (the same
// "module.name" convention as esbuild's side: "module.default" for a default import, the module for a namespace import).
type tsSyntax struct {
	text    string
	aliases map[string]string
}

// tsOut is what a walk of the tree yields; plus is the union of two.
type tsOut struct {
	Types       []TypeDecl
	Annotations []Annotation
	Graph       Graph
}

func (a tsOut) plus(b tsOut) tsOut {
	return tsOut{
		Types:       append(slices.Clone(a.Types), b.Types...),
		Annotations: append(slices.Clone(a.Annotations), b.Annotations...),
		Graph:       merge(a.Graph, b.Graph),
	}
}

// tsParse parses TypeScript and walks its tree. The text is parsed as TypeScript, and as TSX if that has syntax errors (the parser needs to
// be told, and the language table only gives it the text): a syntax error in both fails it, with the one of TypeScript.
func tsParse(text string) (out tsOut, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			out, err = tsOut{}, fmt.Errorf("memory_code: parse typescript: %v", rec)
		}
	}()
	file := parseTSFile(text,
		core.ScriptKindTS)
	if len(file.Diagnostics()) > 0 {
		tsx := parseTSFile(text,
			core.ScriptKindTSX)
		if len(tsx.Diagnostics()) > 0 {
			return tsOut{}, fmt.Errorf("memory_code: parse typescript: %s",
				firstDiagnostic(text,
					file))
		}
		file = tsx
	}
	syn := tsSyntax{
		text: text,
	}
	syn.aliases = syn.importAliases(file.Statements.Nodes)
	root := scope{
		name: RootName,
		kind: TypeModule,
	}
	return tsOut{
		Graph: Graph{
			Entities: []graph.Entity{{Name: RootName, Type: TypeModule}},
		},
	}.plus(syn.walkAll(file.Statements.Nodes,
		root,
		nil)), nil
}

func parseTSFile(text string,
	kind core.ScriptKind) *ast.SourceFile {
	return parser.ParseSourceFile(ast.SourceFileParseOptions{
		FileName: "/input.ts",
		Path:     "/input.ts",
	},
		text,
		kind)
}

// firstDiagnostic is the first syntax error of the file, with its line.
func firstDiagnostic(text string,
	file *ast.SourceFile) string {
	d := file.Diagnostics()[0]
	return fmt.Sprintf("line %d: %s",
		1+strings.Count(text[:min(d.Pos(), len(text))], "\n"),
		d.MessageText())
}

// importAliases maps the local names of the import declarations to what they stand for.
func (s tsSyntax) importAliases(stmts []*ast.Node) map[string]string {
	out := map[string]string{}
	for _, st := range stmts {
		if st.Kind == ast.KindImportEqualsDeclaration {
			if mod, ok := requiredModule(st); ok {
				out[st.Name().Text()] = mod
			}
			continue
		}
		if st.Kind != ast.KindImportDeclaration || st.ImportClause() == nil {
			continue
		}
		mod := st.ModuleSpecifier().Text()
		clause := st.ImportClause()
		if n := clause.Name(); n != nil {
			out[n.Text()] = mod + ".default"
		}
		bindings := clause.AsImportClause().NamedBindings
		switch {
		case bindings == nil:
		case bindings.Kind == ast.KindNamespaceImport:
			out[bindings.Name().Text()] = mod
		case bindings.Kind == ast.KindNamedImports:
			for _, el := range bindings.AsNamedImports().Elements.Nodes {
				imported := el.Name().Text()
				if p := el.AsImportSpecifier().PropertyName; p != nil {
					imported = p.Text()
				}
				out[el.Name().Text()] = mod + "." + imported
			}
		}
	}
	return out
}

// sumOut is the union of the outputs.
func sumOut(outs []tsOut) tsOut {
	acc := tsOut{}
	for _, o := range outs {
		acc = acc.plus(o)
	}
	return acc
}

// foldOut is the union of the outputs that f makes of the nodes.
func foldOut(nodes []*ast.Node,
	f func(*ast.Node) tsOut) tsOut {
	outs := make([]tsOut, len(nodes))
	for i, n := range nodes {
		outs[i] = f(n)
	}
	return sumOut(outs)
}

// walkAll is the output of the nodes, in a scope. tparams are the type parameters in scope.
func (s tsSyntax) walkAll(nodes []*ast.Node,
	sc scope,
	tparams []string) tsOut {
	return foldOut(nodes, func(n *ast.Node) tsOut {
		return s.walk(n,
			sc,
			tparams)
	})
}

// walk is the output of a node and everything inside it: it dispatches on the kind of the node. Declarations give entities (and their
// own scope); calls, imports and "new" give edges of the scope; any other node (statements, expressions, anonymous functions and classes,
// whose code belongs to the scope) is walked through its children.
func (s tsSyntax) walk(n *ast.Node,
	sc scope,
	tparams []string) tsOut {
	switch n.Kind {
	case ast.KindImportDeclaration:
		return s.importDecl(n,
			sc)
	case ast.KindImportEqualsDeclaration:
		if mod, ok := requiredModule(n); ok {
			return tsOut{
				Graph: imported(sc,
					mod,
					TypeModule),
			}
		}
		return tsOut{}
	case ast.KindExportDeclaration:
		if spec := n.ModuleSpecifier(); spec != nil {
			return tsOut{
				Graph: imported(sc,
					spec.Text(),
					TypeModule),
			}
		}
		return tsOut{}
	case ast.KindInterfaceDeclaration:
		return s.interfaceDecl(n,
			sc,
			tparams)
	case ast.KindTypeAliasDeclaration:
		return s.aliasDecl(n,
			sc,
			tparams)
	case ast.KindEnumDeclaration:
		return s.enumDecl(n,
			sc)
	case ast.KindClassDeclaration:
		if name, ok := declName(n); ok {
			return s.class(n,
				name,
				sc,
				tparams)
		}
		return tsOut{}
	case ast.KindClassExpression:
		if n.Name() == nil { // no name, no symbol: its code belongs to the scope
			return s.classBody(n,
				sc,
				tparams)
		}
		return s.class(n,
			n.Name().Text(),
			sc,
			tparams)
	case ast.KindFunctionDeclaration:
		if name, ok := declName(n); ok {
			return s.callable(n,
				name,
				sc,
				tparams)
		}
		return tsOut{}
	case ast.KindVariableDeclaration:
		return s.variable(n,
			sc,
			tparams)
	case ast.KindModuleDeclaration:
		return s.module(n,
			sc,
			tparams)
	case ast.KindCallExpression:
		return s.call(n,
			sc,
			tparams)
	case ast.KindNewExpression:
		return s.call(n,
			sc,
			tparams)
	}
	return s.children(n,
		sc,
		tparams)
}

// children is the output of the children of a node.
func (s tsSyntax) children(n *ast.Node,
	sc scope,
	tparams []string) tsOut {
	kids := []*ast.Node{}
	n.ForEachChild(func(c *ast.Node) bool {
		kids = append(kids,
			c)
		return false
	})
	return s.walkAll(kids,
		sc,
		tparams)
}

// declName is the name of a function or class declaration: its own, or "default" for "export default function () {}".
func declName(n *ast.Node) (string, bool) {
	switch {
	case n.Name() != nil:
		return n.Name().Text(), true
	case n.ModifierFlags()&ast.ModifierFlagsDefault != 0:
		return "default", true
	}
	return "", false
}

// requiredModule is the module of "import x = require('m')".
func requiredModule(n *ast.Node) (string, bool) {
	ref := n.AsImportEqualsDeclaration().ModuleReference
	if ref == nil || ref.Kind != ast.KindExternalModuleReference {
		return "", false
	}
	return ref.Expression().Text(), true
}

// importDecl is "import d, {a as b} from 'm'": the scope imports the module and each name of it (a namespace import is just the module).
func (s tsSyntax) importDecl(n *ast.Node,
	sc scope) tsOut {
	mod := n.ModuleSpecifier().Text()
	g := imported(sc,
		mod,
		TypeModule)
	clause := n.ImportClause()
	if clause == nil {
		return tsOut{Graph: g}
	}
	if clause.Name() != nil {
		g = merge(g, imported(sc,
			mod+".default",
			TypeSymbol))
	}
	if b := clause.AsImportClause().NamedBindings; b != nil && b.Kind == ast.KindNamedImports {
		for _, el := range b.AsNamedImports().Elements.Nodes {
			imp := el.Name().Text()
			if p := el.AsImportSpecifier().PropertyName; p != nil {
				imp = p.Text()
			}
			g = merge(g, imported(sc,
				mod+"."+imp,
				TypeSymbol))
		}
	}
	return tsOut{Graph: g}
}

// call is a call or "new": the "calls" edge, if the callee has a name, and what is in the arguments and the callee expression. "import(
// 'm')" and "require('m')" are imports of the scope instead.
func (s tsSyntax) call(n *ast.Node,
	sc scope,
	tparams []string) tsOut {
	callee := n.Expression()
	args := n.Arguments()
	if mod, ok := moduleCall(n); ok {
		return tsOut{
			Graph: imported(sc,
				mod,
				TypeModule),
		}
	}
	argOut := s.walkAll(args,
		sc,
		tparams)
	name, ok := s.chain(callee)
	if !ok {
		return argOut.plus(s.walk(callee,
			sc,
			tparams))
	}
	return argOut.plus(tsOut{
		Graph: Graph{
			Entities: []graph.Entity{{Name: name, Type: TypeFunction}},
			Edges: []graph.Edge{{
				Source:   sc.name,
				Target:   name,
				Relation: RelCalls,
			}},
		},
	})
}

// moduleCall is the module of "import('m')" and "require('m')" with a string literal argument.
func moduleCall(n *ast.Node) (string, bool) {
	if n.Kind != ast.KindCallExpression || len(n.Arguments()) == 0 {
		return "", false
	}
	arg := n.Arguments()[0]
	if arg.Kind != ast.KindStringLiteral && arg.Kind != ast.KindNoSubstitutionTemplateLiteral {
		return "", false
	}
	callee := n.Expression()
	if callee.Kind == ast.KindImportKeyword || (callee.Kind == ast.KindIdentifier && callee.Text() == "require") {
		return arg.Text(), true
	}
	return "", false
}

// chain is the dotted name of an expression that is a name or a member access of names ("a", "a.b.c", "this.x"); an imported name is
// replaced by what it stands for. Parentheses, "!" and "as" do not matter. false for anything else (a call, an index, ...).
func (s tsSyntax) chain(e *ast.Node) (string, bool) {
	switch e.Kind {
	case ast.KindIdentifier:
		return s.resolve(e.Text()), true
	case ast.KindThisKeyword:
		return "this", true
	case ast.KindParenthesizedExpression, ast.KindNonNullExpression, ast.KindAsExpression, ast.KindSatisfiesExpression:
		return s.chain(e.Expression())
	case ast.KindPropertyAccessExpression:
		base, ok := s.chain(e.Expression())
		return base + "." + e.Name().Text(), ok
	}
	return "", false
}

// module is a namespace (or "declare module"): the entity, and the declarations of its body in its scope. "namespace A.B" is a body that
// is a namespace itself.
func (s tsSyntax) module(n *ast.Node,
	sc scope,
	tparams []string) tsOut {
	name := n.Name().Text()
	q := qualify(sc,
		name)
	inner := scope{
		name: q,
		kind: TypeNamespace,
	}
	out := tsOut{
		Graph: definedBy(sc,
			q,
			TypeNamespace,
			""),
	}
	body := n.Body()
	switch {
	case body == nil:
		return out
	case body.Kind == ast.KindModuleBlock:
		return out.plus(s.walkAll(body.Statements(),
			inner,
			tparams))
	case body.Kind == ast.KindModuleDeclaration:
		return out.plus(s.module(body,
			inner,
			tparams))
	}
	return out
}

// variable is a declared variable: a function or class value is that function or class (named after the variable); a type annotation is
// kept as the annotation of the variable.
func (s tsSyntax) variable(d *ast.Node,
	sc scope,
	tparams []string) tsOut {
	nameNode := d.Name()
	init := d.Initializer()
	if nameNode == nil || nameNode.Kind != ast.KindIdentifier {
		return s.walkOpt(init,
			sc,
			tparams)
	}
	name := nameNode.Text()
	typ := d.Type()
	annotated := s.annotation(sc.name,
		RoleVariable,
		name,
		typ,
		false,
		tparams)
	switch {
	case init == nil:
		return annotated
	case init.Kind == ast.KindArrowFunction || init.Kind == ast.KindFunctionExpression:
		return annotated.plus(s.callable(init,
			name,
			sc,
			tparams))
	case init.Kind == ast.KindClassExpression:
		return annotated.plus(s.class(init,
			name,
			sc,
			tparams))
	}
	return annotated.plus(s.walk(init,
		sc,
		tparams))
}

// walkOpt is walk of a node that may be missing.
func (s tsSyntax) walkOpt(n *ast.Node,
	sc scope,
	tparams []string) tsOut {
	if n == nil {
		return tsOut{}
	}
	return s.walk(n,
		sc,
		tparams)
}

// callable is a function, method, constructor or arrow function: its entity (the description is its signature), the annotations of its
// parameters and return type, and the declarations of its body.
func (s tsSyntax) callable(n *ast.Node,
	name string,
	sc scope,
	tparams []string) tsOut {
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
	data := n.FunctionLikeData()
	own := append(slices.Clone(tparams), s.typeParamNames(data.TypeParameters)...)
	params := paramList(data.Parameters)
	out := tsOut{
		Graph: definedBy(sc,
			q,
			typ,
			s.signature(n,
				name)),
	}.plus(foldOut(params, func(p *ast.Node) tsOut {
		return s.param(p,
			q,
			sc,
			own)
	})).plus(s.annotation(q,
		RoleReturn,
		"",
		data.Type,
		false,
		own))
	defaults := foldOut(params, func(p *ast.Node) tsOut {
		return s.walkOpt(p.Initializer(),
			inner,
			own)
	})
	return out.plus(defaults).plus(s.walkOpt(n.Body(),
		inner,
		own))
}

func paramList(l *ast.ParameterList) []*ast.Node {
	if l == nil {
		return nil
	}
	return l.Nodes
}

// param is the annotation of a parameter. A constructor parameter with a modifier ("private name: string") is also a property of the
// class.
func (s tsSyntax) param(p *ast.Node,
	owner string,
	sc scope,
	tparams []string) tsOut {
	if p.Name() == nil || p.Name().Kind != ast.KindIdentifier {
		return tsOut{}
	}
	name := p.Name().Text()
	optional := p.QuestionToken() != nil
	out := s.annotation(owner,
		RoleParam,
		name,
		p.Type(),
		optional,
		tparams)
	if sc.kind == TypeClass && p.ModifierFlags()&ast.ModifierFlagsParameterPropertyModifier != 0 {
		return out.plus(s.annotation(sc.name,
			RoleProperty,
			name,
			p.Type(),
			optional,
			tparams))
	}
	return out
}

// annotation is the annotation of a type (none for a missing type), and the "uses_type" edges from the owner to the types it names.
func (s tsSyntax) annotation(owner,
	role,
	name string,
	typ *ast.Node,
	optional bool,
	tparams []string) tsOut {
	if typ == nil {
		return tsOut{}
	}
	return tsOut{
		Annotations: []Annotation{{
			Owner:    owner,
			Role:     role,
			Name:     name,
			Type:     s.src(typ),
			Optional: optional,
		}},
		Graph: s.usesType(owner,
			[]*ast.Node{typ},
			tparams),
	}
}

// class is a class: its entity, "inherits" and "implements" edges, and its body.
func (s tsSyntax) class(n *ast.Node,
	name string,
	sc scope,
	tparams []string) tsOut {
	q := qualify(sc,
		name)
	inner := scope{
		name: q,
		kind: TypeClass,
	}
	data := n.ClassLikeData()
	own := append(slices.Clone(tparams), s.typeParamNames(data.TypeParameters)...)
	out := tsOut{
		Graph: definedBy(sc,
			q,
			TypeClass,
			""),
	}
	for _, h := range heritage(data.HeritageClauses) {
		out = out.plus(s.heritageClause(h,
			q,
			sc,
			tparams))
	}
	return out.plus(foldOut(n.Members(), func(m *ast.Node) tsOut {
		return s.member(m,
			inner,
			own)
	}))
}

// heritageClause is an "extends" or "implements" clause of a class: an edge to each named type ("inherits" to a class, "implements" to an
// interface), and what the expression of an "extends" contains (a call that makes the base class).
func (s tsSyntax) heritageClause(h *ast.Node,
	q string,
	sc scope,
	tparams []string) tsOut {
	clause := h.AsHeritageClause()
	rel, typ := RelInherits, TypeClass
	if clause.Token == ast.KindImplementsKeyword {
		rel, typ = RelImplements, TypeInterface
	}
	return foldOut(clause.Types.Nodes, func(t *ast.Node) tsOut {
		expr := t
		if t.Kind == ast.KindExpressionWithTypeArguments {
			expr = t.Expression()
		}
		name, ok := s.chain(expr)
		if t.Kind == ast.KindTypeReference {
			name, ok = s.heritageName(t), true
		}
		if !ok {
			return s.walk(expr,
				sc,
				tparams)
		}
		return tsOut{
			Graph: Graph{
				Entities: []graph.Entity{{Name: name, Type: typ}},
				Edges: []graph.Edge{{
					Source:   q,
					Target:   name,
					Relation: rel,
				}},
			},
		}
	})
}

// classBody is the members of a class without a name: they belong to the scope.
func (s tsSyntax) classBody(n *ast.Node,
	sc scope,
	tparams []string) tsOut {
	data := n.ClassLikeData()
	return foldOut(heritage(data.HeritageClauses), func(h *ast.Node) tsOut {
		return s.heritageClause(h,
			sc.name,
			sc,
			tparams)
	}).plus(foldOut(n.Members(), func(m *ast.Node) tsOut {
		return s.member(m,
			sc,
			tparams)
	}))
}

func heritage(l *ast.HeritageClauseList) []*ast.Node {
	if l == nil {
		return nil
	}
	return l.Nodes
}

// member is a member of a class: a method, accessor or constructor is a callable, a property is an annotation (a property holding a
// function is a method).
func (s tsSyntax) member(m *ast.Node,
	sc scope,
	tparams []string) tsOut {
	switch m.Kind {
	case ast.KindConstructor:
		return s.callable(m,
			"constructor",
			sc,
			tparams)
	case ast.KindMethodDeclaration, ast.KindGetAccessor, ast.KindSetAccessor:
		return s.namedMember(m,
			sc,
			func(name string) tsOut {
				return s.callable(m,
					name,
					sc,
					tparams)
			})
	case ast.KindClassStaticBlockDeclaration:
		return s.walkOpt(m.Body(),
			sc,
			tparams)
	case ast.KindPropertyDeclaration:
		return s.namedMember(m,
			sc,
			func(name string) tsOut {
				annotated := s.annotation(sc.name,
					RoleProperty,
					name,
					m.Type(),
					m.PostfixToken() != nil && m.PostfixToken().Kind == ast.KindQuestionToken,
					tparams)
				init := m.Initializer()
				if init != nil && (init.Kind == ast.KindArrowFunction || init.Kind == ast.KindFunctionExpression) {
					return annotated.plus(s.callable(init,
						name,
						sc,
						tparams))
				}
				return annotated.plus(s.walkOpt(init,
					sc,
					tparams))
			})
	}
	return tsOut{}
}

// namedMember applies f to the name of a member that has a plain name (an identifier, string, number or private name; not a computed one).
func (s tsSyntax) namedMember(m *ast.Node,
	sc scope,
	f func(name string) tsOut) tsOut {
	n := m.Name()
	if n == nil || n.Kind == ast.KindComputedPropertyName {
		return tsOut{}
	}
	return f(n.Text())
}

// interfaceDecl is an interface: the declaration with its members, the entity, "inherits" edges to the interfaces it extends and the
// "uses_type" edges of its members.
func (s tsSyntax) interfaceDecl(n *ast.Node,
	sc scope,
	tparams []string) tsOut {
	q := qualify(sc,
		n.Name().Text())
	data := n.AsInterfaceDeclaration()
	own := append(slices.Clone(tparams), s.typeParamNames(data.TypeParameters)...)
	extends := []string{}
	for _, h := range heritage(data.HeritageClauses) {
		for _, t := range h.AsHeritageClause().Types.Nodes {
			extends = append(extends,
				s.heritageName(t))
		}
	}
	members := []TypeMember{}
	typeNodes := []*ast.Node{}
	for _, m := range n.Members() {
		tm, nodes := s.typeMember(m)
		members = append(members,
			tm...)
		typeNodes = append(typeNodes,
			nodes...)
	}
	edges := []graph.Edge{}
	entities := []graph.Entity{}
	for _, e := range extends {
		entities = append(entities,
			graph.Entity{Name: e, Type: TypeInterface})
		edges = append(edges,
			graph.Edge{Source: q, Target: e, Relation: RelInherits})
	}
	return tsOut{
		Types: []TypeDecl{{
			Kind:       TypeDeclInterface,
			Name:       q,
			Exported:   n.ModifierFlags()&ast.ModifierFlagsExport != 0,
			TypeParams: s.typeParams(data.TypeParameters),
			Extends:    extends,
			Members:    members,
		}},
		Graph: concat(definedBy(sc,
			q,
			TypeInterface,
			""), Graph{
			Entities: entities,
			Edges:    edges,
		}, s.usesType(q,
			typeNodes,
			own)),
	}
}

// typeMember is the members an interface member gives (none for an unknown kind) and the type nodes it uses.
func (s tsSyntax) typeMember(m *ast.Node) ([]TypeMember, []*ast.Node) {
	kinds := map[ast.Kind]string{
		ast.KindPropertySignature:  MemberProperty,
		ast.KindMethodSignature:    MemberMethod,
		ast.KindCallSignature:      MemberCall,
		ast.KindConstructSignature: MemberConstruct,
		ast.KindIndexSignature:     MemberIndex,
		ast.KindGetAccessor:        MemberProperty,
		ast.KindSetAccessor:        MemberProperty,
	}
	kind, ok := kinds[m.Kind]
	if !ok {
		return nil, nil
	}
	name := ""
	if n := m.Name(); n != nil {
		name = s.src(n)
	}
	optional := m.PostfixToken() != nil && m.PostfixToken().Kind == ast.KindQuestionToken
	member := TypeMember{
		Kind:     kind,
		Name:     name,
		Optional: optional,
		Readonly: m.ModifierFlags()&ast.ModifierFlagsReadonly != 0,
	}
	if kind == MemberProperty && m.Kind == ast.KindPropertySignature {
		member.Type = s.src(m.Type())
		return []TypeMember{member}, nonNil(m.Type())
	}
	// a signature: type parameters, parameters and return type, as written
	data := m.FunctionLikeData()
	member.Type = s.signatureText(data)
	nodes := []*ast.Node{}
	for _, p := range paramList(data.Parameters) {
		nodes = append(nodes,
			nonNil(p.Type())...)
	}
	return []TypeMember{member}, append(nodes,
		nonNil(data.Type)...)
}

func nonNil(n *ast.Node) []*ast.Node {
	if n == nil {
		return nil
	}
	return []*ast.Node{n}
}

// aliasDecl is a type alias: the declaration with its right hand side, the entity and the "uses_type" edges.
func (s tsSyntax) aliasDecl(n *ast.Node,
	sc scope,
	tparams []string) tsOut {
	q := qualify(sc,
		n.Name().Text())
	data := n.AsTypeAliasDeclaration()
	own := append(slices.Clone(tparams), s.typeParamNames(data.TypeParameters)...)
	return tsOut{
		Types: []TypeDecl{{
			Kind:       TypeDeclAlias,
			Name:       q,
			Exported:   n.ModifierFlags()&ast.ModifierFlagsExport != 0,
			TypeParams: s.typeParams(data.TypeParameters),
			Type:       s.src(data.Type),
		}},
		Graph: merge(definedBy(sc,
			q,
			TypeAlias,
			""), s.usesType(q,
			nonNil(data.Type),
			own)),
	}
}

// enumDecl is an enum: its entity, and the declaration with its members.
func (s tsSyntax) enumDecl(n *ast.Node,
	sc scope) tsOut {
	kind := TypeDeclEnum
	if n.ModifierFlags()&ast.ModifierFlagsConst != 0 {
		kind = TypeDeclConstEnum
	}
	members := []TypeMember{}
	for _, m := range n.Members() {
		value := ""
		if init := m.Initializer(); init != nil {
			value = s.src(init)
		}
		members = append(members,
			TypeMember{
				Kind: MemberEnum,
				Name: m.Name().Text(),
				Type: value,
			})
	}
	return tsOut{
		Types: []TypeDecl{{
			Kind:     kind,
			Name:     qualify(sc, n.Name().Text()),
			Exported: n.ModifierFlags()&ast.ModifierFlagsExport != 0,
			Members:  members,
		}},
		Graph: definedBy(sc,
			qualify(sc, n.Name().Text()),
			TypeEnum,
			""),
	}
}

// usesType is the "uses_type" edges (and an entity for each type) from the owner to the types the type nodes name: type references,
// with imports resolved and the type parameters in scope left out.
func (s tsSyntax) usesType(owner string,
	typeNodes []*ast.Node,
	tparams []string) Graph {
	names := []string{}
	inner := []string{}
	var visit func(*ast.Node) bool
	visit = func(n *ast.Node) bool {
		switch n.Kind {
		case ast.KindTypeReference:
			names = append(names,
				entityText(n.AsTypeReferenceNode().TypeName))
		case ast.KindTypeParameter:
			inner = append(inner,
				n.Name().Text())
		}
		n.ForEachChild(visit)
		return false
	}
	for _, t := range typeNodes {
		visit(t)
	}
	g := Graph{}
	seen := map[string]bool{}
	for _, name := range names {
		head, _, _ := strings.Cut(name, ".")
		if slices.Contains(tparams, head) || slices.Contains(inner, head) {
			continue
		}
		full := s.resolve(name)
		if seen[full] {
			continue
		}
		seen[full] = true
		g = concat(g, Graph{
			Entities: []graph.Entity{{Name: full, Type: TypeInterface}},
			Edges: []graph.Edge{{
				Source:   owner,
				Target:   full,
				Relation: RelUsesType,
			}},
		})
	}
	return g
}

// entityText is the dotted text of an entity name ("a.b.C").
func entityText(n *ast.Node) string {
	if n.Kind == ast.KindQualifiedName {
		q := n.AsQualifiedName()
		return entityText(q.Left) + "." + q.Right.Text()
	}
	return n.Text()
}

// entityName is the dotted name of an expression that is a name or a property access chain, imports resolved.
func (s tsSyntax) entityName(e *ast.Node) string {
	return s.resolve(strings.Join(strings.Fields(s.src(e)), ""))
}

// heritageName is the name of a type in an "extends" or "implements" clause (the parser makes either an expression with type arguments or a
// type reference), imports resolved.
func (s tsSyntax) heritageName(t *ast.Node) string {
	if t.Kind == ast.KindTypeReference {
		return s.resolve(entityText(t.AsTypeReferenceNode().TypeName))
	}
	return s.entityName(t.Expression())
}

// resolve replaces the head of a dotted name that an import binds with what it stands for.
func (s tsSyntax) resolve(name string) string {
	head, rest, dotted := strings.Cut(name, ".")
	full, ok := s.aliases[head]
	switch {
	case !ok:
		return name
	case dotted:
		return full + "." + rest
	}
	return full
}

// typeParamNames are the names of the type parameters of a declaration.
func (s tsSyntax) typeParamNames(l *ast.TypeParameterList) []string {
	if l == nil {
		return nil
	}
	names := make([]string, len(l.Nodes))
	for i, p := range l.Nodes {
		names[i] = p.Name().Text()
	}
	return names
}

// typeParams are the type parameters as written ("T extends Base = Default").
func (s tsSyntax) typeParams(l *ast.TypeParameterList) []string {
	if l == nil {
		return nil
	}
	out := make([]string, len(l.Nodes))
	for i, p := range l.Nodes {
		out[i] = s.src(p)
	}
	return out
}

// signature is the signature of a function-like node as one line: "name<T>(a: A, b?: B): R".
func (s tsSyntax) signature(n *ast.Node,
	name string) string {
	return name + s.signatureText(n.FunctionLikeData())
}

// signatureText is the type parameters, the parameters and the return type as written.
func (s tsSyntax) signatureText(data *ast.FunctionLikeBase) string {
	params := make([]string, 0)
	for _, p := range paramList(data.Parameters) {
		params = append(params,
			s.src(p))
	}
	ret := ""
	if data.Type != nil {
		ret = ": " + s.src(data.Type)
	}
	generics := ""
	if tp := s.typeParams(data.TypeParameters); len(tp) > 0 {
		generics = "<" + strings.Join(tp,
			", ") + ">"
	}
	return generics + "(" + strings.Join(params,
		", ") + ")" + ret
}

// src is the source text of a node, on one line.
func (s tsSyntax) src(n *ast.Node) string {
	if n == nil {
		return ""
	}
	start := scanner.SkipTrivia(s.text,
		n.Pos())
	return strings.Join(strings.Fields(s.text[start:n.End()]),
		" ")
}
