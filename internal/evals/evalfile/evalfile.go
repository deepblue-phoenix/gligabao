// Package evalfile reads the evals from their files: a directory of JSON files, one eval per file, as in bin/evals_knowledge and
// bin/evals_training. A file is {"name": "<eval name>", "description": "...", "cases": [...]}; what a case holds depends on the
// Type of the eval, so the case type is a parameter. The files are the source of the evals: the runner puts them in the
// database as defs (internal/evals/runner) before it runs them.
package evalfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Eval is one file: a named set of cases of type C.
type Eval[C any] struct {
	// File is the name of the file in the directory.
	File        string
	Name        string
	Description string
	Cases       []C
}

// file is the content of a file with its cases left raw.
type file struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Cases       []json.RawMessage `json:"cases"`
}

// Read reads the evals of the directory dir (dir/*.json, ordered by file name). nameOf gives the name of a case, which must be
// set and unique over all the evals, so that a case can be picked by its name alone. check, if not nil, validates a case (it is
// given the eval's file name for its message). The names of the evals must be set and unique too.
func Read[C any](dir string, nameOf func(*C) string, check func(*C) error) ([]Eval[C], error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var evals []Eval[C]
	evalNames, caseNames := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var f file
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if f.Name == "" {
			return nil, fmt.Errorf("%s: the eval has no name", e.Name())
		}
		if evalNames[f.Name] {
			return nil, fmt.Errorf("%s: duplicate eval name %q", e.Name(), f.Name)
		}
		evalNames[f.Name] = true
		ev := Eval[C]{File: e.Name(), Name: f.Name, Description: f.Description, Cases: []C{}}
		for i, raw := range f.Cases {
			var c C
			if err := json.Unmarshal(raw, &c); err != nil {
				return nil, fmt.Errorf("%s: case %d: %w", e.Name(), i, err)
			}
			name := nameOf(&c)
			switch {
			case name == "":
				return nil, fmt.Errorf("%s: case %d has no name", e.Name(), i)
			case caseNames[name]:
				return nil, fmt.Errorf("%s: duplicate case name %q", e.Name(), name)
			}
			if check != nil {
				if err := check(&c); err != nil {
					return nil, fmt.Errorf("%s: case %q: %w", e.Name(), name, err)
				}
			}
			caseNames[name] = true
			ev.Cases = append(ev.Cases, c)
		}
		evals = append(evals, ev)
	}
	return evals, nil
}

// Names are the names of the evals.
func Names[C any](evals []Eval[C]) []string {
	names := make([]string, len(evals))
	for i, e := range evals {
		names[i] = e.Name
	}
	return names
}

// Select returns the evals to run: the one called eval (all of them if eval is empty), with only the cases called by the names
// in only (all of them if only is empty). An eval left without cases is dropped. An unknown eval or case name is an error, and
// so is a case that is not in the eval asked for.
func Select[C any](evals []Eval[C], nameOf func(*C) string, eval string, only []string) ([]Eval[C], error) {
	if eval != "" && !slices.ContainsFunc(evals, func(e Eval[C]) bool { return e.Name == eval }) {
		return nil, fmt.Errorf("unknown eval %q (available: %s)", eval, strings.Join(Names(evals), ", "))
	}
	var out []Eval[C]
	found := map[string]bool{}
	for _, e := range evals {
		if eval != "" && e.Name != eval {
			continue
		}
		sel := Eval[C]{File: e.File, Name: e.Name, Description: e.Description, Cases: []C{}}
		for _, c := range e.Cases {
			if n := nameOf(&c); len(only) == 0 || slices.Contains(only, n) {
				sel.Cases = append(sel.Cases, c)
				found[n] = true
			}
		}
		if len(sel.Cases) > 0 {
			out = append(out, sel)
		}
	}
	for _, n := range only {
		if !found[n] {
			return nil, fmt.Errorf("unknown case %q", n)
		}
	}
	return out, nil
}
