package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "k" {
			t.Errorf("bad request: %s %v", r.URL.Path, r.Header)
		}
		w.Write([]byte(`{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":2}}`))
	}))
	defer srv.Close()

	c, _ := New("k", WithBaseURL(srv.URL))
	got, err := c.Ask(context.Background(), "yo")
	if err != nil || got != "hi" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"type":"authentication_error","message":"bad key"}}`))
	}))
	defer srv.Close()

	c, _ := New("k", WithBaseURL(srv.URL))
	if _, err := c.Ask(context.Background(), "yo"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1000000,"output_tokens":100000,"cache_read_input_tokens":1000000}}`))
	}))
	defer srv.Close()

	c, _ := New("k", WithBaseURL(srv.URL))
	r, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "yo"}}})
	if err != nil {
		t.Fatal(err)
	}
	// 1M*$1 + 0.1M*$5 + 1M*$0.1 = 1.6
	if r.CostUSD < 1.5999 || r.CostUSD > 1.6001 || r.InputTokens != 2000000 || r.OutputTokens != 100000 || r.Provider != ProviderAnthropic {
		t.Fatalf("bad response: %+v", r)
	}
	if _, ok := NewPriceTable(nil).Cost("unknown-model", Usage{InputTokens: 1}); ok {
		t.Fatal("unknown model should have no price")
	}
}

func TestCompleteImage(t *testing.T) {
	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content json.RawMessage
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	c, _ := New("k", WithBaseURL(srv.URL))
	_, err := c.Complete(context.Background(), Request{Messages: []Message{
		{Role: RoleUser, Content: "plain"},
		{Role: RoleUser, Content: "look", Images: []Image{{MediaType: "image/png", Data: []byte("abc")}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || string(got.Messages[0].Content) != `"plain"` {
		t.Fatalf("plain message: %+v", got.Messages)
	}
	want := `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YWJj"}},{"type":"text","text":"look"}]`
	if string(got.Messages[1].Content) != want {
		t.Fatalf("image message content = %s, want %s", got.Messages[1].Content, want)
	}
}

func TestWebSearch(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if len(bodies) == 1 { // the first turn is paused after one search
			w.Write([]byte(`{"model":"claude-haiku-4-5-20251001","stop_reason":"pause_turn","content":[
				{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"acme news"}},
				{"type":"web_search_tool_result","tool_use_id":"s1","content":[
					{"type":"web_search_result","url":"https://a.example/1","title":"A1","page_age":"May 1, 2025","encrypted_content":"x"},
					{"type":"web_search_result","url":"https://a.example/2","title":"A2","encrypted_content":"y"}]}],
				"usage":{"input_tokens":1000000,"output_tokens":0,"server_tool_use":{"web_search_requests":1}}}`))
			return
		}
		w.Write([]byte(`{"model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","content":[
			{"type":"server_tool_use","id":"s2","name":"web_search","input":{"query":"acme ceo"}},
			{"type":"web_search_tool_result","tool_use_id":"s2","content":{"type":"web_search_tool_result_error","error_code":"unavailable"}},
			{"type":"text","text":"Acme opened a plant. ","citations":[{"type":"web_search_result_location","url":"https://a.example/1","title":"","cited_text":"..."}]},
			{"type":"text","text":"Its CEO changed.","citations":[{"type":"web_search_result_location","url":"https://a.example/1","title":"A1"}]}],
			"usage":{"input_tokens":0,"output_tokens":100000,"server_tool_use":{"web_search_requests":1}}}`))
	}))
	defer srv.Close()

	c, _ := New("k", WithBaseURL(srv.URL))
	got, err := c.WebSearch(context.Background(), WebSearchRequest{System: "sys", Prompt: "news on Acme", MaxUses: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d requests, want 2 (the paused turn is resumed)", len(bodies))
	}
	tool := bodies[0]["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "web_search_20250305" || tool["max_uses"].(float64) != 2 || bodies[0]["system"] != "sys" {
		t.Errorf("request = %v", bodies[0])
	}
	msgs := bodies[1]["messages"].([]any)
	if len(msgs) != 2 || msgs[1].(map[string]any)["role"] != "assistant" {
		t.Errorf("resumed messages = %v", msgs)
	}
	if got.Text != "Acme opened a plant. Its CEO changed." || got.Searches != 2 || len(got.Queries) != 2 || got.Queries[1] != "acme ceo" {
		t.Errorf("got %+v", got)
	}
	if len(got.Sources) != 1 || got.Sources[0].URL != "https://a.example/1" || got.Sources[0].Title != "A1" || got.Sources[0].PageAge != "May 1, 2025" {
		t.Errorf("sources = %+v", got.Sources)
	}
	if len(got.SearchErrors) != 1 || got.SearchErrors[0] != "unavailable" {
		t.Errorf("search errors = %v", got.SearchErrors)
	}
	// 1M*$1 + 0.1M*$5 + 2 searches * $0.01 = 1.52
	if got.CostUSD < 1.5199 || got.CostUSD > 1.5201 || got.InputTokens != 1000000 || got.OutputTokens != 100000 {
		t.Errorf("usage = %+v", got)
	}
	if _, err := c.WebSearch(context.Background(), WebSearchRequest{}); err == nil {
		t.Error("an empty prompt must fail")
	}
}
