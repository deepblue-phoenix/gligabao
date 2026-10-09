package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivan/gligabao/internal/ai"
	"github.com/ivan/gligabao/internal/db"
)

func TestLLMCallsRecorded(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	llm := &costLLM{outs: []string{
		`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`,
		`{"answer":"Alice.","entities":[1],"edges":[]}`,
	}}
	mux := http.NewServeMux()
	Register(mux, d, llm)
	for _, c := range [][2]string{{"/v1/gb/remember", `{"user_id":"u1","text":"Alice"}`}, {"/v1/gb/recall", `{"user_id":"u1","query":"Alice?"}`}} { // order matters: recall needs the graph
		path, body := c[0], c[1]
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	rows, err := d.Query(`SELECT type, COUNT(*), SUM(input_tokens), SUM(output_tokens), SUM(cost_usd) FROM gb_llm_calls GROUP BY type ORDER BY type`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][4]float64{}
	for rows.Next() {
		var typ string
		var v [4]float64
		if err := rows.Scan(&typ, &v[0], &v[1], &v[2], &v[3]); err != nil {
			t.Fatal(err)
		}
		got[typ] = v
	}
	// remember makes 1 LLM call when there is nothing stored to compare against, recall makes 1.
	if got["remember"] != [4]float64{1, 10, 5, 0.01} || got["recall"] != [4]float64{1, 10, 5, 0.01} {
		t.Fatalf("got %v", got)
	}
}

type costLLM struct{ outs []string }

func (f *costLLM) Complete(_ context.Context, _ ai.Request) (*ai.Response, error) {
	out := f.outs[0]
	f.outs = f.outs[1:]
	return &ai.Response{Text: out, StopReason: "end_turn", Provider: ai.ProviderAnthropic, Model: "m", InputTokens: 10, OutputTokens: 5, CostUSD: 0.01}, nil
}
