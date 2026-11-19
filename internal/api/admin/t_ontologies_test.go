package admin_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology"
)

// TestOntologiesOf checks that the admin lists the ontologies of every user, with their specs.
func TestOntologiesOf(t *testing.T) { dbtest.Run(t, testOntologiesOf) }

func testOntologiesOf(t *testing.T,
	d *sql.DB) {
	for _, user := range []string{"alice", "bob"} {
		if _, err := memory_ontology.Create(t.Context(),
			d,
			user,
			"m",
			"",
			"yaml",
			[]byte("classes:\n  A: {}\n  B:\n    subClassOf: A\n")); err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	admin.OntologiesOf([]admin.Named{{Name: "standard", DB: d}})(w, httptest.NewRequest(http.MethodGet,
		"/admin/api/ontologies",
		nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var out struct {
		Ontologies []struct {
			UserID string          `json:"user_id"`
			Spec   json.RawMessage `json:"spec"`
		} `json:"ontologies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Ontologies) != 2 || out.Ontologies[0].UserID != "alice" || out.Ontologies[1].UserID != "bob" || len(out.Ontologies[0].Spec) == 0 {
		t.Fatalf("ontologies: %s", w.Body)
	}

	w = httptest.NewRecorder()
	admin.OntologiesOf([]admin.Named{{Name: "standard", DB: d}})(w, httptest.NewRequest(http.MethodGet,
		"/admin/api/ontologies?db=nope",
		nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown db: %d", w.Code)
	}
}
