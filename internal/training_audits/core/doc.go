// Package core is the training audits data model (built like internal/evals_neo/core, which it does not import): plain structs, no
// logic. The tables that hold them are in the sibling package db.
//
// # What an audit is
//
// An audit asks a trained model the questions of its OWN fine-tuning dataset and has an LLM judge each answer against the answer the
// model was trained to give. The score says whether the training took (memorization and fit), not whether the model generalizes:
// that is what an eval is for, and an audit is not one. They share nothing.
//
// # Vocabulary
//
// An audit has no definition to author, store and reuse: what is asked is the dataset of the trained model, which lives in a file
// of the training datasets directory. So only the RUNS side of the evals vocabulary exists here:
//
//	AuditRun     - one execution of an audit of a trained model, over one dataset (the run of an EvalDef)
//	AuditCaseRun - one example of that dataset, asked and judged (the run of a CaseDef)
//
//	AuditRun ──────< (owns) >────── AuditCaseRun
//
// # Rules of the model
//
//  1. Links go from child to parent (AuditCaseRun.RunID). AuditRun.CaseRuns is filled by getters only and is not stored.
//  2. The case runs are append-only: the run is given the examples, so the case run keeps the question and the expected answer as
//     they were (Instruction, Input, Reference). The dataset file may change after the run; the run keeps the digest of the file it
//     read (DatasetSHA256) and the case run its Idx in it. The examples have no names.
//  3. Status is the LIFECYCLE of a run (did it run to the end?). It is never a verdict. A case run has no verdict either: the
//     judge's score is its outcome (JudgeScore, 0 to 5, and Score, that over 5). "errored" always means "could not be run to the
//     end" (the model or the judge failed); the old vocabulary's "error" and "judged" are Errored and Completed.
//  4. Runs record the Params they were invoked with, so a run can be understood without looking at the settings as they are now.
//  5. The statistics of the run as a whole (the judge, the split of the judged answers, the score sum) are AuditStats, kept in the
//     AuditRun itself: there is one kind of audit, so there is no per-type metadata table.
package core
