package training

import (
	"errors"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
)

// DefaultEvalDir is the directory the evals are read from when none is given: bin/evals_training of the repository, next to the
// training datasets (bin/datasets_training). They are static definitions kept apart from the code, so they are read from disk, not
// embedded.
const DefaultEvalDir = "bin/evals_training"

// Eval is an eval file: a name, and its cases ({name, instruction, input?, output}). Its name is that of the training dataset its
// cases ask about, so that a model trained on a dataset has an eval of that name.
type Eval = evalfile.Eval[core.TrainingCase]

func caseName(c *core.TrainingCase) string { return c.Name }

func checkCase(c *core.TrainingCase) error {
	if strings.TrimSpace(c.Instruction) == "" || strings.TrimSpace(c.Output) == "" {
		return errors.New("needs an instruction and an output")
	}
	return nil
}

// LoadEvals reads the evals of the directory dir. Case names are unique over all of them.
func LoadEvals(dir string) ([]Eval, error) {
	return evalfile.Read(dir, caseName, checkCase)
}

// SelectEvals picks the eval called eval (all if empty) and, in it, the cases called by the names in only (all if none); see
// evalfile.Select.
func SelectEvals(evals []Eval, eval string, only []string) ([]Eval, error) {
	return evalfile.Select(evals, caseName, eval, only)
}

// Spec is the eval as the runner puts it in the database: an eval def of the Type training, with a case def for each case.
func Spec(e Eval) runner.EvalSpec {
	spec := runner.EvalSpec{Name: e.Name, Type: core.TypeTraining, Description: e.Description}
	for _, c := range e.Cases {
		spec.Cases = append(spec.Cases, runner.CaseSpec{Name: c.Name, Meta: &core.TrainingCaseDefMeta{TrainingInput: c.TrainingInput, TrainingExpected: c.TrainingExpected}})
	}
	return spec
}
