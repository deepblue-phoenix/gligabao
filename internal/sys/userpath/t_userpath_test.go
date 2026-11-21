package userpath

import (
	"errors"
	"strings"
	"testing"
)

func TestSegment(t *testing.T) {
	for user, want := range map[string]string{
		"alice":  "alice",
		".":      "%2E",
		"..":     "%2E%2E",
		"a/b":    "a%2Fb",
		"../x":   "..%2Fx",
		"a b":    "a+b",
		"a%2Fb":  "a%252Fb",
		"é":      "%C3%A9",
		"system": "system",
	} {
		got, err := Segment(user)
		if err != nil || got != want {
			t.Errorf("Segment(%q) = %q, %v; want %q", user, got, err, want)
		}
		if strings.Contains(got, "/") {
			t.Errorf("Segment(%q) = %q contains a slash", user, got)
		}
	}
}

func TestSegmentEmpty(t *testing.T) {
	for _, user := range []string{"", "  ", "\t"} {
		if _, err := Segment(user); !errors.Is(err, ErrEmptyUser) {
			t.Errorf("Segment(%q) err = %v", user, err)
		}
	}
}
