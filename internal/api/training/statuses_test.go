package training_test

import (
	"testing"

	trainingeval "github.com/deepblue-phoenix/gligabao/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

// Evals and audits share the statuses (model/trained_model_status.go) as Go constants, so they cannot drift apart. This checks that
// the engine's and the relational package's names are the same constants and have the words the admin UI (eval_live.ts) and the
// docs use.
func TestSharedStatusWords(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{trainingeval.CaseJudged, "judged"}, {relational.TrainedModelCaseJudged, "judged"},
		{trainingeval.CaseFailed, "failed"}, {relational.TrainedModelCaseFailed, "failed"},
		{relational.TrainedModelCaseAnswered, "answered"},
		{relational.TrainedModelRunRunning, "running"}, {relational.TrainedModelRunCompleted, "completed"},
		{relational.TrainedModelRunFailed, "failed"}, {relational.TrainedModelRunCancelled, "cancelled"},
	} {
		if c.got != c.want {
			t.Errorf("status %q, want %q (admin_ui/src/eval_live.ts and the docs use that word)", c.got, c.want)
		}
	}
}
