//go:build esbuild

// Needs the build tag (and so the generated esparser/): see typescript.go.

package memory_code

import (
	"errors"
	"os"
	"testing"
)

func edgeSet(g Graph) map[[3]string]bool {
	out := map[[3]string]bool{}
	for _, e := range g.Edges {
		out[[3]string{e.Source, e.Relation, e.Target}] = true
	}
	return out
}

func TestExtractTypeScript(t *testing.T) {
	text, err := os.ReadFile("testdata/typescript/sample.ts")
	if err != nil {
		t.Fatal(err)
	}
	g, err := ExtractAuto("src/sample.ts", string(text))
	if err != nil {
		t.Fatal(err)
	}
	have := edgeSet(g)
	for _, w := range [][3]string{
		{"module", "imports", "fs"},
		{"module", "imports", "node:path"},
		{"module", "imports", "./lib/util"},
		{"module", "imports", "./lib/util.Helper"},
		{"module", "imports", "./lib/util.a"},
		{"module", "defines", "Foo"},
		{"Foo", "inherits", "Base"},
		{"Foo", "defines", "Foo.area"},
		{"Foo", "defines", "Foo.handler"},
		{"Foo", "defines", "Foo.make"},
		{"Foo.handler", "calls", "Base.log"},
		{"Foo.area", "calls", "Foo.helper"},
		{"Foo.area", "calls", "./lib/util.a"},
		{"Foo.make", "calls", "Foo"},
		{"Foo.helper", "calls", "node:path.join"},
		{"Foo.helper", "calls", "top"},
		{"module", "defines", "Color"},
		{"module", "defines", "Geo"},
		{"Geo", "defines", "Geo.dist"},
		{"Geo.dist", "calls", "fs.default.readFileSync"},
		{"module", "defines", "top"},
		{"top", "defines", "top.inner"},
		{"top.inner", "calls", "./lib/util.Helper"},
		{"top", "calls", "top.inner"},
		{"module", "defines", "main"},
		{"load", "imports", "./dyn"},
	} {
		if !have[w] {
			t.Errorf("missing edge %v", w)
		}
	}
	if t.Failed() {
		t.Logf("edges: %+v", g.Edges)
	}
}

func TestExtractJavaScriptAndTSX(t *testing.T) {
	g, err := ExtractAuto("a.jsx", "export function App() { return <div>{f()}</div> }\nfunction f() {}\n")
	if err != nil {
		t.Fatal(err)
	}
	if have := edgeSet(g); !have[[3]string{"App", "calls", "f"}] {
		t.Errorf("edges: %+v", g.Edges)
	}
}

func TestDetectByFilename(t *testing.T) {
	for file, want := range map[string]string{"a.PY": LangPython, "x/y.tsx": LangTypeScript, "m.mjs": LangJavaScript} {
		got, err := Detect(file, "")
		if err != nil || got != want {
			t.Errorf("%s: %q %v", file, got, err)
		}
	}
	if _, err := Detect("main.go", ""); !errors.Is(err, ErrUnsupportedLanguage) {
		t.Errorf("go file: %v", err)
	}
}

func TestTypeScriptSyntaxError(t *testing.T) {
	if _, err := ExtractAuto("a.ts", "class {"); err == nil {
		t.Fatal("expected a syntax error")
	}
}
