package sidecar

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadTrainingDataset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/datasets/upload" {
			http.NotFound(w, r)
			return
		}
		var in struct{ Filename, Content string }
		json.NewDecoder(r.Body).Decode(&in)
		raw, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil || in.Filename != "a.jsonl" || string(raw) != "{}\n" {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"status":"success","message":"ok","size_bytes":3}`))
	}))
	defer srv.Close()

	out, err := New(srv.URL).UploadTrainingDataset(context.Background(), "a.jsonl", []byte("{}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out.SizeBytes != 3 {
		t.Fatalf("size_bytes = %d", out.SizeBytes)
	}
	if _, err := New(srv.URL).UploadTrainingDataset(context.Background(), "", nil); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestListTrainingDatasets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/datasets/list" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"datasets":[{"filename":"a.jsonl","size_bytes":3,"uploaded_at":null,"modified_at":"2025-01-02T03:04:05Z"}]}`))
	}))
	defer srv.Close()

	ds, err := New(srv.URL).ListTrainingDatasets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].Filename != "a.jsonl" || *ds[0].SizeBytes != 3 {
		t.Fatalf("got %+v", ds)
	}
}
