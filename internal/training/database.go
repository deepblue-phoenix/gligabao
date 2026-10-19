// Package training is the training subsystem, without HTTP: the training datasets (the subdirectories of the training datasets
// directory that have a manifest, see manifest.go), starting and stopping training jobs on the sidecar, following their progress
// (the Monitor), inferences with trained models, and the evals and audits of trained models (RunManager). The HTTP handlers of the
// training API (/v1/gb/training/) in internal/api/training are thin wrappers around it.
package training

import "database/sql"

// Database is a database the training can work with (selected by the ?db= parameter, by Name).
type Database struct {
	Name string
	DB   *sql.DB
	// DatasetDir is the training datasets directory of DB: the datasets are its subdirectories with a manifest, and
	// uploads are saved there ("" = DB has no datasets and uploads are refused).
	DatasetDir string
}

// Pick returns the database called name (the first one if name is empty).
func Pick(dbs []Database, name string) (Database, bool) {
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
