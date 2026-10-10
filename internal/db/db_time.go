package db

import (
	"time"

	"github.com/ivan/gligabao/internal/db/sqlite"
)

// Timestamp helpers delegate to the backend subpackage, which owns the storage format.
// Only sqlite exists so far; when postgres is added, these will dispatch on the selected backend.

// Now returns the current time as UTC with second precision.
func Now() time.Time { return sqlite.Now() }

// Normalize converts t to UTC with second precision, the precision that survives a storage round trip.
func Normalize(t time.Time) time.Time { return sqlite.Normalize(t) }

// FormatTime formats t for storage (and for display to the LLM).
func FormatTime(t time.Time) string { return sqlite.FormatTime(t) }

// FormatTimePtr formats t for storage; nil becomes NULL.
func FormatTimePtr(t *time.Time) any { return sqlite.FormatTimePtr(t) }

// ParseTimeStrict parses an RFC 3339 string (for validating API input) and returns it in UTC.
func ParseTimeStrict(s string) (time.Time, error) { return sqlite.ParseTimeStrict(s) }

// ParseTime leniently parses a stored value or LLM-produced date and returns UTC, or nil if empty/unparseable.
func ParseTime(s string) *time.Time { return sqlite.ParseTime(s) }
