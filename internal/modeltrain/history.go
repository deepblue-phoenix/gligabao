package modeltrain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The history of the trainings on datasets: every one leaves a directory <dir>/<user>/<dataset>/<run id>/ with run.json (what Run
// holds) and classes.json (the class names, as sargo's model has them), so the sets a model was trained on can be told later. The
// weights themselves stay in sargo, which keeps one model per user (the last).

// TrainParams are the training settings sent to sargo (0 is sargo's default).
type TrainParams struct {
	Epochs int     `json:"epochs,omitempty"`
	LR     float64 `json:"lr,omitempty"`
	Batch  int     `json:"batch,omitempty"`
	Hidden int     `json:"hidden,omitempty"`
	Depth  int     `json:"depth,omitempty"`
	Seed   *uint64 `json:"seed,omitempty"`
}

// Run is the record of one training on a dataset.
type Run struct {
	// ID names the run directory: the start time, so that the runs sort by it.
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Dataset   string    `json:"dataset"`
	StartedAt time.Time `json:"started_at"`
	// Options and Params are what the request asked for.
	Options Options     `json:"options"`
	Params  TrainParams `json:"params"`
	// Classes are the classes of both sets, sorted.
	Classes []string `json:"classes"`
	// TrainCounts and ValidationCounts say how many graphs of each class are in each set.
	TrainCounts      map[string]int `json:"train_counts"`
	ValidationCounts map[string]int `json:"validation_counts"`
	// ArtifactsTrain and ArtifactsValidation are the artifacts of the graphs of each set.
	ArtifactsTrain      []int64 `json:"artifacts_train"`
	ArtifactsValidation []int64 `json:"artifacts_validation"`
}

// NewRun makes the record of a training that starts at now on sets.
func NewRun(userID,
	dataset string,
	now time.Time,
	opts Options,
	params TrainParams,
	sets Sets) Run {
	return Run{
		ID:                  now.UTC().Format("20060102T150405.000000000Z"),
		UserID:              userID,
		Dataset:             dataset,
		StartedAt:           now.UTC(),
		Options:             opts,
		Params:              params,
		Classes:             sets.Classes,
		TrainCounts:         classCounts(sets.Train),
		ValidationCounts:    classCounts(sets.Validation),
		ArtifactsTrain:      sets.ArtifactsTrain,
		ArtifactsValidation: sets.ArtifactsValidation,
	}
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeName makes s usable as one path element.
func safeName(s string) string {
	s = strings.Trim(unsafeName.ReplaceAllString(s, "_"), "._")
	if s == "" {
		return "_"
	}
	return s[:min(len(s), 64)]
}

func datasetDir(dir,
	userID,
	dataset string) string {
	return filepath.Join(dir, safeName(userID), safeName(dataset))
}

// SaveRun writes the run under dir (created if needed). A run directory is never overwritten.
func SaveRun(dir string,
	run Run) error {
	runDir := filepath.Join(datasetDir(dir, run.UserID, run.Dataset), run.ID)
	if err := os.MkdirAll(filepath.Dir(runDir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(runDir, 0o755); err != nil {
		return fmt.Errorf("run directory: %w", err)
	}
	for name, v := range map[string]any{"run.json": run, "classes.json": run.Classes} {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(runDir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ListRuns returns the runs of userID's dataset saved under dir, the newest first (none: an empty list). A run directory that cannot be
// read is skipped.
func ListRuns(dir,
	userID,
	dataset string) ([]Run, error) {
	entries, err := os.ReadDir(datasetDir(dir, userID, dataset))
	if errors.Is(err, os.ErrNotExist) {
		return []Run{}, nil
	}
	if err != nil {
		return nil, err
	}
	runs := []Run{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(datasetDir(dir, userID, dataset), e.Name(), "run.json"))
		if err != nil {
			continue
		}
		var run Run
		if json.Unmarshal(b, &run) == nil {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	return runs, nil
}
