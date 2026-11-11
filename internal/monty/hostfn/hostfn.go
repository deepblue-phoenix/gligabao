// Package hostfn is the registry of the Go functions the code of the run_code tool may call (docs/agents/monty-code-execution.md,
// "Host functions"). The registry is an immutable value: a Spec declares one function, New validates a set of them, and the stubs for the
// sandbox, the list in the tool description and the decoding of the arguments all come from it. It has no dependency on the process code.
package hostfn

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Class says what a function reaches.
type Class int

const (
	// ClassRead reads the state of the run only.
	ClassRead Class = iota
	// ClassFetch reaches the outside world: it costs and is capped.
	ClassFetch
)

// Param is one parameter of a function. Type is one of str, int, float, bool, list[str].
type Param struct {
	Name     string
	Type     string
	Required bool
	Default  any
}

// Args are the decoded arguments of a call by parameter name, with the defaults filled in.
type Args map[string]any

// Str returns a string argument ("" if absent).
func (a Args) Str(name string) string { s, _ := a[name].(string); return s }

// Int returns an int argument (0 if absent).
func (a Args) Int(name string) int { n, _ := a[name].(int); return n }

// Env is the read-only view a handler gets for one run_code call: the state of the run as plain values (taken at the start of the call)
// and the narrow effects it may use. It never holds the database, the session or the model client.
type Env struct {
	Notes     string
	Questions []map[string]any
	Claims    []map[string]any
	// Fetch runs the tool of the agent called tool (web_search, fetch_page, query) with that input and returns the text it gives. Nil: none.
	Fetch func(ctx context.Context, tool string, input map[string]any) (string, error)
	// Log adds a line to the trace of the call (nil: dropped).
	Log func(line string)
}

// Handler is the implementation of a function: a function of the view and the arguments.
type Handler func(ctx context.Context, env Env, args Args) (any, error)

// Spec declares one function of the sandbox's world. Immutable.
type Spec struct {
	// Name is the Python name: lowercase snake_case, prefixed by the area (notes_, frontier_, claims_, kb_, web_).
	Name string
	// Doc is one line for the model.
	Doc    string
	Params []Param
	// Result is the Python return type, for the stub and the doc.
	Result  string
	Class   Class
	Handler Handler
}

// Registry is the immutable set of specs, in declaration order.
type Registry struct{ specs []Spec }

var (
	identRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	paramTypes = []string{"str", "int", "float", "bool", "list[str]"}
	resultType = []string{"str", "int", "float", "bool", "None", "dict", "list[dict]", "list[str]"}
)

// New validates the specs (unique valid names, consistent params, a handler) and returns the registry.
func New(specs ...Spec) (Registry, error) {
	var problems []string
	seen := map[string]bool{}
	for _, s := range specs {
		switch {
		case !identRe.MatchString(s.Name):
			problems = append(problems, fmt.Sprintf("%q is not a valid function name", s.Name))
		case seen[s.Name]:
			problems = append(problems, fmt.Sprintf("duplicate function %q", s.Name))
		}
		seen[s.Name] = true
		if s.Handler == nil {
			problems = append(problems, s.Name+": no handler")
		}
		if !slices.Contains(resultType, s.Result) {
			problems = append(problems, fmt.Sprintf("%s: result type %q is not supported", s.Name, s.Result))
		}
		names, optional := map[string]bool{}, false
		for _, p := range s.Params {
			switch {
			case !identRe.MatchString(p.Name) || names[p.Name]:
				problems = append(problems, fmt.Sprintf("%s: bad or duplicate parameter %q", s.Name, p.Name))
			case !slices.Contains(paramTypes, p.Type):
				problems = append(problems, fmt.Sprintf("%s.%s: type %q is not supported", s.Name, p.Name, p.Type))
			case p.Required && optional:
				problems = append(problems, fmt.Sprintf("%s.%s: a required parameter after an optional one", s.Name, p.Name))
			case !p.Required && p.Default == nil:
				problems = append(problems, fmt.Sprintf("%s.%s: an optional parameter needs a default", s.Name, p.Name))
			}
			names[p.Name] = true
			optional = optional || !p.Required
		}
	}
	if len(problems) > 0 {
		return Registry{}, fmt.Errorf("host functions: %s", strings.Join(problems, "; "))
	}
	return Registry{specs: slices.Clone(specs)}, nil
}

// Names are the function names in order.
func (r Registry) Names() []string {
	out := make([]string, len(r.specs))
	for i, s := range r.specs {
		out[i] = s.Name
	}
	return out
}

// Get returns the spec of a function.
func (r Registry) Get(name string) (Spec, bool) {
	i := slices.IndexFunc(r.specs, func(s Spec) bool { return s.Name == name })
	if i < 0 {
		return Spec{}, false
	}
	return r.specs[i], true
}

// Only returns the registry narrowed to the names (in the registry's order). An empty list keeps all; a name that is not in the registry is
// an error naming every unknown one.
func (r Registry) Only(names []string) (Registry, error) {
	if len(names) == 0 {
		return r, nil
	}
	have := r.Names()
	var unknown []string
	for _, n := range names {
		if !slices.Contains(have, n) {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return Registry{}, fmt.Errorf("unknown host function(s): %s (known: %s)", strings.Join(unknown, ", "), strings.Join(have, ", "))
	}
	return r.Filter(func(s Spec) bool { return slices.Contains(names, s.Name) }), nil
}

// Filter returns the registry of the specs for which keep is true.
func (r Registry) Filter(keep func(Spec) bool) Registry {
	var out []Spec
	for _, s := range r.specs {
		if keep(s) {
			out = append(out, s)
		}
	}
	return Registry{specs: out}
}

// Len is the number of functions.
func (r Registry) Len() int { return len(r.specs) }

func pyDefault(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	if v == nil {
		return "None"
	}
	if b, ok := v.(bool); ok {
		if b {
			return "True"
		}
		return "False"
	}
	if l, ok := v.([]string); ok && len(l) == 0 {
		return "[]"
	}
	return fmt.Sprint(v)
}

// Signature is the Python signature of a spec: name(a: str, b: int = 0) -> str.
func (s Spec) Signature() string {
	ps := make([]string, len(s.Params))
	for i, p := range s.Params {
		ps[i] = p.Name + ": " + p.Type
		if !p.Required {
			ps[i] += " = " + pyDefault(p.Default)
		}
	}
	return fmt.Sprintf("%s(%s) -> %s", s.Name, strings.Join(ps, ", "), s.Result)
}

// Stubs is the .pyi-style text of the registry (sent to the sandbox for the optional type check).
func (r Registry) Stubs() string {
	var b strings.Builder
	for _, s := range r.specs {
		b.WriteString("def " + s.Signature() + ": ...\n")
	}
	return b.String()
}

// Describe is the list appended to the tool description: one line per function.
func (r Registry) Describe() string {
	lines := make([]string, len(r.specs))
	for i, s := range r.specs {
		lines[i] = fmt.Sprintf("- %s: %s", s.Signature(), s.Doc)
	}
	return strings.Join(lines, "\n")
}

// Decode matches the call's positional and keyword arguments to the parameters, checks their types and fills the defaults.
func (s Spec) Decode(args []any, kwargs map[string]any) (Args, error) {
	if len(args) > len(s.Params) {
		return nil, fmt.Errorf("%s takes at most %d argument(s), got %d", s.Name, len(s.Params), len(args))
	}
	out := Args{}
	for i, v := range args {
		out[s.Params[i].Name] = v
	}
	for k, v := range kwargs {
		if !slices.ContainsFunc(s.Params, func(p Param) bool { return p.Name == k }) {
			return nil, fmt.Errorf("%s got an unexpected argument %q", s.Name, k)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("%s got several values for %q", s.Name, k)
		}
		out[k] = v
	}
	for _, p := range s.Params {
		v, ok := out[p.Name]
		if !ok {
			if p.Required {
				return nil, fmt.Errorf("%s needs the argument %q", s.Name, p.Name)
			}
			out[p.Name] = p.Default
			continue
		}
		c, err := coerce(p.Type, v)
		if err != nil {
			return nil, fmt.Errorf("%s: argument %q: %w", s.Name, p.Name, err)
		}
		out[p.Name] = c
	}
	return out, nil
}

// coerce checks a JSON-decoded value against a parameter type (numbers arrive as float64 or json.Number).
func coerce(typ string, v any) (any, error) {
	bad := fmt.Errorf("expected %s, got %T", typ, v)
	switch typ {
	case "str":
		if s, ok := v.(string); ok {
			return s, nil
		}
	case "bool":
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case "int":
		switch n := v.(type) {
		case int:
			return n, nil
		case float64:
			if n == float64(int(n)) {
				return int(n), nil
			}
		}
	case "float":
		switch n := v.(type) {
		case float64:
			return n, nil
		case int:
			return float64(n), nil
		}
	case "list[str]":
		if l, ok := v.([]any); ok {
			out := make([]string, len(l))
			for i, e := range l {
				s, ok := e.(string)
				if !ok {
					return nil, bad
				}
				out[i] = s
			}
			return out, nil
		}
	}
	return nil, bad
}

// Call runs the function name with the arguments of the call. A result over maxResultChars (0: no limit) is refused.
func (r Registry) Call(ctx context.Context, env Env, name string, args []any, kwargs map[string]any, maxResultChars int) (any, error) {
	s, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("%s is not a function you may call", name)
	}
	a, err := s.Decode(args, kwargs)
	if err != nil {
		return nil, err
	}
	v, err := s.Handler(ctx, env, a)
	if err != nil {
		return nil, err
	}
	if maxResultChars > 0 {
		b, merr := json.Marshal(v)
		if merr != nil {
			return nil, fmt.Errorf("%s: the result cannot be passed to the sandbox: %w", name, merr)
		}
		if n := len([]rune(string(b))); n > maxResultChars {
			return nil, fmt.Errorf("%s: the result is %d chars, over the limit of %d: ask for less (a smaller max_chars, a narrower filter)", name, n, maxResultChars)
		}
	}
	return v, nil
}
