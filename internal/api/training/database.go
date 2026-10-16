// Package training holds the HTTP handlers of the training API (/v1/gb/training/): starting training jobs on the sidecar,
// listing them, following their progress (the Monitor and its SSE feed), and the training datasets, both the JSONL files
// uploaded to the server and the ones on the sidecar. They are registered by internal/server; the admin UI is one of
// their clients.
package training

import "database/sql"

// Database is a database the handlers can work with (selected by the ?db= parameter, by Name).
type Database struct {
	Name string
	DB   *sql.DB
	// DatasetDir is the directory the JSONL training datasets uploaded to DB are saved in ("" = uploads are refused).
	DatasetDir string
}

// pick returns the database called name (the first one if name is empty).
func pick(dbs []Database, name string) (Database, bool) {
	if name == "" && len(dbs) > 0 {
		return dbs[0], true
	}
	for _, n := range dbs {
		if n.Name == name {
			return n, true
		}
	}
	return Database{}, false
}
