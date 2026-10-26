// Package evals is the evals system: the data model, its storage, and the runners that execute the evals. Its parts are in
// subpackages:
//
//	core       the data model: the structs and their vocabulary (see its package doc)
//	db         the storage of the data model: the schema and the functions that write and read the records (sqlite, postgres)
//	evalfile   reading the evals from their JSON files (build/evals_knowledge, build/evals_training)
//	runner     what every Type of eval shares in running: putting the evals of the files in the DB as defs, recording experiment
//	           runs, eval runs and case runs
//	knowledge  the knowledge evals: remember some texts, then query (the executor of the Type "knowledge"), and its command
//	training   the training evals: a trained model answers, an LLM judges (the executor of the Type "training"), and its command
//
// The root package has Open, which applies the schema to the database (internal/db does it when it opens one), and StoreFor, which
// gives the functions of a backend. The old evals package (internal/evals) is gone; nothing
// refers to its tables (gb_experiment*) any more.
package evals
