package memory_remember

import (
	"testing"
	"time"
)

func TestFixSpan(t *testing.T) {
	at := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return &v
	}
	tests := []struct {
		name         string
		valid, inval *time.Time
		wantValid    *time.Time
		wantInvalid  *time.Time
	}{
		{"no end", at("2024-09-10T00:00:00Z"), nil, at("2024-09-10T00:00:00Z"), nil},
		{"normal span", at("2023-01-01T00:00:00Z"), at("2024-06-01T00:00:00Z"), at("2023-01-01T00:00:00Z"), at("2024-06-01T00:00:00Z")},
		{"empty span is kept", at("2024-08-01T00:00:00Z"), at("2024-08-01T00:00:00Z"), at("2024-08-01T00:00:00Z"), at("2024-08-01T00:00:00Z")},
		{"start after end: end is kept", at("2024-09-10T00:00:00Z"), at("2024-06-01T00:00:00Z"), at("2024-06-01T00:00:00Z"), at("2024-06-01T00:00:00Z")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := Edge{ValidAt: tc.valid, InvalidAt: tc.inval}
			fixSpan(&e)
			if !e.ValidAt.Equal(*tc.wantValid) {
				t.Errorf("ValidAt = %v, want %v", e.ValidAt, tc.wantValid)
			}
			if (e.InvalidAt == nil) != (tc.wantInvalid == nil) || (e.InvalidAt != nil && !e.InvalidAt.Equal(*tc.wantInvalid)) {
				t.Errorf("InvalidAt = %v, want %v", e.InvalidAt, tc.wantInvalid)
			}
		})
	}
}
