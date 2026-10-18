// Package training holds the HTTP handlers of the training API (/v1/gb/training/): starting training jobs on the sidecar,
// listing them, following their progress (the Monitor and its SSE feed), and the training datasets (the subdirectories of
// the training datasets directory that have a manifest, see manifest.go). They are registered by internal/server; the admin UI is
// one of their clients.
package training

import "database/sql"

// Database is a database the handlers can work with (selected by the ?db= parameter, by Name).
type Database struct {
	Name string
	DB   *sql.DB
	// DatasetDir is the training datasets directory of DB: the datasets are its subdirectories with a manifest, and
	// uploads are saved there ("" = DB has no datasets and uploads are refused).
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
