package training_test

import (
	"testing"

	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	trainingeval "github.com/deepblue-phoenix/gligabao/internal/evals/training"
)

// Evals and audits share the statuses (model/trained_model_status.go) as Go constants, so they cannot drift apart. This checks that
// the engine's and the relational package's names are the same constants and have the words the admin UI (eval_live.ts) and the
// docs use.
func TestSharedStatusWords(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{trainingeval.CaseJudged, "judged"}, {evalsdb.TrainedModelCaseJudged, "judged"},
		{trainingeval.CaseErrored, "error"}, {evalsdb.TrainedModelCaseErrored, "error"},
		{evalsdb.TrainedModelCaseAnswered, "answered"},
		{evalsdb.TrainedModelRunRunning, "running"}, {evalsdb.TrainedModelRunCompleted, "completed"},
		{evalsdb.TrainedModelRunErrored, "error"}, {evalsdb.TrainedModelRunCancelled, "cancelled"},
	} {
		if c.got != c.want {
			t.Errorf("status %q, want %q (admin_ui/src/eval_live.ts and the docs use that word)", c.got, c.want)
		}
	}
}
