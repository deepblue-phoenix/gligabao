package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchLinkup(t *testing.T) {
	var got linkupFetchBody
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"markdown":"# Title\n\nHello world"}`))
	}))
	defer srv.Close()
	r, err := Fetch(context.Background(),
		Linkup,
		Deps{LinkupKey: "k", LinkupBaseURL: srv.URL},
		FetchRequest{URL: "https://a.example/p", MaxChars: 7})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer k" || path != "/v1/fetch" || got.URL != "https://a.example/p" || got.Mode != "standard" || !got.RenderJS {
		t.Errorf("request: %q %q %+v", auth, path, got)
	}
	if r.Text != "# Title" || r.Provider != Linkup || r.CostUSD != linkupFetchCostUSD {
		t.Errorf("response: %+v", r)
	}
}

func TestFetchLinkupErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Fetch(ctx, Linkup, Deps{}, FetchRequest{URL: "https://a.example"}); err == nil || !strings.Contains(err.Error(), "LINKUP_API_KEY") {
		t.Errorf("no key: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		http.Error(w, "bad url", http.StatusBadRequest)
	}))
	defer srv.Close()
	d := Deps{LinkupKey: "k", LinkupBaseURL: srv.URL}
	if _, err := Fetch(ctx, Linkup, d, FetchRequest{URL: "https://a.example"}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("status: %v", err)
	}
	if _, err := Fetch(ctx, Linkup, d, FetchRequest{URL: "https://a.example", BlockedDomains: []string{"a.example"}}); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("blocked: %v", err)
	}
	if _, err := Fetch(ctx, Linkup, d, FetchRequest{URL: "ftp://a.example"}); err == nil {
		t.Error("scheme must fail")
	}
}

func TestSearchLinkup(t *testing.T) {
	var got linkupSearchBody
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"answer":"42","sources":[{"name":"N","url":"https://a.example","snippet":"S"}]}`))
	}))
	defer srv.Close()
	r, err := Search(context.Background(),
		Linkup,
		Deps{LinkupKey: "k", LinkupBaseURL: srv.URL},
		SearchRequest{Query: "q", AllowedDomains: []string{"a.example"}, BlockedDomains: []string{"b.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer k" || path != "/v1/search" || got.Q != "q" || got.Depth != "standard" || got.OutputType != "sourcedAnswer" ||
		len(got.IncludeDomains) != 1 || got.IncludeDomains[0] != "a.example" || got.ExcludeDomains[0] != "b.example" {
		t.Errorf("request: %q %q %+v", auth, path, got)
	}
	if r.Answer != "42" || len(r.Results) != 1 || r.Results[0].Snippet != "S" || r.Results[0].Title != "N" || r.CostUSD != linkupSearchCostUSD {
		t.Errorf("response: %+v", r)
	}
	if _, err := Search(context.Background(), Linkup, Deps{LinkupKey: "k"}, SearchRequest{}); err == nil {
		t.Error("empty query must fail")
	}
}
