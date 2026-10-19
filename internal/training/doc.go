// Package training is the training subsystem, without HTTP (but for the audits' endpoints in the subpackage audits): the training datasets (the subdirectories of the training datasets
// directory that have a manifest, see manifest.go), starting and stopping training jobs on the sidecar, following their progress
// (the Monitor), inferences with trained models, and the runs of trained models judged by an LLM (RunManager): the evals, and the audits (subpackage audits), which are not evals. The HTTP handlers of the
// training API (/v1/gb/training/) in internal/api/training are thin wrappers around it.
package training
