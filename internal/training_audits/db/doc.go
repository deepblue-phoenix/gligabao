// Package db is the storage of the training audits data model (package core), one subpackage per database: sqlite and postgres. Each
// holds the schema and the functions that create the records of its tables (for now only creation). The two have the same
// functions, so they can be swapped.
//
// The functions take an Execer, which is a *sql.DB or a *sql.Tx. One function writes one table: a run and its case runs are written
// by calling the functions one after the other, in a transaction if it must be all or nothing.
//
// The tables have the prefix gb_neo_training_audit_, so that they can live next to the old gb_training_audit_ tables of internal/evals
// while the audits are moved here.
package db
