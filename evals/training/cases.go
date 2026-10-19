package training

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// DefaultEvalDir is the directory the evals are read from when none is given: bin/evals_training of the repository, next to
// the training datasets (bin/datasets_training). They are static definitions kept apart from the code, so they are read from
// disk, not embedded.
const DefaultEvalDir = "bin/evals_training"

// Case is one eval case of a trained model: a question and the answer expected (what the judge compares the model's answer to),
// plus a name, unique among all the evals' cases. The cases of an eval ask about the knowledge in a training dataset (the eval is
// named after it) but are phrased differently from its examples (several facts per question), so they check whether the model
// learned the facts, not the exact training prompts: they are cases the model never saw.
type Case struct {
	// Eval is the name of the eval (the "name" of its file) the case was loaded from; it is not part of the JSON.
	Eval        string `json:"-"`
	Name        string `json:"name"`
	Instruction string `json:"instruction"`
	Input       string `json:"input,omitempty"`
	// Output is the expected answer.
	Output string `json:"output"`
}

// Example is the case as a dataset record, which Run takes.
func (c Case) Example() Example {
	return Example{Instruction: c.Instruction, Input: c.Input, Output: c.Output}
}

// Eval is a named set of cases: one *.json file of the evals directory. Its name is the "name" key of the file (its "cases" key holds the list of cases), and is the name of the
// training dataset its cases ask about when they are about one, so that a model trained on a dataset has an eval of that name.
type Eval struct {
	Name  string `json:"name"`
	Cases []Case `json:"cases"`
}

// Evals returns the evals of the directory dir, ordered by name.
func Evals(dir string) ([]Eval, error) {
	cases, err := LoadEvals(dir, nil, "")
	if err != nil {
		return nil, err
	}
	var out []Eval
	for _, name := range EvalNames(dir) {
		e := Eval{Name: name, Cases: []Case{}}
		for _, c := range cases {
			if c.Eval == name {
				e.Cases = append(e.Cases, c)
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// LoadEvals loads the cases of the evals in the directory dir (dir/*.json). A non-empty eval restricts them to that eval, and a
// non-empty only to the cases with those names; unknown names are errors.
func LoadEvals(dir string, only []string, eval string) ([]Case, error) {
	fsys := os.DirFS(dir)
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var cases []Case
	seen := map[string]bool{}
	evalFound := eval == ""
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		var file evalFile
		if err := json.Unmarshal(data, &file); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		name, evalCases := file.Name, file.Cases
		if name == "" {
			return nil, fmt.Errorf("%s: the eval has no name", e.Name())
		}
		evalFound = evalFound || name == eval
		for i, c := range evalCases {
			c.Eval = name
			switch {
			case c.Name == "":
				return nil, fmt.Errorf("%s: case %d has no name", e.Name(), i)
			case seen[c.Name]:
				return nil, fmt.Errorf("%s: duplicate case name %q", e.Name(), c.Name)
			case strings.TrimSpace(c.Instruction) == "" || strings.TrimSpace(c.Output) == "":
				return nil, fmt.Errorf("%s: case %q needs an instruction and an output", e.Name(), c.Name)
			}
			seen[c.Name] = true
			if (eval == "" || name == eval) && (len(only) == 0 || slices.Contains(only, c.Name)) {
				cases = append(cases, c)
			}
		}
	}
	if !evalFound {
		return nil, fmt.Errorf("unknown eval %q (available: %s)", eval, strings.Join(EvalNames(dir), ", "))
	}
	for _, n := range only {
		if !slices.ContainsFunc(cases, func(c Case) bool { return c.Name == n }) {
			return nil, fmt.Errorf("unknown case %q", n)
		}
	}
	return cases, nil
}

// evalFile is the content of an eval file: the name of the eval and its cases.
type evalFile struct {
	Name  string `json:"name"`
	Cases []Case `json:"cases"`
}

// EvalNames are the names of the evals in the directory dir (the "name" of each of its *.json files; files that cannot be read
// or have no name are skipped).
func EvalNames(dir string) []string {
	fsys := os.DirFS(dir)
	entries, _ := fs.ReadDir(fsys, ".")
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			continue
		}
		var file struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &file) == nil && file.Name != "" {
			names = append(names, file.Name)
		}
	}
	return names
}
