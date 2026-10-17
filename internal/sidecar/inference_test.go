package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInfer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/infer/query" || in["prompt"] != "hi" || in["model_size"] != "12b" || in["model_path"] != "trained/j1" || in["temperature"] != 0.0 {
			t.Errorf("request: %s %v", r.URL.Path, in)
		}
		w.Write([]byte(`{"response":"hello","model_size":"12b","model_path":"trained/j1","adapter":"/models/trained/j1"}`))
	}))
	defer srv.Close()
	zero := 0.0
	out, err := New(srv.URL).Infer(context.Background(), InferRequest{Prompt: "hi", ModelSize: "12b", ModelPath: "trained/j1", Temperature: &zero})
	if err != nil || out.Response != "hello" || out.ModelPath != "trained/j1" {
		t.Fatalf("got %+v, %v", out, err)
	}
}

func TestInferErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"error":"No trained model at 'trained/x'"}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL).Infer(context.Background(), InferRequest{Prompt: "hi", ModelPath: "trained/x"}); err == nil {
		t.Fatal("expected the sidecar's error")
	}
	if _, err := New(srv.URL).Infer(context.Background(), InferRequest{}); err == nil {
		t.Fatal("expected error for an empty prompt")
	}
}
