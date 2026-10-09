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
