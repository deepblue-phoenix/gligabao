// Package core is the data model of the research agents (package agent_research): a Run, which is one execution of the deep-research
// agent on a topic, and the States it went through, one record each time the agent moves from one stage of its work to the next.
package core

import "time"

// Status is the lifecycle of a Run as a whole.
type Status string

const (
	// StatusRunning: the agent is working on the topic.
	StatusRunning Status = "running"
	// StatusCompleted: the agent ended with a report (Run.Report).
	StatusCompleted Status = "completed"
	// StatusFailed: the agent could not finish (a model call failed, or the server stopped). Run.Error says why.
	StatusFailed Status = "failed"
)

// Statuses lists the statuses.
var Statuses = []Status{StatusRunning, StatusCompleted, StatusFailed}

// Active reports whether a run with this status is still working.
func (s Status) Active() bool { return s == StatusRunning }

// The states of a run: what the agent is doing. A run enters them one after the other (State.Seq), and each is recorded in
// gb_agent_research_states when it is entered. A run is recorded in StateStarting, then the agent alternates between StateThinking, where it waits for the model to decide
// what to do next, and the states of the tools the model chose (StateSearching, StateReading, StateQuerying, StateRemembering, StateUsingTool); the step of the
// last model call, which may not use tools, is StateWritingReport. A run ends in StateCompleted or StateFailed, which are
// recorded too, so that the states are the whole history.
const (
	// StateStarting: the run is recorded and the agent has not made its first model call yet.
	StateStarting = "starting"
	// StateThinking: a model call is running; it decides whether to search or read more, or writes the report.
	StateThinking = "thinking"
	// StateSearching: the web_search tool is running; the detail is the query.
	StateSearching = "searching"
	// StateReading: the fetch_url tool is running; the detail is the URL.
	StateReading = "reading"
	// StateQuerying: the query tool is running; the detail is the question asked of the knowledge base.
	StateQuerying = "querying"
	// StateRemembering: the remember tool is running; the detail says whether it waits (sync) or not, and the start of the text.
	StateRemembering = "remembering"
	// StateWaiting: the agent waits for its sub-agents to end (wait_subagents) or for a message of a sibling (read_messages with a wait).
	StateWaiting = "waiting"
	// StateSpawning: the spawn_subagent tool is running; the detail is the name of the sub-agent and its reason.
	StateSpawning = "spawning"
	// StateMessaging: the send_message, read_messages (without a wait) or list_siblings tool is running; the detail is the sibling it writes to.
	StateMessaging = "messaging"
	// StateUsingTool: any other tool is running; the detail is its name.
	StateUsingTool = "using_tool"
	// StateWritingReport: the last model call, the one that may not call tools, is running: its text is the report.
	StateWritingReport = "writing_report"
	// StateCompleted: the run ended with a report.
	StateCompleted = "completed"
	// StateFailed: the run ended with an error.
	StateFailed = "failed"
)

// Run is one execution of the research agent on a topic.
type Run struct {
	ID     int64  `json:"id"`
	UserID string `json:"user_id"`
	// ParentID is the run of the agent that spawned this one as a sub-agent (nil for a research started by a user). Name is what
	// the parent called it; it is unique among the sub-agents of one parent, and "" for a research started by a user.
	ParentID *int64 `json:"parent_id"`
	Name     string `json:"name"`
	// Reason is why the parent spawned it: the reason it submitted with spawn_subagent, which the siblings of the sub-agent can
	// read. "" for a research started by a user.
	Reason string `json:"reason"`
	// Topic is what the agent researches (for a sub-agent: the sub-task it was given).
	Topic  string `json:"topic"`
	Status Status `json:"status"`
	// State is the state the run is in now: the State of its last record (one of the State constants).
	State string `json:"state"`
	// Model is the model the agent runs on ("" for the server's default); MaxSteps the most model calls it may make.
	Model    string `json:"model"`
	MaxSteps int    `json:"max_steps"`
	// Steps is the number of model calls made so far and ToolCalls the number of tool runs; they, the tokens and the cost are
	// updated as the run goes. CostUSD includes the model calls of the tools (the web searches).
	Steps        int     `json:"steps"`
	ToolCalls    int     `json:"tool_calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	// Report is the answer of the agent, written when the run completes ("" until then). Error is why a failed run failed.
	Report string `json:"report"`
	Error  string `json:"error"`
	// CreatedAt is when the run was started; FinishedAt when it ended (nil while it is active).
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`

	// States are the states the run has been through, in order. Filled by getters that are asked for them; not stored with the run.
	States []State `json:"states,omitempty"`
}

// State is one stage of the work of a run: the record that the agent entered it. The state that the run is in has no FinishedAt.
type State struct {
	ID    int64 `json:"id"`
	RunID int64 `json:"run_id"`
	// Seq numbers the states of a run from 1, in the order they were entered.
	Seq int `json:"seq"`
	// State is what the agent was doing (the State constants).
	State string `json:"state"`
	// Step is the model call the agent was on when it entered the state (from 1; 0 for the states a run starts and ends in).
	Step int `json:"step"`
	// Detail says what the state is about: the query searched, the URL read, the tool used, "step 3 of 20".
	Detail string `json:"detail"`
	// Error is why the work of the state failed ("" if it did not). A tool that fails does not fail the run: the model is told and
	// goes on, so a run that completed can have failed states; the last state of a failed run holds the error of the run.
	Error      string     `json:"error"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// RunFilter selects runs; an empty field matches all.
type RunFilter struct {
	UserID string
	// Statuses are the statuses wanted (any of them); empty means all.
	Statuses []Status
	// ParentID, if not nil, keeps only the sub-agents of that run.
	ParentID *int64
}
