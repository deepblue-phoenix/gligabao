// Package evals_neo is the rewrite of the evals data model. For now it holds only plain structs (no DB tables, no logic).
//
// # Vocabulary
//
// There are two sides, and every concept exists on both:
//
//	DEFINITIONS (what to run; stored, edited, reused)     RUNS (what happened; append-only history)
//	ExperimentDef  - which evals, plus settings           ExperimentRun - one execution of an ExperimentDef
//	EvalDef        - a named collection of cases          EvalRun       - one execution of an EvalDef
//	CaseDef        - one input / expected output + params CaseRun       - one execution of a CaseDef
//
// # Shape
//
//	ExperimentDef ──< (ordered, many-to-many) >── EvalDef ──< (owns, one-to-many) >── CaseDef
//	      │                                          │                                   │
//	      ▼ DefID                                    ▼ DefID                             ▼ DefID
//	ExperimentRun ──────< (owns, optional) >──── EvalRun ───────< (owns) >─────────── CaseRun
//
// # Rules of the model
//
//  1. Defs and runs never share a struct. A def says what to run; a run says what happened.
//  2. Links between runs go from child to parent (CaseRun.EvalRunID, EvalRun.ExperimentRunID); a parent never stores a list
//     of child IDs. The slices on the parents (EvalRun.CaseRuns, ...) are filled by getters only and are not stored.
//     Links between defs: an EvalDef is reusable by many ExperimentDefs, so ExperimentDef holds the ordered list of EvalDefIDs;
//     a CaseDef belongs to exactly one EvalDef (CaseDef.EvalDefID).
//  3. An EvalRun may exist without an ExperimentRun (ExperimentRunID is nil): running one eval on its own is allowed.
//     A CaseRun always belongs to an EvalRun, and an EvalRun always has an EvalDef.
//  4. Status is the LIFECYCLE of a run (did it run to the end?). It is never a verdict. The verdict about what was run
//     (did it pass, what score) is a separate field set on a finished run. The old model mixed the two ("failed" vs "error",
//     "judged" vs "passed"); here "errored" always means "could not be run to the end".
//  5. What is specific to a kind of eval (knowledge extraction, trained models, ...) is not a column of these structs: the
//     EvalDef has a Type, and the type-specific parts of a case or a result are opaque JSON (Input, Expected, Output, Details)
//     that the code of that Type interprets.
//  6. Runs record the Params they were invoked with, so a run can be understood without looking at the def as it is now.
package evals_neo
