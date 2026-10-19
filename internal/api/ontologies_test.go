package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestOntologies(t *testing.T) { dbtest.Run(t, testOntologies) }

func testOntologies(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	mux := http.NewServeMux()
	Register(mux, mgrFor(t, &db.DB{Relational: d, Graph: d}, nil), &db.DB{Relational: d, Graph: d}, nil, nil, nil, nil, nil) // loading an ontology needs no LLM

	doc, err := os.ReadFile("../../bin/ontology/market.owl")
	if err != nil {
		t.Fatal(err)
	}
	post := func(v map[string]any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(v)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/ontologies", strings.NewReader(string(body))))
		return rec
	}
	want := func(rec *httptest.ResponseRecorder, code int) {
		t.Helper()
		if rec.Code != code {
			t.Fatalf("status %d, want %d: %s", rec.Code, code, rec.Body)
		}
	}

	// Load.
	rec := post(map[string]any{"user_id": "u1", "name": "market", "content": string(doc)})
	want(rec, http.StatusCreated)
	var got relational.Ontology
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	if got.ID == 0 || got.UserID != "u1" || got.Name != "market" || got.Format != "rdfxml" || got.SizeBytes != len(doc) ||
		got.SHA256 != hex.EncodeToString(sum[:]) || got.Classes != 20 || got.Properties != 26 || got.Edges < 26 ||
		got.IRI != "https://gligabao.example/ontology/market" || got.Title != "Market ontology" ||
		got.Description == "" || got.CreatedAt.IsZero() || !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Fatalf("metadata: %+v", got)
	}

	// The metadata is tracked in the relational DB.
	list, err := relational.ListOntologies(ctx, d, "u1")
	if err != nil || len(list) != 1 || list[0] != got {
		t.Fatalf("stored metadata %+v (err %v), want %+v", list, err, got)
	}
	if other, _ := relational.ListOntologies(ctx, d, "u2"); len(other) != 0 {
		t.Fatalf("u2 sees %d ontologies", len(other))
	}

	// The ontology itself is in the graph DB.
	classes, err := graph.ListOntologyClasses(ctx, d, got.ID)
	if err != nil || len(classes) != got.Classes {
		t.Fatalf("%d classes (err %v), want %d", len(classes), err, got.Classes)
	}
	edges, err := graph.ListOntologyEdges(ctx, d, got.ID)
	if err != nil || len(edges) != got.Edges {
		t.Fatalf("%d edges (err %v), want %d", len(edges), err, got.Edges)
	}
	found := false
	for _, e := range edges {
		if strings.HasSuffix(e.SourceIRI, "#Trade") && e.Relation == "hasBuyer" && strings.HasSuffix(e.TargetIRI, "#Buyer") && e.Kind == "property" {
			found = true
		}
	}
	if !found {
		t.Errorf("edge Trade -hasBuyer-> Buyer not stored: %+v", edges)
	}

	// Names are unique per user; another user may reuse one; invalid input is rejected and stores nothing.
	want(post(map[string]any{"user_id": "u1", "name": "market", "content": string(doc)}), http.StatusConflict)
	want(post(map[string]any{"user_id": "u2", "name": "market", "content": string(doc), "description": "mine"}), http.StatusCreated)
	want(post(map[string]any{"name": "x", "content": string(doc)}), http.StatusBadRequest)                                                                           // no user
	want(post(map[string]any{"user_id": "u1", "content": string(doc)}), http.StatusBadRequest)                                                                       // no name
	want(post(map[string]any{"user_id": "u1", "name": "bad name!", "content": string(doc)}), http.StatusBadRequest)                                                  // bad name
	want(post(map[string]any{"user_id": "u1", "name": "x"}), http.StatusBadRequest)                                                                                  // no content
	want(post(map[string]any{"user_id": "u1", "name": "x", "content": "{}"}), http.StatusBadRequest)                                                                 // not RDF/XML
	want(post(map[string]any{"user_id": "u1", "name": "x", "format": "turtle", "content": string(doc)}), http.StatusBadRequest)                                      // unsupported
	want(post(map[string]any{"user_id": "u1", "name": "x", "content": `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/>`}), http.StatusBadRequest) // no classes
	if list, _ := relational.ListOntologies(ctx, d, "u1"); len(list) != 1 {
		t.Fatalf("u1 has %d ontologies after failed loads, want 1", len(list))
	}
	if u2, _ := relational.ListOntologies(ctx, d, "u2"); len(u2) != 1 || u2[0].Description != "mine" {
		t.Fatalf("u2 ontologies: %+v", u2)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/ontologies", strings.NewReader("not json")))
	want(rec, http.StatusBadRequest)

	// A failed graph load leaves nothing behind, and the name stays usable.
	if err := graph.LoadOntology(ctx, d, got.ID, nil, []graph.OntologyEdge{{SourceIRI: "a", TargetIRI: "b"}}); err == nil {
		t.Fatal("edge between unknown classes was stored")
	}
	if c, _ := graph.ListOntologyClasses(ctx, d, got.ID); len(c) != got.Classes {
		t.Fatalf("failed load changed the stored classes: %d", len(c))
	}
	if err := relational.DeleteOntology(ctx, d, got.ID); err != nil {
		t.Fatal(err)
	}
	if c, _ := graph.ListOntologyClasses(ctx, d, got.ID); len(c) != 0 {
		t.Fatalf("deleting the metadata left %d classes", len(c))
	}
	want(post(map[string]any{"user_id": "u1", "name": "market", "content": string(doc)}), http.StatusCreated)
}

func TestOntologyUpdate(t *testing.T) { dbtest.Run(t, testOntologyUpdate) }

const smallOntology = `<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:rdfs="http://www.w3.org/2000/01/rdf-schema#"
    xmlns:owl="http://www.w3.org/2002/07/owl#" xml:base="http://ex.org/small">
  <owl:Ontology rdf:about="http://ex.org/small"><rdfs:label>Small</rdfs:label><rdfs:comment>Two classes.</rdfs:comment></owl:Ontology>
  <owl:Class rdf:about="#Cat"><rdfs:subClassOf rdf:resource="#Animal"/></owl:Class>
  <owl:Class rdf:about="#Animal"/>
</rdf:RDF>`

func testOntologyUpdate(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	mux := http.NewServeMux()
	Register(mux, mgrFor(t, &db.DB{Relational: d, Graph: d}, nil), &db.DB{Relational: d, Graph: d}, nil, nil, nil, nil, nil)

	doc, err := os.ReadFile("../../bin/ontology/market.owl")
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, v map[string]any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(v)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(string(body))))
		return rec
	}
	want := func(rec *httptest.ResponseRecorder, code int) {
		t.Helper()
		if rec.Code != code {
			t.Fatalf("status %d, want %d: %s", rec.Code, code, rec.Body)
		}
	}
	stored := func(userID string) []relational.Ontology {
		t.Helper()
		list, err := relational.ListOntologies(ctx, d, userID)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	want(do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "o", "content": string(doc)}), http.StatusCreated)
	want(do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u2", "name": "o", "content": string(doc)}), http.StatusCreated)
	before := stored("u1")[0]

	// Replace: same ontology (id, name, creation time), new content and metadata.
	rec := do("PUT", "/v1/gb/ontologies/o", map[string]any{"user_id": "u1", "content": smallOntology, "replace": true})
	want(rec, http.StatusOK)
	var got relational.Ontology
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(smallOntology))
	if got.ID != before.ID || got.UserID != "u1" || got.Name != "o" || !got.CreatedAt.Equal(before.CreatedAt) || got.UpdatedAt.Before(got.CreatedAt) ||
		got.IRI != "http://ex.org/small" || got.Title != "Small" || got.Description != "Two classes." ||
		got.SHA256 != hex.EncodeToString(sum[:]) || got.SizeBytes != len(smallOntology) || got.Classes != 2 || got.Properties != 0 || got.Edges != 1 {
		t.Fatalf("updated metadata: %+v (before %+v)", got, before)
	}
	if list := stored("u1"); len(list) != 1 || list[0] != got {
		t.Fatalf("stored metadata %+v, want %+v", list, got)
	}
	// The graph holds exactly the new ontology: nothing of the old one is left, and it is not duplicated.
	classes, _ := graph.ListOntologyClasses(ctx, d, got.ID)
	edges, _ := graph.ListOntologyEdges(ctx, d, got.ID)
	if len(classes) != 2 || len(edges) != 1 || edges[0].Relation != "subclass_of" || !strings.HasSuffix(edges[0].SourceIRI, "#Cat") {
		t.Fatalf("graph after update: %+v %+v", classes, edges)
	}
	// Another user's ontology of the same name is untouched.
	if u2 := stored("u2")[0]; u2.SHA256 != before.SHA256 {
		t.Fatalf("u2's ontology changed: %+v", u2)
	}

	// An explicit description is kept, and the original can be put back.
	want(do("PUT", "/v1/gb/ontologies/o", map[string]any{"user_id": "u1", "content": string(doc), "description": "back", "replace": true}), http.StatusOK)
	if o := stored("u1")[0]; o.Description != "back" || o.Classes != 20 || o.SHA256 != before.SHA256 {
		t.Fatalf("after restoring the market ontology: %+v", o)
	}
	if c, _ := graph.ListOntologyClasses(ctx, d, before.ID); len(c) != 20 {
		t.Fatalf("%d classes after restoring, want 20", len(c))
	}

	// Failures leave the stored ontology as it was.
	cur := stored("u1")[0]
	want(do("PUT", "/v1/gb/ontologies/missing", map[string]any{"user_id": "u1", "content": smallOntology}), http.StatusNotFound)
	want(do("PUT", "/v1/gb/ontologies/o", map[string]any{"user_id": "u3", "content": smallOntology}), http.StatusNotFound) // not theirs
	want(do("PUT", "/v1/gb/ontologies/o", map[string]any{"content": smallOntology}), http.StatusBadRequest)                // no user
	want(do("PUT", "/v1/gb/ontologies/bad%20name!", map[string]any{"user_id": "u1", "content": smallOntology}), http.StatusBadRequest)
	want(do("PUT", "/v1/gb/ontologies/o", map[string]any{"user_id": "u1"}), http.StatusBadRequest)
	want(do("PUT", "/v1/gb/ontologies/o", map[string]any{"user_id": "u1", "content": "{}"}), http.StatusBadRequest)
	want(do("PUT", "/v1/gb/ontologies/o", map[string]any{"user_id": "u1", "format": "turtle", "content": smallOntology}), http.StatusBadRequest)
	want(do("PUT", "/v1/gb/ontologies/o", map[string]any{"user_id": "u1", "content": `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/>`}), http.StatusBadRequest)
	if now := stored("u1"); len(now) != 1 || now[0] != cur {
		t.Fatalf("a failed update changed the metadata: %+v, want %+v", now, cur)
	}
	if c, _ := graph.ListOntologyClasses(ctx, d, cur.ID); len(c) != 20 {
		t.Fatalf("a failed update changed the graph: %d classes", len(c))
	}
	// Nothing was created by the unknown-name updates.
	if n := len(stored("u3")); n != 0 {
		t.Fatalf("u3 has %d ontologies", n)
	}
}

// Fragments of the market ontology (the same xml:base, so the IRIs match the stored classes) used to test merging.
const mergeHead = `<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:rdfs="http://www.w3.org/2000/01/rdf-schema#"
    xmlns:owl="http://www.w3.org/2002/07/owl#" xml:base="https://gligabao.example/ontology/market">`

const mergeFragment = mergeHead + `
  <owl:Class rdf:about="#Market"><rdfs:label>Marketplace</rdfs:label></owl:Class>
  <owl:Class rdf:about="#Stock"><rdfs:label>Stock</rdfs:label><rdfs:subClassOf rdf:resource="#Security"/></owl:Class>
  <owl:ObjectProperty rdf:about="#tradedOn">
    <rdfs:label>trades on</rdfs:label>
    <rdfs:domain rdf:resource="#Asset"/><rdfs:range rdf:resource="#Market"/>
  </owl:ObjectProperty>
  <owl:ObjectProperty rdf:about="#issuedBy">
    <rdfs:label>issued by</rdfs:label>
    <rdfs:domain rdf:resource="#Security"/><rdfs:range rdf:resource="#MarketParticipant"/>
  </owl:ObjectProperty>
</rdf:RDF>`

func TestOntologyMerge(t *testing.T) { dbtest.Run(t, testOntologyMerge) }

func testOntologyMerge(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	mux := http.NewServeMux()
	Register(mux, mgrFor(t, &db.DB{Relational: d, Graph: d}, nil), &db.DB{Relational: d, Graph: d}, nil, nil, nil, nil, nil)

	doc, err := os.ReadFile("../../bin/ontology/market.owl")
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, v map[string]any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(v)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(string(body))))
		return rec
	}
	put := func(content string, extra map[string]any) relational.Ontology {
		t.Helper()
		req := map[string]any{"user_id": "u1", "content": content}
		for k, v := range extra {
			req[k] = v
		}
		rec := do("PUT", "/v1/gb/ontologies/m", req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var o relational.Ontology
		if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	if rec := do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "m", "content": string(doc)}); rec.Code != http.StatusCreated {
		t.Fatalf("load: %d %s", rec.Code, rec.Body)
	}
	base := func() relational.Ontology {
		l, err := relational.ListOntologies(ctx, d, "u1")
		if err != nil || len(l) != 1 {
			t.Fatalf("ontologies %v: %v", l, err)
		}
		return l[0]
	}
	orig := base()
	class := func(id int64, name string) graph.OntologyClass {
		t.Helper()
		cs, err := graph.ListOntologyClasses(ctx, d, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("class %s not found", name)
		return graph.OntologyClass{}
	}
	edge := func(id int64, src, rel, tgt string) (graph.OntologyEdge, bool) {
		t.Helper()
		es, err := graph.ListOntologyEdges(ctx, d, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if strings.HasSuffix(e.SourceIRI, "#"+src) && e.Relation == rel && strings.HasSuffix(e.TargetIRI, "#"+tgt) {
				return e, true
			}
		}
		return graph.OntologyEdge{}, false
	}
	marketBefore, assetBefore := class(orig.ID, "Market"), class(orig.ID, "Asset")
	if marketBefore.Comment == "" || assetBefore.Label == "" || assetBefore.Comment == "" {
		t.Fatalf("test needs labelled classes: %+v %+v", marketBefore, assetBefore)
	}

	// Merge (no "replace"): adds what is new, overwrites what exists, deletes nothing.
	got := put(mergeFragment, nil)
	sum := sha256.Sum256([]byte(mergeFragment))
	if got.ID != orig.ID || !got.CreatedAt.Equal(orig.CreatedAt) {
		t.Errorf("identity changed: %+v", got)
	}
	// Counts are those of the merged ontology (20 + Stock classes; 38 + 2 edges; 26 + issuedBy properties).
	if got.Classes != 21 || got.Edges != 40 || got.Properties != 27 {
		t.Errorf("counts %d/%d/%d, want 21/40/27", got.Classes, got.Edges, got.Properties)
	}
	// The document has no ontology header, so the stored description, IRI and title stay; hash and size are the document's.
	if got.Description != orig.Description || got.IRI != orig.IRI || got.Title != orig.Title ||
		got.SHA256 != hex.EncodeToString(sum[:]) || got.SizeBytes != len(mergeFragment) {
		t.Errorf("metadata after merge: %+v (before %+v)", got, orig)
	}
	if base() != got {
		t.Errorf("stored metadata %+v, want %+v", base(), got)
	}
	if c, _ := graph.ListOntologyClasses(ctx, d, orig.ID); len(c) != 21 {
		t.Errorf("%d classes stored, want 21", len(c))
	}
	if e, _ := graph.ListOntologyEdges(ctx, d, orig.ID); len(e) != 40 {
		t.Errorf("%d edges stored, want 40", len(e))
	}
	// A declared class is overwritten as a whole: the new label, and no comment (the old one is not kept).
	if m := class(orig.ID, "Market"); m.Label != "Marketplace" || m.Comment != "" {
		t.Errorf("declared class Market: %+v", m)
	}
	// A class that is only mentioned by an edge is not touched.
	if a := class(orig.ID, "Asset"); a != assetBefore {
		t.Errorf("mentioned class Asset changed: %+v, was %+v", a, assetBefore)
	}
	// New class and edges are added.
	if s := class(orig.ID, "Stock"); s.Label != "Stock" {
		t.Errorf("new class Stock: %+v", s)
	}
	if _, ok := edge(orig.ID, "Stock", "subclass_of", "Security"); !ok {
		t.Error("edge Stock -subclass_of-> Security not added")
	}
	if e, ok := edge(orig.ID, "Security", "issuedBy", "MarketParticipant"); !ok || e.Label != "issued by" {
		t.Errorf("edge issuedBy: %+v %v", e, ok)
	}
	// An existing edge is overwritten, its old label, comment and inverse included.
	if e, ok := edge(orig.ID, "Asset", "tradedOn", "Market"); !ok || e.Label != "trades on" || e.Comment != "" || e.InverseOf != "" {
		t.Errorf("overwritten edge tradedOn: %+v %v", e, ok)
	}
	// Edges and classes the document does not mention are kept as they were.
	if e, ok := edge(orig.ID, "Market", "listsAsset", "Asset"); !ok || e.InverseOf != "tradedOn" || e.Label == "" {
		t.Errorf("untouched edge listsAsset: %+v %v", e, ok)
	}
	if _, ok := edge(orig.ID, "Exchange", "subclass_of", "Market"); !ok {
		t.Error("untouched edge Exchange -subclass_of-> Market is gone")
	}
	if c := class(orig.ID, "Risk"); c.Comment == "" {
		t.Errorf("untouched class Risk changed: %+v", c)
	}

	// Merging the same document again changes nothing but the timestamp.
	classes1, _ := graph.ListOntologyClasses(ctx, d, orig.ID)
	edges1, _ := graph.ListOntologyEdges(ctx, d, orig.ID)
	again := put(mergeFragment, map[string]any{"replace": false})
	classes2, _ := graph.ListOntologyClasses(ctx, d, orig.ID)
	edges2, _ := graph.ListOntologyEdges(ctx, d, orig.ID)
	if again.Classes != 21 || again.Edges != 40 || again.Properties != 27 || len(classes2) != len(classes1) || len(edges2) != len(edges1) {
		t.Errorf("second merge: %+v, %d classes, %d edges", again, len(classes2), len(edges2))
	}

	// A document that has an ontology header and a description in the request: those overwrite the stored ones.
	withHeader := mergeHead + `<owl:Ontology rdf:about="http://ex.org/extension"><rdfs:label>Extension</rdfs:label>
	  <rdfs:comment>Adds nothing.</rdfs:comment></owl:Ontology><owl:Class rdf:about="#Bond"/></rdf:RDF>`
	got = put(withHeader, nil)
	if got.IRI != "http://ex.org/extension" || got.Title != "Extension" || got.Description != "Adds nothing." || got.Classes != 22 || got.Edges != 40 {
		t.Errorf("merge with header: %+v", got)
	}
	got = put(withHeader, map[string]any{"description": "explicit"})
	if got.Description != "explicit" {
		t.Errorf("explicit description: %q", got.Description)
	}

	// An invalid document merges nothing.
	cur := base()
	if rec := do("PUT", "/v1/gb/ontologies/m", map[string]any{"user_id": "u1", "content": "{}"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid merge: %d", rec.Code)
	}
	if base() != cur {
		t.Errorf("a failed merge changed the metadata")
	}

	// With "replace": true the fragment replaces everything, and what it does not contain is gone.
	got = put(mergeFragment, map[string]any{"replace": true})
	// Classes: Market, Stock, and the mentioned Security, Asset and MarketParticipant. Edges: Stock subclass_of
	// Security, tradedOn and issuedBy.
	if got.Classes != 5 || got.Edges != 3 || got.Properties != 2 {
		t.Errorf("after replace: %d classes, %d edges, %d properties, want 5/3/2", got.Classes, got.Edges, got.Properties)
	}
	if c, _ := graph.ListOntologyClasses(ctx, d, orig.ID); len(c) != 5 {
		t.Errorf("after replace: %d classes stored, want 5", len(c))
	}
	if _, ok := edge(orig.ID, "Exchange", "subclass_of", "Market"); ok {
		t.Error("replace kept an edge that is not in the document")
	}
}
