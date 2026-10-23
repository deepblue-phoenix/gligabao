package knowledge

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
)

// DefaultEvalDir is the directory the evals are read from when none is configured (evals_knowledge_dirs, or -evals; relative to the
// repository root, where the runner is run from): static definitions kept in bin/, apart from the code, next to the training datasets.
const DefaultEvalDir = "bin/evals_knowledge"

// Eval is an eval file: a name and its cases (see core.KnowledgeCase).
type Eval = evalfile.Eval[core.KnowledgeCase]

func caseName(c *core.KnowledgeCase) string { return c.Name }

// LoadEvals reads the evals of the directories dirs, in order: an eval whose name was found in an earlier directory hides the same
// name in a later one. Case names are unique over all of them. A text of a case is given by its Text, or by its TextFile (a file
// of one of dirs, the first that has it, read when the case runs), not both, and the file must exist.
func LoadEvals(dirs ...string) ([]Eval, error) {
	return evalfile.ReadDirs(dirs, caseName, func(c *core.KnowledgeCase) error {
		for i, m := range c.Remember {
			if m.TextFile == "" {
				continue
			}
			if m.Text != "" {
				return fmt.Errorf("remember #%d has both text and text_file", i+1)
			}
			if _, err := statTextFile(dirs, m.TextFile); err != nil {
				return fmt.Errorf("remember #%d: %w", i+1, err)
			}
		}
		return nil
	})
}

// SelectEvals picks the eval called eval (all if empty) and, in it, the cases called by the names in only (all if none); see
// evalfile.Select.
func SelectEvals(evals []Eval, eval string, only []string) ([]Eval, error) {
	return evalfile.Select(evals, caseName, eval, only)
}

// Spec is the eval as the runner puts it in the database: an eval def of the Type knowledge, with a case def for each case.
func Spec(e Eval) runner.EvalSpec {
	spec := runner.EvalSpec{Name: e.Name, Type: core.TypeKnowledge, Description: e.Description}
	for _, c := range e.Cases {
		spec.Cases = append(spec.Cases, runner.CaseSpec{Name: c.Name, Meta: &core.KnowledgeCaseDefMeta{
			KnowledgeInput: c.KnowledgeInput, KnowledgeExpected: c.KnowledgeExpected, KnowledgeParams: c.KnowledgeParams}})
	}
	return spec
}

// statTextFile finds the TextFile name in the eval directories: the first of dirs that has it. If none has, the error is the one
// of the first directory.
func statTextFile(dirs []string, name string) (fs.FS, error) {
	var firstErr error
	for _, dir := range dirs {
		fsys := os.DirFS(dir)
		_, err := fs.Stat(fsys, name)
		if err == nil {
			return fsys, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("%s: no eval directories", name)
	}
	return nil, firstErr
}

// readTexts returns a copy of in with the text of every TextFile read from the eval directories dirs (the first that has the file)
// into Text (the TextFile stays, as the reference).
func readTexts(dirs []string, in core.KnowledgeInput) (core.KnowledgeInput, error) {
	if len(dirs) == 0 {
		dirs = []string{DefaultEvalDir}
	}
	out := in
	out.Remember = make([]core.KnowledgeArtifact, len(in.Remember))
	copy(out.Remember, in.Remember)
	for i, m := range out.Remember {
		if m.TextFile == "" {
			continue
		}
		if m.Text != "" {
			return in, errors.New("remember #" + fmt.Sprint(i+1) + " has both text and text_file")
		}
		fsys, err := statTextFile(dirs, m.TextFile)
		if err != nil {
			return in, fmt.Errorf("remember #%d: %w", i+1, err)
		}
		b, err := fs.ReadFile(fsys, m.TextFile)
		if err != nil {
			return in, fmt.Errorf("remember #%d: %w", i+1, err)
		}
		out.Remember[i].Text = string(b)
	}
	return out, nil
}
