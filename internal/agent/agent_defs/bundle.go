package agent_defs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// Bundle is an agent with everything it needs, as one JSON document: the definition, with
// the prompts and the skills as text. A server that runs an agent somewhere that has no agent_defs directory of its own (the Modal
// container) sends it a bundle; gligabao_agent --agent-bundle reads it. The definitions keep their hashes, which are checked on load.
type Bundle struct {
	// Agent is the name of the definition the bundle is for (the root of the research).
	Agent string `json:"agent"`
	Defs  []*Def `json:"defs"`
}

// Bundle makes the bundle of the agent called name: its definition and, through its roles, all the definitions it may start.
func (r *Registry) Bundle(name string) (*Bundle, error) {
	root, ok := r.defs[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", name)
	}
	b := &Bundle{Agent: name, Defs: []*Def{root}}
	if _, err := b.JSON(); err != nil {
		return nil, err
	}
	return b, nil
}

// JSON is the bundle as JSON, at most MaxBundleBytes.
func (b *Bundle) JSON() ([]byte, error) {
	out, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	if len(out) > MaxBundleBytes {
		return nil, fmt.Errorf("the bundle of agent %q is %d bytes, more than the %d allowed: shorten the prompts or the skills", b.Agent, len(out), MaxBundleBytes)
	}
	return out, nil
}

// ParseBundle reads a bundle and makes the registry of its definitions, checking them as Load does (and that each hash is that of its
// content). It returns the registry and the name of the agent the bundle is for.
func ParseBundle(data []byte) (*Registry, string, error) {
	if len(data) > MaxBundleBytes {
		return nil, "", fmt.Errorf("agent bundle: %d bytes, more than the %d allowed", len(data), MaxBundleBytes)
	}
	var b Bundle
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, "", fmt.Errorf("agent bundle: invalid JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, "", errors.New("agent bundle: invalid JSON: more than one value")
	}
	var errs []error
	seen := map[string]bool{}
	for _, d := range b.Defs {
		if d == nil {
			errs = append(errs, errors.New("agent bundle: a definition is null"))
			continue
		}
		want := d.Hash
		normalize(d)
		for _, p := range check(d) {
			errs = append(errs, fmt.Errorf("agent bundle: %s: %s", d.Name, p))
		}
		if got := hashOf(d); got != want {
			errs = append(errs, fmt.Errorf("agent bundle: %s: the hash does not match the content", d.Name))
		}
		if seen[d.Name] {
			errs = append(errs, fmt.Errorf("agent bundle: %s is in the bundle twice", d.Name))
		}
		seen[d.Name] = true
	}
	if err := errors.Join(errs...); err != nil {
		return nil, "", err
	}
	reg, err := newRegistry("", b.Defs)
	if err != nil {
		return nil, "", fmt.Errorf("agent bundle: %w", err)
	}
	if b.Agent == "" {
		b.Agent = DefaultName
	}
	if _, ok := reg.Get(b.Agent); !ok {
		return nil, "", fmt.Errorf("agent bundle: it has no agent called %q", b.Agent)
	}
	return reg, b.Agent, nil
}

// LoadBundle reads the bundle file at path (see ParseBundle).
func LoadBundle(path string) (*Registry, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("agent bundle: %w", err)
	}
	return ParseBundle(data)
}
