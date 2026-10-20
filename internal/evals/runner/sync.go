package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
)

// CaseSpec is a case as the files have it: a name and the metadata of the eval's Type (a *core.KnowledgeCaseDefMeta, ...; its
// CaseDefID is ignored).
type CaseSpec struct {
	Name string
	// Description and Tags are kept as the database has them if left empty.
	Description string
	Tags        []string
	Meta        core.CaseDefMeta
}

// EvalSpec is an eval as the files have it. SyncEval makes the database's eval def say the same.
type EvalSpec struct {
	Name string
	Type core.Type
	// Description, Tags and Params are kept as the database has them if left empty.
	Description string
	Tags        []string
	Params      core.Params
	// Cases are in the order they run.
	Cases []CaseSpec
}

// tempPosition is where a case def is parked while the positions of the others are rearranged (the position is unique in an
// eval, so two cases cannot swap at once): above every real one.
const tempPosition = 1 << 30

// SyncEval makes the eval def that the spec describes exist in the database: it creates it, or brings the stored one up to date,
// in one transaction. The files are the source, so a case that exists is rewritten if its metadata differs (the case runs of the
// past keep what they were given, see core.CaseRun), a case that is new is created, and the order of the cases is the spec's. A
// case that is in the database but not in the spec is kept (it may have runs), after the others, and is not in the returned def.
// The returned def has the cases of the spec only, each with its Meta (pointers: see db.Store.GetEvalDefByName).
func (r *Runner) SyncEval(ctx context.Context, spec EvalSpec) (*core.EvalDef, error) {
	if spec.Name == "" || spec.Type == "" {
		return nil, errors.New("sync eval: the name and type are required")
	}
	for _, c := range spec.Cases {
		if c.Name == "" || c.Meta == nil || c.Meta.CaseType() != spec.Type {
			return nil, fmt.Errorf("sync eval %q: case %q has no metadata of type %q", spec.Name, c.Name, spec.Type)
		}
	}
	var out *core.EvalDef
	err := r.tx(ctx, func(q db.Execer) error {
		def, err := r.syncEvalDef(ctx, q, spec)
		if err != nil {
			return err
		}
		if out, err = r.syncCases(ctx, q, def, spec); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sync eval %q: %w", spec.Name, err)
	}
	return out, nil
}

// syncEvalDef creates or updates the eval def itself (not its cases).
func (r *Runner) syncEvalDef(ctx context.Context, q db.Execer, spec EvalSpec) (*core.EvalDef, error) {
	def, err := r.Store.GetEvalDefByName(ctx, q, spec.Name)
	if errors.Is(err, db.ErrNotFound) {
		def = &core.EvalDef{Name: spec.Name, Type: spec.Type, Description: spec.Description, Tags: spec.Tags, Params: spec.Params}
		if err := r.Store.CreateEvalDef(ctx, q, def); err != nil {
			return nil, err
		}
		return def, nil
	}
	if err != nil {
		return nil, err
	}
	if def.Type != spec.Type {
		return nil, fmt.Errorf("the eval exists with type %q, not %q", def.Type, spec.Type)
	}
	changed := false
	if spec.Description != "" && spec.Description != def.Description {
		def.Description, changed = spec.Description, true
	}
	if len(spec.Tags) > 0 && !slices.Equal(spec.Tags, def.Tags) {
		def.Tags, changed = spec.Tags, true
	}
	if len(spec.Params) > 0 && !sameJSON(spec.Params, def.Params) {
		def.Params, changed = spec.Params, true
	}
	if changed {
		if err := r.Store.UpdateEvalDef(ctx, q, def); err != nil {
			return nil, err
		}
	}
	return def, nil
}

// syncCases creates, updates and orders the case defs of def (which has the cases that are stored) to match spec, and returns
// def with the cases of the spec.
func (r *Runner) syncCases(ctx context.Context, q db.Execer, def *core.EvalDef, spec EvalSpec) (*core.EvalDef, error) {
	stored := map[string]*core.CaseDef{}
	for i := range def.Cases {
		stored[def.Cases[i].Name] = &def.Cases[i]
	}
	// The target position of every case: the spec's in order, then the stored ones that are not in it, in their order.
	target := map[string]int{}
	for i, c := range spec.Cases {
		target[c.Name] = i
	}
	next := len(spec.Cases)
	for _, c := range def.Cases {
		if _, ok := target[c.Name]; !ok {
			target[c.Name] = next
			next++
		}
	}

	// Pass 1: create the new cases (parked, as their final place may be taken), rewrite the metadata of the others, and park the
	// ones whose position changes, so that pass 2 finds every final position free.
	park := 0
	parked := func() int { park++; return tempPosition + park }
	var all []*core.CaseDef // the cases of the spec, in order, then the stored ones that are not in it
	for _, c := range spec.Cases {
		cd := stored[c.Name]
		if cd == nil {
			cd = &core.CaseDef{EvalDefID: def.ID, Type: def.Type, Name: c.Name, Position: parked(), Description: c.Description, Tags: c.Tags}
			if err := r.Store.CreateCaseDef(ctx, q, cd); err != nil {
				return nil, err
			}
			meta := withCaseDefID(c.Meta, cd.ID)
			if err := r.Store.CreateCaseDefMeta(ctx, q, meta); err != nil {
				return nil, err
			}
			cd.Meta = meta
			all = append(all, cd)
			continue
		}
		meta := withCaseDefID(c.Meta, cd.ID)
		if !sameJSON(meta, cd.Meta) {
			if err := r.Store.UpdateCaseDefMeta(ctx, q, meta); err != nil {
				return nil, err
			}
			cd.Meta = meta
		}
		dirty := false
		if c.Description != "" && c.Description != cd.Description {
			cd.Description, dirty = c.Description, true
		}
		if len(c.Tags) > 0 && !slices.Equal(c.Tags, cd.Tags) {
			cd.Tags, dirty = c.Tags, true
		}
		if cd.Position != target[c.Name] {
			cd.Position, dirty = parked(), true
		}
		if dirty {
			if err := r.Store.UpdateCaseDef(ctx, q, cd); err != nil {
				return nil, err
			}
		}
		all = append(all, cd)
	}
	nSpec := len(all)
	for i := range def.Cases { // the stored cases that are not in the spec
		cd := &def.Cases[i]
		if target[cd.Name] < len(spec.Cases) {
			continue
		}
		if cd.Position != target[cd.Name] {
			cd.Position = parked()
			if err := r.Store.UpdateCaseDef(ctx, q, cd); err != nil {
				return nil, err
			}
		}
		all = append(all, cd)
	}
	// Pass 2: every case that is parked goes to its position.
	for _, cd := range all {
		if cd.Position != target[cd.Name] {
			cd.Position = target[cd.Name]
			if err := r.Store.UpdateCaseDef(ctx, q, cd); err != nil {
				return nil, err
			}
		}
	}
	def.Cases = make([]core.CaseDef, nSpec)
	for i, cd := range all[:nSpec] {
		def.Cases[i] = *cd
	}
	return def, nil
}

// withCaseDefID returns a copy of m, a metadata struct (by pointer or value), with its CaseDefID set.
func withCaseDefID(m core.CaseDefMeta, id core.CaseDefID) core.CaseDefMeta {
	switch m := m.(type) {
	case *core.KnowledgeCaseDefMeta:
		c := *m
		c.CaseDefID = id
		return &c
	case core.KnowledgeCaseDefMeta:
		m.CaseDefID = id
		return &m
	case *core.TrainingCaseDefMeta:
		c := *m
		c.CaseDefID = id
		return &c
	case core.TrainingCaseDefMeta:
		m.CaseDefID = id
		return &m
	}
	return m
}

// sameJSON reports whether a and b have the same JSON (empty and nil values included: they are the same on disk).
func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

// EnsureExperiment returns the experiment def called name, creating it (with the description) if it does not exist. Its evals are
// the given ones, in this order, appended to the ones it has when they are not among them.
func (r *Runner) EnsureExperiment(ctx context.Context, name, description string, evals []core.EvalDefID) (*core.ExperimentDef, error) {
	return r.EnsureExperimentWith(ctx, name, description, nil, evals)
}

// EnsureExperimentWith is EnsureExperiment for a caller that has the settings under test: params are stored when the experiment is
// created (an existing experiment keeps its own).
func (r *Runner) EnsureExperimentWith(ctx context.Context, name, description string, params core.Params, evals []core.EvalDefID) (*core.ExperimentDef, error) {
	if name == "" {
		return nil, errors.New("ensure experiment: the name is required")
	}
	var def *core.ExperimentDef
	err := r.tx(ctx, func(q db.Execer) error {
		var err error
		def, err = r.Store.GetExperimentDefByName(ctx, q, name)
		if errors.Is(err, db.ErrNotFound) {
			def = &core.ExperimentDef{Name: name, Description: description, Params: params, EvalDefIDs: []core.EvalDefID{}}
			if err := r.Store.CreateExperimentDef(ctx, q, def); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		for _, id := range evals {
			if slices.Contains(def.EvalDefIDs, id) {
				continue
			}
			if err := r.Store.CreateExperimentDefEval(ctx, q, def.ID, id, len(def.EvalDefIDs)); err != nil {
				return err
			}
			def.EvalDefIDs = append(def.EvalDefIDs, id)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ensure experiment %q: %w", name, err)
	}
	return def, nil
}
