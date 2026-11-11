package agent_defs

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/agent/prompts"
)

// builtinSource is the Def.Source of the built-in default agent.
const builtinSource = "builtin"

// Builtin returns a new copy of the built-in default agent: today's research agent (web_search, fetch_url, the knowledge base, 20
// steps, free-form sub-agents that message each other), uncapped. A file called default.toml replaces it.
func Builtin() *Def {
	d := &Def{
		Name:         DefaultName,
		Version:      "1.0.0",
		Description:  "The built-in deep-research agent: searches the web, reads pages, works with the knowledge base, may spawn sub-agents, and writes a report.",
		SystemPrompt: prompts.Research,
		Spawn:        Spawn{Enabled: true},
		Source:       builtinSource,
	}
	normalize(d)
	d.Hash = hashOf(d)
	return d
}

// BuiltinRegistry returns a registry of the built-in default agent alone.
func BuiltinRegistry() *Registry {
	r, err := newRegistry("", nil)
	if err != nil {
		panic("agent_defs: " + err.Error()) // the built-in agent is valid
	}
	return r
}

// Registry is the loaded definitions, by name. It is immutable once made, so it can be shared and read from any goroutine.
type Registry struct {
	defs map[string]*Def
	dir  string
}

// NewRegistry checks defs against each other (every role names a definition that exists and meets its version constraint, no
// definition starts itself through its roles, a role's steps fit its definition) and returns the registry. The built-in default is
// added unless defs has one. The definitions must have been through normalize and check, as Load and ParseBundle do.
func newRegistry(dir string, defs []*Def) (*Registry, error) {
	r := &Registry{defs: map[string]*Def{}, dir: dir}
	for _, d := range defs {
		r.defs[d.Name] = d
	}
	if _, ok := r.defs[DefaultName]; !ok {
		r.defs[DefaultName] = Builtin()
	}
	var errs []error
	for _, d := range r.List() {
		for _, e := range r.checkRoles(d) {
			errs = append(errs, fmt.Errorf("%s: %s", d.sourceName(), e))
		}
	}
	if len(errs) == 0 {
		for _, e := range r.findCycles() {
			errs = append(errs, e)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return r, nil
}

func (d *Def) sourceName() string {
	if d.Source != "" {
		return d.Source
	}
	return d.Name
}

func (r *Registry) checkRoles(d *Def) []string {
	var errs []string
	for i, role := range d.Spawn.Roles {
		key := fmt.Sprintf("spawn.roles[%d] (%s)", i, role.Name)
		child, ok := r.defs[role.Agent]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s.agent: no agent called %q (known: %s)", key, role.Agent, strings.Join(r.names(), ", ")))
			continue
		}
		if okVer, err := satisfies(child.Version, role.Version); err != nil {
			errs = append(errs, fmt.Sprintf("%s.version: %v", key, err))
		} else if !okVer {
			errs = append(errs, fmt.Sprintf("%s.version: agent %q is at version %s, which does not meet %q", key, child.Name, child.Version, role.Version))
		}
		def, limit := role.Steps(child)
		if def > limit || limit > MaxStepsCeiling {
			errs = append(errs, fmt.Sprintf("%s: the steps of the role (default %d, limit %d) do not fit: the limit is at most %d and not below the default; agent %q has max_steps %d", key, def, limit, MaxStepsCeiling, child.Name, child.MaxSteps))
		}
	}
	return errs
}

// findCycles reports the definitions that start themselves through their roles (a research tree with such a loop is a mistake: the
// depth ceiling would stop it, but only after it spent money).
func (r *Registry) findCycles() []error {
	const (
		white = iota
		grey
		black
	)
	state := map[string]int{}
	var errs []error
	var path []string
	var visit func(name string)
	visit = func(name string) {
		state[name] = grey
		path = append(path, name)
		for _, role := range r.defs[name].Spawn.Roles {
			switch state[role.Agent] {
			case white:
				visit(role.Agent)
			case grey:
				i := 0
				for path[i] != role.Agent {
					i++
				}
				loop := append(append([]string(nil), path[i:]...), role.Agent)
				errs = append(errs, fmt.Errorf("%s: spawn.roles: %s starts itself through its roles", r.defs[name].sourceName(), strings.Join(loop, " -> ")))
			}
		}
		path = path[:len(path)-1]
		state[name] = black
	}
	for _, name := range r.names() {
		if state[name] == white {
			visit(name)
		}
	}
	return errs
}

func (r *Registry) names() []string {
	names := make([]string, 0, len(r.defs))
	for n := range r.defs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Get returns the definition called name.
func (r *Registry) Get(name string) (*Def, bool) {
	d, ok := r.defs[name]
	return d, ok
}

// Default returns the agent a request that names none runs.
func (r *Registry) Default() *Def { return r.defs[DefaultName] }

// List returns the definitions, by name.
func (r *Registry) List() []*Def {
	out := make([]*Def, 0, len(r.defs))
	for _, n := range r.names() {
		out = append(out, r.defs[n])
	}
	return out
}

// Dir is the directory the definitions were loaded from ("" for the built-in registry and for one made from a bundle).
func (r *Registry) Dir() string { return r.dir }

// Resolve returns the definition called name ("" is the default), and checks it against a version constraint ("" for none; "1.2.0"
// is exactly that version, see Role.Version for the syntax). The error says which agents there are.
func (r *Registry) Resolve(name, constraint string) (*Def, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultName
	}
	d, ok := r.defs[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q (available: %s)", name, strings.Join(r.names(), ", "))
	}
	ok, err := satisfies(d.Version, strings.TrimSpace(constraint))
	if err != nil {
		return nil, fmt.Errorf("agent_version: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("agent %q is at version %s, which does not meet %q", name, d.Version, strings.TrimSpace(constraint))
	}
	return d, nil
}

// RoleDef returns the definition a role of d starts.
func (r *Registry) RoleDef(role Role) (*Def, error) {
	d, ok := r.defs[role.Agent]
	if !ok {
		return nil, fmt.Errorf("role %q: no agent called %q", role.Name, role.Agent)
	}
	return d, nil
}
