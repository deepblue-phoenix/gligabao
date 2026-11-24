package training

import (
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/sys/ml_sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
)

func TestOfRunCompletesTheEventWithItsJob(t *testing.T) {
	run := trainingdb.TrainingJobRun{ID: 7, JobID: "j1", UserID: "u"}
	step := 12
	ev := ml_sidecar.TrainingEvent{Seq: 3, Type: ml_sidecar.EventProgress, Step: &step}
	got := ofRun(run, ev)
	if got.RunID != 7 || got.JobID != "j1" || got.UserID != "u" || got.ProviderType != monitoring.ProviderModal {
		t.Fatalf("job fields: %+v", got)
	}
	if got.Seq != 3 || got.Type != ml_sidecar.EventProgress || got.Step != &step {
		t.Fatalf("the event changed: %+v", got)
	}
	if ev.RunID != 0 || ev.UserID != "" {
		t.Fatalf("the argument was changed: %+v", ev)
	}
}

func TestJobEventIsLiveOnlyAndStamped(t *testing.T) {
	run := trainingdb.TrainingJobRun{ID: 7, JobID: "j1", UserID: "u"}
	got := jobEvent(run, monitoring.EventStopRequested)
	if got.Seq != monitoring.NoSeq || got.Type != monitoring.EventStopRequested || got.Timestamp <= 0 {
		t.Fatalf("%+v", got)
	}
	if got.RunID != 7 || got.UserID != "u" || got.ProviderType != monitoring.ProviderModal {
		t.Fatalf("job fields: %+v", got)
	}
}
