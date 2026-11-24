package training

import (
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/sys/ml_sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
)

func TestTrainingEventToProviderEvent(t *testing.T) {
	run := trainingdb.TrainingJobRun{ID: 7, JobID: "j1", UserID: "u", Dataset: "d", ModelSize: "4b"}
	step, loss := 12, 0.5
	cases := []struct {
		name     string
		ev       ml_sidecar.TrainingEvent
		wantType string
		wantKind string
		wantKey  string
	}{
		{"status", ml_sidecar.TrainingEvent{Type: ml_sidecar.EventStatus, Status: "completed"}, ProviderEventJobStatus, monitoring.KindLifecycle, "status"},
		{"progress", ml_sidecar.TrainingEvent{Type: ml_sidecar.EventProgress, Step: &step, Loss: &loss, Timestamp: 1700000000.5}, ProviderEventJobProgress, monitoring.KindProgress, "step"},
		{"log", ml_sidecar.TrainingEvent{Type: ml_sidecar.EventLog, Message: "hi"}, ProviderEventJobLog, monitoring.KindLog, "message"},
	}
	for _, c := range cases {
		got := trainingEventToProviderEvent(run, c.ev)
		if got.ProviderType != monitoring.ProviderModal || got.Type != c.wantType || got.Kind != c.wantKind {
			t.Fatalf("%s: %+v", c.name, got)
		}
		if _, ok := got.Data[c.wantKey]; !ok || got.Data["job_id"] != "j1" || got.Data["run_id"] != int64(7) {
			t.Fatalf("%s: data %v", c.name, got.Data)
		}
	}
	if p := trainingEventToProviderEvent(run, cases[1].ev); p.Time.Unix() != 1700000000 {
		t.Fatalf("time %v", p.Time)
	}
}
