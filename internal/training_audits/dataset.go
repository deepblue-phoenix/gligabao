package training_audits

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/deepblue-phoenix/gligabao/internal/training"
	log "github.com/sirupsen/logrus"
)

// Example is one record of a training dataset (docs/training/training_data_format.md): the question (Instruction, plus Input as
// context) and the answer the model was trained to give (Output). The examples of a dataset are the cases of an audit.
type Example struct {
	Instruction string `json:"instruction"`
	Input       string `json:"input"`
	Output      string `json:"output"`
}

// Prompt is what the trained model is asked: the user turn the training text was made from (the same join as
// format_instruction in sidecar/ai_train/ai_train_core/gemma4/prompt_format.py: the instruction, and the input after a
// blank line when there is one). The sidecar adds the chat markers.
func (e Example) Prompt() string {
	if e.Input != "" {
		return e.Instruction + "\n\n" + e.Input
	}
	return e.Instruction
}

// ParseExamples reads the examples of a JSONL dataset: one JSON object per non-blank line, with string "instruction" and
// "output" and optionally an "input". The error names the first bad line.
func ParseExamples(content []byte) ([]Example, error) {
	if !utf8.Valid(content) {
		return nil, errors.New("the dataset is not valid UTF-8")
	}
	var out []Example
	for i, line := range bytes.Split(content, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e Example
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("line %d is not a dataset record: %w", i+1, err)
		}
		if strings.TrimSpace(e.Instruction) == "" {
			return nil, fmt.Errorf("line %d has no instruction", i+1)
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, errors.New("the dataset has no records")
	}
	return out, nil
}

// Dataset is the training dataset of a trained model as an audit reads it.
type Dataset struct {
	// Name is the dataset's name (without ".jsonl"), SHA256 the hex digest of the file that was read: what an audit run records
	// as core.AuditRun.Dataset and DatasetSHA256.
	Name, SHA256 string
	// Examples are its records, in file order: the cases of the audit.
	Examples []Example
}

// LoadDataset reads the training dataset of a trained model from the training datasets directory dir. trainedDataset is the
// model's dataset as the model records it (its file name, "" if it was trained on the sidecar's default dataset). The errors are
// safe to show to the user.
func LoadDataset(dirs []string, trainedDataset string) (*Dataset, error) {
	name := strings.TrimSuffix(trainedDataset, ".jsonl")
	if name == "" {
		return nil, errors.New("this model was trained on the sidecar's default dataset, which gligabao does not have: there are no questions to ask")
	}
	ds, err := training.FindDataset(dirs, name)
	if err != nil {
		return nil, fmt.Errorf("the training dataset %s of this model is not in the training datasets directory", name)
	}
	content, err := os.ReadFile(ds.Path)
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training audits: read training dataset file failed")
		return nil, fmt.Errorf("the data file of dataset %s cannot be read", name)
	}
	if len(content) > training.MaxDatasetBytes {
		return nil, fmt.Errorf("the data file of dataset %s is over %d MB", name, training.MaxDatasetBytes>>20)
	}
	examples, err := ParseExamples(content)
	if err != nil {
		return nil, fmt.Errorf("dataset %s: %w", name, err)
	}
	h := sha256.Sum256(content)
	return &Dataset{Name: name, SHA256: hex.EncodeToString(h[:]), Examples: examples}, nil
}
