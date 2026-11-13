package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// keenableSearchReply is the 200 response of the search, with a usage object when the call was keyed.
const keenableSearchReply = `{
  "query": "q",
  "mode": "pro",
  "results": [
    {"title": "N", "url": "https://a.example/p", "description": "", "snippet": "S", "published_at": "2026-10-30T00:00:00Z", "acquired_at": "2026-11-03T08:57:22Z"}
  ],
  "usage": {"sku": "search.pro", "amount": 1, "credits": 2, "paid": true, "credits_remaining": 99120}
}`

func TestSearchKeenableKeyless(t *testing.T) {
	var got keenableSearchBody
	var path, title, apiKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		path, title, apiKey = r.URL.Path, r.Header.Get("X-Keenable-Title"), r.Header.Get("X-Api-Key")
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"query":"q","mode":"pro","results":[{"title":"N","url":"https://a.example/p","snippet":"S","published_at":"2026-10-30T00:00:00Z"}]}`))
	}))
	defer srv.Close()
	r, err := Search(context.Background(),
		Keenable,
		Deps{KeenableBaseURL: srv.URL},
		SearchRequest{
			Query:          "q",
			AllowedDomains: []string{"a.example"},
			BlockedDomains: []string{"b.example", "c.example"},
		})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/search/public" || title != keenableTitle || apiKey != "" {
		t.Errorf("request: path %q title %q key %q", path, title, apiKey)
	}
	if got.Query != "q" || got.Site != "a.example" || !slices.Equal(got.ExcludeDomains, []string{"b.example", "c.example"}) ||
		got.SnippetMaxLength != keenableSnippetMaxLength || !got.DisableCards {
		t.Errorf("body: %+v", got)
	}
	if r.Provider != Keenable || r.Answer != "" || r.Searches != 1 || r.CostUSD != 0 || len(r.Results) != 1 {
		t.Fatalf("response: %+v", r)
	}
	res := r.Results[0]
	if res.URL != "https://a.example/p" || res.Title != "N" || res.Snippet != "S" || res.PageAge != "2026-10-30T00:00:00Z" {
		t.Errorf("result: %+v", res)
	}
}

func TestSearchKeenableKeyed(t *testing.T) {
	var path, apiKey, title string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		path, apiKey, title = r.URL.Path, r.Header.Get("X-Api-Key"), r.Header.Get("X-Keenable-Title")
		w.Write([]byte(keenableSearchReply))
	}))
	defer srv.Close()
	r, err := Search(context.Background(),
		Keenable,
		Deps{KeenableKey: "keen_k", KeenableBaseURL: srv.URL},
		SearchRequest{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/search" || apiKey != "keen_k" || title != "" {
		t.Errorf("request: path %q key %q title %q", path, apiKey, title)
	}
	// Two paid credits at the list price of 4 US dollars per 1,000.
	if r.CostUSD != 2*keenableCreditUSD {
		t.Errorf("cost: %v", r.CostUSD)
	}
}

func TestKeenableCostFreeAllowance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		w.Write([]byte(`{"query":"q","mode":"pro","results":[],"usage":{"sku":"search.pro","amount":1,"credits":1,"paid":false}}`))
	}))
	defer srv.Close()
	r, err := Search(context.Background(),
		Keenable,
		Deps{KeenableKey: "keen_k", KeenableBaseURL: srv.URL},
		SearchRequest{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if r.CostUSD != 0 {
		t.Errorf("the free allowance costs no dollars: %v", r.CostUSD)
	}
}

func TestKeenableScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		allowed   []string
		wantSite  string
		wantQuery string
	}{
		{"none", nil, "", "q"},
		{"one", []string{"a.example"}, "a.example", "q"},
		{"path", []string{"github.com/microsoft"}, "github.com/microsoft", "q"},
		{"several", []string{"a.example", "b.example"}, "", "q (site:a.example OR site:b.example)"},
	} {
		site, query, err := keenableScope("q", tc.allowed)
		if err != nil || site != tc.wantSite || query != tc.wantQuery {
			t.Errorf("%s: site %q query %q err %v", tc.name, site, query, err)
		}
	}
	for _, bad := range []string{"", "a example", `a"b`, "a(b)"} {
		if _, _, err := keenableScope("q", []string{bad}); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestSearchKeenableErrors(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		http.Error(w, `{"error":"Invalid parameter","message":"max_results"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	d := Deps{KeenableBaseURL: srv.URL}
	if _, err := Search(ctx, Keenable, d, SearchRequest{Query: "  "}); err == nil || !strings.Contains(err.Error(), "empty query") {
		t.Errorf("empty query: %v", err)
	}
	if _, err := Search(ctx, Keenable, d, SearchRequest{Query: "q", AllowedDomains: []string{"a b"}}); err == nil {
		t.Error("bad allowed domain must fail")
	}
	if _, err := Search(ctx, Keenable, d, SearchRequest{Query: "q", BlockedDomains: make([]string, maxKeenableDomains+1)}); err == nil {
		t.Error("too many blocked domains must fail, not be cut")
	}
	if _, err := Search(ctx, Keenable, d, SearchRequest{Query: "q"}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("status: %v", err)
	}
}

func TestFetchKeenableKeyless(t *testing.T) {
	var path, title, apiKey, pageURL, maxChars string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		path, title, apiKey = r.URL.Path, r.Header.Get("X-Keenable-Title"), r.Header.Get("X-Api-Key")
		pageURL, maxChars = r.URL.Query().Get("url"), r.URL.Query().Get("max_chars")
		w.Write([]byte(`{"url":"https://a.example/final","title":"T","content":"  # Hello world  ","description":""}`))
	}))
	defer srv.Close()
	r, err := Fetch(context.Background(),
		Keenable,
		Deps{KeenableBaseURL: srv.URL},
		FetchRequest{URL: "https://a.example/p?x=1&y=2", MaxChars: 7})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/fetch/public" || title != keenableTitle || apiKey != "" {
		t.Errorf("request: path %q title %q key %q", path, title, apiKey)
	}
	if pageURL != "https://a.example/p?x=1&y=2" || maxChars != "7" {
		t.Errorf("query: url %q max_chars %q", pageURL, maxChars)
	}
	if r.Provider != Keenable || r.URL != "https://a.example/final" || r.Text != "# Hello" || r.CostUSD != 0 {
		t.Errorf("response: %+v", r)
	}
}

func TestFetchKeenableKeyed(t *testing.T) {
	var path, apiKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		path, apiKey = r.URL.Path, r.Header.Get("X-Api-Key")
		w.Write([]byte(`{"url":"https://a.example/p","content":"x","usage":{"sku":"fetch","amount":1,"credits":1,"paid":true}}`))
	}))
	defer srv.Close()
	r, err := Fetch(context.Background(),
		Keenable,
		Deps{KeenableKey: "keen_k", KeenableBaseURL: srv.URL},
		FetchRequest{URL: "https://a.example/p"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/fetch" || apiKey != "keen_k" || r.CostUSD != keenableCreditUSD {
		t.Errorf("path %q key %q cost %v", path, apiKey, r.CostUSD)
	}
}

func TestFetchKeenableDomains(t *testing.T) {
	ctx := context.Background()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		calls++
		// The page redirected to a host on the blocked list.
		w.Write([]byte(`{"url":"https://evil.example/landing","content":"secret"}`))
	}))
	defer srv.Close()
	d := Deps{KeenableBaseURL: srv.URL}
	if _, err := Fetch(ctx, Keenable, d, FetchRequest{URL: "https://evil.example/p", BlockedDomains: []string{"evil.example"}}); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("requested host blocked: %v", err)
	}
	if calls != 0 {
		t.Errorf("a blocked host must not reach Keenable (%d calls)", calls)
	}
	if _, err := Fetch(ctx, Keenable, d, FetchRequest{URL: "https://a.example/p", BlockedDomains: []string{"evil.example"}}); err == nil || !strings.Contains(err.Error(), "redirected") {
		t.Errorf("final host blocked: %v", err)
	}
	if _, err := Fetch(ctx, Keenable, d, FetchRequest{URL: "https://a.example/p", AllowedDomains: []string{"a.example"}}); err == nil {
		t.Errorf("a final host outside the allowed list must fail")
	}
	if _, err := Fetch(ctx, Keenable, d, FetchRequest{URL: "example.com"}); err == nil {
		t.Error("a url without a scheme must fail")
	}
}

func TestFetchKeenableErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		http.Error(w, `{"error":"Not found","message":"The page does not exist"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), Keenable, Deps{KeenableBaseURL: srv.URL}, FetchRequest{URL: "https://a.example/missing"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("status: %v", err)
	}
}
