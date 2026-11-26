//go:build tsjs_css

// Needs the build tag (and so the generated esparser/ and tsparser/): see typescript.go.

package memory_code

import (
	"os"
	"slices"
	"testing"
)

func TestParseTypeScriptTypes(t *testing.T) {
	text, err := os.ReadFile("testdata/typescript/sample.ts")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseAuto("src/sample.ts", string(text))
	if err != nil {
		t.Fatal(err)
	}
	have := edgeSet(r.Graph)
	for _, w := range [][3]string{
		{"Foo", "implements", "Shape"},
		{"module", "defines", "Shape"},
		{"Foo.area", "calls", "Foo.helper"}, // esbuild's side is kept
		{"Foo.make", "uses_type", "Foo"},
	} {
		if !have[w] {
			t.Errorf("missing edge %v", w)
		}
	}
	if r.Language != LangTypeScript {
		t.Errorf("language %q", r.Language)
	}
	var shape *TypeDecl
	for i := range r.Types {
		if r.Types[i].Name == "Shape" {
			shape = &r.Types[i]
		}
	}
	if shape == nil || shape.Kind != TypeDeclInterface || len(shape.Members) != 1 ||
		shape.Members[0].Name != "area" || shape.Members[0].Type != "(): number" {
		t.Errorf("Shape: %+v", shape)
	}
	wantAnn := []Annotation{
		{Owner: "Foo.area", Role: RoleReturn, Type: "number"},
		{Owner: "Foo.constructor", Role: RoleParam, Name: "name", Type: "string"},
		{Owner: "Foo", Role: RoleProperty, Name: "name", Type: "string"},
		{Owner: "top", Role: RoleReturn, Type: "number"},
	}
	for _, w := range wantAnn {
		if !slices.Contains(r.Annotations, w) {
			t.Errorf("missing annotation %+v", w)
		}
	}
	if t.Failed() {
		t.Logf("types: %+v\nannotations: %+v\nedges: %+v", r.Types, r.Annotations, r.Graph.Edges)
	}
}

func TestParseTypeScriptDeclarations(t *testing.T) {
	r, err := Parse(LangTypeScript, `
import { Dep } from "./dep";
import * as ns from "lib";
export interface Box<T extends Dep = Dep> extends ns.Base { readonly id: number; label?: string; get(key: T): Dep | undefined; [k: string]: unknown }
export type Pair<A> = [A, Dep];
export const enum Level { Low = 1, High }
function f<U>(x: U, y?: Box<U>): ns.Out { return null as any }
const v: Dep = make();
`)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]TypeDecl{}
	for _, d := range r.Types {
		byName[d.Name] = d
	}
	box := byName["Box"]
	if box.Kind != TypeDeclInterface || !box.Exported || !slices.Equal(box.Extends, []string{"lib.Base"}) ||
		!slices.Equal(box.TypeParams, []string{"T extends Dep = Dep"}) || len(box.Members) != 4 {
		t.Errorf("Box: %+v", box)
	}
	if m := box.Members[1]; m.Name != "label" || !m.Optional || m.Type != "string" {
		t.Errorf("label: %+v", m)
	}
	if m := box.Members[0]; !m.Readonly {
		t.Errorf("id: %+v", m)
	}
	if p := byName["Pair"]; p.Kind != TypeDeclAlias || p.Type != "[A, Dep]" {
		t.Errorf("Pair: %+v", p)
	}
	if l := byName["Level"]; l.Kind != TypeDeclConstEnum || len(l.Members) != 2 || l.Members[0].Type != "1" {
		t.Errorf("Level: %+v", l)
	}
	have := edgeSet(r.Graph)
	for _, w := range [][3]string{
		{"Box", "inherits", "lib.Base"},
		{"Box", "uses_type", "./dep.Dep"},
		{"Pair", "uses_type", "./dep.Dep"},
		{"f", "uses_type", "lib.Out"},
		{"module", "uses_type", "./dep.Dep"},
	} {
		if !have[w] {
			t.Errorf("missing edge %v", w)
		}
	}
	if have[[3]string{"f", "uses_type", "U"}] || have[[3]string{"Box", "uses_type", "T"}] {
		t.Error("a type parameter is a used type")
	}
}

func TestParseCSS(t *testing.T) {
	text, err := os.ReadFile("testdata/css/sample.css")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseAuto("a/sample.css", string(text))
	if err != nil {
		t.Fatal(err)
	}
	have := edgeSet(r.Graph)
	for _, w := range [][3]string{
		{"module", "imports", "./base.css"},
		{"module", "defines", "spin"},
		{"module", "defines", ".card"},
		{"module", "defines", ".card:hover > a"},
		{".card", "uses", "--accent"},
		{".card", "uses", "--gap"},
		{".card", "uses", "spin"},
		{":root", "defines", "--accent"},
		{".card", "defines", "&.active"},
		{"&.active", "uses", "--accent"},
	} {
		if !have[w] {
			t.Errorf("missing edge %v", w)
		}
	}
	if len(r.Styles) < 4 {
		t.Fatalf("styles: %+v", r.Styles)
	}
	card := r.Styles[1]
	if !slices.Equal(card.Selectors, []string{".card", ".card:hover > a"}) || card.Declarations[1].Value != "var(--gap) 0" ||
		!card.Declarations[1].Important {
		t.Errorf("card: %+v", card)
	}
	last := r.Styles[len(r.Styles)-1]
	if !slices.Equal(last.Context, []string{"@media (min-width: 600px)"}) || last.Declarations[0].Property != "padding" {
		t.Errorf("last: %+v", last)
	}
	if t.Failed() {
		t.Logf("styles: %+v\nedges: %+v", r.Styles, r.Graph.Edges)
	}
}

func TestParseJavaScriptAndPython(t *testing.T) {
	r, err := Parse(LangJavaScript, "function a() { b() }\nfunction b() {}\n")
	if err != nil || r.Language != LangJavaScript || len(r.Types) != 0 || !edgeSet(r.Graph)[[3]string{"a", "calls", "b"}] {
		t.Errorf("%+v %v", r, err)
	}
	r, err = Parse(LangPython, "def a():\n    b()\n")
	if err != nil || r.Language != LangPython {
		t.Errorf("%+v %v", r, err)
	}
}

func TestParseTypeScriptCodeGraph(t *testing.T) {
	r, err := Parse(LangTypeScript, `
import fs = require("fs");
const lib = require("./lib");
export * from "./all";
export { x } from "./x";
export default function () { return lib.run(); }
class Base<T> { run(): T { return this.go(); } go(): T { return null as any } }
class Child extends Base<number> { run() { return super.run() + helper()!; } }
const make = () => new Child();
function helper() { return (fs as any).readFileSync("a") }
`)
	if err != nil {
		t.Fatal(err)
	}
	have := edgeSet(r.Graph)
	for _, w := range [][3]string{
		{"module", "imports", "fs"},
		{"module", "imports", "./lib"},
		{"module", "imports", "./all"},
		{"module", "imports", "./x"},
		{"module", "defines", "default"},
		{"default", "calls", "lib.run"},
		{"Child", "inherits", "Base"},
		{"Base.run", "calls", "Base.go"},
		{"Child.run", "calls", "helper"},
		{"make", "calls", "Child"},
		{"helper", "calls", "fs.readFileSync"},
	} {
		if !have[w] {
			t.Errorf("missing edge %v", w)
		}
	}
	if t.Failed() {
		t.Logf("edges: %+v", edgeSet(r.Graph))
	}
}

func TestParseTSXFallbackAndSyntaxError(t *testing.T) {
	r, err := Parse(LangTypeScript, "export function App(): JSX.Element { return <div>{f()}</div> }\nfunction f() {}\n")
	if err != nil || !edgeSet(r.Graph)[[3]string{"App", "calls", "f"}] {
		t.Errorf("tsx: %v %+v", err, r.Graph.Edges)
	}
	if _, err := Parse(LangTypeScript, "function f( {"); err == nil {
		t.Error("expected a syntax error")
	}
}
