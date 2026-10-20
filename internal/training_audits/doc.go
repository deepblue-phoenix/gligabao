// Package training_audits is the training data audits of trained models, detached from the evals: an audit asks a trained model the
// questions of its OWN fine-tuning dataset and has an LLM judge the answers, to see whether the training took and which training
// examples are bad. An eval (internal/evals_neo, type "training") asks cases the model never saw and measures how well it generalizes;
// the two share no types, tables or code.
//
// It is built like internal/evals_neo (same vocabulary, same rules, same layout) and takes the audit logic of internal/training/audits.
// Its parts:
//
//	core     the data model: the structs and their vocabulary (see its package doc)
//	db       the storage of the data model: the schema and the functions that create the records (for now only creation)
//	dataset  (this package) reading the training dataset of a trained model: LoadDataset
//
// Not here yet (still in internal/training and internal/training/audits, on the old tables): the engine that asks and judges, the
// manager that runs audits in the background, the live feed and the HTTP handlers. Nothing uses this package yet.
package training_audits
