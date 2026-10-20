// Package db is the storage of the evals data model (package core), one subpackage per database: sqlite and postgres. Each
// holds the schema and the functions that create the records of its tables (for now only creation). The two have the same
// functions, so they can be swapped.
//
// The functions take an Execer, which is a *sql.DB or a *sql.Tx. One function writes one table: a record that spans tables
// (a CaseDef and its Meta, an ExperimentDef and its evals) is written by calling the functions one after the other, in a
// transaction if it must be all or nothing.
package db
