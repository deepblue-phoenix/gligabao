package agent_research

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/deepblue-phoenix/gligabao/internal/monty"
	"github.com/deepblue-phoenix/gligabao/internal/monty/hostfn"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// The investigative loop (loop = "investigative" in an agent definition, docs/agents/deep-research-loop.md): the loop itself is package
// investigate; this file connects it to the researcher: the limits from the definition and the options, the stores, the states, the usage
// and the phase of the run.

// investigateLimits are the limits of the loop: the defaults of the design, the knobs of the definition and the options over them, and
// the cost cap (the lowest of the caps that apply, 0 for none).
func investigateLimits(k agent_defs.Investigate, costCap float64) investigate_core.Limits {
	l := investigate_core.DefaultLimits()
	pick := func(v, def int) int {
		if v > 0 {
			return v
		}
		return def
	}
	l.MaxDepth, l.MaxAttempts, l.MaxQuestions = pick(k.MaxDepth, l.MaxDepth), pick(k.MaxAttempts, l.MaxAttempts), pick(k.MaxQuestions, l.MaxQuestions)
	l.MaxIterations, l.MaxTotalTokens = pick(k.MaxIterations, l.MaxIterations), k.MaxTotalTokens
	l.KeepTurns, l.ExtractionThreshold, l.NotesMaxTokens = pick(k.KeepTurns, l.KeepTurns), pick(k.ExtractionThreshold, l.ExtractionThreshold), pick(k.NotesMaxTokens, l.NotesMaxTokens)
	l.MinSources, l.MinDomains = pick(k.MinSources, l.MinSources), pick(k.MinDomains, l.MinDomains)
	l.MinIndependentSources, l.MinChallenges = pick(k.MinIndependentSources, l.MinIndependentSources), pick(k.MinChallenges, l.MinChallenges)
	l.MaxChallengesPerTarget, l.MaxReopens = pick(k.MaxChallengesPerTarget, l.MaxChallengesPerTarget), pick(k.MaxReopens, l.MaxReopens)
	if k.SynthesisReserve > 0 {
		l.SynthesisReserve = k.SynthesisReserve
	}
	if k.GatePriority > 0 {
		l.GatePriority = k.GatePriority
	}
	l.MaxCostUSD = costCap
	return l
}

// notesDir is the directory of the notes files.
func (m *Manager) notesDir() string {
	if m.NotesDir != "" {
		return m.NotesDir
	}
	return config.DefaultResearchNotesDirName
}

// sandbox is the code sandbox of the manager: Sandbox if set, else the sidecar, made at the first call and stopped with the manager's context.
func (m *Manager) sandbox() monty.Runner {
	if m.Sandbox != nil {
		return m.Sandbox
	}
	m.sandboxOnce.Do(func() {
		m.sidecar = monty.NewSidecar(monty.ConfigFromEnv(m.Monty), nil)
		context.AfterFunc(m.ctx, m.sidecar.Close)
	})
	return m.sidecar
}

// codeConfig is the run_code setup of the agent: nil if its definition has no [code]; an error if the sandbox is not there, so that the run
// fails at its start with the missing piece named and not in the middle.
func (r *researcher) codeConfig() (*investigate.CodeConfig, error) {
	if !r.def.CodeEnabled() {
		return nil, nil
	}
	sb := r.mgr.sandbox()
	if c, ok := sb.(interface{ Check(context.Context) error }); ok {
		if err := c.Check(r.runCtx); err != nil {
			return nil, fmt.Errorf("the agent %q needs the code sandbox (its [code] is enabled): %w", r.def.Name, err)
		}
	}
	return &investigate.CodeConfig{Runner: sb, Settings: *r.def.Code, Registry: hostfn.Standard()}, nil
}

// runInvestigative runs the investigative loop of the agent and returns its outcome in the shape of the simple loop's.
func (r *researcher) runInvestigative() (*agent.Result, error) {
	limits := investigateLimits(r.opts.investigateKnobs(r.def), lowestCap(r.def.Limits.MaxCostUSD, r.opts.treeCostCap))
	local := investigate.NotesFile{Dir: r.mgr.notesDir()}
	var files investigate_core.NotesStore = local
	if r.mgr.NotesS3 != nil {
		files = r.mgr.NotesS3.Mirror(local, r.run.UserID)
	}
	sess := investigate_core.Session{
		Env: investigate_core.Env{
			Store: r.mgr.Ops.Investigate(r.db), Files: files, RunID: r.run.ID,
			NotesPath: local.Path(r.run.UserID, r.run.ID, r.run.NotesID),
		},
		Registry: investigate_core.LoopRegistry(),
		Limits:   limits,
	}
	code, err := r.codeConfig()
	if err != nil {
		return nil, err
	}
	usage := func(t investigate.Totals) { r.saveUsage(totalsOf(t)) }
	cfg := investigate.Config{
		Session: sess, Model: r.client.Raw, ModelName: r.opts.Model, MaxTokens: r.agent.MaxTokens,
		System: r.agent.System, Task: r.opts.Topic, External: r.agent.Tools, Code: code,
		Hooks: investigate.Hooks{
			BeforeModel: func(turn int, state, detail string) { r.enter(state, turn, detail) },
			Entered:     func(turn int, state, detail string) { r.enter(state, turn, detail) },
			AfterModel:  func(_ int, t investigate.Totals) { usage(t) },
			BeforeTool:  func(turn int, name string, input json.RawMessage) { r.beforeTool(turn, name, input) },
			AfterTool: func(turn int, name, out string, isErr bool, t investigate.Totals) {
				r.afterTool(turn, name, out, isErr, totalsOf(t))
			},
			PhaseChanged: r.savePhase,
		},
	}
	if r.onTrajectory != nil {
		cfg.Trajectory = &investigate.Trajectory{Name: r.agent.Name, Version: r.agent.Version, SessionID: SessionID(r.run.ID)}
	}
	res, err := investigate.Run(r.runCtx, cfg)
	out := totalsOf(res.Totals)
	out.Answer, out.Trajectory = res.Report, res.Trajectory
	return &out, err
}

func totalsOf(t investigate.Totals) agent.Result {
	return agent.Result{Steps: t.Turns, ToolCalls: t.ToolCalls, InputTokens: t.InputTokens, OutputTokens: t.OutputTokens, CostUSD: t.CostUSD}
}

// savePhase writes the phase and the stop reason of the run. A failed write is logged and does not stop the agent.
func (r *researcher) savePhase(phase investigate_core.Phase, stopReason string) {
	if err := r.store.SetPhase(r.dbCtx, r.db, r.run.ID, string(phase), stopReason); err != nil {
		r.logf.WithError(err).Warn("agent research: recording the phase failed")
	}
}
