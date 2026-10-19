package training_test

import (
	"testing"

	trainingeval "github.com/deepblue-phoenix/gligabao/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

// Evals and audits share the engine and the live view of the admin UI, so they must use the same words: the engine's case
// statuses are stored as they are, and a run of either kind ends "completed".
func TestEvalAndAuditStatusesAgree(t *testing.T) {
	same := func(what string, got ...string) {
		t.Helper()
		for _, g := range got[1:] {
			if g != got[0] {
				t.Errorf("%s: %q differs from %q", what, g, got[0])
			}
		}
	}
	same("judged case", trainingeval.CaseJudged, relational.TrainedModelCaseJudged, relational.TrainedModelAuditCaseJudged)
	same("failed case", trainingeval.CaseFailed, relational.TrainedModelCaseFailed, relational.TrainedModelAuditCaseFailed)
	same("answered case", relational.TrainedModelCaseAnswered, relational.TrainedModelAuditCaseAnswered)
	same("completed run", relational.TrainedModelEvalRunCompleted, relational.TrainedModelAuditRunCompleted)
	same("failed run", relational.TrainedModelEvalRunFailed, relational.TrainedModelAuditRunFailed)
	same("cancelled run", relational.TrainedModelEvalRunCancelled, relational.TrainedModelAuditRunCancelled)
	same("running run", relational.TrainedModelEvalRunRunning, relational.TrainedModelAuditRunRunning)
}
