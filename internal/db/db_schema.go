package db

import "github.com/ivan/gligabao/internal/db/sqlite"

//---------------------------------------------------------------------------------------------

// The schema itself is backend-specific and lives in the backend subpackage (e.g. sqlite/db_schema.go); it is
// applied by Open. Only backend-independent schema constants live here.

// DefaultDataset is the dataset used when a request names none. It is created per user on first remember.
const DefaultDataset = "main"

// SQLNow is the SQL expression for the current time in the storage format of the active backend.
const SQLNow = sqlite.SQLNow
