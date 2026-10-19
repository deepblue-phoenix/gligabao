// Package training is the training subsystem, without HTTP: the training datasets (the subdirectories of the training datasets
// directory that have a manifest, see manifest.go), starting and stopping training jobs on the sidecar, following their progress
// (the Monitor), inferences with trained models, and the evals and audits of trained models (RunManager). The HTTP handlers of the
// training API (/v1/gb/training/) in internal/api/training are thin wrappers around it.
package training
