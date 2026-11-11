package agent_research

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// Options are what a research is started with. Its JSON is the body of POST /v1/gb/research (the user is the gb-user-id header).
type Options struct {
	// Topic is what to research; a question, or a subject for a report. Required.
	Topic string `json:"topic"`
	// Agent is the name of the agent definition to run (see package agent_defs; GET /v1/gb/research/agents lists them): its prompt,
	// tools, search and knowledge settings, cost caps and sub-agents. Empty is "default", the built-in research agent. AgentVersion, if
	// given, is a constraint the definition's version must meet ("1.2.0", ">=1.0", "^1.2"): a research that must run a known version.
	Agent        string `json:"agent,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
	// MaxSteps is the most model calls the agent makes, the last of which writes the report (default: the max_steps of its definition,
	// 20 for the built-in agent; at most 40). More steps let it search and read more, and cost more.
	MaxSteps int `json:"max_steps,omitempty"`
	// Model is the model the agent runs on; it must support Anthropic's web search tool. Empty is the model of the definition, else the
	// server's default model. It is the model of the main agent: a sub-agent runs on the model of its own definition, else on its parent's.
	Model string `json:"model,omitempty"`
	// MaxCostUSD caps what the whole research may spend, the sub-agents included (0: only the caps of the definition and of the server
	// apply). It can lower those caps, never raise them. When it is reached the agents stop searching and write their reports, and the
	// report says so.
	MaxCostUSD float64 `json:"max_cost_usd,omitempty"`
	// Dataset is the dataset of the user's knowledge base the agent queries and remembers into, when the server gives the agents one
	// (default "main"). It is created by the first remember if it does not exist. It is not stored with the run.
	Dataset string `json:"dataset,omitempty"`
	// NoRemember turns off the automatic remember call at the end of an investigative research: by default the final report is
	// remembered (sync) into Dataset when the agent has written it. The remember tool of the agent is not affected.
	NoRemember bool `json:"no_remember,omitempty"`
	// Runner is where the agent runs: core.RunnerLocal ("local", the default: in this process, like always) or core.RunnerModal ("modal":
	// the gligabao_agent binary in a Modal container, started through the sidecar, see Manager.UseRemote). With "modal" the agent works
	// with a knowledge base of its own in the container, not the user's (so Dataset has no effect there).
	Runner string `json:"runner,omitempty"`
	// Investigate are the knobs of the investigative loop (max_iterations, max_total_tokens, min_sources, ...; see agent_defs.Investigate), over
	// those of the definition. They only take effect for an agent with loop = "investigative"; with another loop they are an error. With that
	// loop max_iterations replaces max_steps (which is then not allowed) and max_cost_usd caps the cost as always.
	Investigate *agent_defs.Investigate `json:"investigate,omitempty"`

	// datasetGiven says that the request named a dataset (a definition may have a default of its own); treeCostCap is the cap on the cost
	// of the whole research that results from the request, the definition and the server. Both are set by the Manager.
	datasetGiven bool
	treeCostCap  float64
}

// ListOptions select the runs List returns.
type ListOptions struct {
	// Active keeps only the runs that are still working (core.Status.Active).
	Active bool
	// Finished keeps only the runs that have ended (completed, failed or stopped); not together with Active or Status.
	Finished bool
	// Status keeps only the runs with this status ("running", "completed", "failed" or "stopped"); not together with Active.
	Status string
	// ParentID, if not 0, keeps only the sub-agents of that run.
	ParentID int64
	// RootsOnly keeps only the researches a user started, not the sub-agents.
	RootsOnly bool
	// Limit is the most runs returned, newest first; 0 means DefaultListLimit, and it is cut to MaxListLimit.
	Limit int
}

// Limits of Options.
const (
	// MaxTopicLen is the longest topic, in characters.
	MaxTopicLen = 2000
	// DefaultMaxSteps is the number of model calls the built-in agent may make when the options say none (the other agents have the
	// max_steps of their definition); MaxMaxSteps is the most any agent may be given.
	DefaultMaxSteps = agent_defs.DefaultMaxSteps
	MaxMaxSteps     = agent_defs.MaxStepsCeiling
	// maxModelLen is the longest model name.
	maxModelLen = 100
)

// resolve checks the options and returns them with the defaults filled in. The steps and the model default to those of the agent
// definition, which the Manager applies once it has found it (applyDef).
func (o Options) resolve() (Options, error) {
	o.Topic = strings.TrimSpace(o.Topic)
	o.Model = strings.TrimSpace(o.Model)
	o.Runner = strings.ToLower(strings.TrimSpace(o.Runner))
	o.Agent, o.AgentVersion = strings.TrimSpace(o.Agent), strings.TrimSpace(o.AgentVersion)
	o.datasetGiven = strings.TrimSpace(o.Dataset) != ""
	if o.Runner == "" {
		o.Runner = core.RunnerLocal
	}
	dataset, err := memory_core.ResolveDatasetName(o.Dataset)
	if err != nil {
		return o, invalidErr(err.Error())
	}
	o.Dataset = dataset
	switch {
	case o.Topic == "":
		return o, invalidErr("topic is required")
	case utf8.RuneCountInString(o.Topic) > MaxTopicLen:
		return o, invalidErr("topic is too long (at most 2000 characters)")
	case o.MaxSteps < 0 || o.MaxSteps > MaxMaxSteps:
		return o, invalidErr("max_steps must be between 1 and 40")
	case o.Investigate != nil && len(o.Investigate.Check()) > 0:
		return o, invalidErr(o.Investigate.Check()[0])
	case o.Runner != core.RunnerLocal && o.Runner != core.RunnerModal:
		return o, invalidErr("runner must be local or modal")
	case len(o.Model) > maxModelLen || strings.ContainsAny(o.Model, " \t\r\n"):
		return o, invalidErr("model is not a valid model name")
	case o.Agent != "" && !agent_defs.ValidName(o.Agent):
		return o, invalidErr("agent is not a valid agent name")
	case len(o.AgentVersion) > maxModelLen:
		return o, invalidErr("agent_version is not a valid version constraint")
	case o.MaxCostUSD < 0 || math.IsNaN(o.MaxCostUSD) || math.IsInf(o.MaxCostUSD, 0):
		return o, invalidErr("max_cost_usd must be 0 (no cap) or a positive number of USD")
	}
	return o, nil
}

// The number of runs List returns by default and at most.
const (
	DefaultListLimit = 50
	MaxListLimit     = 500
)

// investigateKnobs are the knobs of the investigative loop of a run: those of the definition, with the options' over them (0 is the default).
func (o Options) investigateKnobs(def *agent_defs.Def) agent_defs.Investigate {
	var base, over agent_defs.Investigate
	if def.Investigate != nil {
		base = *def.Investigate
	}
	if o.Investigate != nil {
		over = *o.Investigate
	}
	return base.Merge(over)
}
