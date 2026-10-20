// Package db is the storage of the training audits data model (package core), one subpackage per database: sqlite and postgres. Each
// holds the schema and the functions that write and read the records of its tables: create, update, finish, get and list. The two
// have the same functions (and a Store that holds them, see db.Store), so they can be swapped.
//
// The functions take an Execer, which is a *sql.DB or a *sql.Tx. One function writes one table: a run and its case runs are written
// by calling the functions one after the other, in a transaction if it must be all or nothing.
//
// The run is updated in place (its statistics after every judge batch, its status when it ends); the case runs are append-only,
// written once, when their outcome is final, so there is no update of a case run. A function that is given an ID that does not
// exist returns ErrNotFound.
//
// The tables have the prefix gb_neo_training_audit_, so that they can live next to the old gb_training_audit_ tables of internal/evals
// while the audits are moved here.
package db
