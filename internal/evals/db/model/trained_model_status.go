package model

// The statuses of the RUNS and CASES of a trained model: the two kinds of TrainedModelRun, an eval and a training data audit,
// and their TrainedModelCase use the same words, defined here once. The engine that produces them
// (internal/evals/training) and the admin UI's shared live view (admin_ui/src/eval_live.ts) know no other, so a status is never converted
// between the two kinds of run, and a typo is a compile error.
//
// They are not the Experiment* statuses of the knowledge evals (see model.go): there, passed and failed are verdicts about
// checks (the training statuses have no verdict; "error" is the one word they share with the knowledge evals, and means the
// same: something could not be run to the end); a training score is not a verdict. The CHECKs of the schema (gb_experiment_runs and gb_experiment_case_runs for evals,
// gb_training_audit_runs and gb_training_audit_cases for audits) spell the same words.

// Statuses of a run of a trained model.
const (
	TrainedModelRunRunning   = "running"
	TrainedModelRunCompleted = "completed" // it ran to the end: it has a score, not a verdict
	TrainedModelRunErrored   = "error"     // the run itself errored: Error says why (the model or the judge kept failing, or a restart interrupted it)
	TrainedModelRunCancelled = "cancelled" // stopped by the user; what was done is kept
)

// Statuses of a case (one example of the dataset) of a run of a trained model.
const (
	TrainedModelCaseJudged  = "judged" // the model answered and the judge scored it (a score of 0 too)
	TrainedModelCaseErrored = "error"  // the model or the judge errored: no score, Error says why
	// TrainedModelCaseAnswered is never stored: the live feed of a run (GET .../evals/{id}/events or .../audits/{id}/events) uses
	// it for a case the model answered whose batch the judge has not scored yet.
	TrainedModelCaseAnswered = "answered"
)
