package memory_code

import (
	"testing"
)

const goSrc = `package demo

import (
	"fmt"
	"os"
)

// Base is a base.
type Base struct{}

type Svc struct {
	Base
	n int
}

type Doer interface{ Do() }

type ID int

const Max = 3

func (s *Svc) Run() {
	s.step()
	fmt.Println("x")
}

func (s *Svc) step() { os.Exit(0) }

func main() { helper() }

func helper() {}
`

func TestGoExtract(t *testing.T) {
	g, err := ExtractAuto("a.go", goSrc)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	for _, e := range g.Entities {
		types[e.Name] = e.Type
	}
	for name, want := range map[string]string{
		"Base": TypeClass, "Svc": TypeClass, "Doer": TypeInterface, "ID": TypeAlias, "Max": TypeVariable,
		"Svc.Run": TypeMethod, "Svc.step": TypeMethod, "main": TypeFunction, "os": "", "fmt": TypeModule,
	} {
		if name == "os" {
			continue
		}
		if types[name] != want {
			t.Errorf("%s: %q, want %q", name, types[name], want)
		}
	}
	edges := map[[3]string]bool{}
	for _, e := range g.Edges {
		edges[[3]string{e.Source, e.Relation, e.Target}] = true
	}
	for _, w := range [][3]string{
		{"module", RelDefines, "Svc"},
		{"module", RelImports, "fmt"},
		{"Svc", RelInherits, "Base"},
		{"Svc.Run", RelCalls, "Svc.step"},
		{"Svc.Run", RelCalls, "fmt.Println"},
		{"main", RelCalls, "helper"},
	} {
		if !edges[w] {
			t.Errorf("missing edge %v", w)
		}
	}
}

func TestGoDetectWithoutName(t *testing.T) {
	if lang, _ := Detect("", goSrc); lang != LangGo {
		t.Errorf("got %q", lang)
	}
}
