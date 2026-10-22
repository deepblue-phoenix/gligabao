package sidecar

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadModelFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/models/file" || q.Get("model_path") != "trained/j1" {
			t.Errorf("request: %s", r.URL)
		}
		switch q.Get("file") {
		case "w.safetensors":
			w.Write([]byte("weights"))
		case "missing":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no file 'missing'"}`))
		default:
			t.Errorf("unexpected file %q", q.Get("file"))
		}
	}))
	defer srv.Close()
	c := New(srv.URL)

	var buf bytes.Buffer
	n, err := c.DownloadModelFile(context.Background(), "trained/j1", "w.safetensors", &buf)
	if err != nil || n != 7 || buf.String() != "weights" {
		t.Fatalf("got %d %q %v", n, buf.String(), err)
	}

	_, err = c.DownloadModelFile(context.Background(), "trained/j1", "missing", &buf)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound || se.Message() != "no file 'missing'" {
		t.Fatalf("missing file: %v", err)
	}
}
