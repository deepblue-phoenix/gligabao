package gcnii

import (
	"encoding/json"
	"sort"

	"github.com/deepblue-phoenix/gligabao/internal/modeltrain"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

// MetaSchema names the shape of Meta in gb_training_job_runs_meta.meta_schema; change it (gcnii.v2) when the shape changes in a way a reader
// must tell apart.
const MetaSchema = "gcnii.v1"

// Meta is what only a GCNII* run has of the training configuration and results: the JSON meta of its gb_training_job_runs_meta row.
// The hyperparameters and sizes every model has are columns of the row (epochs, lr, batch size, seed, the split); this is the rest.
type Meta struct {
	// Hidden and Depth are the settings of the network as the request gave them (0: sargo's default).
	Hidden int `json:"hidden,omitempty"`
	Depth  int `json:"depth,omitempty"`
	// Options is how the graphs of the dataset were selected and labelled (nil for a request that brought its own graphs).
	Options *modeltrain.Options `json:"options,omitempty"`
	// Classes are the classes of both sets, sorted; TrainCounts and ValidationCounts how many graphs of each class each set has.
	Classes          []string       `json:"classes"`
	TrainCounts      map[string]int `json:"train_counts"`
	ValidationCounts map[string]int `json:"validation_counts"`
	// HistoryRun is the ID of the record of the training in the history ("" if none; it holds the artifact IDs of both sets).
	HistoryRun string `json:"history_run,omitempty"`
	// Results, set when the run completed: the scores on the training graphs and on the validation graphs (nil: not measured), and the
	// mean training loss of every epoch.
	Accuracy    *float64  `json:"accuracy,omitempty"`
	ValAccuracy *float64  `json:"val_accuracy,omitempty"`
	EpochLosses []float64 `json:"epoch_losses,omitempty"`
}

// classCounts is how many of the graphs have each class.
func classCounts(graphs []sargo.GCNIIGraph) map[string]int {
	out := map[string]int{}
	for _, g := range graphs {
		out[g.Class]++
	}
	return out
}

// classesOf are the sorted classes of the two counts.
func classesOf(a,
	b map[string]int) []string {
	seen := map[string]struct{}{}
	for c := range a {
		seen[c] = struct{}{}
	}
	for c := range b {
		seen[c] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func ptr[T any](v T) *T { return &v }

// positiveInt is a pointer to n, nil for 0 (the default of sargo, not known here).
func positiveInt(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

func positiveFloat(f float64) *float64 {
	if f <= 0 {
		return nil
	}
	return &f
}

// newMeta is the meta row of a run that starts: what the request asked for (p), the sets sargo is given (train and validation) and the
// options they were made with (nil for graphs that came in the request). epochs is the epochs the job was asked to train.
func newMeta(run trainingdb.TrainingJobRun,
	p Params,
	opts *modeltrain.Options,
	historyRun string,
	epochs int,
	train,
	validation []sargo.GCNIIGraph) trainingdb.TrainingJobRunMeta {
	trainCounts, validationCounts := classCounts(train), classCounts(validation)
	meta, _ := json.Marshal(Meta{
		Hidden:           p.Hidden,
		Depth:            p.Depth,
		Options:          opts,
		Classes:          classesOf(trainCounts, validationCounts),
		TrainCounts:      trainCounts,
		ValidationCounts: validationCounts,
		HistoryRun:       historyRun,
	})
	m := trainingdb.TrainingJobRunMeta{
		RunID:                 run.ID,
		UserID:                run.UserID,
		ModelType:             trainingdb.ModelTypeGCNII,
		Epochs:                ptr(epochs),
		LR:                    positiveFloat(p.LR),
		BatchSize:             positiveInt(p.Batch),
		NumTrainExamples:      ptr(len(train)),
		NumValidationExamples: ptr(len(validation)),
		MetaSchema:            MetaSchema,
		Meta:                  meta,
	}
	if p.Seed != nil {
		m.Seed = ptr(int64(*p.Seed))
	}
	if opts != nil {
		m.ValidationFraction = ptr(opts.ValidationFraction)
		m.SplitSeed = ptr(int64(opts.Seed))
	}
	return m
}

// withResults is m with the results of a completed job (the data of its "completed" event: sargo's job); m if the data is not a job. The
// Meta of m that is not a GCNII* one is left alone.
func withResults(m trainingdb.TrainingJobRunMeta,
	data map[string]any) trainingdb.TrainingJobRunMeta {
	raw, err := json.Marshal(data)
	if err != nil {
		return m
	}
	var job sargo.GCNIIJob
	if json.Unmarshal(raw, &job) != nil {
		return m
	}
	var meta Meta
	if m.MetaSchema != MetaSchema || json.Unmarshal(m.Meta, &meta) != nil {
		return m
	}
	meta.Accuracy, meta.ValAccuracy, meta.EpochLosses = job.Accuracy, job.ValAccuracy, job.EpochLosses
	m.FinalValLoss = job.ValLoss
	m.Meta, _ = json.Marshal(meta)
	return m
}
