package investigate

import (
	"encoding/json"
	"slices"
	"strings"
)

// The loop state is the pure value behind the investigative loop (loop.go runs it): the phase, the counters the breakers read, the window
// of recent turns the prompt is made of and the results the notes do not hold yet. Every transition is a pure function that returns a new
// value; the loop only decides when to call them.

// Phase is the part of the run the loop is in. Its values are the ones of gb_agent_research_runs.phase (not constrained by the database,
// so the vocabulary is enforced here).
type Phase string

const (
	// PhaseExploration: the cycle on the frontier (search, read, extract, verify, challenge).
	PhaseExploration Phase = "exploration"
	// PhaseSynthesis: the report is written from the notes and the frontier, without searching.
	PhaseSynthesis Phase = "synthesis"
)

// The stop reasons (gb_agent_research_runs.stop_reason; "" while the run is working and exploring normally). A reason that cuts exploration
// short is set when exploration ends and is kept when the report is accepted; StopFinalized is the reason of a run that explored until its
// own gate said so.
const (
	StopFinalized         = "finalized"
	StopFrontierExhausted = "frontier_exhausted"
	StopIterations        = "breaker:iterations"
	StopTokens            = "breaker:tokens"
	StopCost              = "breaker:cost"
)

// The states a turn of the investigative loop is recorded in (gb_agent_research_states.state), next to "thinking" and the tool states.
const (
	StatePlanning      = "planning"
	StateSelecting     = "selecting"
	StateExtracting    = "extracting"
	StateVerifying     = "verifying"
	StateChallenging   = "challenging"
	StateConsolidating = "consolidating"
	StateSynthesizing  = "synthesizing"
	stateThinking      = "thinking"
)

// TurnKind says what the next model call is for; it decides the prompt and the tools.
type TurnKind string

const (
	// TurnPlanning: the root has no sub-question yet, the model is asked for the first level.
	TurnPlanning TurnKind = "planning"
	// TurnExplore: a normal turn of the exploration cycle.
	TurnExplore TurnKind = "explore"
	// TurnExtract: forced extraction, only the notes tools.
	TurnExtract TurnKind = "extract"
	// TurnConsolidate: the notes are over their budget; only the notes tools.
	TurnConsolidate TurnKind = "consolidate"
	// TurnSynthesize: a turn of the synthesis loop.
	TurnSynthesize TurnKind = "synthesize"
	// TurnFinal: the last turn the run may take: tool_choice is finalize_report.
	TurnFinal TurnKind = "final"
)

// Call is one tool call of a turn and what it returned.
type Call struct {
	ID     string
	Name   string
	Input  json.RawMessage
	Output string
	IsErr  bool
}

// Turn is one model call: what the model said (the content blocks as the API sent them, to go back unchanged), its tool calls with their
// results, and the nudge the loop gave in place of results when the model called no tool.
type Turn struct {
	N       int
	Content []json.RawMessage
	Calls   []Call
	Nudge   string
}

// Pending is a tool result that the notes do not hold yet (it is "uncommitted"): it is never evicted, and the next turn is a forced
// extraction when it is large or about to leave the window.
type Pending struct {
	CallID, Tool string
	// Arg is the query, URL or question of the call; QuestionID the question it was made for.
	Arg, QuestionID string
	// Size is the length of the result in characters; Turn the turn it came in.
	Size, Turn int
}

// LoopState is the whole state of the loop apart from the frontier, the notes and the challenges (State). It is an immutable value.
type LoopState struct {
	Phase Phase
	// Turns is the number of model calls made (the run's steps); Tokens, input plus output, and CostUSD what they and the tools cost.
	Turns   int
	Tokens  int
	CostUSD float64
	// Reopens is how many times synthesis sent the run back to exploration.
	Reopens int
	// StopReason is "" while the run explores normally (see the Stop constants); Cut is the breaker that cut exploration short ("" if none).
	StopReason string
	Cut        string
	// Window holds the recent turns (Evict trims it); Pending the results the notes do not hold yet.
	Window  []Turn
	Pending []Pending
	// Consol counts the consolidation turns in a row and ConsolFrom is the size of the notes (in tokens) before the last one.
	Consol, ConsolFrom int
	// WrapUp is the number of turns given to wrap up after the frontier ran out.
	WrapUp int
	// Done is set when the report was accepted.
	Done bool
}

// StartLoop is the state of a run that starts: exploration, nothing spent.
func StartLoop() LoopState { return LoopState{Phase: PhaseExploration} }

// AfterModel counts a model call and what it spent (the tools' spending is added with Spend).
func (ls LoopState) AfterModel(inputTokens, outputTokens int, costUSD float64) LoopState {
	ls.Turns++
	return ls.Spend(inputTokens, outputTokens, costUSD)
}

// Spend adds spending that is not a model call of the loop (the tools: the web searches).
func (ls LoopState) Spend(inputTokens, outputTokens int, costUSD float64) LoopState {
	ls.Tokens += inputTokens + outputTokens
	ls.CostUSD += costUSD
	return ls
}

// AddTurn appends a turn to the window and the pending results it brought.
func (ls LoopState) AddTurn(t Turn, pending []Pending) LoopState {
	ls.Window = append(slices.Clone(ls.Window), t)
	ls.Pending = append(slices.Clone(ls.Pending), pending...)
	return ls
}

// Commit says that the notes hold what the pending results had: none is pending any more.
func (ls LoopState) Commit() LoopState {
	ls.Pending = nil
	return ls
}

// CommitUpTo says that the notes hold the pending results that came in turn n or before.
func (ls LoopState) CommitUpTo(n int) LoopState {
	ls.Pending = slices.DeleteFunc(slices.Clone(ls.Pending), func(p Pending) bool { return p.Turn <= n })
	return ls
}

// ToSynthesis moves exploration to synthesis. reason is the stop reason ("" when the gate passed: finish_exploration); a breaker sets Cut.
// The window is dropped: the synthesis prompt is made of the notes and the frontier, not of the search turns.
func (ls LoopState) ToSynthesis(reason string) LoopState {
	ls.Phase = PhaseSynthesis
	ls.StopReason = reason
	if strings.HasPrefix(reason, "breaker:") {
		ls.Cut = reason
	}
	ls.Window, ls.Pending, ls.WrapUp, ls.Consol = nil, nil, 0, 0
	return ls
}

// CanReopen reports whether synthesis may send the run back to exploration once more.
func (ls LoopState) CanReopen(limits Limits) bool {
	return ls.Phase == PhaseSynthesis && ls.Reopens < limits.MaxReopens
}

// Reopen moves synthesis back to exploration (a question was reopened), counting it.
func (ls LoopState) Reopen() LoopState {
	ls.Phase = PhaseExploration
	ls.Reopens++
	ls.StopReason = ""
	ls.Window, ls.Pending, ls.WrapUp = nil, nil, 0
	return ls
}

// Finalized ends the run: the report was accepted. A reason that cut exploration short is kept; else the reason is "finalized".
func (ls LoopState) Finalized() LoopState {
	if ls.StopReason == "" {
		ls.StopReason = StopFinalized
	}
	ls.Done = true
	return ls
}

// Failed ends the run without a report because a breaker fired in synthesis: the stop reason names it.
func (ls LoopState) Failed(reason string) LoopState {
	ls.StopReason = reason
	ls.Done = true
	return ls
}

// OpenCalls are the calls of the window, oldest first (for tests and traces).
func (ls LoopState) OpenCalls() []Call {
	var out []Call
	for _, t := range ls.Window {
		out = append(out, t.Calls...)
	}
	return out
}

// rawTools are the tools whose results are raw material the notes must take in (the extraction rule applies to them).
var rawTools = []string{"web_search", "fetch_page", "query"}

// IsRawTool reports whether the results of the tool are raw material (a search briefing, a page, a knowledge-base answer).
func IsRawTool(name string) bool { return slices.Contains(rawTools, name) }

// ArgOf is the query, URL or question of a call of a raw tool.
func ArgOf(c Call) string {
	var m map[string]any
	if json.Unmarshal(c.Input, &m) != nil {
		return ""
	}
	for _, k := range []string{"query", "url", "question"} {
		if s, ok := m[k].(string); ok {
			return s
		}
	}
	return ""
}

// PendingOf are the pending results a turn brings: the successful results of its raw tools. questionID is the question being worked on.
func PendingOf(t Turn, questionID string) []Pending {
	var out []Pending
	for _, c := range t.Calls {
		if c.IsErr || !IsRawTool(c.Name) {
			continue
		}
		out = append(out, Pending{CallID: c.ID, Tool: c.Name, Arg: ArgOf(c), QuestionID: questionID, Size: len([]rune(c.Output)), Turn: t.N})
	}
	return out
}

// notesWriters are the tools that put what was read into the notes.
var notesWriters = []string{"notes_append", "notes_update", "notes_scratch"}

// WroteNotes reports whether a turn changed the notes (a notes tool call that succeeded).
func WroteNotes(t Turn) bool {
	return slices.ContainsFunc(t.Calls, func(c Call) bool { return !c.IsErr && slices.Contains(notesWriters, c.Name) })
}

// Commits reports whether the turn is a commit point: a turn that wrote the notes, or a forced extraction turn (after which what the model
// did not take is recorded as read, with nothing relevant).
func Commits(t Turn, kind TurnKind) bool { return WroteNotes(t) || kind == TurnExtract }
