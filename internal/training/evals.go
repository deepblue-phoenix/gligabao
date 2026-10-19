package training

import trainingeval "github.com/deepblue-phoenix/gligabao/internal/evals/training"

// HasEval reports whether the directory dir of evals has an eval called name.
func HasEval(dir, name string) bool {
	for _, n := range trainingeval.EvalNames(dir) {
		if n == name {
			return true
		}
	}
	return false
}
