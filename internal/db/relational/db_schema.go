package relational

//---------------------------------------------------------------------------------------------

// The schema itself is backend-specific and lives in the backend subpackage (e.g. sqlite/schema.sql); it is
// applied by Open. Only backend-independent schema constants live here.

// DefaultDataset is the dataset used when a request names none. It is created per user on first remember.
const DefaultDataset = "main"
