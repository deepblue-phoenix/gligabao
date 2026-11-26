package gcnii

import (
	"encoding/json"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/modeltrain"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

func TestNewMetaAndResults(t *testing.T) {
	train := []sargo.GCNIIGraph{{Class: "b"}, {Class: "a"}, {Class: "a"}}
	val := []sargo.GCNIIGraph{{Class: "c"}}
	seed := uint64(9)
	m := newMeta(trainingdb.TrainingJobRun{ID: 3, UserID: "u"},
		Params{LR: 0.1, Hidden: 8, Seed: &seed},
		&modeltrain.Options{ValidationFraction: 0.25, Seed: 4},
		"h1",
		30,
		train,
		val)
	if m.RunID != 3 || m.ModelType != "gcnii" || m.MetaSchema != MetaSchema || *m.Epochs != 30 || *m.LR != 0.1 || m.BatchSize != nil ||
		*m.Seed != 9 || *m.NumTrainExamples != 3 || *m.NumValidationExamples != 1 || *m.ValidationFraction != 0.25 || *m.SplitSeed != 4 {
		t.Fatalf("meta: %+v", m)
	}
	var meta Meta
	if err := json.Unmarshal(m.Meta, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Classes) != 3 || meta.Classes[0] != "a" || meta.TrainCounts["a"] != 2 || meta.ValidationCounts["c"] != 1 || meta.Hidden != 8 || meta.HistoryRun != "h1" {
		t.Fatalf("Meta: %+v", meta)
	}

	done := withResults(m,
		map[string]any{"val_loss": 0.7, "accuracy": 0.9, "epoch_losses": []float64{1, 0.5}})
	if done.FinalValLoss == nil || *done.FinalValLoss != 0.7 {
		t.Fatalf("results: %+v", done)
	}
	if err := json.Unmarshal(done.Meta, &meta); err != nil || meta.Accuracy == nil || *meta.Accuracy != 0.9 || len(meta.EpochLosses) != 2 || meta.TrainCounts["a"] != 2 {
		t.Fatalf("Meta with results: %+v, %v", meta, err)
	}
	// meta of another shape is left alone
	other := m
	other.MetaSchema = "lora.v1"
	if got := withResults(other,
		map[string]any{"val_loss": 1.0}); got.FinalValLoss != nil {
		t.Fatalf("foreign meta changed: %+v", got)
	}
}
