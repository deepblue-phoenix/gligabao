//go:build tsjs_css

// CSS: parsed with the CSS parser of esbuild (vendored in esparser by gen_esbuild; see typescript.go for the build tag).

package memory_code

import (
	"fmt"
	"slices"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/config"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/css_ast"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/css_lexer"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/css_parser"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/esparser/logger"
)

// cssFile is what the walk of a parsed stylesheet needs: the text (rule preludes are slices of it), the symbols (names of keyframes) and
// the import records (the paths of "@import").
type cssFile struct {
	text      string
	tree      css_ast.AST
	keyframes []string
}

// cssOut is what a walk of the rules yields.
type cssOut struct {
	rules []StyleRule
	graph Graph
}

// parseCSS is the Result of a stylesheet. The graph: selectors, custom properties (variables) and keyframes are entities that the
// stylesheet (or the enclosing selector) "defines"; a selector "uses" the custom properties it reads with var() and the keyframes it
// names in "animation"; "@import" is an "imports" edge of the module. esbuild recovers from most CSS syntax errors (they are warnings), so only the errors it reports fail it.
func parseCSS(text string) (res Result, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			res, err = Result{}, fmt.Errorf("memory_code: parse css: %v", rec)
		}
	}()
	opts := &config.Options{}
	log := logger.NewDeferLog(logger.DeferLogNoVerboseOrDebug, nil)
	tree := css_parser.Parse(log,
		logger.Source{
			Contents: text,
			KeyPath:  logger.Path{Text: "input.css"},
		},
		css_parser.OptionsFromConfig(config.LoaderCSS,
			opts))
	if log.HasErrors() {
		return Result{}, fmt.Errorf("memory_code: parse css: %s", firstError(log.Done()))
	}
	f := cssFile{
		text: text,
		tree: tree,
	}
	f.keyframes = f.keyframeNames(tree.Rules)
	root := scope{
		name: RootName,
		kind: TypeModule,
	}
	out := f.rules(tree.Rules,
		nil,
		root)
	return Result{
		Language: LangCSS,
		Graph: dedupe(concat(Graph{
			Entities: []graph.Entity{{Name: RootName, Type: TypeModule}},
		}, out.graph)),
		Styles: out.rules,
	}, nil
}

// keyframeNames are the names of all "@keyframes" of the rules, nested ones too.
func (f cssFile) keyframeNames(rules []css_ast.Rule) []string {
	names := []string{}
	for _, r := range rules {
		if k, ok := r.Data.(*css_ast.RAtKeyframes); ok {
			names = append(names,
				f.tree.Symbols[k.Name.Ref.InnerIndex].OriginalName)
		}
		if inner, ok := nestedRules(r.Data); ok {
			names = append(names,
				f.keyframeNames(inner)...)
		}
	}
	return names
}

// nestedRules are the rules inside a rule that has a block of rules.
func nestedRules(r css_ast.R) ([]css_ast.Rule, bool) {
	switch v := r.(type) {
	case *css_ast.RSelector:
		return v.Rules, true
	case *css_ast.RQualified:
		return v.Rules, true
	case *css_ast.RKnownAt:
		return v.Rules, true
	case *css_ast.RAtMedia:
		return v.Rules, true
	case *css_ast.RAtLayer:
		return v.Rules, true
	case *css_ast.RAtScope:
		return v.Rules, true
	}
	return nil, false
}

// rules is the output of a list of rules; ctx are the enclosing preludes, and sc the entity that defines what the rules declare.
func (f cssFile) rules(rules []css_ast.Rule,
	ctx []string,
	sc scope) cssOut {
	out := cssOut{}
	for _, r := range rules {
		next := f.rule(r,
			ctx,
			sc)
		out = cssOut{
			rules: append(out.rules, next.rules...),
			graph: merge(out.graph, next.graph),
		}
	}
	return out
}

// rule is the output of one rule: a selector rule is a style rule (and a scope for what it nests), the other rules with a block are
// context of the rules inside.
func (f cssFile) rule(r css_ast.Rule,
	ctx []string,
	sc scope) cssOut {
	switch v := r.Data.(type) {
	case *css_ast.RAtImport:
		return cssOut{
			graph: Graph{
				Entities: []graph.Entity{{Name: f.tree.ImportRecords[v.ImportRecordIndex].Path.Text, Type: TypeModule}},
				Edges: []graph.Edge{{
					Source:   sc.name,
					Target:   f.tree.ImportRecords[v.ImportRecordIndex].Path.Text,
					Relation: RelImports,
				}},
			},
		}
	case *css_ast.RAtKeyframes:
		return cssOut{
			graph: definedBy(sc,
				f.tree.Symbols[v.Name.Ref.InnerIndex].OriginalName,
				TypeKeyframes,
				""),
		}
	case *css_ast.RSelector:
		return f.styleRule(r,
			v.Rules,
			ctx,
			sc)
	case *css_ast.RQualified:
		return f.styleRule(r,
			v.Rules,
			ctx,
			sc)
	}
	if inner, ok := nestedRules(r.Data); ok {
		return f.rules(inner,
			append(slices.Clone(ctx), f.prelude(r)),
			sc)
	}
	return cssOut{}
}

// styleRule is a selector rule: the style rule of its declarations, the entities of its selectors, and the rules nested in it.
func (f cssFile) styleRule(r css_ast.Rule,
	inner []css_ast.Rule,
	ctx []string,
	sc scope) cssOut {
	selectors := splitTop(f.prelude(r))
	decls := []StyleDecl{}
	nested := []css_ast.Rule{}
	for _, in := range inner {
		if d, ok := in.Data.(*css_ast.RDeclaration); ok {
			decls = append(decls, StyleDecl{
				Property:  d.KeyText,
				Value:     tokensText(d.Value),
				Important: d.Important,
			})
		} else {
			nested = append(nested, in)
		}
	}
	graphOfSelectors := Graph{}
	for _, sel := range selectors {
		own := scope{
			name: sel,
			kind: TypeSelector,
		}
		graphOfSelectors = concat(graphOfSelectors,
			definedBy(sc,
				sel,
				TypeSelector,
				""),
			f.declGraph(inner,
				own))
	}
	rule := []StyleRule{}
	if len(decls) > 0 {
		rule = append(rule, StyleRule{
			Selectors:    selectors,
			Context:      slices.Clone(ctx),
			Declarations: decls,
		})
	}
	// what is nested in a rule is defined by the first of its selectors and has the selector list as context
	nestedOut := f.rules(nested,
		append(slices.Clone(ctx), strings.Join(selectors, ", ")),
		scope{
			name: first(selectors),
			kind: TypeSelector,
		})
	return cssOut{
		rules: append(rule, nestedOut.rules...),
		graph: merge(graphOfSelectors, nestedOut.graph),
	}
}

func first(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

// declGraph is what the declarations of a selector define and use: a custom property it sets, the custom properties it reads, the
// keyframes it names.
func (f cssFile) declGraph(rules []css_ast.Rule,
	sc scope) Graph {
	g := Graph{}
	for _, in := range rules {
		d, ok := in.Data.(*css_ast.RDeclaration)
		if !ok {
			continue
		}
		if strings.HasPrefix(d.KeyText, "--") {
			g = merge(g,
				definedBy(sc,
					d.KeyText,
					TypeVariable,
					""))
		}
		for _, name := range f.usedNames(d) {
			g = merge(g, Graph{
				Entities: []graph.Entity{{Name: name.name, Type: name.typ}},
				Edges: []graph.Edge{{
					Source:   sc.name,
					Target:   name.name,
					Relation: RelUses,
				}},
			})
		}
	}
	return g
}

type usedName struct {
	name string
	typ  string
}

// usedNames are the custom properties a declaration reads (var(--x)) and, for "animation" and "animation-name", the keyframes it names.
func (f cssFile) usedNames(d *css_ast.RDeclaration) []usedName {
	out := []usedName{}
	animation := d.KeyText == "animation" || d.KeyText == "animation-name"
	var visit func(ts []css_ast.Token)
	visit = func(ts []css_ast.Token) {
		for _, t := range ts {
			switch {
			case t.Kind == css_lexer.TFunction && strings.EqualFold(t.Text, "var") && t.Children != nil &&
				len(*t.Children) > 0 && (*t.Children)[0].Kind == css_lexer.TIdent:
				out = append(out, usedName{(*t.Children)[0].Text, TypeVariable})
			case animation && t.Kind == css_lexer.TSymbol && slices.Contains(f.keyframes, f.tree.Symbols[t.PayloadIndex].OriginalName):
				// a name that matches a keyframes is a symbol token, not an identifier
				out = append(out, usedName{f.tree.Symbols[t.PayloadIndex].OriginalName, TypeKeyframes})
			case animation && t.Kind == css_lexer.TIdent && slices.Contains(f.keyframes, t.Text):
				out = append(out, usedName{t.Text, TypeKeyframes})
			}
			if t.Children != nil {
				visit(*t.Children)
			}
		}
	}
	visit(d.Value)
	return out
}

// prelude is the text of a rule before its "{", on one line: the selectors, or the at-rule with its condition.
func (f cssFile) prelude(r css_ast.Rule) string {
	start := int(r.Loc.Start)
	if start < 0 || start > len(f.text) {
		return ""
	}
	end := strings.IndexByte(f.text[start:], '{')
	if end < 0 {
		end = len(f.text) - start
	}
	return strings.Join(strings.Fields(f.text[start:start+end]),
		" ")
}

// splitTop splits a selector list at the commas that are not inside parentheses, brackets or strings.
func splitTop(s string) []string {
	out := []string{}
	depth, from := 0, 0
	var quote rune
	for i, c := range s {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[from:i]))
			from = i + 1
		}
	}
	return append(out, strings.TrimSpace(s[from:]))
}

// tokensText is the text of the value tokens of a declaration, one line.
func tokensText(ts []css_ast.Token) string {
	var b strings.Builder
	for i, t := range ts {
		if i > 0 && (ts[i-1].Whitespace&css_ast.WhitespaceAfter != 0 || t.Whitespace&css_ast.WhitespaceBefore != 0) {
			b.WriteByte(' ')
		}
		b.WriteString(tokenText(t))
	}
	return b.String()
}

func tokenText(t css_ast.Token) string {
	inner := ""
	if t.Children != nil {
		inner = tokensText(*t.Children)
	}
	switch t.Kind {
	case css_lexer.TFunction:
		return t.Text + "(" + inner + ")"
	case css_lexer.TOpenParen:
		return "(" + inner + ")"
	case css_lexer.TOpenBracket:
		return "[" + inner + "]"
	case css_lexer.TOpenBrace:
		return "{" + inner + "}"
	case css_lexer.TString:
		return `"` + t.Text + `"`
	case css_lexer.TURL:
		return "url(" + t.Text + ")"
	case css_lexer.THash:
		return "#" + t.Text
	case css_lexer.TAtKeyword:
		return "@" + t.Text
	}
	return t.Text
}
