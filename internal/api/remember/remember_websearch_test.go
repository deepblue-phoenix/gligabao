package remember

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// webLLM answers the extraction of a text. The text of a web search briefing ("BRIEFING about X") yields X and a new
// entity, Initech, that the main text never mentions.
type webLLM struct{ main string }

var briefingAbout = regexp.MustCompile(`BRIEFING about (\w+)`)

func (l webLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	out := l.main
	switch msg := req.Messages[0].Content; {
	case strings.HasPrefix(req.System, "You compare NEW facts"):
		out = `{"duplicates":[],"contradictions":[]}`
	case briefingAbout.MatchString(msg):
		x := briefingAbout.FindStringSubmatch(msg)[1]
		out = `{"entities":[{"name":"Initech","type":"organization","description":"news"},{"name":"` + x + `","type":"organization"}],
			"edges":[{"source":"Initech","target":"` + x + `","relation":"partnered_with"}]}`
	}
	return &ai.Response{Text: out, StopReason: "end_turn", Model: "fake-model"}, nil
}

// fakeSearcher answers every search with a briefing about the entity of the prompt, and records the requests.
type fakeSearcher struct {
	mu   sync.Mutex
	reqs []ai.WebSearchRequest
	text string // the answer; "" means a briefing about the entity
	err  error
}

func (f *fakeSearcher) WebSearch(_ context.Context, req ai.WebSearchRequest) (*ai.WebSearchResponse, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	text := f.text
	if text == "" {
		name := regexp.MustCompile(`name: (\w+)`).FindStringSubmatch(req.Prompt)[1]
		text = "BRIEFING about " + name + " in 2026."
	}
	return &ai.WebSearchResponse{Text: text, Queries: []string{"q1"}, Provider: ai.ProviderAnthropic, Model: "fake-search-model",
		Sources: []ai.WebSource{{URL: "https://news.example/1", Title: "News", PageAge: "May 1, 2026"}}, Searches: 1, InputTokens: 10, OutputTokens: 5, CostUSD: 0.02}, nil
}

const webMain = `{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"},{"name":"Globex","type":"organization"},{"name":"Innovation","type":"concept"}],
	"edges":[{"source":"Alice","target":"Acme","relation":"works_at"},{"source":"Acme","target":"Globex","relation":"acquired"}]}`

func rememberPost(t *testing.T, h http.HandlerFunc, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestRememberWebSearch(t *testing.T) { dbtest.Run(t, testRememberWebSearch) }

// testRememberWebSearch: with web_search the key entities (the ones in the most facts, no concepts) are searched for; every
// result is a web_search artifact that saves its query and is a child of the artifact the entity came from; the
// entities found on the web are stored like any other and linked to the web_search artifact; without the flag nothing is
// searched.
func testRememberWebSearch(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	fs := &fakeSearcher{}
	h := testHandler(t, d, webLLM{webMain}, memory_core.WithWebSearcher(fs))

	// Without the flag the searcher is not used.
	if code, body := rememberPost(t, h, `{"user_id":"u1","dataset":"plain","text":"Alice works at Acme."}`); code != 200 || len(fs.reqs) != 0 {
		t.Fatalf("no flag: status %d, %d searches: %s", code, len(fs.reqs), body)
	}

	code, body := rememberPost(t, h, `{"user_id":"u1","text":"Alice works at Acme.","session_id":"s1","web_search":true,"models":{"web_search":"search-model"}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var res memory_remember.Result
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}

	// Three searches (Innovation is a concept), Acme first: it is in two facts.
	if len(fs.reqs) != 3 || len(res.WebSearches) != 3 {
		t.Fatalf("%d searches, %d results: %s", len(fs.reqs), len(res.WebSearches), body)
	}
	for _, r := range fs.reqs {
		if r.Model != "search-model" || r.System == "" || !strings.Contains(r.Prompt, "latest world events") {
			t.Errorf("search request = %+v", r)
		}
	}
	if res.WebSearches[0].Entity != "Acme" || res.WebSearches[0].Query != "latest world events and news about Acme (organization)" {
		t.Errorf("first search = %+v", res.WebSearches[0])
	}

	dsID, err := relational.DatasetID(ctx, d, "u1", relational.DefaultDataset)
	if err != nil {
		t.Fatal(err)
	}
	arts, err := graph.ListArtifacts(ctx, d, dsID)
	if err != nil || len(arts) != 4 {
		t.Fatalf("artifacts: %+v, %v", arts, err)
	}
	main := arts[0]
	if main.Kind != graph.ArtifactKnowledge || main.Query != "" || main.ParentID != 0 {
		t.Errorf("main artifact = %+v", main)
	}
	byID := map[int64]graph.Artifact{}
	for _, a := range arts {
		byID[a.ID] = a
	}
	for _, w := range res.WebSearches {
		a, ok := byID[w.ArtifactID]
		if !ok || a.Kind != graph.ArtifactWebSearch || a.ParentID != main.ID || w.ParentArtifactID != main.ID || a.Query != w.Query ||
			a.SessionID != "s1" || a.IsChunk() || !strings.Contains(a.Text, "BRIEFING about "+w.Entity) || !strings.Contains(a.Text, "<https://news.example/1>") {
			t.Errorf("web search %+v: artifact %+v", w, a)
		}
	}

	// The count of chunks is not confused by the web searches, which have a parent too.
	if c, err := graph.DatasetCounts(ctx, d, dsID); err != nil || c.Artifacts != 4 || c.Chunks != 0 {
		t.Errorf("counts = %+v, %v", c, err)
	}

	// Initech is only on the web: it is stored as an entity, linked to the web_search artifacts and not to the text.
	ents, err := graph.ListEntities(ctx, d, dsID)
	if err != nil {
		t.Fatal(err)
	}
	var initech int64
	for _, e := range ents {
		if e.Name == "Initech" {
			initech = e.ID
		}
	}
	if initech == 0 {
		t.Fatalf("entities: %+v", ents)
	}
	links, _ := graph.ListEntityArtifacts(ctx, d, dsID)
	onWeb, onText := 0, 0
	for _, l := range links {
		if l.EntityID == initech && byID[l.ArtifactID].Kind == graph.ArtifactWebSearch {
			onWeb++
		} else if l.EntityID == initech {
			onText++
		}
	}
	if onWeb != 3 || onText != 0 {
		t.Errorf("Initech links: %d web, %d text", onWeb, onText)
	}
	// The web result's session is recorded against its entities too.
	if es, _ := graph.ListEntitySessions(ctx, d, dsID); len(es) != len(ents) {
		t.Errorf("entity sessions = %d, entities = %d", len(es), len(ents))
	}
	// The search was billed: its call is recorded.
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_inferences WHERE model_id = '`+relational.ModelID("fake-search-model")+`' AND stage = 'web_search' AND reasoning = 'none'`).Scan(&n); err != nil || n != 3 {
		t.Errorf("recorded searches = %d, %v", n, err)
	}
}

func TestRememberWebSearchTrajectory(t *testing.T) { dbtest.Run(t, testRememberWebSearchTrajectory) }

func testRememberWebSearchTrajectory(t *testing.T, d *sql.DB) {
	h := testEvalHandler(t, d, webLLM{webMain}, memory_core.WithWebSearcher(&fakeSearcher{}))
	code, body := rememberPost(t, h, `{"user_id":"u1","text":"Alice works at Acme.","web_search":true}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Trajectory struct {
			Steps []struct {
				ToolCalls []struct {
					Name string `json:"function_name"`
				} `json:"tool_calls"`
				Metrics *struct {
					Cost float64 `json:"cost_usd"`
				} `json:"metrics"`
			} `json:"steps"`
		} `json:"trajectory"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	tools := map[string]int{}
	cost := 0.0
	for _, s := range out.Trajectory.Steps {
		for _, tc := range s.ToolCalls {
			tools[tc.Name]++
			if tc.Name == "web_search" && s.Metrics != nil {
				cost += s.Metrics.Cost
			}
		}
	}
	if tools["select_web_search_entities"] != 1 || tools["web_search"] != 3 || cost < 0.0599 || cost > 0.0601 {
		t.Errorf("tools = %v, web search cost = %v", tools, cost)
	}
}

func TestRememberWebSearchUnavailable(t *testing.T) { dbtest.Run(t, testRememberWebSearchUnavailable) }

func testRememberWebSearchUnavailable(t *testing.T, d *sql.DB) {
	// No searcher (no Anthropic key): 503, and nothing is stored.
	if code, body := rememberPost(t, testHandler(t, d, webLLM{webMain}), `{"user_id":"u1","text":"Alice works at Acme.","web_search":true}`); code != 503 {
		t.Errorf("status %d: %s", code, body)
	}
	// Not for an image.
	img := `{"user_id":"u1","image":"AAAA","web_search":true}`
	if code, _ := rememberPost(t, testHandler(t, d, webLLM{webMain}, memory_core.WithWebSearcher(&fakeSearcher{})), img); code != 400 {
		t.Errorf("image: status %d", code)
	}
	// Go callers get the sentinel.
	_, err := memory_remember.Remember(context.Background(), d, webLLM{webMain}, "u1", "", "", "Alice works at Acme.", time.Time{}, memory_core.WithWebSearch(true))
	if !errors.Is(err, memory_core.ErrWebSearchUnavailable) {
		t.Errorf("err = %v", err)
	}
	// A caller cannot ask for the web_search kind.
	if code, _ := rememberPost(t, testHandler(t, d, webLLM{webMain}), `{"user_id":"u1","text":"x","kind":"web_search"}`); code != 400 {
		t.Errorf("kind web_search: status %d", code)
	}
}

func TestRememberWebSearchFailures(t *testing.T) { dbtest.Run(t, testRememberWebSearchFailures) }

// testRememberWebSearchFailures: the text is stored first and the web search is an enrichment: a failing search, or one that
// finds nothing, never fails remember and stores no web_search artifact.
func testRememberWebSearchFailures(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	for name, fs := range map[string]*fakeSearcher{
		"failed":  {err: errors.New("anthropic down")},
		"nothing": {text: "NO_RESULTS"},
	} {
		h := testHandler(t, d, webLLM{webMain}, memory_core.WithWebSearcher(fs))
		code, body := rememberPost(t, h, `{"user_id":"u1","dataset":"`+name+`","text":"Alice works at Acme.","web_search":true}`)
		if code != 200 {
			t.Fatalf("%s: status %d: %s", name, code, body)
		}
		var res memory_remember.Result
		if err := json.Unmarshal([]byte(body), &res); err != nil {
			t.Fatal(err)
		}
		if len(res.WebSearches) != 3 || len(res.Entities) != 4 {
			t.Fatalf("%s: %s", name, body)
		}
		for _, w := range res.WebSearches {
			if w.ArtifactID != 0 || (name == "failed") != strings.Contains(w.Error, "anthropic down") {
				t.Errorf("%s: %+v", name, w)
			}
		}
		dsID, _ := relational.DatasetID(ctx, d, "u1", name)
		if c, err := graph.DatasetCounts(ctx, d, dsID); err != nil || c.Artifacts != 1 {
			t.Errorf("%s: counts = %+v, %v", name, c, err)
		}
	}
}

func TestRememberWebSearchChunks(t *testing.T) { dbtest.Run(t, testRememberWebSearchChunks) }

// testRememberWebSearchChunks: for a long text the web search hangs off the chunk the entity was extracted from (the first
// one, here), not off the parent, and the web searches are not chunks.
func testRememberWebSearchChunks(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	h := testHandler(t, d, webLLM{`{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"}],"edges":[{"source":"Alice","target":"Acme","relation":"works_at"}]}`},
		memory_core.WithWebSearcher(&fakeSearcher{}), memory_core.WithChunking(memory_chunk.Config{Size: 40, OverlapPercent: 20}))
	var paras []string
	for i := 0; i < 8; i++ {
		paras = append(paras, "Alice walked along the river and talked about the weather and her plans for the summer holidays.")
	}
	text, _ := json.Marshal(strings.Join(paras, "\n\n"))
	code, body := rememberPost(t, h, `{"user_id":"u1","text":`+string(text)+`,"web_search":true}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var res memory_remember.Result
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) < 4 || len(res.WebSearches) != 2 {
		t.Fatalf("chunks %d, web searches %d", len(res.Chunks), len(res.WebSearches))
	}
	for _, w := range res.WebSearches { // Alice and Acme are in every chunk: the first chunk is where they came from
		if w.ParentArtifactID != res.Chunks[0].ArtifactID || w.ArtifactID == 0 {
			t.Errorf("web search %+v, first chunk %d (parent %d)", w, res.Chunks[0].ArtifactID, res.ArtifactID)
		}
	}
	dsID, _ := relational.DatasetID(ctx, d, "u1", relational.DefaultDataset)
	if c, err := graph.DatasetCounts(ctx, d, dsID); err != nil || c.Chunks != len(res.Chunks) || c.Artifacts != 1+2*len(res.Chunks)+2 {
		t.Errorf("counts = %+v, %v", c, err)
	}
}
