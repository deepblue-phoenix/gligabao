package relational

import (
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// Timestamp helpers route to the backend in use, which owns the storage convention (sqlite: RFC 3339 text with
// second precision; postgres: TIMESTAMPTZ with microsecond precision).

// Now returns the current time as UTC at the precision of the backend.
func Now() time.Time {
	if backend() == config.DBBackendPostgres {
		return postgres.Now()
	}
	return sqlite.Now()
}

// Normalize converts t to UTC at the precision that survives a storage round trip.
func Normalize(t time.Time) time.Time {
	if backend() == config.DBBackendPostgres {
		return postgres.Normalize(t)
	}
	return sqlite.Normalize(t)
}

// FormatTime formats t for display to the LLM and in the API.
func FormatTime(t time.Time) string {
	if backend() == config.DBBackendPostgres {
		return postgres.FormatTime(t)
	}
	return sqlite.FormatTime(t)
}

// FormatTimePtr formats t like FormatTime; nil becomes nil (NULL).
func FormatTimePtr(t *time.Time) any {
	if backend() == config.DBBackendPostgres {
		return postgres.FormatTimePtr(t)
	}
	return sqlite.FormatTimePtr(t)
}

// ParseTimeStrict parses an RFC 3339 string (for validating API input) and returns it in UTC.
func ParseTimeStrict(s string) (time.Time, error) {
	if backend() == config.DBBackendPostgres {
		return postgres.ParseTimeStrict(s)
	}
	return sqlite.ParseTimeStrict(s)
}

// ParseTime leniently parses an LLM-produced date and returns UTC, or nil if empty/unparseable.
func ParseTime(s string) *time.Time {
	if backend() == config.DBBackendPostgres {
		return postgres.ParseTime(s)
	}
	return sqlite.ParseTime(s)
}
