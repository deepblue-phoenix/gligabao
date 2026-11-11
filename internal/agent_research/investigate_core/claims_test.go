package investigate_core

import (
	"errors"
	"testing"
)

func TestDomain(t *testing.T) {
	cases := map[string]string{
		"https://arxiv.org/abs/1":           "arxiv.org",
		"https://www.Example.com/a?b=1#c":   "example.com",
		"https://en.wikipedia.org/wiki/X":   "wikipedia.org",
		"https://docs.python.org:8080/3/":   "python.org",
		"https://news.bbc.co.uk/story":      "bbc.co.uk",
		"https://www.shop.example.com.au/x": "example.com.au",
		"https://u.ac.jp/p":                 "u.ac.jp",
		"http://localhost:3000/x":           "localhost",
		"http://192.168.0.1/x":              "192.168.0.1",
		"http://[::1]:80/x":                 "::1",
		"example.org/path":                  "example.org",
		"https://a.b.c.example.io/":         "example.io",
		"https://sub.example.com./":         "example.com",
		"":                                  "",
		"https://":                          "",
	}
	for in, want := range cases {
		if got := Domain(in); got != want {
			t.Errorf("Domain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddSource(t *testing.T) {
	var sources []Source
	sources, a, err := AddSource(sources, "https://www.arxiv.org/abs/1/", `"T" | x`, "n")
	if err != nil || a.ID != "s_001" || a.Domain != "arxiv.org" || a.Title != "'T' / x" || a.URL != "https://www.arxiv.org/abs/1" {
		t.Fatalf("%v %+v", err, a)
	}
	sources2, b, err := AddSource(sources, "https://www.arxiv.org/abs/1#top", "other", "")
	if err != nil || b.ID != "s_001" || len(sources2) != 1 {
		t.Fatalf("a known URL gives the existing source: %v %+v", err, b)
	}
	for _, bad := range []string{"", "plain text", "file:///etc/passwd", "https://a b.com"} {
		if _, _, err := AddSource(sources, bad, "", ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if len(sources) != 1 {
		t.Fatal("input mutated")
	}
}

func TestSupportReplacesLink(t *testing.T) {
	sources, _, _ := AddSource(nil, "https://a.org", "", "")
	c := Claim{ID: "c_001"}
	c, err := Support(c, "s_001", "", false, sources)
	if err != nil || c.Links[0].Stance != StanceSupports || c.Links[0].Independent {
		t.Fatalf("%v %+v", err, c)
	}
	c, _ = Support(c, "s_001", StanceSupports, true, sources)
	if len(c.Links) != 1 || !c.Links[0].Independent {
		t.Fatalf("%+v", c.Links)
	}
	if _, err := Support(c, "s_009", StanceSupports, true, sources); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := Support(c, "s_001", "meh", true, sources); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
