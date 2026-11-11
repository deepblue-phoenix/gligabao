package agent_research

import (
	"context"
	"encoding/json"
	"time"

	log "github.com/sirupsen/logrus"

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

// InitSandbox is the startup of the code-execution subsystem: it checks that Monty is installed (it starts the bridge, which imports
// pydantic_monty) and only then makes the sidecar, which is stopped with the manager's context. If Monty is not there, nothing is made: the
// agents then run without the run_code tool. Call it once, before starting any agent. cfg's unset fields come from the environment.
func (m *Manager) InitSandbox(cfg monty.Config) {
	m.Monty = cfg
	sc := monty.NewSidecar(monty.ConfigFromEnv(cfg), nil)
	ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
	defer cancel()
	if err := sc.Check(ctx); err != nil {
		sc.Close()
		log.WithError(err).Info("agent research: Monty is not installed, the code execution (run_code) is not available")
		return
	}
	m.sidecar = sc
	context.AfterFunc(m.ctx, sc.Close)
	log.Infof("agent research: code execution (run_code) is available (Monty %s)", sc.Version())
}

// sandbox is the code sandbox of the manager: Sandbox if set, else the sidecar made by InitSandbox; nil if there is none.
func (m *Manager) sandbox() monty.Runner {
	if m.Sandbox != nil {
		return m.Sandbox
	}
	if m.sidecar == nil {
		return nil
	}
	return m.sidecar
}

// codeConfig is the run_code setup of the agent: nil if its definition has no [code] or the code sandbox was not initialized (Monty is not
// installed): the agent then runs without the run_code tool.
func (r *researcher) codeConfig() (*investigate.CodeConfig, error) {
	if !r.def.CodeEnabled() {
		return nil, nil
	}
	sb := r.mgr.sandbox()
	if sb == nil {
		log.Warnf("agent research: the agent %q has [code] enabled but the code sandbox is not available (Monty is not installed): it runs without run_code", r.def.Name)
		return nil, nil
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
