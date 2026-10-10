package ontology

import (
	"os"
	"sort"
	"strings"
	"testing"
)

const header = `<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:rdfs="http://www.w3.org/2000/01/rdf-schema#"
  xmlns:owl="http://www.w3.org/2002/07/owl#" xmlns:ex="http://ex.org/o#" xml:base="http://ex.org/o">`

func parse(t *testing.T, body string) *Ontology {
	t.Helper()
	o, err := Parse(strings.NewReader(header + body + `</rdf:RDF>`))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestMarketOntology(t *testing.T) {
	f, err := os.Open("../../ontology/market.owl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	o, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if o.IRI != "https://gligabao.example/ontology/market" || o.Title != "Market ontology" || o.Comment == "" {
		t.Errorf("ontology header: %q %q %q", o.IRI, o.Title, o.Comment)
	}
	if len(o.Classes) != 20 {
		t.Errorf("%d classes, want 20", len(o.Classes))
	}
	byName := map[string]Class{}
	for _, c := range o.Classes {
		byName[c.Name] = c
		if c.Label == "" || c.Comment == "" {
			t.Errorf("class %s lacks a label or comment", c.Name)
		}
	}
	has := func(src, rel, tgt string) Edge {
		t.Helper()
		for _, e := range o.Edges {
			if LocalName(e.SourceIRI) == src && e.Relation == rel && LocalName(e.TargetIRI) == tgt {
				return e
			}
		}
		t.Fatalf("missing edge %s -%s-> %s", src, rel, tgt)
		return Edge{}
	}
	has("Exchange", "subclass_of", "Market")
	has("Buyer", "subclass_of", "MarketParticipant")
	has("Trade", "hasBuyer", "Buyer")
	// Union domains and ranges expand to one edge per member.
	for _, d := range []string{"Asset", "Order", "Trade"} {
		has(d, "hasPrice", "Price")
	}
	has("Regulator", "regulates", "Market")
	has("Regulator", "regulates", "MarketParticipant")
	if e := has("Asset", "tradedOn", "Market"); e.InverseOf != "listsAsset" || e.Label == "" || e.Kind != KindProperty {
		t.Errorf("tradedOn edge: %+v", e)
	}
	if e := has("Market", "listsAsset", "Asset"); e.InverseOf != "tradedOn" {
		t.Errorf("listsAsset edge: %+v", e)
	}
	if o.Properties() != 26 {
		t.Errorf("%d properties, want 26", o.Properties())
	}
	// Every edge endpoint is a class.
	for _, e := range o.Edges {
		if _, ok := byName[LocalName(e.SourceIRI)]; !ok {
			t.Errorf("edge from unknown class %s", e.SourceIRI)
		}
		if _, ok := byName[LocalName(e.TargetIRI)]; !ok {
			t.Errorf("edge to unknown class %s", e.TargetIRI)
		}
	}
}

func TestRDFXMLForms(t *testing.T) {
	o := parse(t, `
	  <owl:Ontology rdf:about=""><rdfs:label>T</rdfs:label></owl:Ontology>
	  <owl:Class rdf:ID="A"/>
	  <rdf:Description rdf:about="#B"><rdf:type rdf:resource="http://www.w3.org/2002/07/owl#Class"/>
	    <rdfs:subClassOf rdf:resource="#A"/>
	    <rdfs:subClassOf><owl:Restriction/></rdfs:subClassOf>
	  </rdf:Description>
	  <rdf:Description rdf:about="#C" rdf:type="http://www.w3.org/2002/07/owl#Class" rdfs:label="Cee"/>
	  <owl:Class rdf:about="#D"><owl:equivalentClass rdf:resource="#C"/></owl:Class>
	  <owl:Class rdf:about="#C"><owl:equivalentClass rdf:resource="#D"/><owl:disjointWith rdf:resource="#A"/></owl:Class>
	  <owl:SymmetricProperty rdf:about="#near">
	    <rdfs:domain rdf:resource="#A"/>
	    <rdfs:range rdf:parseType="Resource"><owl:unionOf rdf:parseType="Collection"><owl:Class rdf:about="#B"/><owl:Class rdf:nodeID="x"/></owl:unionOf></rdfs:range>
	  </owl:SymmetricProperty>
	  <owl:DatatypeProperty rdf:about="#size"><rdfs:domain rdf:resource="#A"/><rdfs:range rdf:resource="http://www.w3.org/2001/XMLSchema#int"/></owl:DatatypeProperty>
	  <owl:ObjectProperty rdf:about="#orphan"><rdfs:domain rdf:resource="#A"/></owl:ObjectProperty>`)
	if o.IRI != "http://ex.org/o" || o.Title != "T" {
		t.Errorf("ontology: %q %q", o.IRI, o.Title)
	}
	names := []string{}
	for _, c := range o.Classes {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "A,B,C,D" {
		t.Errorf("classes %s, want A,B,C,D", got)
	}
	if o.Classes[2].Label != "Cee" {
		t.Errorf("property attribute label: %q", o.Classes[2].Label)
	}
	var got []string
	for _, e := range o.Edges {
		got = append(got, LocalName(e.SourceIRI)+" "+e.Relation+" "+LocalName(e.TargetIRI))
	}
	// The blank-node member of the union, the restriction, the datatype property and the property without a range
	// produce nothing; the symmetric equivalence (stated both ways) is stored once.
	sort.Strings(got)
	want := "A near B|B subclass_of A|C disjoint_with A|C equivalent_class D"
	if g := strings.Join(got, "|"); g != want {
		t.Errorf("edges %q, want %q", g, want)
	}
}

func TestParseErrors(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":          ``,
		"not xml":        `{"a": 1}`,
		"truncated":      header + `<owl:Class rdf:about="#A">`,
		"mismatched tag": header + `<owl:Class rdf:about="#A"></owl:Thing></rdf:RDF>`,
		"no classes":     header + `<owl:Ontology rdf:about=""/></rdf:RDF>`,
	} {
		if _, err := Parse(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestDeclaredClasses(t *testing.T) {
	o := parse(t, `
	  <owl:Class rdf:about="#A"/>
	  <rdf:Description rdf:about="#B"><rdfs:subClassOf rdf:resource="#A"/></rdf:Description>
	  <owl:ObjectProperty rdf:about="#p"><rdfs:domain rdf:resource="#C"/><rdfs:range rdf:resource="#A"/></owl:ObjectProperty>`)
	got := map[string]bool{}
	for _, c := range o.Classes {
		got[c.Name] = c.Declared
	}
	// Only A is typed as a class; B and C are merely mentioned by relationships.
	if len(got) != 3 || !got["A"] || got["B"] || got["C"] {
		t.Errorf("declared flags: %v", got)
	}
}
