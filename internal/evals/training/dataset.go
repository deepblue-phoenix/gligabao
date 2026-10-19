// Package training is the engine of the LLM-judged checks of freshly trained models: a run asks a trained model a list of cases and has
// an external LLM (the one gligabao uses for knowledge ingestion) judge, in batches, how well the model's answers agree with the
// expected ones. Two things use it, kept apart in the database but sharing the types and the API shapes (internal/api/training):
//   - the EVAL (evalsdb.TrainedModelKindEval): the cases are those of an eval (Evals, LoadEvals: bin/evals_training/*.json), cases the model
//     never saw, so the score says how well it generalizes. Run by the server, by the Eval button of the admin UI, or by the
//     command-line runner internal/evals/training/cmd;
//   - the training data AUDIT (evalsdb.TrainedModelKindAudit): the cases are the examples of the model's own fine-tuning
//     dataset (ParseExamples), so the score says whether the training took, not how well the model generalizes.
//
// It is separate from internal/evals/knowledge, which checks the knowledge extraction (remember / recall) end to end.
//
// The package knows nothing about HTTP, the sidecar or the database: Run takes the model as a function (GenerateFunc),
// the judge as an LLM and reports through a Sink. internal/api/training wires those up and stores the results.
package training

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Example is one record of a training dataset (docs/training/TRAINING_DATA_FORMAT.md) or the input and expected output of an eval Case: the question (Instruction, plus
// Input as context) and the answer the model was trained to give (Output).
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
