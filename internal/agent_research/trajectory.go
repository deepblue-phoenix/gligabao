package agent_research

import (
	"sort"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
)

// agentInfo is what the trajectory says of each agent of the research (its "agents" extra): who it is, where it is in the tree and how
// its run went. The totals are its own (the tools' spending included), not those of its sub-agents, which have entries of their own.
type agentInfo struct {
	Agent        string  `json:"agent"` // "main", or the name the parent gave the sub-agent
	RunID        int64   `json:"run_id"`
	ParentRunID  int64   `json:"parent_run_id,omitempty"`
	Depth        int     `json:"depth"`
	AgentDef     string  `json:"agent_def,omitempty"` // the definition it was built from (package agent_defs) and its version
	AgentVersion string  `json:"agent_version,omitempty"`
	Status       string  `json:"status"`
	Error        string  `json:"error,omitempty"`
	Steps        int     `json:"steps"`
	ToolCalls    int     `json:"tool_calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// ATIF trajectories of a research. The agent (package agent) records its own run when asked to (agent.Agent.Trajectory); here the
// trajectories of the agents of a research are merged into one, the root's: when an agent has ended, and its sub-agents with it, the
// steps of the sub-agents (each already merged with theirs) are put among its own, in the order the steps started. The root's merged
// trajectory is what Manager.UseTrajectories hands over.

// TrajectoryFunc receives the trajectory of a research: the one of the agent a user started (run runID), with the steps of all its
// sub-agents merged in. It is called on the goroutine of the root agent, after its run has ended and was recorded, also for a research
// that failed or was stopped.
type TrajectoryFunc func(runID int64, t *atif.Trajectory)

// mainAgent is the "agent" tag of the steps of the agent the user started.
const mainAgent = "main"

// UseTrajectories makes the agents started from now on record their runs as ATIF trajectories, and fn receive the merged trajectory of
// each research when it is over (see TrajectoryFunc). With nil (the default) nothing is recorded. Agents already working are not changed.
func (m *Manager) UseTrajectories(fn TrajectoryFunc) {
	m.mu.Lock()
	m.onTrajectory = fn
	m.mu.Unlock()
}

// whoAmI is how the steps of the agent are tagged.
func (r *researcher) whoAmI() string {
	if r.parent == nil {
		return mainAgent
	}
	return r.run.Name
}

// finishTrajectory is called when the agent and its sub-agents have ended and its run is recorded. It merges the trajectories of its
// sub-agents into its own (res.Trajectory), keeps the result for its parent (a root hands it to the manager's function). It does
// nothing if trajectories were not asked for.
func (r *researcher) finishTrajectory(res *agent.Result) {
	if res == nil || res.Trajectory == nil {
		return
	}
	info := agentInfo{
		Agent: r.whoAmI(), RunID: r.run.ID, Depth: r.depth, AgentDef: r.def.Name, AgentVersion: r.def.Version, Status: string(r.out.status), Error: r.out.err,
		Steps: res.Steps, ToolCalls: res.ToolCalls, InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CostUSD: res.CostUSD,
	}
	if r.parent != nil {
		info.ParentRunID = r.parent.run.ID
	}
	var kids []*member
	for _, k := range r.kids.all() {
		if k.traj != nil {
			kids = append(kids, k)
		}
	}
	r.traj, r.agents = mergeTrajectories(res.Trajectory, info, kids)
	if r.parent == nil && r.onTrajectory != nil {
		r.onTrajectory(r.run.ID, r.traj)
	}
}

// mergeTrajectories returns own with the steps of the sub-agents' trajectories merged in, and the entries of all the agents.
//
// Every step is tagged with the agent that took it ("agent", "run_id", "parent_run_id", "depth" in its extra), so the steps of the
// agents can be told apart; they are put in the order they started (the timestamp of an agent step is the start of its model call, so
// a step of a parent that waits for its sub-agents comes before their steps, with the reports in its observation), and numbered again.
// The final metrics are those of all the steps. The trajectory keeps the ID, the session and the agent of own, and gets the "agents"
// extra with one entry per agent (own first).
func mergeTrajectories(own *atif.Trajectory, info agentInfo, kids []*member) (*atif.Trajectory, []agentInfo) {
	tag(own.Steps, info)
	agents := []agentInfo{info}
	merged := *own
	merged.Steps = append([]atif.Step(nil), own.Steps...)
	for _, k := range kids { // in the order they were spawned
		merged.Steps = append(merged.Steps, k.traj.Steps...)
		agents = append(agents, k.agents...)
	}
	if len(kids) > 0 {
		sortByStart(merged.Steps)
		for i := range merged.Steps {
			merged.Steps[i].StepID = i + 1
		}
		merged.ComputeFinalMetrics()
	}
	extra := make(map[string]any, len(own.Extra)+1)
	for k, v := range own.Extra {
		extra[k] = v
	}
	extra["agents"] = agents
	merged.Extra = extra
	return &merged, agents
}

// tag adds the agent's identity to the extra of its steps.
func tag(steps []atif.Step, info agentInfo) {
	for i := range steps {
		extra := make(map[string]any, len(steps[i].Extra)+4)
		for k, v := range steps[i].Extra {
			extra[k] = v
		}
		extra["agent"], extra["run_id"], extra["depth"] = info.Agent, info.RunID, info.Depth
		if info.ParentRunID != 0 {
			extra["parent_run_id"] = info.ParentRunID
		}
		steps[i].Extra = extra
	}
}

// sortByStart orders the steps by their timestamps (stable: steps with the same timestamp keep their order; an unreadable one counts as the zero time).
func sortByStart(steps []atif.Step) {
	at := func(s atif.Step) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, s.Timestamp) // the zero time for an unreadable one
		return t
	}
	sort.SliceStable(steps, func(i, j int) bool { return at(steps[i]).Before(at(steps[j])) })
}
