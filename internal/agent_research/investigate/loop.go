package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
	"slices"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
)

// This file is the effect layer of the investigative loop: it runs the cycle (state, decide, act, observe, update) with the pure functions of
// the package deciding every step. The model, the tools, the database and the notes file are touched here and only here.

// ModelFunc makes one model call: (*ai.Client).Raw has this shape.
type ModelFunc func(ctx context.Context, payload map[string]any) (*ai.RawResponse, error)

// Totals are what the run has spent so far.
type Totals struct {
	Turns, ToolCalls, InputTokens, OutputTokens int
	CostUSD                                     float64
}

// Hooks tell the caller about the progress of the run (all optional). They run on the goroutine of the loop, one at a time.
type Hooks struct {
	// BeforeModel is called before each model call with the number of the turn (from 1), the research state it is recorded in and its detail.
	BeforeModel func(turn int, state, detail string)
	// AfterModel is called after a model call with the totals.
	AfterModel func(turn int, t Totals)
	// BeforeTool and AfterTool are called around each tool call the model asked for (AfterTool with the totals, the tool's spending in).
	BeforeTool func(turn int, name string, input json.RawMessage)
	AfterTool  func(turn int, name, output string, isErr bool, t Totals)
	// Entered is called when the loop enters a state of its own, outside a model call (selecting the next question).
	Entered func(turn int, state, detail string)
	// PhaseChanged is called when the phase or the stop reason changes (and at the start), to store them.
	PhaseChanged func(phase investigate_core.Phase, stopReason string)
}

// Trajectory asks the loop to record an ATIF trajectory of the run.
type Trajectory struct {
	Name, Version, SessionID string
}

// Config is everything Run needs.
type Config struct {
	Session investigate_core.Session
	Model   ModelFunc
	// ModelName and MaxTokens go into every request ("" and 0: the client's defaults).
	ModelName string
	MaxTokens int
	// System is the system prompt of the agent; Task the objective; External the web and knowledge tools of the agent.
	System   string
	Task     string
	External []agent.Tool
	Hooks    Hooks
	// Trajectory, if not nil, makes Run record the trajectory.
	Trajectory *Trajectory
}

// RunResult is how the run went.
type RunResult struct {
	// Report is the accepted report, with the source ids as links ("" if the run did not end with one).
	Report     string
	Phase      investigate_core.Phase
	StopReason string
	Totals
	Trajectory *atif.Trajectory
}

// BreakerError is the error of a run that ended in synthesis because a breaker fired there (the reserve was not enough).
type BreakerError struct{ Breaker investigate_core.Breaker }

func (e *BreakerError) Error() string {
	return fmt.Sprintf("the synthesis reserve was exhausted without a report (%s)", e.Breaker)
}

// Run runs the investigation to the end: exploration, then synthesis, until a report is accepted, or a breaker fires in synthesis (a
// *BreakerError), the model fails or ctx ends. The run's state is in the store of cfg.Session: a first call records the root question.
func Run(ctx context.Context, cfg Config) (RunResult, error) {
	r := &runner{cfg: cfg, ls: investigate_core.StartLoop(), sess: cfg.Session}
	r.prompt = r.buildPrompt()
	if t := cfg.Trajectory; t != nil {
		r.tr = newRecorder(cfg, r.prompt)
	}
	err := r.run(ctx)
	res := RunResult{Report: r.report, Phase: r.ls.Phase, StopReason: r.ls.StopReason, Totals: r.totals()}
	res.Trajectory = r.tr.finish(res, err)
	return res, err
}

type runner struct {
	cfg    Config
	sess   investigate_core.Session
	prompt investigate_core.Prompt
	ls     investigate_core.LoopState
	report string
	// toolCalls and in/out are the totals not kept by the loop state (the state has the tokens and the cost).
	toolCalls, in, out int
	tr                 *recorder
}

func (r *runner) totals() Totals {
	return Totals{Turns: r.ls.Turns, ToolCalls: r.toolCalls, InputTokens: r.in, OutputTokens: r.out, CostUSD: r.ls.CostUSD}
}

func (r *runner) buildPrompt() investigate_core.Prompt {
	var cat []investigate_core.ToolSpec
	var ext []string
	for _, t := range r.cfg.External {
		cat = append(cat, investigate_core.ToolSpec{Name: t.Name, Description: t.Description, Schema: t.Schema})
		ext = append(ext, t.Name)
	}
	for _, t := range r.sess.Registry.Tools() {
		cat = append(cat, investigate_core.ToolSpec{Name: t.Name, Description: t.Description, Schema: t.Schema})
	}
	return investigate_core.Prompt{System: r.cfg.System, Task: r.cfg.Task, Catalog: cat, External: ext}
}

func (r *runner) phaseChanged() {
	if h := r.cfg.Hooks.PhaseChanged; h != nil {
		h(r.ls.Phase, r.ls.StopReason)
	}
}

func (r *runner) run(ctx context.Context) error {
	st, err := r.sess.Load(ctx)
	if err != nil {
		return err
	}
	if _, ok := st.Frontier.Root(); !ok {
		if err := r.sess.Start(ctx, r.cfg.Task); err != nil {
			return err
		}
	}
	r.phaseChanged()
	for !r.ls.Done {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.step(ctx); err != nil {
			return err
		}
	}
	return nil
}

// step is one iteration of the loop: restore the notes, check the breakers, decide what the next turn is, make it.
func (r *runner) step(ctx context.Context) error {
	if err := r.restoreNotes(ctx); err != nil {
		return err
	}
	st, err := r.sess.Load(ctx)
	if err != nil {
		return err
	}
	if bs := investigate_core.CheckBreakers(r.ls, st.Limits); len(bs) > 0 {
		return r.onBreaker(ctx, st, bs[0])
	}
	kind := investigate_core.NextKind(r.ls, st)
	if r.ls.Phase == investigate_core.PhaseExploration && kind == investigate_core.TurnExplore && needsSelection(st) {
		var moved bool
		if st, moved, err = r.selectNext(ctx, st); err != nil || moved {
			return err
		}
	}
	return r.turn(ctx, kind, st)
}

// onBreaker: in exploration the run moves to synthesis with a note of the cut (it is not failed); in synthesis the reserve is exhausted and
// the run fails with the breaker named.
func (r *runner) onBreaker(ctx context.Context, st investigate_core.State, b investigate_core.Breaker) error {
	if r.ls.Phase == investigate_core.PhaseSynthesis {
		r.ls = r.ls.Failed(b.StopReason())
		r.phaseChanged()
		return &BreakerError{Breaker: b}
	}
	if err := r.sess.Apply(ctx, "breaker", func(s investigate_core.State) (investigate_core.State, []investigate_core.Transition) {
		return investigate_core.CutShort(s, b), nil
	}); err != nil {
		return err
	}
	r.ls = r.ls.ToSynthesis(b.StopReason())
	r.phaseChanged()
	return nil
}

// needsSelection: the objective has sub-questions and none of them is in progress.
func needsSelection(s investigate_core.State) bool {
	root, ok := s.Frontier.Root()
	if !ok || len(s.Frontier.Children(root.ID)) == 0 {
		return false
	}
	cur, ok := s.Frontier.Current()
	return !ok || cur.ParentID == ""
}

// selectNext is the decide step: no model call, the frontier's selection rule runs (frontier_next). If nothing is actionable, the frontier is
// exhausted: the final check queues the claims still UNVERIFIED behind resolved questions; if it queued none, the gate is looked at, and
// either exploration ends or the model is given a few turns to wrap up. moved says that the loop state or the frontier changed and the
// iteration is over.
func (r *runner) selectNext(ctx context.Context, st investigate_core.State) (investigate_core.State, bool, error) {
	turn := r.ls.Turns + 1
	if h := r.cfg.Hooks.Entered; h != nil {
		h(turn, investigate_core.StateSelecting, "frontier_next")
	}
	if _, err := r.sess.Call(ctx, "frontier_next", json.RawMessage(`{}`)); err != nil && !errors.Is(err, investigate_core.ErrNotesFile) {
		return st, false, fmt.Errorf("selecting the next question: %w", err)
	}
	next, err := r.sess.Load(ctx)
	if err != nil {
		return st, false, err
	}
	if cur, ok := next.Frontier.Current(); ok && cur.ParentID != "" {
		r.ls.WrapUp = 0
		return next, false, nil
	}
	// Exhausted.
	checked, ts, ids := investigate_core.FinalCheck(next)
	if len(ids) > 0 {
		return next, true, r.sess.Apply(ctx, "", func(investigate_core.State) (investigate_core.State, []investigate_core.Transition) {
			return checked, ts
		})
	}
	if len(investigate_core.ExplorationGate(next)) == 0 || r.ls.WrapUp >= investigate_core.WrapUpTurns {
		r.ls = r.ls.ToSynthesis(investigate_core.StopFrontierExhausted)
		r.phaseChanged()
		return next, true, nil
	}
	r.ls.WrapUp++
	return next, false, nil
}

// restoreNotes makes sure the notes file is there before a turn: if it is missing, unreadable or not what the latest stored version says, it is
// written again from that version and a version with the reason restore is added. With no stored version the file is written from the state.
func (r *runner) restoreNotes(ctx context.Context) error {
	env := r.sess.Env
	if env.Files == nil {
		return nil
	}
	v, ok, err := env.Store.LatestNotesVersion(ctx, env.RunID)
	if err != nil {
		return err
	}
	want := v.Text
	if !ok {
		st, err := r.sess.Load(ctx)
		if err != nil {
			return err
		}
		want = investigate_core.Render(st.Notes)
	}
	if got, rerr := env.Files.Read(env.NotesPath); rerr == nil && got == want {
		return nil
	}
	if !ok {
		return env.Files.Write(env.NotesPath, want)
	}
	seq, err := env.Store.StateSeq(ctx, env.RunID)
	if err != nil {
		return err
	}
	return investigate_core.Execute(ctx, env, seq, []investigate_core.Effect{investigate_core.WriteNotes{Text: want, Reason: "restore"}})
}

// turn is the act step: build the request, call the model, run the tools it asked for, then update (commit, evict, reblock).
func (r *runner) turn(ctx context.Context, kind investigate_core.TurnKind, st investigate_core.State) error {
	n, phaseBefore := r.ls.Turns+1, r.ls.Phase
	req := investigate_core.BuildRequest(r.prompt, kind, r.ls, st)
	payload := req.Payload()
	if r.cfg.ModelName != "" {
		payload["model"] = r.cfg.ModelName
	}
	if r.cfg.MaxTokens > 0 {
		payload["max_tokens"] = r.cfg.MaxTokens
	}
	state := investigate_core.StateOf(kind, r.ls, st)
	detail := fmt.Sprintf("turn %d of %d", n, st.Limits.MaxIterations)
	if q := investigate_core.CurrentID(st); q != "" && r.ls.Phase == investigate_core.PhaseExploration {
		detail += ", question " + q
	}
	if h := r.cfg.Hooks.BeforeModel; h != nil {
		h(n, state, detail)
	}
	started := time.Now()
	resp, err := r.cfg.Model(ctx, payload)
	if err != nil {
		return err
	}
	r.ls = r.ls.AfterModel(resp.InputTokens, resp.OutputTokens, resp.CostUSD)
	r.in, r.out = r.in+resp.InputTokens, r.out+resp.OutputTokens
	if h := r.cfg.Hooks.AfterModel; h != nil {
		h(n, r.totals())
	}
	rec := r.tr.begin(started, resp, n, kind, state, r.ls.Phase, investigate_core.CurrentID(st))

	var text strings.Builder
	var calls []investigate_core.Call
	for _, raw := range resp.Content {
		var b struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("decode content block: %w", err)
		}
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			rec.call(b.ID, b.Name, b.Input)
			c := r.runTool(ctx, kind, n, st, investigate_core.Call{ID: b.ID, Name: b.Name, Input: b.Input})
			rec.result(c)
			calls = append(calls, c)
		}
	}
	rec.end(text.String())

	// Update. A turn that ended a phase (finish_exploration, finalize_report, a reopen) leaves nothing for the next one: the window is new.
	if r.ls.Done || r.ls.Phase != phaseBefore {
		return nil
	}
	next, err := r.sess.Load(ctx)
	if err != nil {
		return err
	}
	turn := investigate_core.Turn{N: n, Content: resp.Content, Calls: calls}
	if len(calls) == 0 {
		turn.Nudge = investigate_core.Reminder(kind, next)
	}
	r.ls = r.ls.AddTurn(turn, investigate_core.PendingOf(turn, investigate_core.CurrentID(st)))
	switch {
	case kind == investigate_core.TurnExtract:
		if !investigate_core.WroteNotes(turn) {
			r.noteNothingRelevant(ctx)
		}
		r.ls = r.ls.CommitUpTo(n)
	case investigate_core.WroteNotes(turn):
		r.ls = r.ls.CommitUpTo(n - 1)
	}
	if kind == investigate_core.TurnConsolidate {
		r.ls = investigate_core.AfterConsolidation(r.ls, investigate_core.TokenEstimate(st.Notes), investigate_core.TokenEstimate(next.Notes), next.Limits)
	}
	r.ls = investigate_core.Evict(r.ls, next.Limits.KeepTurns)
	if r.ls.Phase == investigate_core.PhaseExploration {
		if err := r.sess.Apply(ctx, "", func(s investigate_core.State) (investigate_core.State, []investigate_core.Transition) {
			f, ts := investigate_core.Reblock(s.Frontier)
			s.Frontier = f
			return s, ts
		}); err != nil {
			return err
		}
	}
	return nil
}

// noteNothingRelevant records the pages of the pending results as read with nothing relevant, after an extraction turn in which the model took
// nothing from them (so that they count as committed and the sources are in the notes). An error is not fatal: the result is committed anyway.
func (r *runner) noteNothingRelevant(ctx context.Context) {
	for _, p := range r.ls.Pending {
		if p.Tool != "fetch_page" || p.Arg == "" {
			continue
		}
		args, _ := json.Marshal(map[string]any{"section": "Sources", "entries": []map[string]string{{"url": p.Arg, "title": p.Arg, "note": "read, nothing relevant"}}})
		r.sess.Call(ctx, "notes_append", args)
	}
}

// runTool runs one tool call of a turn and returns it with its output.
func (r *runner) runTool(ctx context.Context, kind investigate_core.TurnKind, turn int, st investigate_core.State, c investigate_core.Call) investigate_core.Call {
	r.toolCalls++
	if h := r.cfg.Hooks.BeforeTool; h != nil {
		h(turn, c.Name, c.Input)
	}
	out, isErr := r.dispatch(ctx, kind, st, c)
	c.Output, c.IsErr = out, isErr
	if h := r.cfg.Hooks.AfterTool; h != nil {
		h(turn, c.Name, out, isErr, r.totals())
	}
	return c
}

func (r *runner) dispatch(ctx context.Context, kind investigate_core.TurnKind, st investigate_core.State, c investigate_core.Call) (out string, isErr bool) {
	defer func() {
		if p := recover(); p != nil { // a tool that panics must not take the run down
			out, isErr = fmt.Sprintf("tool %s failed: %v", c.Name, p), true
		}
	}()
	external := map[string]agent.Tool{}
	for _, t := range r.cfg.External {
		external[t.Name] = t
	}
	registry := r.sess.Registry.Names()
	extNames := make([]string, 0, len(external))
	for n := range external {
		extNames = append(extNames, n)
	}
	if !slices.Contains(investigate_core.OfferedTools(kind, extNames, registry), c.Name) {
		return fmt.Sprintf("the tool %s is not available in this turn", c.Name), true
	}
	if t, ok := external[c.Name]; ok {
		return r.runExternal(ctx, t, st, c)
	}
	return r.runRegistry(ctx, st, c)
}

func (r *runner) runExternal(ctx context.Context, t agent.Tool, st investigate_core.State, c investigate_core.Call) (string, bool) {
	uctx, drain := agent.WithUsage(ctx)
	out, err := t.Run(uctx, c.Input)
	in, o, cost := drain()
	r.ls = r.ls.Spend(in, o, cost)
	r.in, r.out = r.in+in, r.out+o
	if err != nil {
		out, c.IsErr = err.Error(), true
	}
	c.Output = out
	if !c.IsErr && investigate_core.IsRawTool(c.Name) {
		r.logAttempt(ctx, c)
	}
	return out, c.IsErr
}

// logAttempt writes the attempt a search, a page read or a knowledge-base query made for the question to the attempt log, in the effect
// list of its own; the registered query of an open challenge is linked to its challenge instead. A query that is already in the log (the
// one retry_question logged) is not logged again. A failure is not fatal: the attempt log is a record.
func (r *runner) logAttempt(ctx context.Context, c investigate_core.Call) {
	arg := investigate_core.ArgOf(c)
	if arg == "" {
		return
	}
	outcome := fmt.Sprintf("%d chars: %s", len([]rune(c.Output)), investigate_core.Excerpt(c.Output, 100))
	_ = r.sess.Apply(ctx, "", func(s investigate_core.State) (investigate_core.State, []investigate_core.Transition) {
		next := s
		if i := slices.IndexFunc(s.Challenges, func(h investigate_core.Challenge) bool {
			return h.Status == investigate_core.ChallengeOpen && h.AttemptSeq == 0 && investigate_core.Normalize(h.Query) == investigate_core.Normalize(arg)
		}); i >= 0 {
			if f, chs, err := investigate_core.LinkAttempt(s.Frontier, s.Challenges, s.Challenges[i].ID, c.Name, outcome); err == nil {
				next.Frontier, next.Challenges = f, chs
			}
			return next, nil
		}
		qid := investigate_core.CurrentID(s)
		q, ok := s.Frontier.Get(qid)
		if !ok || slices.ContainsFunc(q.Attempts, func(a investigate_core.Attempt) bool {
			return investigate_core.Normalize(a.Query) == investigate_core.Normalize(arg)
		}) {
			return s, nil
		}
		if f, _, err := investigate_core.AddAttempt(s.Frontier, qid, investigate_core.Attempt{Query: arg, Tool: c.Name, Outcome: outcome}); err == nil {
			next.Frontier = f
		}
		return next, nil
	})
}

func (r *runner) runRegistry(ctx context.Context, st investigate_core.State, c investigate_core.Call) (string, bool) {
	// Only synthesis may reopen, and only max_reopens times: then the run goes back to exploration.
	if r.ls.Phase == investigate_core.PhaseSynthesis && c.Name == "reopen_question" && !r.ls.CanReopen(st.Limits) {
		return fmt.Sprintf("the report cannot reopen exploration again (at most %d times): state the gap in the report", st.Limits.MaxReopens), true
	}
	text, err := r.sess.Call(ctx, c.Name, c.Input)
	if err != nil && !errors.Is(err, investigate_core.ErrNotesFile) {
		return err.Error(), true
	}
	switch c.Name {
	case investigate_core.ToolFinishExploration:
		r.ls = r.ls.ToSynthesis("")
		r.phaseChanged()
	case investigate_core.ToolFinalizeReport:
		var a struct {
			Report string `json:"report"`
		}
		_ = json.Unmarshal(c.Input, &a)
		r.report = investigate_core.RenderReport(strings.TrimSpace(a.Report), st.Notes)
		r.ls = r.ls.Finalized()
		r.phaseChanged()
	case "reopen_question":
		if r.ls.Phase == investigate_core.PhaseSynthesis {
			r.ls = r.ls.Reopen()
			r.phaseChanged()
		}
	}
	return text, false
}

// recorder records the run as an ATIF trajectory; a nil recorder records nothing.
type recorder struct {
	b     *atif.Builder
	model string
}

func newRecorder(cfg Config, p investigate_core.Prompt) *recorder {
	t := cfg.Trajectory
	name, version := t.Name, t.Version
	if name == "" {
		name = "gligabao-agent"
	}
	if version == "" {
		version = "0.1"
	}
	b := atif.NewBuilder(atif.Agent{Name: name, Version: version, ModelName: cfg.ModelName})
	if t.SessionID != "" {
		b.SetSessionID(t.SessionID)
	}
	names := make([]string, len(p.Catalog))
	for i, s := range p.Catalog {
		names[i] = s.Name
	}
	b.SetExtra("input", map[string]any{"loop": "investigative", "tools": names})
	b.System(p.System)
	b.User(p.Task)
	return &recorder{b: b}
}

// stepRec is one model call of the run, an agent step of the trajectory.
type stepRec struct {
	step   atif.Step
	failed []string
	r      *recorder
}

func (t *recorder) begin(started time.Time, resp *ai.RawResponse, turn int, kind investigate_core.TurnKind, state string, phase investigate_core.Phase, question string) *stepRec {
	if t == nil {
		return nil
	}
	if t.model == "" {
		t.model = resp.Model
	}
	return &stepRec{r: t, step: atif.Step{
		Timestamp: started.UTC().Format(time.RFC3339Nano),
		ModelName: resp.Model,
		Metrics:   &atif.Metrics{PromptTokens: resp.InputTokens, CompletionTokens: resp.OutputTokens, CostUSD: resp.CostUSD},
		Extra: map[string]any{
			"provider": "anthropic", "stop_reason": resp.StopReason, "turn": turn, "turn_kind": string(kind), "state": state,
			"phase": string(phase), "question_id": question, "latency_ms": float64(time.Since(started).Microseconds()) / 1000,
		},
	}}
}

func (s *stepRec) call(id, name string, input json.RawMessage) {
	if s == nil {
		return
	}
	args := map[string]any{}
	if err := json.Unmarshal(input, &args); err != nil || args == nil {
		args = map[string]any{"_raw": string(input)}
	}
	s.step.ToolCalls = append(s.step.ToolCalls, atif.ToolCall{ToolCallID: id, FunctionName: name, Arguments: args})
}

func (s *stepRec) result(c investigate_core.Call) {
	if s == nil {
		return
	}
	if s.step.Observation == nil {
		s.step.Observation = &atif.Observation{}
	}
	s.step.Observation.Results = append(s.step.Observation.Results, atif.ObservationResult{SourceCallID: c.ID, Content: c.Output})
	if c.IsErr {
		s.failed = append(s.failed, c.ID)
	}
}

func (s *stepRec) end(text string) {
	if s == nil {
		return
	}
	s.step.Message = strings.TrimSpace(text)
	if len(s.failed) > 0 {
		s.step.Extra["failed_tool_calls"] = s.failed
	}
	s.r.b.Agent(s.step)
}

// finish ends the trajectory with the totals, the phase and the stop reason, and the error if the run failed.
func (t *recorder) finish(res RunResult, runErr error) *atif.Trajectory {
	if t == nil {
		return nil
	}
	if runErr != nil {
		t.b.SetNotes("failed: " + runErr.Error())
	}
	t.b.SetExtra("result", map[string]any{
		"steps": res.Turns, "tool_calls": res.ToolCalls, "input_tokens": res.InputTokens, "output_tokens": res.OutputTokens,
		"cost_usd": res.CostUSD, "answer": res.Report, "phase": string(res.Phase), "stop_reason": res.StopReason,
	})
	traj := t.b.Build()
	if traj.Agent.ModelName == "" {
		traj.Agent.ModelName = t.model
	}
	return traj
}
