package training

import (
	"context"
	"errors"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

// only the LoRA models run on the ML sidecar: the GCNII* models (sargo) are refused by every caller that goes there.
func TestGetMLSidecarModelRefusesOtherTypes(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx := context.Background()
	loraID, _, err := trainingdb.RecordTrainedModel(ctx, d, trainingdb.TrainedModel{UserID: "alice", JobID: "l1", ModelSize: "4b", NumTrainSteps: 1, Path: "trained/l1"})
	if err != nil {
		t.Fatal(err)
	}
	gcniiID, _, err := trainingdb.RecordTrainedModel(ctx, d, trainingdb.TrainedModel{
		ModelType: trainingdb.ModelTypeGCNII, UserID: "alice", Storage: trainingdb.StorageS3, JobID: "g1", NumTrainSteps: 1, Path: "model_weights/gcnii/alice/g1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GetMLSidecarModel(ctx, d, "alice", loraID); err != nil {
		t.Fatalf("lora: %v", err)
	}
	var e *Error
	if _, err := GetMLSidecarModel(ctx, d, "alice", gcniiID); !errors.As(err, &e) || e.Kind != KindConflict {
		t.Fatalf("gcnii: %v", err)
	}
	if _, err := GetModel(ctx, d, "alice", gcniiID); err != nil {
		t.Fatalf("reading a gcnii row is fine: %v", err)
	}
	// a model of another user is as unknown as one that does not exist
	for _, id := range []int64{loraID, gcniiID} {
		if _, err := GetModel(ctx, d, "bob", id); !errors.As(err, &e) || e.Kind != KindNotFound {
			t.Fatalf("model %d of another user: %v", id, err)
		}
	}
}
