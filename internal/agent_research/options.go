package agent_research

import (
	"strings"
	"unicode/utf8"
)

// Limits of Options.
const (
	// MaxTopicLen is the longest topic, in characters.
	MaxTopicLen = 2000
	// DefaultMaxSteps is the number of model calls an agent may make when the options say none; MaxMaxSteps is the most they may ask for.
	DefaultMaxSteps = 20
	MaxMaxSteps     = 40
	// maxModelLen is the longest model name.
	maxModelLen = 100
)

// Options are what a research is started with. Its JSON is the body of POST /v1/gb/research (with user_id).
type Options struct {
	// Topic is what to research; a question, or a subject for a report. Required.
	Topic string `json:"topic"`
	// MaxSteps is the most model calls the agent makes, the last of which writes the report (default 20, at most 40). More steps let
	// it search and read more, and cost more.
	MaxSteps int `json:"max_steps,omitempty"`
	// Model is the model the agent runs on; it must support Anthropic's web search tool. Empty is the server's default model.
	Model string `json:"model,omitempty"`
}

// resolve checks the options and returns them with the defaults filled in.
func (o Options) resolve() (Options, error) {
	o.Topic = strings.TrimSpace(o.Topic)
	o.Model = strings.TrimSpace(o.Model)
	switch {
	case o.Topic == "":
		return o, invalidErr("topic is required")
	case utf8.RuneCountInString(o.Topic) > MaxTopicLen:
		return o, invalidErr("topic is too long (at most 2000 characters)")
	case o.MaxSteps < 0 || o.MaxSteps > MaxMaxSteps:
		return o, invalidErr("max_steps must be between 1 and 40")
	case len(o.Model) > maxModelLen || strings.ContainsAny(o.Model, " \t\r\n"):
		return o, invalidErr("model is not a valid model name")
	}
	if o.MaxSteps == 0 {
		o.MaxSteps = DefaultMaxSteps
	}
	return o, nil
}

// ListOptions select the runs List returns.
type ListOptions struct {
	// Active keeps only the runs that are still working (core.Status.Active).
	Active bool
	// Status keeps only the runs with this status ("running", "completed" or "failed"); not together with Active.
	Status string
	// Limit is the most runs returned, newest first; 0 means DefaultListLimit, and it is cut to MaxListLimit.
	Limit int
}

// The number of runs List returns by default and at most.
const (
	DefaultListLimit = 50
	MaxListLimit     = 500
)
