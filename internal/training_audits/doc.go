// Package training_audits is the training data audits of trained models, detached from the evals: an audit asks a trained model the
// questions of its OWN fine-tuning dataset and has an LLM judge the answers, to see whether the training took and which training
// examples are bad. An eval (internal/evals, type "training") asks cases the model never saw and measures how well it generalizes;
// the two share no types, tables or code.
//
// It is built like internal/evals (same vocabulary, same rules, same layout) and its audit logic is that of the audits that were
// in internal/training/audits and of the old evals (internal/evals, since removed), rewritten over its own data model. Its parts:
//
//	core       the data model: the structs and their vocabulary (see its package doc)
//	db         the storage of the data model: the schema and the functions that write and read the records (sqlite, postgres)
//	api        the HTTP handlers: start, list, get, cancel and watch the audits
//	audittest  fakes and databases for the tests
//	(this package):
//	  dataset.go   reading the training dataset of a trained model: LoadDataset
//	  engine.go    the engine: Run asks the model the examples one at a time and has the judge grade them in batches
//	  judge.go     the judge: the prompt, the scores and how its reply is read
//	  options.go   the settings an audit is started with (Options) and stores (RunParams)
//	  manager.go   the Manager: starts audits and runs them in the background, one at a time per trained model; lists, reads,
//	               cancels and watches them
//	  ops.go       Ops, what the Manager needs of the database backend (OpsFor, SQLiteOps, PostgresOps)
//	  feed.go      the live feed (Feed, Event) of a running audit, which the Manager's Watch hands out
//	  errors.go    Error, with a Kind that the HTTP handlers map to a status
//
// An audit runs like this: the Manager records an AuditRun (running) and starts it in the background. The engine takes the examples
// in batches: the model answers the questions of a batch (the feed announces each answer at once), the judge grades the batch with
// one LLM call, and the batch's case runs are written together with the run's updated Counts and AuditStats, in one transaction (so
// the case runs are written once, final, and the run shows its progress). The run ends completed, cancelled (what was done is
// kept: the case runs of the batch it was in that were reached are stored as cancelled) or errored (the model or the judge kept
// failing, or the server stopped); audits that a restart interrupted are errored when the Manager is made.
//
// The server wires this package (internal/sys/server) and the admin UI shows its audits; the old audits of internal/training/audits
// and internal/api/training are not used any more. This package shares no tables with them (Open creates its own). It uses internal/training only for what is not part of the audits: the trained models, the training
// datasets and the sidecar's inference.
package training_audits
