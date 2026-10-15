package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestLLMCallsRecorded(t *testing.T) { dbtest.Run(t, testLLMCallsRecorded) }

func testLLMCallsRecorded(t *testing.T, d *sql.DB) {
	llm := &costLLM{outs: []string{
		`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`,
		`{"answer":"Alice.","entities":[1],"edges":[]}`,
	}}
	mux := http.NewServeMux()
	Register(mux, &db.DB{Relational: d, Graph: d}, llm, nil, nil, nil, nil)
	for _, c := range [][2]string{{"/v1/gb/remember", `{"user_id":"u1","text":"Alice"}`}, {"/v1/gb/recall", `{"user_id":"u1","query":"Alice?"}`}} { // order matters: recall needs the graph
		path, body := c[0], c[1]
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	got, err := relational.ModelRunStats(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	// remember makes 1 LLM call when there is nothing stored to compare against, recall makes 1.
	want := relational.ModelRunStat{Runs: 1, InputTokens: 10, OutputTokens: 5, CostUSD: 0.01}
	if len(got) != 2 || got["remember"] != want || got["recall"] != want {
		t.Fatalf("got %v", got)
	}
}

type costLLM struct{ outs []string }

func (f *costLLM) Complete(_ context.Context, _ ai.Request) (*ai.Response, error) {
	out := f.outs[0]
	f.outs = f.outs[1:]
	return &ai.Response{Text: out, StopReason: "end_turn", Provider: ai.ProviderAnthropic, Model: "m", InputTokens: 10, OutputTokens: 5, CostUSD: 0.01}, nil
}
