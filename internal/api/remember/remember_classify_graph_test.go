package remember

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/llm"

	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil/httputiltest"
)

// classifyLLM extracts one fact, and answers the classification requests with a class and a class that is not allowed.
type classifyLLM struct{ models, levels, prompts []string }

func (l *classifyLLM) Complete(_ context.Context,
	req llm.Request) (*llm.Response, error) {
	if strings.Contains(req.System, "You classify a knowledge graph") {
		l.models, l.levels, l.prompts = append(l.models, req.Model), append(l.levels, req.Reasoning), append(l.prompts, req.Messages[0].Content)
		return &llm.Response{
			Text:       `{"classes": ["Other Thing", "Staff"]}`,
			StopReason: "end_turn",
			Model:      "class-model",
		}, nil
	}
	return &llm.Response{
		Text:       `{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"}],"edges":[{"source":"Alice","target":"Acme","relation":"works_at"}]}`,
		StopReason: "end_turn",
		Model:      "fake-model",
	}, nil
}

func TestRememberClassifyGraph(t *testing.T) { dbtest.Run(t, testRememberClassifyGraph) }

func testRememberClassifyGraph(t *testing.T,
	d *sql.DB) {
	llm := &classifyLLM{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t,
		d,
		llm))
	post := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httputiltest.NewRequest("POST",
			"/v1/gb/remember",
			strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}

	code, body := post(`{"user_id":"u1","dataset":"d","text":"Alice works at Acme.","classify_graph":true,"graph_classes":["Staff","Customers"],` +
		`"models":{"classify_graph":"small-model"},"reasoning":{"classify_graph":"medium"}}`)
	if code != 200 || !strings.Contains(body, `"classes":[{"artifact_id":1,"classes":["staff"],"inference_id":`) {
		t.Fatalf("status %d: %s",
			code,
			body)
	}
	if len(llm.models) != 1 || llm.models[0] != "small-model" || llm.levels[0] != "medium" || !strings.Contains(llm.prompts[0], "<allowed_classes>\nstaff\ncustomers\n</allowed_classes>") {
		t.Fatalf("request: models %v, levels %v, prompts %q",
			llm.models,
			llm.levels,
			llm.prompts)
	}
	dsID, _ := relational.DatasetID(context.Background(),
		d,
		"u1",
		"d")
	if recs, _ := graph.ListArtifactClasses(context.Background(),
		d,
		dsID); len(recs) != 1 || recs[0].Class != "staff" || recs[0].InferenceID == 0 {
		t.Fatalf("records %+v", recs)
	}

	// classify_graph false: nothing is classified
	if code, body := post(`{"user_id":"u1","dataset":"d","text":"Alice works at Acme.","classify_graph":false}`); code != 200 || strings.Contains(body, `"classes"`) || len(llm.prompts) != 1 {
		t.Fatalf("status %d: %s (%d class requests)",
			code,
			body,
			len(llm.prompts))
	}

	// on by default: without the flag the graph is classified (the real handler, no flag injected)
	raw := httptest.NewRecorder()
	rawMux := http.NewServeMux()
	rawMux.HandleFunc("POST /v1/gb/remember", testRawHandler(t,
		d,
		&classifyLLM{}))
	rawMux.ServeHTTP(raw, httputiltest.NewRequest("POST",
		"/v1/gb/remember",
		strings.NewReader(`{"user_id":"u1","dataset":"d2","text":"Alice works at Acme."}`)))
	if raw.Code != 200 || !strings.Contains(raw.Body.String(), `"classes":[{"artifact_id":`) {
		t.Fatalf("default: status %d: %s",
			raw.Code,
			raw.Body)
	}

	// refused: classes with the flag off the flag, a malformed class, too many classes, an image
	for name, req := range map[string]string{
		"classes with the flag off": `{"user_id":"u1","text":"x","classify_graph":false,"graph_classes":["a"]}`,
		"empty class":               `{"user_id":"u1","text":"x","classify_graph":true,"graph_classes":["!!"]}`,
		"too many":                  `{"user_id":"u1","text":"x","classify_graph":true,"graph_classes":[` + `"c"` + manyClasses(60) + `]}`,
		"image":                     `{"user_id":"u1","image":"aGk=","classify_graph":true}`,
	} {
		if code, body := post(req); code != 400 {
			t.Errorf("%s: status %d: %s",
				name,
				code,
				body)
		}
	}
}

// manyClasses is `,"c1","c2",...,"cN"`.
func manyClasses(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(`,"c` + strings.Repeat("x", i) + `"`)
	}
	return b.String()
}
