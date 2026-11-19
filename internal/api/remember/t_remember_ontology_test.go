package remember

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil/httputiltest"
)

func TestRememberOntologyGrounding(t *testing.T) { dbtest.Run(t, testRememberOntologyGrounding) }

func testRememberOntologyGrounding(t *testing.T,
	d *sql.DB) {
	ctx := t.Context()
	for name, doc := range map[string]string{
		"people":   "classes:\n  Person:\n    aliases: [human]\n  Organization: {}\nrelations:\n  works_at: {domain: Person, range: Organization}",
		"clashing": "classes:\n  Person:\n    description: not the same person",
	} {
		if _, err := memory_ontology.Create(ctx, d, "u1", name, "", "", []byte(doc)); err != nil {
			t.Fatal(err)
		}
	}
	llm := &classifyLLM{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm))
	post := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httputiltest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}

	// grounded in the one ontology: the response has the summary of annotate mode
	code, body := post(`{"user_id":"u1","dataset":"d","text":"Alice works at Acme.","ontology_grounding":true,"ontologies":["people"]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var res struct {
		Grounding struct {
			Mode       string                       `json:"mode"`
			Ontologies []struct{ Name string }      `json:"ontologies"`
			Nodes      struct{ Total, Matched int } `json:"nodes"`
			Edges      struct{ Total, Matched int } `json:"edges"`
			Percent    float64                      `json:"matched_percent"`
		} `json:"ontology_grounding"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	// Alice (person) and the organization match; so does works_at. (Alice's type is "person", Acme's "organization".)
	if g := res.Grounding; g.Mode != "annotate" || len(g.Ontologies) != 1 || g.Ontologies[0].Name != "people" || g.Nodes.Total != 2 || g.Nodes.Matched != 2 || g.Edges.Matched != 1 || g.Percent != 100 {
		t.Errorf("grounding = %+v", res.Grounding)
	}
	if code, body := post(`{"user_id":"u1","dataset":"d","text":"Alice works at Acme.","ontology_grounding":true,"ontology_grounding_mode":"strict"}`); code != 400 {
		// no ontologies named means all of them, and two of them clash
		t.Errorf("clashing ontologies: status %d: %s", code, body)
	}
	if code, body := post(`{"user_id":"u1","dataset":"d","text":"Alice works at Acme.","ontology_grounding":true,"ontology_grounding_mode":"strict","ontologies":["people"]}`); code != 200 || !strings.Contains(body, `"dropped_ratio"`) {
		t.Errorf("strict: status %d: %s", code, body)
	}

	// refused: fields without the flag, an unknown name or mode, ontologies that clash, an image
	for name, req := range map[string]string{
		"ontologies without the flag": `{"user_id":"u1","text":"x","ontologies":["people"]}`,
		"mode without the flag":       `{"user_id":"u1","text":"x","ontology_grounding_mode":"strict"}`,
		"flag false":                  `{"user_id":"u1","text":"x","ontology_grounding":false,"ontologies":["people"]}`,
		"unknown ontology":            `{"user_id":"u1","text":"x","ontology_grounding":true,"ontologies":["nope"]}`,
		"someone else's":              `{"user_id":"u2","text":"x","ontology_grounding":true,"ontologies":["people"]}`,
		"no ontologies at all":        `{"user_id":"u2","text":"x","ontology_grounding":true}`,
		"unknown mode":                `{"user_id":"u1","text":"x","ontology_grounding":true,"ontology_grounding_mode":"loose","ontologies":["people"]}`,
		"conflict":                    `{"user_id":"u1","text":"x","ontology_grounding":true,"ontologies":["people","clashing"]}`,
		"image":                       `{"user_id":"u1","image":"aGk=","ontology_grounding":true}`,
	} {
		if code, body := post(req); code != 400 {
			t.Errorf("%s: status %d: %s", name, code, body)
		}
	}
	// without the flag nothing changes
	if code, body := post(`{"user_id":"u1","dataset":"d","text":"Alice works at Acme."}`); code != 200 || strings.Contains(body, "ontology_grounding") {
		t.Errorf("no grounding: status %d: %s", code, body)
	}
}
