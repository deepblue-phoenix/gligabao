package memory_core

import (
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology/ontospec"
)

// ErrInvalidGroundingMode means ontology_grounding_mode is neither "annotate" nor "strict".
var ErrInvalidGroundingMode = errors.New("invalid ontology grounding mode")

// ResolveGroundingMode trims and lowercases a grounding mode; empty is ontospec.ModeAnnotate.
func ResolveGroundingMode(mode string) (string, error) {
	switch m := strings.ToLower(strings.TrimSpace(mode)); m {
	case "":
		return ontospec.ModeAnnotate, nil
	case ontospec.ModeAnnotate, ontospec.ModeStrict:
		return m, nil
	default:
		return "", fmt.Errorf("%w: %q (use %q or %q)",
			ErrInvalidGroundingMode,
			mode,
			ontospec.ModeAnnotate,
			ontospec.ModeStrict)
	}
}

// WithOntologyGrounding makes Remember ground the graph it extracts against the user's ontologies (the ground_graph stage; off by
// default). names are the ontologies to use (all of the user's if empty; they are merged into one compiled ontology for the call); mode is
// ontospec.ModeAnnotate (the default: tag what matches, flag the rest, drop nothing) or ontospec.ModeStrict (drop what does not
// conform). Validate the mode with ResolveGroundingMode first. See docs/ontologies/ontologies.md.
func WithOntologyGrounding(names []string,
	mode string) Option {
	return func(o *CallOptions) {
		o.OntologyGrounding, o.Ontologies, o.OntologyMode = true, names, mode
	}
}
