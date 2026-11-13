package web

import (
	"context"
	"errors"
	"testing"
)

func TestPageText(t *testing.T) {
	got := PageText("<html><style>p{}</style><body><p>Hello</p>  <script>x()</script><b>world</b></body></html>", 100)
	if got != "Hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestHostMatches(t *testing.T) {
	list := []string{"example.com"}
	for host, want := range map[string]bool{"example.com": true, "www.example.com": true, "EXAMPLE.com.": true, "badexample.com": false, "example.com.evil.org": false} {
		if got := hostMatches(host, list); got != want {
			t.Errorf("%s: %v", host, got)
		}
	}
}

func TestProvidersNotImplementedOrUnknown(t *testing.T) {
	ctx := context.Background()
	if _, err := Search(ctx, Keenable, Deps{}, SearchRequest{Query: "q"}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("search keenable: %v", err)
	}
	if _, err := Fetch(ctx, Keenable, Deps{}, FetchRequest{URL: "https://a.example"}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("fetch keenable: %v", err)
	}
	if _, err := Search(ctx, "nope", Deps{}, SearchRequest{}); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := Search(ctx, Anthropic, Deps{}, SearchRequest{Query: "q"}); err == nil {
		t.Error("anthropic without a client must fail")
	}
}
