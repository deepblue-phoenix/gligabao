package query

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// vecLLM plays every LLM call of remember and query for the tests of the similarity retrieval: the extraction returns
// extraction, a chunk summary is summary, a comparison of facts finds nothing, and the answer cites the first passage it is
// given. It keeps the user message of each query.
type vecLLM struct {
	extraction, summary string

	mu      sync.Mutex
	prompts []string // the user messages of the answering calls
}

var passageRe = regexp.MustCompile(`\[passage (\d+)\]`)

func (l *vecLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	text := func(s string) (*ai.Response, error) { return &ai.Response{Text: s, StopReason: "end_turn"}, nil }
	switch {
	case strings.Contains(req.System, "extract a temporal knowledge graph"):
		if l.extraction != "" {
			return text(l.extraction)
		}
		// The text of a chunk: PersonN works at Acme, once per person.
		var ents, edges []string
		seen := map[string]bool{}
		for _, m := range regexp.MustCompile(`Person(\d+) works at Acme`).FindAllStringSubmatch(req.Messages[0].Content, -1) {
			if p := "Person" + m[1]; !seen[p] {
				seen[p] = true
				ents = append(ents, fmt.Sprintf(`{"name":%q,"type":"person"}`, p))
				edges = append(edges, fmt.Sprintf(`{"source":%q,"target":"Acme","relation":"works at"}`, p))
			}
		}
		ents = append(ents, `{"name":"Acme","type":"organization"}`)
		return text(`{"entities":[` + strings.Join(ents, ",") + `],"edges":[` + strings.Join(edges, ",") + `]}`)
	case strings.Contains(req.System, "You summarize a passage"):
		return text(l.summary)
	case strings.Contains(req.System, "ONLY a knowledge graph"):
		user := req.Messages[0].Content
		l.mu.Lock()
		l.prompts = append(l.prompts, user)
		l.mu.Unlock()
		cited := "[]"
		if m := passageRe.FindStringSubmatch(user); m != nil {
			cited = "[" + m[1] + ",999999]"
		}
		return text(`{"answer":"ok","entities":[],"edges":[],"passages":` + cited + `}`)
	}
	return text(`{"duplicates":[],"contradictions":[]}`) // resolving facts
}

func (l *vecLLM) lastPrompt() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.prompts[len(l.prompts)-1]
}

const vecExtraction = `{"entities":[{"name":"Alice","type":"person","description":"engineer"},{"name":"Acme","type":"organization"},{"name":"Bob","type":"person"},{"name":"Rome","type":"place"}],
	"edges":[{"source":"Alice","target":"Acme","relation":"works_at"},{"source":"Bob","target":"Rome","relation":"lives_in"}]}`

// TestQueryVectorSeeds checks that the entities and the facts whose embedding is close to the question are in the context
// although no word of the question is in them, and that what is not close is left out.
func TestQueryVectorSeeds(t *testing.T) { dbtest.Run(t, testQueryVectorSeeds) }

func testQueryVectorSeeds(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	llm := &vecLLM{extraction: vecExtraction}
	vec := withVectors(t)
	if _, err := memory_remember.Remember(ctx, d, llm, "u1", "", "", "x", time.Time{}, vec); err != nil {
		t.Fatal(err)
	}

	// "employer" is the topic of "Alice works at Acme.", but no entity has it in its name or description.
	if _, err := memory_query.Query(ctx, d, llm, "u1", "", "Which employer does the person have?", vec); err != nil {
		t.Fatal(err)
	}
	p := llm.lastPrompt()
	if !strings.Contains(p, "works_at") || !strings.Contains(p, "Alice") || !strings.Contains(p, "Acme") {
		t.Errorf("the fact close to the question is not in the context:\n%s", p)
	}
	if strings.Contains(p, "Bob") || strings.Contains(p, "Rome") || strings.Contains(p, "lives_in") {
		t.Errorf("what is not close to the question is in the context:\n%s", p)
	}

	// "profession" is the topic of the description of Alice: she is a seed, so her facts come with her.
	if _, err := memory_query.Query(ctx, d, llm, "u1", "", "Tell me about the profession", vec); err != nil {
		t.Fatal(err)
	}
	p = llm.lastPrompt()
	if !strings.Contains(p, "[entity 1] Alice (person): engineer") || !strings.Contains(p, "works_at") || strings.Contains(p, "Bob") {
		t.Errorf("the entity close to the question is not a seed:\n%s", p)
	}

	// The keyword retrieval still works next to it: Bob is named, and the unrelated embedding adds nothing else.
	if _, err := memory_query.Query(ctx, d, llm, "u1", "", "Where is Bob?", vec); err != nil {
		t.Fatal(err)
	}
	if p = llm.lastPrompt(); !strings.Contains(p, "lives_in") || strings.Contains(p, "works_at") {
		t.Errorf("keyword retrieval:\n%s", p)
	}

	// The embedding of the question is an inference of the query.
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM gb_inferences WHERE type = 'query' AND stage = 'embed'`).Scan(&n); err != nil || n != 3 {
		t.Errorf("embedding inferences of query = %d, %v; want 3", n, err)
	}
}

// TestQueryPassages checks that the chunks and the summaries of a long text that are close to the question are given to the
// LLM, the best ones with the text of the chunk, and that the passages the answer cites are in the evidence.
func TestQueryPassages(t *testing.T) { dbtest.Run(t, testQueryPassages) }

func testQueryPassages(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	llm := &vecLLM{summary: "Summary: outdoor life."}
	vec := withVectors(t)
	var ps []string
	for i := 0; i < 12; i++ {
		ps = append(ps, fmt.Sprintf("Person%d works at Acme and enjoys long walks in the park on weekends.", i))
	}
	res, err := memory_remember.Remember(ctx, d, llm, "u1", "", "", strings.Join(ps, "\n\n"), time.Time{}, vec,
		memory_core.WithChunking(chunk.Config{Size: 64, OverlapPercent: 40}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) < 4 {
		t.Fatalf("%d chunks", len(res.Chunks))
	}
	chunkIDs := map[int64]int{}
	for i, c := range res.Chunks {
		chunkIDs[c.ArtifactID] = i
	}

	// Chunks and summaries are both about outdoor activities, the question is too (by topic, not by words).
	got, err := memory_query.Query(ctx, d, llm, "u1", "", "What do they do outdoors?", vec)
	if err != nil {
		t.Fatal(err)
	}
	p := llm.lastPrompt()
	if !strings.Contains(p, "<passages>") {
		t.Fatalf("no passages in the context:\n%s", p)
	}
	n := strings.Count(p, "[passage ")
	withText := strings.Count(p, "\ntext: ")
	if n < 3 || n > 8 || withText != 3 {
		t.Errorf("%d passages, %d with text; want 3 with text among 3 to 8:\n%s", n, withText, p)
	}
	if !strings.Contains(p, "summary: Summary: outdoor life.") {
		t.Errorf("no summary in the passages:\n%s", p)
	}

	// The answer cited the first passage and an ID that was not given: only the first is evidence.
	if len(got.Evidence.Passages) != 1 {
		t.Fatalf("evidence passages = %+v", got.Evidence.Passages)
	}
	ev := got.Evidence.Passages[0]
	idx, ok := chunkIDs[ev.ArtifactID]
	if !ok || ev.ParentID != res.ArtifactID || ev.Index != idx || ev.Text == "" || ev.Summary != "Summary: outdoor life." {
		t.Errorf("evidence passage = %+v (chunk %d of %v, parent %d)", ev, idx, ok, res.ArtifactID)
	}
	if first := passageRe.FindStringSubmatch(p); first == nil || first[1] != strconv.FormatInt(ev.ArtifactID, 10) {
		t.Errorf("the evidence is not the cited passage: %v vs %+v", first, ev)
	}

	// A question about nothing the memory has gets no passages, and no section for them.
	if _, err := memory_query.Query(ctx, d, llm, "u1", "", "Anything else?", vec); err != nil {
		t.Fatal(err)
	}
	if p = llm.lastPrompt(); strings.Contains(p, "<passages>") {
		t.Errorf("passages for a question that is close to none:\n%s", p)
	}
}

// TestQueryLiveFactsOnly checks that a fact that newer knowledge superseded (expired) is not in the context, whether it was
// found by its entities or by its embedding, which outlives the validity of the edge.
func TestQueryLiveFactsOnly(t *testing.T) { dbtest.Run(t, testQueryLiveFactsOnly) }

func testQueryLiveFactsOnly(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	llm := &vecLLM{extraction: vecExtraction}
	vec := withVectors(t)
	if _, err := memory_remember.Remember(ctx, d, llm, "u1", "", "", "x", time.Time{}, vec); err != nil {
		t.Fatal(err)
	}
	// Both facts are superseded: edge 1 (Alice works at Acme) and edge 2 (Bob lives in Rome).
	if _, err := d.ExecContext(ctx, `UPDATE gb_edges SET expired_at = CURRENT_TIMESTAMP, invalid_at = CURRENT_TIMESTAMP`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"Which employer does the person have?", // finds edge 1 by its embedding
		"Where does Alice work?",               // finds Alice by her name
		"Where is Bob?",
	} {
		if _, err := memory_query.Query(ctx, d, llm, "u1", "", q, vec); err != nil {
			t.Fatal(err)
		}
		if p := llm.lastPrompt(); strings.Contains(p, "works_at") || strings.Contains(p, "lives_in") || strings.Contains(p, "[edge ") {
			t.Errorf("%q: an expired fact is in the context:\n%s", q, p)
		}
	}
}
