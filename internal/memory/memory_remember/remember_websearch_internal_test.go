package memory_remember

import "testing"

func TestPickKeyEntities(t *testing.T) {
	mk := func(name, typ string, edges int) *keyEntity {
		return &keyEntity{Entity: Entity{Name: name, Type: typ}, ArtifactID: 1, Edges: edges}
	}
	all := []*keyEntity{mk("A", "person", 1), mk("Idea", "concept", 9), mk("B", "organization", 3), mk("C", "place", 1), mk("D", "event", 0), mk("E", "other", 3)}
	var got []string
	for _, e := range pickKeyEntities(all, 3) {
		got = append(got, e.Name)
	}
	// Most facts first, extraction order on a tie, no concepts, at most 3.
	want := []string{"B", "E", "A"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("picked %v, want %v", got, want)
	}
	if len(pickKeyEntities(nil, 3)) != 0 {
		t.Error("nothing to pick")
	}
}

func TestNoteKeyEntities(t *testing.T) {
	j := &job{}
	ex := &Extraction{Entities: []Entity{{Name: " Acme ", Type: "Organization"}, {Name: "Alice", Type: "person"}},
		Edges: []Edge{{Source: "Alice", Target: "Acme"}, {Source: "acme", Target: "acme"}}}
	j.noteKeyEntities(ex, 1) // the call does not search the web: nothing is noted
	if len(j.keyOrder) != 0 {
		t.Fatalf("noted without web search: %v", j.keyOrder)
	}
	j.co.WebSearch = true
	j.noteKeyEntities(ex, 1)
	j.noteKeyEntities(&Extraction{Entities: []Entity{{Name: "ACME", Type: "organization", Description: "a firm"}}, Edges: []Edge{{Source: "Acme", Target: "Bob"}}}, 2)
	acme := j.keyEntities["acme"]
	// first artifact wins; facts add up over the texts (Acme: Alice-Acme, Acme-Acme (once), Acme-Bob); a description fills in
	if len(j.keyOrder) != 2 || acme == nil || acme.ArtifactID != 1 || acme.Edges != 3 || acme.Type != "organization" || acme.Description != "a firm" {
		t.Errorf("acme = %+v, order %v", acme, j.keyOrder)
	}
	j.web = &webPart{} // the entities of a web search result are never noted
	j.noteKeyEntities(&Extraction{Entities: []Entity{{Name: "Initech"}}}, 3)
	if len(j.keyOrder) != 2 {
		t.Errorf("noted from a web search result: %v", j.keyOrder)
	}
}
