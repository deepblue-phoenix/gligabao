package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("authorization") != "Bearer k" {
			t.Errorf("bad request: %s %v", r.URL.Path, r.Header)
		}
		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "text-embedding-3-small" || len(req.Input) != 2 {
			t.Errorf("bad body: %+v", req)
		}
		// Return out of order to verify sorting.
		w.Write([]byte(`{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()

	e, _ := NewEmbedder("k", WithEmbedBaseURL(srv.URL))
	got, err := e.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil || len(got) != 2 || got[0][0] != 1 || got[1][1] != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestEmbedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"bad key"}}`))
	}))
	defer srv.Close()

	e, _ := NewEmbedder("k", WithEmbedBaseURL(srv.URL))
	if _, err := e.Embed(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCosineSimilarity(t *testing.T) {
	if s := CosineSimilarity([]float32{1, 0}, []float32{1, 0}); s < 0.999 {
		t.Fatalf("got %v", s)
	}
	if s := CosineSimilarity([]float32{1, 0}, []float32{0, 1}); s != 0 {
		t.Fatalf("got %v", s)
	}
}
