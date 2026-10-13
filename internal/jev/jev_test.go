package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// server answers the classify question with the given raw "complexity" answer.
func server(t *testing.T, answer string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("authorization") != "Bearer k" {
			t.Errorf("unexpected request %s auth=%q", r.URL.Path, r.Header.Get("authorization"))
		}
		var req struct {
			State     string              `json:"state"`
			Model     string              `json:"model"`
			Questions map[string]Question `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		q, ok := req.Questions["complexity"]
		if req.Model != "jev-latest" || req.State != "Alice works at Acme." || !ok || q.Type != "choice" {
			t.Errorf("bad request %+v", req)
		}
		w.Write([]byte(`{"model":"jev-1.13.0","answers":{"complexity":` + answer + `},"usage":{"input_tokens":30,"output_tokens":5}}`))
	}))
}

func TestClassify(t *testing.T) {
	srv := server(t, `{"type":"choice","choice":"medium","probabilities":{"simple":0.2,"medium":0.7,"complex":0.1},"confidence":0.64}`)
	defer srv.Close()
	c, err := New("k", WithBaseURL(srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	cl, call, err := c.Classify(context.Background(), "Alice works at Acme.")
	if err != nil {
		t.Fatal(err)
	}
	if cl.Complexity != Medium || cl.Confidence != 0.64 || cl.InputTokens != 30 || cl.OutputTokens != 5 || cl.Model != "jev-1.13.0" || cl.Probabilities["medium"] != 0.7 {
		t.Errorf("classification = %+v", cl)
	}
	if call.Started.IsZero() || call.Latency <= 0 {
		t.Errorf("call = %+v: latency not measured", call)
	}
}

func TestClassifyBadAnswers(t *testing.T) {
	for name, answer := range map[string]string{
		"unknown option": `{"type":"choice","choice":"huge","confidence":0.9}`,
		"wrong type":     `{"type":"noul","noul":0.5}`,
	} {
		srv := server(t, answer)
		c, _ := New("k", WithBaseURL(srv.URL))
		if _, _, err := c.Classify(context.Background(), "Alice works at Acme."); err == nil {
			t.Errorf("%s: want an error", name)
		}
		srv.Close()
	}
}

func TestConfidenceMissingOrOutOfRange(t *testing.T) {
	for _, answer := range []string{`{"type":"choice","choice":"simple"}`, `{"type":"choice","choice":"simple","confidence":7}`} {
		srv := server(t, answer)
		c, _ := New("k", WithBaseURL(srv.URL))
		cl, _, err := c.Classify(context.Background(), "Alice works at Acme.")
		if err != nil || cl.Confidence != -1 {
			t.Errorf("%s: %+v, %v; want confidence -1", answer, cl, err)
		}
		srv.Close()
	}
}

func TestNewRequiresKey(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("missing key")
	}
	c, _ := New("k")
	if c.baseURL != DefaultBaseURL || c.model != DefaultModel {
		t.Errorf("defaults: %s %s", c.baseURL, c.model)
	}
}

func TestErrorStillTimed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"bad question"}`, 422)
	}))
	defer srv.Close()
	c, _ := New("k", WithBaseURL(srv.URL))
	_, call, err := c.Classify(context.Background(), "x")
	if err == nil || call.Latency <= 0 {
		t.Errorf("err=%v call=%+v", err, call)
	}
}

func TestCutKeepsRunes(t *testing.T) {
	s, tr := cut("aé", 2) // é is 2 bytes starting at 1
	if !tr || s != "a" {
		t.Errorf("cut = %q, %v", s, tr)
	}
}
