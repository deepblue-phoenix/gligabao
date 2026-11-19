package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

const smallOntology = `
id: small
description: Two classes.
classes:
  Cat:
    subClassOf: Animal
  Animal: {}
`

// ontologyAPI serves the ontologies API on d, with the ontology directories dirs.
func ontologyAPI(t *testing.T,
	d *sql.DB,
	dirs []string) func(method,
	path string,
	v map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	database := &db.DB{
		Relational: d,
		Graph:      d,
	}
	mux := http.NewServeMux()
	Register(mux,
		mgrFor(t,
			database,
			nil),
		database,
		nil,
		nil,
		nil,
		nil,
		nil,
		WithOntologyDirs(dirs)) // an ontology needs no LLM
	return func(method,
		path string,
		v map[string]any) *httptest.ResponseRecorder {
		if v == nil {
			v = map[string]any{"user_id": "u1"} // the default user of a request with no body
		}
		body, _ := json.Marshal(v)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, newReq(method,
			path,
			strings.NewReader(string(body))))
		return rec
	}
}

func wantStatus(t *testing.T,
	rec *httptest.ResponseRecorder,
	code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status %d, want %d: %s",
			rec.Code,
			code,
			rec.Body)
	}
}

func TestOntologies(t *testing.T) { dbtest.Run(t, testOntologies) }

func testOntologies(t *testing.T,
	d *sql.DB) {
	do := ontologyAPI(t, d, nil)
	doc, err := os.ReadFile("../../build/ontology/market.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// Create.
	rec := do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "market", "content": string(doc)})
	wantStatus(t, rec, http.StatusCreated)
	var got relational.Ontology
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 || got.UserID != "u1" || got.Name != "market" || got.Format != "yaml" || got.Version != 1 || got.SizeBytes != len(doc) ||
		got.Classes != 20 || got.Relations != 26 || got.Properties != 4 || got.Description == "" || len(got.Spec) != 0 || !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Fatalf("metadata: %+v", got)
	}

	// The JSON form of the same ontology is accepted as a "spec" object or as content.
	var spec map[string]any
	read := do("GET", "/v1/gb/ontologies/market", nil)
	wantStatus(t, read, http.StatusOK)
	var full relational.Ontology
	if err := json.Unmarshal(read.Body.Bytes(), &full); err != nil || len(full.Spec) == 0 {
		t.Fatalf("get: %s (%v)", read.Body, err)
	}
	if err := json.Unmarshal(full.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "as-spec", "spec": spec}), http.StatusCreated)
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "as-json", "content": string(full.Spec), "format": "json"}), http.StatusCreated)

	// List (no specs) and get; another user sees nothing.
	var list struct {
		Ontologies []relational.Ontology `json:"ontologies"`
	}
	rec = do("GET", "/v1/gb/ontologies", map[string]any{"user_id": "u1"})
	wantStatus(t, rec, http.StatusOK)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Ontologies) != 3 || len(list.Ontologies[0].Spec) != 0 {
		t.Fatalf("list: %s (%v)", rec.Body, err)
	}
	rec = do("GET", "/v1/gb/ontologies", map[string]any{"user_id": "u2"})
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Ontologies) != 0 {
		t.Fatalf("u2 sees %s", rec.Body)
	}
	wantStatus(t, do("GET", "/v1/gb/ontologies/nope", nil), http.StatusNotFound)

	// Names are unique per user; another user may reuse one; invalid input is rejected and stores nothing.
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "market", "content": string(doc)}), http.StatusConflict)
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u2", "name": "market", "content": string(doc), "description": "mine"}), http.StatusCreated)
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"name": "x", "content": string(doc)}), http.StatusBadRequest)                                   // no user
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "content": string(doc)}), http.StatusBadRequest)                               // no name
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "bad name!", "content": string(doc)}), http.StatusBadRequest)          // bad name
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "x"}), http.StatusBadRequest)                                          // no content
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "x", "content": "classes: ["}), http.StatusBadRequest)                 // not YAML
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "x", "format": "owl", "content": string(doc)}), http.StatusBadRequest) // unsupported
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "x", "content": "id: x"}), http.StatusBadRequest)                      // no classes
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "x", "content": string(doc), "spec": spec}), http.StatusBadRequest)    // two sources
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "x", "content": "<rdf:RDF/>"}), http.StatusBadRequest)                 // OWL is gone
	if n := len(storedOntologies(t, d, "u1")); n != 3 {
		t.Fatalf("u1 has %d ontologies after failed loads, want 3", n)
	}

	// An invalid spec is a 400 that lists its problems.
	rec = do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "cyc", "content": "classes:\n  A:\n    subClassOf: B\n  B:\n    subClassOf: A\n  C:\n    subClassOf: Z"})
	wantStatus(t, rec, http.StatusBadRequest)
	var bad struct {
		Error    string `json:"error"`
		Problems []struct{ Path, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bad); err != nil || len(bad.Problems) != 3 || !strings.Contains(bad.Problems[0].Path, "classes.") {
		t.Fatalf("problems: %s (%v)", rec.Body, err)
	}

	// Update: the next version; the same ontology.
	rec = do("PUT", "/v1/gb/ontologies/market", map[string]any{"user_id": "u1", "content": smallOntology})
	wantStatus(t, rec, http.StatusOK)
	var up relational.Ontology
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	if up.ID != got.ID || up.Version != 2 || up.Classes != 2 || up.Relations != 0 || up.Description != "Two classes." || !up.CreatedAt.Equal(got.CreatedAt) {
		t.Fatalf("updated: %+v", up)
	}
	cur := storedOntologies(t, d, "u1")[0]
	wantStatus(t, do("PUT", "/v1/gb/ontologies/missing", map[string]any{"user_id": "u1", "content": smallOntology}), http.StatusNotFound)
	wantStatus(t, do("PUT", "/v1/gb/ontologies/market", map[string]any{"user_id": "u3", "content": smallOntology}), http.StatusNotFound) // not theirs
	wantStatus(t, do("PUT", "/v1/gb/ontologies/market", map[string]any{"content": smallOntology}), http.StatusBadRequest)                // no user
	wantStatus(t, do("PUT", "/v1/gb/ontologies/bad%20name!", map[string]any{"user_id": "u1", "content": smallOntology}), http.StatusBadRequest)
	wantStatus(t, do("PUT", "/v1/gb/ontologies/market", map[string]any{"user_id": "u1"}), http.StatusBadRequest)
	wantStatus(t, do("PUT", "/v1/gb/ontologies/market", map[string]any{"user_id": "u1", "content": "id: x"}), http.StatusBadRequest)
	if now := storedOntologies(t, d, "u1")[0]; now.Version != cur.Version || now.SHA256 != cur.SHA256 {
		t.Fatalf("a failed update changed the ontology: %+v, want %+v", now, cur)
	}
	if u2 := storedOntologies(t, d, "u2")[0]; u2.Version != 1 || u2.Classes != 20 {
		t.Fatalf("u2's ontology changed: %+v", u2)
	}
}

func storedOntologies(t *testing.T,
	d *sql.DB,
	userID string) []relational.Ontology {
	t.Helper()
	list, err := relational.ListOntologies(t.Context(),
		d,
		userID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestOntologyEventsEndpoint(t *testing.T) { dbtest.Run(t, testOntologyEventsEndpoint) }

func testOntologyEventsEndpoint(t *testing.T,
	d *sql.DB) {
	do := ontologyAPI(t, d, nil)
	wantStatus(t, do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u1", "name": "small", "content": smallOntology}), http.StatusCreated)
	rec := do("GET", "/v1/gb/ontologies/small/events", map[string]any{"user_id": "u1"})
	wantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != `{"events":[]}` {
		t.Errorf("events = %s", rec.Body)
	}
	wantStatus(t, do("GET", "/v1/gb/ontologies/small/events?outcome=dropped&limit=5&artifact_id=3", nil), http.StatusOK)
	for _, q := range []string{"?limit=0", "?limit=x", "?limit=100000", "?artifact_id=-1", "?artifact_id=x", "?outcome=bogus"} {
		wantStatus(t, do("GET", "/v1/gb/ontologies/small/events"+q, nil), http.StatusBadRequest)
	}
	wantStatus(t, do("GET", "/v1/gb/ontologies/nope/events", nil), http.StatusNotFound)
}

// "file" loads a document of the configured ontology directories, under the requesting user; it works only for a plain file name.
func TestOntologyFile(t *testing.T) { dbtest.Run(t, testOntologyFile) }

func testOntologyFile(t *testing.T,
	d *sql.DB) {
	first, second := t.TempDir(), t.TempDir()
	write := func(dir,
		name,
		content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(second, "small.yaml", smallOntology)
	write(second, "small.json", `{"classes":{"Cat":{}}}`)
	write(first, "other.yaml", "not an ontology: [")
	if err := os.Mkdir(filepath.Join(first, "sub.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirs := []string{first, second}
	do := ontologyAPI(t, d, dirs)

	rec := do("POST", "/v1/gb/ontologies", map[string]any{"user_id": "u7", "name": "small", "file": "small.yaml"})
	wantStatus(t, rec, http.StatusCreated)
	var got relational.Ontology
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u7" || got.Name != "small" || got.Classes != 2 || got.Format != "yaml" {
		t.Fatalf("loaded: %+v", got)
	}
	rec = do("PUT", "/v1/gb/ontologies/small", map[string]any{"user_id": "u7", "file": "small.json"})
	wantStatus(t, rec, http.StatusOK)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Format != "json" || got.Version != 2 || got.Classes != 1 {
		t.Fatalf("updated from a JSON file: %+v (%v)", got, err)
	}

	post := func(dirs []string,
		v map[string]any) *httptest.ResponseRecorder {
		return ontologyAPI(t, d, dirs)("POST", "/v1/gb/ontologies", v)
	}
	wantStatus(t, post(dirs, map[string]any{"user_id": "u7", "name": "x", "file": "nope.yaml"}), http.StatusNotFound)
	wantStatus(t, post(dirs, map[string]any{"user_id": "u7", "name": "x", "file": "sub.yaml"}), http.StatusNotFound)
	wantStatus(t, post(dirs, map[string]any{"user_id": "u7", "name": "x", "file": "other.yaml"}), http.StatusBadRequest)
	for _, name := range []string{"../small.yaml", "sub/small.yaml", `..\small.yaml`, ".hidden", filepath.Join(second, "small.yaml")} {
		wantStatus(t, post(dirs, map[string]any{"user_id": "u7", "name": "x", "file": name}), http.StatusBadRequest)
	}
	wantStatus(t, post(dirs, map[string]any{"user_id": "u7", "name": "x", "file": "small.yaml", "content": smallOntology}), http.StatusBadRequest)
	wantStatus(t, post(dirs, map[string]any{"user_id": "u7", "name": "x"}), http.StatusBadRequest)
	wantStatus(t, post(nil, map[string]any{"user_id": "u7", "name": "x", "file": "small.yaml"}), http.StatusBadRequest)
}
