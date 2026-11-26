package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClassifyLanguage(t *testing.T) {
	for _, c := range []struct {
		name, answer, want string
		fails              bool
	}{
		{"go", `{"type":"choice","choice":"go","confidence":0.9}`, LangGo, false},
		{"unsupported", `{"type":"choice","choice":"unsupported"}`, Unsupported, false},
		{"unknown class", `{"type":"choice","choice":"rust"}`, "", true},
		{"wrong type", `{"type":"noul","noul":0.5}`, "", true},
	} {
		var sent string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
			r *http.Request) {
			var req struct {
				State     string              `json:"state"`
				Questions map[string]Question `json:"questions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Questions["language"].Type != "choice" {
				t.Errorf("bad request %+v, %v", req, err)
			}
			sent = req.State
			w.Write([]byte(`{"model":"jev-1","answers":{"language":` + c.answer + `},"usage":{"input_tokens":9,"output_tokens":1}}`))
		}))
		cl, _ := New("k", WithBaseURL(srv.URL))
		got, call, err := cl.ClassifyLanguage(context.Background(), strings.Repeat("x\n", 100))
		srv.Close()
		if (err != nil) != c.fails {
			t.Fatalf("%s: err=%v", c.name, err)
		}
		if n := strings.Count(sent, "\n"); n != MaxLanguageLines {
			t.Errorf("%s: sent %d lines, want %d", c.name, n, MaxLanguageLines)
		}
		if call.Latency <= 0 {
			t.Errorf("%s: no latency", c.name)
		}
		if !c.fails && (got.Language != c.want || got.InputTokens != 9) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestSnippetCapsLongLines(t *testing.T) {
	if got := snippet(strings.Repeat("x", 10000)); len(got) != MaxLanguageBytes {
		t.Errorf("got %d bytes, want %d", len(got), MaxLanguageBytes)
	}
	if got := snippet("a\nb"); got != "a\nb" {
		t.Errorf("short code changed: %q", got)
	}
}
