package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewPineconeErrors(t *testing.T) {
	if _, err := NewPinecone("", "h"); err == nil {
		t.Error("expected error for missing key")
	}
	if _, err := NewPinecone("k", " "); err == nil {
		t.Error("expected error for missing host")
	}
	p, err := NewPinecone("k", "idx.svc.pinecone.io/")
	if err != nil || p.baseURL != "https://idx.svc.pinecone.io" {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestUpsertBatchesAndHeaders(t *testing.T) {
	var batches []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/vectors/upsert" || r.Header.Get("api-key") != "k" || r.Header.Get("x-pinecone-api-version") == "" {
			t.Errorf("bad request: %s %v", r.URL.Path, r.Header)
		}
		var req pcUpsertRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Namespace != "main" {
			t.Errorf("namespace %q", req.Namespace)
		}
		batches = append(batches, len(req.Vectors))
		w.Write([]byte(`{"upsertedCount":1}`))
	}))
	defer srv.Close()

	p, _ := NewPinecone("k", srv.URL)
	vs := make([]Vector, 250)
	for i := range vs {
		vs[i] = Vector{ID: "id", Values: []float32{1}}
	}
	if err := p.Upsert(context.Background(), "main", vs); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 || batches[0] != 100 || batches[2] != 50 {
		t.Fatalf("batches %v", batches)
	}
	if err := p.Upsert(context.Background(), "main", []Vector{{ID: "", Values: []float32{1}}}); err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req pcQueryRequest
		json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/query" || req.TopK != 10 || !req.IncludeMetadata || req.Filter["type"] == nil {
			t.Errorf("bad request: %s %+v", r.URL.Path, req)
		}
		w.Write([]byte(`{"matches":[{"id":"a","score":0.9,"metadata":{"type":"person"}},{"id":"b","score":0.5}]}`))
	}))
	defer srv.Close()

	p, _ := NewPinecone("k", srv.URL)
	got, err := p.Query(context.Background(), "", []float32{1, 0}, QueryOptions{Filter: map[string]any{"type": "person"}})
	if err != nil || len(got) != 2 || got[0].ID != "a" || got[0].Score != 0.9 || got[0].Metadata["type"] != "person" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestDelete(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`{"code":5,"message":"Namespace not found"}`))
	}))
	defer srv.Close()

	p, _ := NewPinecone("k", srv.URL)
	if err := p.Delete(context.Background(), "n", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	status = http.StatusNotFound
	if err := p.DeleteAll(context.Background(), "n"); err != nil {
		t.Fatalf("missing namespace should be ignored: %v", err)
	}
	if err := p.Delete(context.Background(), "n", []string{"a"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"bad key"}}`))
	}))
	defer srv.Close()

	p, _ := NewPinecone("k", srv.URL)
	_, err := p.Query(context.Background(), "", []float32{1}, QueryOptions{})
	if err == nil || err.Error() != "vector: pinecone 401: bad key" {
		t.Fatalf("got %v", err)
	}
}
