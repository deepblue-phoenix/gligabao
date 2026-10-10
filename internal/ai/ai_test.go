package ai

import (
	"context"
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
