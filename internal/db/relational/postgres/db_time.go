package postgres

import (
	"strings"
	"time"
)

// PostgreSQL has a real datetime type, so timestamps are stored in TIMESTAMPTZ columns (microsecond precision) and
// passed to and from the driver as time.Time. The string helpers below are for API input and for showing times to
// the LLM; they use RFC 3339 UTC like the other backends.

// SQLNow is the SQL expression for the current time.
const SQLNow = `now()`

// Now returns the current time as UTC with the precision that survives a storage round trip.
func Now() time.Time { return Normalize(time.Now()) }

// Normalize converts t to UTC with microsecond precision, the precision of TIMESTAMPTZ.
func Normalize(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// FormatTime formats t as RFC 3339 UTC (for display to the LLM).
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// FormatTimePtr formats t as RFC 3339 UTC; nil stays nil (NULL).
func FormatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return FormatTime(*t)
}

// ParseTimeStrict parses an RFC 3339 string (for validating API input) and returns it in UTC.
func ParseTimeStrict(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	return t.UTC(), err
}

// ParseTime leniently parses an LLM-produced date and returns UTC, or nil if empty/unparseable.
// Besides RFC 3339 it accepts partial dates (2025-01-31, 2025-01).
func ParseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "null") {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02", "2006-01"} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}
