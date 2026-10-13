package memory

import (
	"testing"
	"time"
)

func TestFact(t *testing.T) {
	v := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	e := Edge{ID: 3, Source: "Alice", Target: "Acme", Relation: "works_at", ValidAt: &v}
	f := FactFromEdge(e)

	if got := f.Text(); got != "Alice works at Acme." {
		t.Errorf("Text = %q", got)
	}
	f.Statement = "Alice is employed by Acme."
	if f.Text() != f.Statement {
		t.Errorf("Text should prefer the statement")
	}
	if f.Span() != "(2023-01-01T00:00:00Z -> ?)" || f.Ended() || f.Superseded() {
		t.Errorf("Span/Ended/Superseded: %s", f.Span())
	}
	if !f.SameAs(Fact{Source: " alice ", Target: "ACME", Relation: "works_at"}) {
		t.Errorf("SameAs should ignore case and spacing of names")
	}
	if f.SameAs(Fact{Source: "Alice", Target: "Acme", Relation: "founded"}) {
		t.Errorf("SameAs should compare the relation")
	}
	if back := f.Edge(); back.ID != 3 || back.Description != f.Statement || back.Source != "Alice" {
		t.Errorf("Edge round trip: %+v", back)
	}
	if got := f.ContextLine(); got != "[edge 3] Alice(0) --works_at--> Acme(0): Alice is employed by Acme. [valid_at=2023-01-01T00:00:00Z]" {
		t.Errorf("ContextLine = %q", got)
	}
}
