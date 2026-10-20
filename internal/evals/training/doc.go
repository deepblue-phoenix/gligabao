// Package training is the executor of the training evals (core.TypeTraining): it asks a trained model the questions of an eval and
// has an external LLM (the one gligabao uses for knowledge ingestion) judge, in batches, how well the model's answers agree with the
// expected ones. The cases are those of an eval of bin/evals_training/*.json (LoadEvals): questions about knowledge of a training
// dataset, phrased differently from its examples, so they are cases the model never saw and the scores say how well it generalizes.
// (Asking a model its own training dataset is a training data audit, internal/training_audits, not an eval.)
//
// The package knows nothing about HTTP, the sidecar or the database: Run (the engine) takes the model as a function
// (GenerateFunc), the judge as an LLM and reports through a Sink; Executor wires the engine to a runner.EvalRecorder, which stores
// the case runs. Whoever starts it (the command in training/cmd) gives the model and the judge.
//
// Files: files.go (reading the evals), judge.go (the prompt and the scores), engine.go (Run), executor.go (Executor).
package training
