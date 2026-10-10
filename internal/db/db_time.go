package db

import (
	"strings"
	"time"
)

// SQLite has no real datetime type, so every timestamp is stored as an RFC 3339 UTC string with second precision
// (e.g. 2025-01-31T09:00:00Z) in a column declared DATETIME. That format sorts and compares correctly as text.
// All Go code that creates, formats or parses such a value goes through the helpers below, so the convention
// lives in exactly one place.

// SQLNow is the SQL expression for the current time in the storage format. Use it in DEFAULTs and statements
// instead of CURRENT_TIMESTAMP, which yields a different format ("2006-01-02 15:04:05").
const SQLNow = `strftime('%Y-%m-%dT%H:%M:%SZ', 'now')`

// Now returns the current time as UTC with second precision.
func Now() time.Time { return Normalize(time.Now()) }

// Normalize converts t to UTC with second precision, the precision that survives a storage round trip.
func Normalize(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// FormatTime formats t for storage (and for display to the LLM).
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// FormatTimePtr formats t for storage; nil becomes NULL.
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

// ParseTime leniently parses a stored value or LLM-produced date and returns UTC, or nil if empty/unparseable.
// Besides RFC 3339 it accepts partial dates and the old "2006-01-02 15:04:05" format of CURRENT_TIMESTAMP.
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
