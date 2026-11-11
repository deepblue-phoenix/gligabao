// Package agent_defs is the declarative definition of the deep-research agents: an agent is described in a TOML or JSON file (name,
// description, version, system prompt, model, limits, tools, skills, search and knowledge settings, cost caps, and when and which
// sub-agents it starts), loaded once at startup from the agent_defs directory next to the binary, validated, and kept as immutable
// structs in a Registry. The research manager (package agent_research) builds each agent, root or sub-agent, from one Def; the
// standalone gligabao_agent binary loads the same files, or a Bundle that a server made for it. See docs/agents/agent-defs.md.
//
// The package depends on the standard library, BurntSushi/toml, logrus and the prompts only, so that the standalone binary stays small.
package agent_defs

import (
	"fmt"
	"strings"
	"text/template"
	"time"
)

// The format version of the files (Def.Schema).
const SchemaVersion = 1

// DefaultName is the name of the built-in agent, which is today's research agent; a request that names no agent runs it.
const DefaultName = "default"

// Defaults of the fields a file leaves out, and the hard ceilings that no file can raise: they bound what one research can cost.
const (
	DefaultMaxSteps  = 20
	DefaultMaxTokens = 8192
	// MaxStepsCeiling is the most model calls any agent may be given (a definition, a request or a parent that spawns).
	MaxStepsCeiling  = 40
	maxTokensCeiling = 64000
	// DefaultSearchUses is the most searches one web_search call makes.
	DefaultSearchUses = 2
	maxSearchUses     = 10
	// DefaultMaxPageChars is how much of a page fetch_url gives the model.
	DefaultMaxPageChars = 15000
	minPageChars        = 1000
	maxPageChars        = 100000

	// MaxSubagentDepth is how many generations of sub-agents a research may have, MaxSubagentsPerAgent the most sub-agents one agent
	// spawns and MaxSubagentsPerTree the most in the whole tree of a research. A definition may lower them, never raise them.
	MaxSubagentDepth     = 2
	MaxSubagentsPerAgent = 5
	MaxSubagentsPerTree  = 10
	// DefaultSubagentSteps is the number of model calls a sub-agent may make when nothing says otherwise (a parent that spawns without
	// a role of its definition).
	DefaultSubagentSteps = 10

	// MaxBundleBytes is the most a Bundle may be as JSON.
	MaxBundleBytes = 1 << 20
	maxPromptBytes = 256 << 10
	maxSkillBytes  = 64 << 10
	maxTextChars   = 2000
)

// The tools a definition may list in Def.Tools. The tools of the knowledge base (query, remember) come from Knowledge.Access, those of the
// sub-agents from Spawn.
const (
	ToolWebSearch = "web_search"
	ToolFetchURL  = "fetch_url"
)

// KnownTools are the tools of Def.Tools, in the order they are given to the agent.
var KnownTools = []string{ToolWebSearch, ToolFetchURL}

// Values of Def.Loop.
const (
	// LoopSimple: the loop of an agent that searches and reads until it writes the report, in at most max_steps model calls. The default.
	LoopSimple = "simple"
	// LoopInvestigative: the exploration loop on a question frontier with notes, challenges and gates, then the synthesis loop.
	LoopInvestigative = "investigative"
)

// Investigate are the knobs of the investigative loop ([investigate] in a file); 0 means the default of the design (see
// docs/agents/deep-research-loop.md, Configuration), and none of them has an effect with another loop. The run options can override them.
type Investigate struct {
	MaxDepth               int `toml:"max_depth" json:"max_depth,omitempty"`
	MaxAttempts            int `toml:"max_attempts" json:"max_attempts,omitempty"`
	MaxQuestions           int `toml:"max_questions" json:"max_questions,omitempty"`
	MaxIterations          int `toml:"max_iterations" json:"max_iterations,omitempty"`
	MaxTotalTokens         int `toml:"max_total_tokens" json:"max_total_tokens,omitempty"`
	KeepTurns              int `toml:"keep_turns" json:"keep_turns,omitempty"`
	ExtractionThreshold    int `toml:"extraction_threshold" json:"extraction_threshold,omitempty"`
	NotesMaxTokens         int `toml:"notes_max_tokens" json:"notes_max_tokens,omitempty"`
	MinSources             int `toml:"min_sources" json:"min_sources,omitempty"`
	MinDomains             int `toml:"min_domains" json:"min_domains,omitempty"`
	MinIndependentSources  int `toml:"min_independent_sources" json:"min_independent_sources,omitempty"`
	MinChallenges          int `toml:"min_challenges" json:"min_challenges,omitempty"`
	MaxChallengesPerTarget int `toml:"max_challenges_per_target" json:"max_challenges_per_target,omitempty"`
	MaxReopens             int `toml:"max_reopens" json:"max_reopens,omitempty"`
	// MaxParallel is the workers on the frontier; only 1 is implemented.
	MaxParallel int `toml:"max_parallel" json:"max_parallel,omitempty"`
	// SynthesisReserve is the fraction (0 to 0.5) of the iteration, token and cost limits that exploration cannot spend; GatePriority (0 to
	// 1) the priority from which a pending question stops the exploration gate.
	SynthesisReserve float64 `toml:"synthesis_reserve" json:"synthesis_reserve,omitempty"`
	GatePriority     float64 `toml:"gate_priority" json:"gate_priority,omitempty"`
}

// Code is the [code] table of an agent that runs the investigative loop: the run_code tool, Python in a sandbox (Pydantic Monty,
// docs/agents/monty-code-execution.md). Without the table, or with enabled = false, the agent has no code tool. 0 means the default
// (Code.WithDefaults), except MaxCalls, where 0 is no limit and nil the default.
type Code struct {
	Enabled        bool     `toml:"enabled" json:"enabled"`
	TimeoutS       int      `toml:"timeout_s" json:"timeout_s,omitempty"`
	MaxMemoryMB    int      `toml:"max_memory_mb" json:"max_memory_mb,omitempty"`
	MaxRecursion   int      `toml:"max_recursion" json:"max_recursion,omitempty"`
	MaxOutputChars int      `toml:"max_output_chars" json:"max_output_chars,omitempty"`
	MaxCalls       *int     `toml:"max_calls" json:"max_calls,omitempty"`
	MaxHostCalls   int      `toml:"max_host_calls" json:"max_host_calls,omitempty"`
	MaxResultChars int      `toml:"max_result_chars" json:"max_result_chars,omitempty"`
	Functions      []string `toml:"functions" json:"functions,omitempty"`
	TypeCheck      bool     `toml:"type_check" json:"type_check,omitempty"`
}

// Defaults of Code.
const (
	DefaultCodeTimeoutS       = 10
	DefaultCodeMaxMemoryMB    = 128
	DefaultCodeMaxRecursion   = 500
	DefaultCodeMaxOutputChars = 4000
	DefaultCodeMaxCalls       = 30
	DefaultCodeMaxHostCalls   = 20
	DefaultCodeMaxResultChars = 20000
)

// WithDefaults returns c with every unset number replaced by its default (MaxCalls is then never nil).
func (c Code) WithDefaults() Code {
	pick := func(v, def int) int {
		if v > 0 {
			return v
		}
		return def
	}
	c.TimeoutS, c.MaxMemoryMB, c.MaxRecursion = pick(c.TimeoutS, DefaultCodeTimeoutS), pick(c.MaxMemoryMB, DefaultCodeMaxMemoryMB), pick(c.MaxRecursion, DefaultCodeMaxRecursion)
	c.MaxOutputChars, c.MaxHostCalls, c.MaxResultChars = pick(c.MaxOutputChars, DefaultCodeMaxOutputChars), pick(c.MaxHostCalls, DefaultCodeMaxHostCalls), pick(c.MaxResultChars, DefaultCodeMaxResultChars)
	if c.MaxCalls == nil {
		n := DefaultCodeMaxCalls
		c.MaxCalls = &n
	}
	return c
}

// CodeEnabled reports whether the agent has the run_code tool.
func (d *Def) CodeEnabled() bool { return d.Code != nil && d.Code.Enabled }

// Investigative reports whether the agent runs the investigative loop.
func (d *Def) Investigative() bool { return d.Loop == LoopInvestigative }

// Values of Knowledge.Access.
const (
	// AccessReadWrite: the agent has query and remember (when the knowledge base is there at all). The default.
	AccessReadWrite = "read_write"
	// AccessReadOnly: it has query only.
	AccessReadOnly = "read_only"
	// AccessNone: it has neither.
	AccessNone = "none"
)

// Values of Role.Trigger.
const (
	// TriggerOnDemand: the parent's model decides when to start the sub-agent (spawn_subagent with the role), guided by Role.When.
	TriggerOnDemand = "on_demand"
	// TriggerOnStart: the runtime starts it before the parent's first model call, with Role.TaskTemplate as its task.
	TriggerOnStart = "on_start"
)

// Values of Search.Recency.
const (
	RecencyAny   = "any"
	RecencyDay   = "day"
	RecencyWeek  = "week"
	RecencyMonth = "month"
	RecencyYear  = "year"
)

// Def is one agent as a file defines it, resolved: the prompt and the skills are the text (not the paths), the defaults are filled in
// and the checks are done. It is immutable once loaded: share it freely. The same struct, as JSON, is what a Bundle carries to another
// process.
type Def struct {
	// Schema is the format version of the file (SchemaVersion).
	Schema int `json:"schema"`
	// Name identifies the agent (lowercase letters, digits, - and _; at most 40); unique in a Registry. Version is its own semantic
	// version; Description says what the agent is for.
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	// SystemPrompt is the base of the system prompt (it replaces the built-in research prompt). What the tools need to be told (the
	// knowledge base, the sub-agents, the siblings) is added by the runtime from the settings below.
	SystemPrompt string `json:"system_prompt"`
	// Model is the model it runs on; "" inherits (a root: the request's model, else the server's default; a sub-agent: its parent's).
	Model string `json:"model"`
	// MaxSteps is the most model calls it makes (the last writes the report) and MaxTokens bounds each answer.
	MaxSteps  int `json:"max_steps"`
	MaxTokens int `json:"max_tokens"`
	// Tools are the tools it has of KnownTools.
	Tools []string `json:"tools"`
	// Skills are the skill files it loads (Markdown from the skills directory), appended to its system prompt in this order.
	Skills    []Skill   `json:"skills,omitempty"`
	Search    Search    `json:"search"`
	Knowledge Knowledge `json:"knowledge"`
	Limits    Limits    `json:"limits"`
	Spawn     Spawn     `json:"spawn"`
	// Loop is the agent loop: LoopSimple ("" is the same: the default) or LoopInvestigative, the deep-research loop of
	// docs/agents/deep-research-loop.md. Investigate holds the knobs of the investigative loop (nil: all defaults); it is an error to set it
	// with the simple loop.
	Loop        string       `json:"loop,omitempty"`
	Investigate *Investigate `json:"investigate,omitempty"`
	// Code is the run_code tool of the investigative loop (nil: no code tool); it is an error to set it with the simple loop.
	Code *Code `json:"code,omitempty"`
	// Schedule says when the server runs the agent by itself (nil: only when a research asks for it). It is not part of Hash: when an
	// agent runs does not change what it is.
	Schedule *Schedule `json:"schedule,omitempty"`

	// Source is the file the definition was loaded from ("builtin" for the built-in one) and Hash the SHA-256 of everything else, so
	// that a run records exactly which definition it ran even when a file changed without a version bump.
	Source string `json:"source,omitempty"`
	Hash   string `json:"hash"`
}

// Defaults and limits of Schedule.
const (
	// DefaultScheduleTimezone is the time zone of a schedule that names none.
	DefaultScheduleTimezone = "UTC"
	// DefaultScheduleUser is the user whose runs a schedule makes when it names none.
	DefaultScheduleUser = "scheduler"
	// MinScheduleInterval is the shortest time allowed between two scheduled runs: every run is paid for.
	MinScheduleInterval = 5 * time.Minute
)

// Schedule makes the server run the agent on a CRON schedule: at every time the expression matches, a research of Topic is started
// with this agent, for User. A run that would start while the previous scheduled run of the agent is still working is skipped, and
// so is every time the server was down (nothing is caught up).
type Schedule struct {
	// Cron is the expression of five fields (minute hour day-of-month month day-of-week) or a macro like @daily; see ParseCron.
	Cron string `toml:"cron" json:"cron"`
	// Timezone is the IANA time zone the expression is read in (default "UTC").
	Timezone string `toml:"timezone" json:"timezone"`
	// Topic is the topic of every scheduled research: a Go text/template with {{.Date}} (YYYY-MM-DD) and {{.Time}} (RFC 3339), both
	// of the moment the run is due, in Timezone.
	Topic string `toml:"topic" json:"topic"`
	// User is the user_id the runs (and the knowledge base the agent writes to) belong to (default "scheduler").
	User string `toml:"user" json:"user"`
	// Paused keeps the schedule in the file without running it.
	Paused bool `toml:"paused" json:"paused,omitempty"`
}

// Next returns the first time after t that the schedule runs the agent (zero if never, or if it is invalid: a loaded definition
// never is), in the schedule's time zone.
func (s *Schedule) Next(t time.Time) time.Time {
	c, err := ParseCron(s.Cron)
	if err != nil {
		return time.Time{}
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}
	}
	return c.Next(t.In(loc))
}

// TopicAt is the topic of the run that is due at t.
func (s *Schedule) TopicAt(t time.Time) (string, error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return "", err
	}
	t = t.In(loc)
	tpl, err := template.New("topic").Option("missingkey=error").Parse(s.Topic)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := tpl.Execute(&sb, struct{ Date, Time string }{t.Format("2006-01-02"), t.Format(time.RFC3339)}); err != nil {
		return "", fmt.Errorf("schedule.topic: %w", err)
	}
	return strings.TrimSpace(sb.String()), nil
}

// Skill is a skill file: a Markdown document of the skills directory that the agent gets in its system prompt.
type Skill struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Search is how the agent searches the web: the domains it may use (enforced), what it prefers (told to it), and limits. The domain
// lists are host names ("example.com"; a subdomain of a listed host matches too).
type Search struct {
	// AllowedDomains: only these (HARD, web_search and fetch_url, not together with BlockedDomains for web_search). BlockedDomains: never these (HARD).
	AllowedDomains []string `toml:"allowed_domains" json:"allowed_domains,omitempty"`
	BlockedDomains []string `toml:"blocked_domains" json:"blocked_domains,omitempty"`
	// PreferredDomains, Recency, Language and Instructions are SOFT: they are in the prompts, nothing enforces them.
	PreferredDomains []string `toml:"preferred_domains" json:"preferred_domains,omitempty"`
	Recency          string   `toml:"recency" json:"recency,omitempty"`
	Language         string   `toml:"language" json:"language,omitempty"`
	Instructions     string   `toml:"instructions" json:"instructions,omitempty"`
	// Location is where the searcher is: it makes the results local (web_search's user_location).
	Location *Location `toml:"location" json:"location,omitempty"`
	// MaxUsesPerCall is the most searches one web_search call makes (default 2). MaxCalls is the most web_search calls in a run (0: no limit).
	MaxUsesPerCall int `toml:"max_uses_per_call" json:"max_uses_per_call"`
	MaxCalls       int `toml:"max_calls" json:"max_calls,omitempty"`
	// Fetch are the settings of fetch_url.
	Fetch Fetch `toml:"fetch" json:"fetch"`
}

// Location is the place a search is made from.
type Location struct {
	Country  string `toml:"country" json:"country,omitempty"` // ISO 3166-1 alpha-2, "DE"
	Region   string `toml:"region" json:"region,omitempty"`
	City     string `toml:"city" json:"city,omitempty"`
	Timezone string `toml:"timezone" json:"timezone,omitempty"` // IANA, "Europe/Berlin"
}

// Fetch are the settings of the fetch_url tool.
type Fetch struct {
	AllowedDomains []string `toml:"allowed_domains" json:"allowed_domains,omitempty"`
	BlockedDomains []string `toml:"blocked_domains" json:"blocked_domains,omitempty"`
	// MaxPageChars is how much of a page the model gets (default 15000). MaxCalls is the most fetch_url calls in a run (0: no limit).
	MaxPageChars int `toml:"max_page_chars" json:"max_page_chars"`
	MaxCalls     int `toml:"max_calls" json:"max_calls,omitempty"`
}

// Knowledge is how the agent works with the knowledge base (the memory of gligabao).
type Knowledge struct {
	// Access is AccessReadWrite (the default), AccessReadOnly or AccessNone; it decides the tools query and remember.
	Access string `toml:"access" json:"access"`
	// Dataset is the dataset used when the request names none ("" is "main"). DatasetLocked rejects a request that names another.
	Dataset       string `toml:"dataset" json:"dataset,omitempty"`
	DatasetLocked bool   `toml:"dataset_locked" json:"dataset_locked,omitempty"`
	// QueryFirst tells the agent to ask the knowledge base before it goes to the web; Instructions are added to its prompt on how to use it.
	QueryFirst   bool   `toml:"query_first" json:"query_first,omitempty"`
	Instructions string `toml:"instructions" json:"instructions,omitempty"`
}

// Limits are the cost caps, in USD, of the model calls and the web searches (0: no cap). A cap is a soft stop: when it is reached the
// agent gets its last step (no tools: it writes the report from what it has), and says so in its report.
type Limits struct {
	// MaxCostUSD caps this agent alone (its own model calls and its tools).
	MaxCostUSD float64 `toml:"max_cost_usd" json:"max_cost_usd,omitempty"`
	// MaxTreeCostUSD caps the whole research, the agent and all the sub-agents below it; only the definition of the root agent counts.
	MaxTreeCostUSD float64 `toml:"max_tree_cost_usd" json:"max_tree_cost_usd,omitempty"`
}

// Spawn says whether the agent starts sub-agents, which, and when.
type Spawn struct {
	// Enabled gives the agent the sub-agent tools. Without Roles it spawns free-form: sub-agents that are copies of itself, as before
	// definitions existed; with Roles, only those.
	Enabled bool `toml:"enabled" json:"enabled"`
	// MaxDepth, MaxPerAgent and MaxPerTree lower the ceilings (MaxSubagentDepth, MaxSubagentsPerAgent, MaxSubagentsPerTree); 0 is the
	// ceiling. The depth counts from the root research (the root is 0); the number per tree is that of the root's definition.
	MaxDepth    int    `toml:"max_depth" json:"max_depth,omitempty"`
	MaxPerAgent int    `toml:"max_per_agent" json:"max_per_agent,omitempty"`
	MaxPerTree  int    `toml:"max_per_tree" json:"max_per_tree,omitempty"`
	Roles       []Role `toml:"roles" json:"roles,omitempty"`
}

// Role is a kind of sub-agent the agent may start: what it does (the definition it runs) and when it is started.
type Role struct {
	// Name is the role (the name of the sub-agent for an on_start role; the model chooses the names of the others).
	Name string `toml:"name" json:"name"`
	// Agent is the definition the sub-agent runs; Version is an optional constraint on it (">=1.0", "^1.2", "1.2.3"), checked at load.
	Agent   string `toml:"agent" json:"agent"`
	Version string `toml:"version" json:"version,omitempty"`
	// When tells the parent's model when to start it (on_demand); Does says what it does, in a sentence (it is also the reason of an on_start sub-agent).
	When string `toml:"when" json:"when,omitempty"`
	Does string `toml:"does" json:"does"`
	// Trigger is TriggerOnDemand (the default) or TriggerOnStart.
	Trigger string `toml:"trigger" json:"trigger"`
	// TaskTemplate is the task of an on_start sub-agent: a Go text/template with {{.Topic}} (default "{{.Topic}}").
	TaskTemplate string `toml:"task_template" json:"task_template,omitempty"`
	// MaxInstances is the most sub-agents of this role the parent has at once (0: only the limits of Spawn count).
	MaxInstances int `toml:"max_instances" json:"max_instances,omitempty"`
	// MaxSteps is the steps the sub-agent gets by default and MaxStepsLimit the most its parent may ask for; they default to the MaxSteps of its definition.
	MaxSteps      int `toml:"max_steps" json:"max_steps,omitempty"`
	MaxStepsLimit int `toml:"max_steps_limit" json:"max_steps_limit,omitempty"`
}

// OnDemandRoles and OnStartRoles are the roles of each trigger, in the order of the file.
func (d *Def) OnDemandRoles() []Role { return d.rolesOf(TriggerOnDemand) }
func (d *Def) OnStartRoles() []Role  { return d.rolesOf(TriggerOnStart) }

func (d *Def) rolesOf(trigger string) []Role {
	var out []Role
	for _, r := range d.Spawn.Roles {
		if r.Trigger == trigger {
			out = append(out, r)
		}
	}
	return out
}

// Role returns the role called name.
func (d *Def) Role(name string) (Role, bool) {
	for _, r := range d.Spawn.Roles {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

// HasTool reports whether the definition lists the tool.
func (d *Def) HasTool(name string) bool {
	for _, t := range d.Tools {
		if t == name {
			return true
		}
	}
	return false
}

// MaxDepth, MaxPerAgent and MaxPerTree are the limits of Spawn with the ceilings applied.
func (d *Def) MaxDepth() int    { return orCeiling(d.Spawn.MaxDepth, MaxSubagentDepth) }
func (d *Def) MaxPerAgent() int { return orCeiling(d.Spawn.MaxPerAgent, MaxSubagentsPerAgent) }
func (d *Def) MaxPerTree() int  { return orCeiling(d.Spawn.MaxPerTree, MaxSubagentsPerTree) }

func orCeiling(v, ceiling int) int {
	if v <= 0 || v > ceiling {
		return ceiling
	}
	return v
}

// Steps returns the steps a sub-agent of role r that runs child gets by default, and the most its parent may ask for.
func (r Role) Steps(child *Def) (def, limit int) {
	limit = r.MaxStepsLimit
	if limit <= 0 {
		limit = child.MaxSteps
	}
	def = r.MaxSteps
	if def <= 0 {
		def = min(child.MaxSteps, limit)
	}
	return def, limit
}

// Task is the task of an on_start sub-agent of the role for a research on topic.
func (r Role) Task(topic string) (string, error) {
	text := r.TaskTemplate
	if strings.TrimSpace(text) == "" {
		text = "{{.Topic}}"
	}
	t, err := template.New("task").Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := t.Execute(&sb, struct{ Topic string }{topic}); err != nil {
		return "", fmt.Errorf("task_template of role %q: %w", r.Name, err)
	}
	return strings.TrimSpace(sb.String()), nil
}

// Short is the first 12 characters of the hash, as it is shown.
func (d *Def) Short() string {
	if len(d.Hash) > 12 {
		return d.Hash[:12]
	}
	return d.Hash
}
