package agent_defs

import (
	"fmt"
	"sort"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/agent/prompts"
)

// builtinSource is the Def.Source of the built-in default agent.
const builtinSource = "builtin"

// Builtin returns a new copy of the built-in default agent: today's research agent (web_search, fetch_url, the knowledge base, 20
// steps), uncapped. A file called default.toml replaces it.
func Builtin() *Def {
	d := &Def{
		Name:         DefaultName,
		Version:      "1.0.0",
		Description:  "The built-in deep-research agent: searches the web, reads pages, works with the knowledge base, and writes a report.",
		SystemPrompt: prompts.Research,
		Source:       builtinSource,
	}
	normalize(d)
	d.Hash = hashOf(d)
	return d
}

// InvestigatorName is the name of the built-in investigative agent (the one the admin UI starts for "investigative").
const InvestigatorName = "investigator"

// investigatorPrompt is the system prompt of the built-in investigative agent.
const investigatorPrompt = "You are an investigative researcher. Break the task into questions, answer them with evidence from the web (prefer primary sources and several independent domains), challenge your own claims before you rely on them, and say plainly what you could not establish. Write the final report with dates and the source URLs."

// BuiltinInvestigator returns a new copy of the built-in investigative agent: the deep-research loop (frontier of questions, notes, claims,
// challenges, then a synthesis) with web_search, fetch_url and the knowledge base, working alone, capped at 2 USD. A file called
// investigator.toml replaces it.
func BuiltinInvestigator() *Def {
	d := &Def{
		Name:         InvestigatorName,
		Version:      "1.0.0",
		Description:  "The built-in investigative agent: explores a frontier of questions, challenges its own claims, then writes a sourced report.",
		SystemPrompt: investigatorPrompt,
		Loop:         LoopInvestigative,
		Limits: Limits{
			MaxCostUSD: 2,
		},
		Source: builtinSource,
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

// newRegistry makes the registry of defs. The built-in default is added unless defs has one (so is the built-in investigator). The definitions must have been through normalize and check, as Load and ParseBundle do.
func newRegistry(dir string,
	defs []*Def) (*Registry, error) {
	r := &Registry{
		defs: map[string]*Def{},
		dir:  dir,
	}
	for _, d := range defs {
		r.defs[d.Name] = d
	}
	if _, ok := r.defs[DefaultName]; !ok {
		r.defs[DefaultName] = Builtin()
	}
	if _, ok := r.defs[InvestigatorName]; !ok {
		r.defs[InvestigatorName] = BuiltinInvestigator()
	}
	return r, nil
}

func (d *Def) sourceName() string {
	if d.Source != "" {
		return d.Source
	}
	return d.Name
}

func (r *Registry) names() []string {
	names := make([]string,
		0,
		len(r.defs))
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
	out := make([]*Def,
		0,
		len(r.defs))
	for _, n := range r.names() {
		out = append(out, r.defs[n])
	}
	return out
}

// Dir is the directory the definitions were loaded from ("" for the built-in registry and for one made from a bundle).
func (r *Registry) Dir() string { return r.dir }

// Resolve returns the definition called name ("" is the default), and checks it against a version constraint ("" for none; "1.2.0"
// is exactly that version, see parseConstraint for the syntax). The error says which agents there are.
func (r *Registry) Resolve(name,
	constraint string) (*Def, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultName
	}
	d, ok := r.defs[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q (available: %s)",
			name,
			strings.Join(r.names(), ", "))
	}
	ok, err := satisfies(d.Version, strings.TrimSpace(constraint))
	if err != nil {
		return nil, fmt.Errorf("agent_version: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("agent %q is at version %s, which does not meet %q",
			name,
			d.Version,
			strings.TrimSpace(constraint))
	}
	return d, nil
}
