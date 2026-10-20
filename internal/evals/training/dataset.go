// Package training is the engine of the LLM-judged checks of freshly trained models: a run asks a trained model a list of cases and has
// an external LLM (the one gligabao uses for knowledge ingestion) judge, in batches, how well the model's answers agree with the
// expected ones. It is the EVAL (evalsdb.TrainedModelKindEval): the cases are those of an eval (Evals, LoadEvals:
// bin/evals_training/*.json), cases the model never saw, so the score says how well it generalizes. Run by the server, by the Eval
// button of the admin UI, or by the command-line runner internal/evals/training/cmd. (The training data audits, which ask the model
// its own training dataset, are in internal/training_audits.)
//
// It is separate from internal/evals/knowledge, which checks the knowledge extraction (remember / recall) end to end.
//
// The package knows nothing about HTTP, the sidecar or the database: Run takes the model as a function (GenerateFunc),
// the judge as an LLM and reports through a Sink. internal/training wires those up and stores the results.
package training

// Example is the input and expected output of an eval Case: the question (Instruction, plus
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
