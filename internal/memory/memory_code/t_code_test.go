package memory_code

import "testing"

const sample = `import os
from a.b import c as d, e

class Base: pass

class Foo(Base):
    """A foo."""
    def run(self):
        self.helper()
        os.getcwd()

    def helper(self):
        return top()

def top():
    pass
`

func TestExtractPython(t *testing.T) {
	g, err := ExtractAuto(sample)
	if err != nil {
		t.Fatal(err)
	}
	has := func(s, r, o string) bool {
		for _, e := range g.Edges {
			if e.Source == s && e.Relation == r && e.Target == o {
				return true
			}
		}
		return false
	}
	for _, w := range [][3]string{
		{"module", "imports", "os"},
		{"module", "imports", "a.b"},
		{"module", "imports", "a.b.c"},
		{"module", "defines", "Foo"},
		{"Foo", "inherits", "Base"},
		{"Foo", "defines", "Foo.run"},
		{"Foo.run", "calls", "Foo.helper"},
		{"Foo.run", "calls", "os.getcwd"},
		{"Foo.helper", "calls", "top"},
	} {
		if !has(w[0], w[1], w[2]) {
			t.Errorf("missing edge %v; got %+v", w, g.Edges)
		}
	}
	for _, e := range g.Entities {
		if e.Name == "Foo" && e.Description != "A foo." {
			t.Errorf("docstring: %q", e.Description)
		}
	}
}

func TestUnsupportedLanguage(t *testing.T) {
	if _, err := ExtractAuto("package main\n"); err == nil {
		t.Fatal("expected error for go")
	}
}
