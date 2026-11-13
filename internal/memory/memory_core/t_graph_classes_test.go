package memory_core

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeGraphClass(t *testing.T) {
	for in, want := range map[string]string{
		"Corporate Ownership":   "corporate_ownership",
		"  supply-chain!! ":     "supply_chain",
		"__a__b__":              "a_b",
		"ÉCOLE du Soir":         "école_du_soir",
		"!!!":                   "",
		"":                      "",
		strings.Repeat("a", 80): strings.Repeat("a", MaxGraphClassLen),
	} {
		if got := NormalizeGraphClass(in); got != want {
			t.Errorf("NormalizeGraphClass(%q) = %q, want %q",
				in,
				got,
				want)
		}
	}
}

func TestResolveGraphClasses(t *testing.T) {
	got, err := ResolveGraphClasses([]string{"Staff", "staff", "Supply Chain"})
	if err != nil || strings.Join(got, ",") != "staff,supply_chain" {
		t.Fatalf("got %v, %v",
			got,
			err)
	}
	if got, err := ResolveGraphClasses(nil); got != nil || err != nil {
		t.Fatalf("nil: %v, %v",
			got,
			err)
	}
	if _, err := ResolveGraphClasses([]string{"ok", "--"}); !errors.Is(err, ErrInvalidGraphClasses) {
		t.Fatalf("empty class: %v", err)
	}
	many := make([]string, MaxGraphClasses+1)
	for i := range many {
		many[i] = "c" + strings.Repeat("x", i)
	}
	if _, err := ResolveGraphClasses(many); !errors.Is(err, ErrInvalidGraphClasses) {
		t.Fatalf("too many: %v", err)
	}
}
