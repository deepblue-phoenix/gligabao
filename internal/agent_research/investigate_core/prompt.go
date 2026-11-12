package investigate_core

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The turn builder: the messages of ONE turn, made from the loop state and nothing else. The history is not accumulated: the prompt is the
// system prompt, the tools of the phase, the task, the scratchpad (the notes and the frontier summary, with the open challenges) and the
// last keep_turns turns, so its size does not depend on how many turns came before.

// ToolSpec is a tool as the model is told about it.
type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Request is one model call, without the transport: the system prompt, the tools offered, the messages and (if not "") the tool the model
// is forced to call.
type Request struct {
	System    string
	Tools     []ToolSpec
	Messages  []any
	ForceTool string
}

// Prompt is what stays the same over the run: the system prompt of the agent, the task, and the tools there are.
type Prompt struct {
	// System is the system prompt of the agent definition (with the parts the runtime adds for its tools).
	System string
	// Task is the objective of the run.
	Task string
	// Catalog is every tool of the run: the web and knowledge tools of the agent, the tools of the registry, finish_exploration and
	// finalize_report. External are the names of the first kind.
	Catalog  []ToolSpec
	External []string
}

// Tool names the loop itself knows.
const (
	ToolFinishExploration = "finish_exploration"
	ToolFinalizeReport    = "finalize_report"
	// ToolRunCode is the code tool (docs/agents/monty-code-execution.md); an external tool like web_search.
	ToolRunCode = "run_code"
)

var notesToolNames = []string{"notes_read", "notes_append", "notes_update", "notes_scratch"}

// OfferedTools are the tools the model may call in a turn of this kind. A forced-extraction or consolidation turn has the notes tools
// only; the planning turn has the tools to build the first level; frontier_next is never offered to the model in exploration (the loop
// selects the question); synthesis has no search tool.
func OfferedTools(kind TurnKind, external, registry []string) []string {
	switch kind {
	case TurnExtract, TurnConsolidate:
		return slices.Clone(notesToolNames)
	case TurnSynthesize, TurnFinal:
		return []string{"notes_read", "frontier_view", "reopen_question", ToolFinalizeReport}
	case TurnPlanning:
		return slices.Concat(external, []string{"add_question", "set_priority", "frontier_view", "frontier_next"}, notesToolNames)
	}
	rest := slices.DeleteFunc(slices.Clone(registry), func(n string) bool {
		return n == "frontier_next" || n == ToolFinishExploration || n == ToolFinalizeReport
	})
	return slices.Concat(external, rest, []string{ToolFinishExploration})
}

// NextKind is what the next model call is for. In synthesis it is the synthesis turn, forced to finalize_report when it is the last the run
// may take. In exploration: a forced extraction when a result is large or about to leave the window; a consolidation when the notes are
// over their budget (one turn, a second only if the first did not make them smaller); the planning turn while the objective has no
// sub-question; else a normal turn.
func NextKind(ls LoopState, s State) TurnKind {
	if ls.Phase == PhaseSynthesis {
		if IsLastTurn(ls, s.Limits) {
			return TurnFinal
		}
		return TurnSynthesize
	}
	if _, ok := NeedsExtraction(ls, s.Limits.ExtractionThreshold, s.Limits.KeepTurns); ok {
		return TurnExtract
	}
	if NeedsConsolidation(ls, s) {
		return TurnConsolidate
	}
	if root, ok := s.Frontier.Root(); ok && len(s.Frontier.Children(root.ID)) == 0 && !isAtomic(s.Notes) {
		return TurnPlanning
	}
	return TurnExplore
}

// NeedsConsolidation reports whether the next turn is a consolidation: the notes are over notes_max_tokens and no consolidation was done,
// or one was done and did not reduce them.
func NeedsConsolidation(ls LoopState, s State) bool {
	size := TokenEstimate(s.Notes)
	if size <= s.Limits.NotesMaxTokens {
		return false
	}
	return ls.Consol == 0 || (ls.Consol == 1 && size >= ls.ConsolFrom)
}

// AfterConsolidation records a consolidation turn: the size of the notes before it and after.
func AfterConsolidation(ls LoopState, before, after int, limits Limits) LoopState {
	if after <= limits.NotesMaxTokens {
		ls.Consol, ls.ConsolFrom = 0, 0
		return ls
	}
	ls.Consol, ls.ConsolFrom = ls.Consol+1, before
	return ls
}

// CurrentID is the id of the question being worked on ("" if none: the root counts only when the objective is atomic or has no
// sub-question yet).
func CurrentID(s State) string {
	cur, ok := s.Frontier.Current()
	if !ok {
		return ""
	}
	return cur.ID
}

// StateOf is the state of the research a turn is recorded in (gb_agent_research_states.state).
func StateOf(kind TurnKind, ls LoopState, s State) string {
	switch kind {
	case TurnPlanning:
		return StatePlanning
	case TurnExtract:
		return StateExtracting
	case TurnConsolidate:
		return StateConsolidating
	case TurnSynthesize, TurnFinal:
		return StateSynthesizing
	}
	if len(OpenChallenges(s.Challenges)) > 0 {
		return StateChallenging
	}
	if len(unchallenged(s)) > 0 {
		return StateVerifying
	}
	return stateThinking
}

// unchallenged are the UNVERIFIED claims of the current question that have no challenge yet: the ones the prompt asks to challenge.
func unchallenged(s State) []Claim {
	cur := CurrentID(s)
	var out []Claim
	for _, c := range s.Notes.Claims {
		if c.Status != ClaimUnverified || c.QuestionID != cur || cur == "" {
			continue
		}
		if len(ChallengesOn(s.Challenges, TargetClaim, c.ID)) == 0 {
			out = append(out, c)
		}
	}
	return out
}

// GapsText lists the questions that did not end resolved, with their reasons: for the synthesis prompt, so that the report says what could
// not be answered.
func GapsText(f Frontier) string {
	var b strings.Builder
	for _, q := range f.Questions {
		switch q.Status {
		case StatusUnresolvable:
			fmt.Fprintf(&b, "- [%s] unresolvable: %s. Assumption: %s\n", q.ID, q.Question, orElse(q.Answer, "(none stated)"))
		case StatusPruned:
			fmt.Fprintf(&b, "- [%s] pruned: %s. Reason: %s\n", q.ID, q.Question, orElse(q.Answer, "(none stated)"))
		case StatusPending, StatusBlocked, StatusInProgress:
			if q.ParentID != "" {
				fmt.Fprintf(&b, "- [%s] still %s (not investigated): %s\n", q.ID, q.Status, q.Question)
			}
		}
	}
	return b.String()
}

const exploreInstructions = `You are running an investigation, one turn at a time. The loop keeps the working state for you: the notes (notes.md) and the frontier of questions are shown in every turn, and older tool outputs are dropped, so anything you want to keep must be in the notes.

Each turn: work on the current question with the web and knowledge tools; when a result has something useful, put it in the notes (notes_append: claims go to Unverified with their source, numbers to Datapoints, every page you used to Sources). Claims start UNVERIFIED; check an important claim with the challenge tool (a search against it), then record_challenge_result; a claim becomes VERIFIED only through notes_update, when the code accepts it. When the current question is answered, resolve_question it; when an attempt gave nothing, retry_question with a different query. Use add_question for a gap or contradiction a source brings.

Questions of depth 0 and 1 need a closed challenge of their answer (or a stated reason) before resolve_question. When the questions are done, state the conclusion in a few sentences in Scratch on a line that starts with "Conclusion:", challenge it (target_type conclusion), and call finish_exploration; it lists what is missing if you are not done. Never write the report in this phase. A turn without a tool call does not end anything: you will be asked to continue.`

const planningInstructions = `The investigation has just started. Break the objective into 3 to 8 sub-questions with add_question (each with validation criteria and a priority), or call frontier_next with atomic_reason if the objective cannot be split. You may search first to learn the lay of the land. When the first level is there, the loop picks the question to work on.`

const extractInstructions = `This turn only extracts: you have the notes tools and nothing else. Put into the notes what matters for the question from the result you just got: claims (Unverified, each linked to its source), datapoints and the sources. If nothing in it is relevant, add the source with the note "read, nothing relevant". The result is dropped from the history after this turn.`

const consolidateInstructions = `The notes are over their size budget. This turn only consolidates: merge duplicates, shorten Scratch, and move settled claims to Verified (notes_update, which the code checks). Keep every source and every open gap.`

const synthesisInstructions = `The investigation is over: write the report. You cannot search. Work from the notes and the frontier below. The report starts with a summary, then the details, then a section on gaps and assumptions (every unresolvable question, by its id, with its assumption), the verification status of the key claims and the sources. Cite claim ids (c_001, d_001) and source ids (s_001) for every statement: the loop turns the source ids into URLs. Do not cite a claim that is REFUTED. Say plainly what could not be covered. Submit it with finalize_report; if it lists problems, fix them. Only if a hole would change the conclusion, reopen_question sends the run back to exploration (once).`

// Reminder is what the loop says when the model answered without a tool call: that is not an end, here is what is still open.
func Reminder(kind TurnKind, s State) string {
	switch kind {
	case TurnSynthesize, TurnFinal:
		return "You answered without calling a tool. The run ends only with finalize_report(report): call it now with the report."
	case TurnExtract, TurnConsolidate:
		return "You answered without calling a tool. Use the notes tools for this turn."
	}
	var b strings.Builder
	b.WriteString("You answered without calling a tool; that does not end the investigation. Continue with a tool call.\n")
	b.WriteString(Summary(s.Frontier, CurrentID(s), s.Limits))
	b.WriteString(ChallengeSummary(s.Challenges))
	return b.String()
}

// WrapUpTurns is how many turns the model gets to wrap up (resolve the root, state and challenge the conclusion, finish) after the
// frontier ran out.
const WrapUpTurns = 6

// instruction is the last block of the prompt: what this turn is for.
func instruction(kind TurnKind, ls LoopState, s State) string {
	switch kind {
	case TurnPlanning:
		return planningInstructions
	case TurnConsolidate:
		return fmt.Sprintf("%s\nThe notes are about %d tokens; the budget is %d.", consolidateInstructions, TokenEstimate(s.Notes), s.Limits.NotesMaxTokens)
	case TurnExtract:
		p, _ := NeedsExtraction(ls, s.Limits.ExtractionThreshold, s.Limits.KeepTurns)
		q := orElse(p.QuestionID, CurrentID(s))
		return fmt.Sprintf("%s\nExtract what matters for question %s from this result.", extractInstructions, q)
	case TurnSynthesize:
		return synthesisInstructions
	case TurnFinal:
		return synthesisInstructions + "\nThis is the last turn: call finalize_report now."
	}
	var parts []string
	if cur, ok := s.Frontier.Current(); ok {
		parts = append(parts, "Current question:\n"+questionView(cur))
		if RequiresAnswerChallenge(cur) {
			parts = append(parts, fmt.Sprintf("%s has depth %d: before resolve_question, its answer needs a closed challenge (target_type question_answer), or a stated reason.", cur.ID, cur.Depth))
		}
	}
	if cs := unchallenged(s); len(cs) > 0 {
		ids := make([]string, len(cs))
		for i, c := range cs {
			ids[i] = c.ID
		}
		parts = append(parts, fmt.Sprintf("Unchallenged UNVERIFIED claims on this question: %s. Challenge the relevant ones: call challenge, then the search with the registered query, then record_challenge_result.", strings.Join(ids, ", ")))
	}
	if open := ChallengeSummary(s.Challenges); open != "" {
		parts = append(parts, open+"Run its search (web_search with exactly its query), then record_challenge_result.")
	}
	if ls.WrapUp > 0 {
		parts = append(parts, "The frontier has no actionable question left. Wrap up: resolve the root question, state the conclusion in Scratch (\"Conclusion: ...\"), challenge it, then call finish_exploration.")
	}
	if len(parts) == 0 || ls.WrapUp > 0 || !hasActionable(s) {
		if missing := ExplorationGate(s); len(missing) > 0 {
			parts = append(parts, "Before finish_exploration passes:\n- "+strings.Join(missing, "\n- "))
		} else {
			parts = append(parts, "The exploration gate passes: call finish_exploration.")
		}
	}
	return strings.Join(parts, "\n\n")
}

func hasActionable(s State) bool {
	return slices.ContainsFunc(s.Frontier.Questions, func(q Question) bool { return s.Frontier.Actionable(q) && q.Priority >= s.Limits.GatePriority })
}

// systemText is the system prompt of a turn: the agent's own and the loop's instructions for the phase.
func systemText(p Prompt, kind TurnKind) string {
	var phase string
	switch kind {
	case TurnSynthesize, TurnFinal:
		phase = synthesisInstructions
	case TurnExtract:
		phase = extractInstructions
	case TurnConsolidate:
		phase = consolidateInstructions
	case TurnPlanning:
		phase = exploreInstructions + "\n\n" + planningInstructions
	default:
		phase = exploreInstructions
	}
	if kind == TurnPlanning || kind == TurnExplore {
		if slices.Contains(p.External, ToolRunCode) {
			phase += "\n\n" + codeInstructions
		}
	}
	return strings.TrimSpace(p.System + "\n\n" + phase)
}

// codeInstructions is the block added to the instructions of the turns that offer run_code.
const codeInstructions = `## Code
You have run_code: a short Python program in a sandbox, for exact work you would otherwise do in your head: arithmetic, unit conversions, ratios, counting, sorting, filtering or parsing a long text, checking whether two figures agree. Compute exact numbers rather than estimating them.
- A run is stateless (variables do not survive to the next call) and the sandbox is a limited subset of Python: no third-party packages, no files, no network. Only what you print and the value of the last expression come back, so print the result, not the data.
- It reads the state (the functions listed in the tool); it cannot change the frontier, the notes or the claims. Record what you learn with the normal tools.
- The code is not a source. A number you computed is recorded as a claim or datapoint with the sources of its inputs.`

type blocks = []any

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

// head is the first user message: the task and the scratchpad.
func head(p Prompt, kind TurnKind, ls LoopState, s State) blocks {
	out := blocks{textBlock("# Task\n" + p.Task)}
	out = append(out, textBlock("# Notes (notes.md)\n"+Render(s.Notes)))
	switch kind {
	case TurnSynthesize, TurnFinal:
		out = append(out, textBlock("# Frontier\n"+Summary(s.Frontier, "", s.Limits)))
		if g := GapsText(s.Frontier); g != "" {
			out = append(out, textBlock("# Questions that did not end resolved (they are gaps of the report)\n"+g))
		}
		if ls.Cut != "" {
			out = append(out, textBlock(fmt.Sprintf("# The investigation was cut short by `%s`\nSay so in the report; the questions still open are listed above as gaps.", ls.Cut)))
		}
	default:
		fr := "# Frontier\n" + Summary(s.Frontier, CurrentID(s), s.Limits)
		if open := ChallengeSummary(s.Challenges); open != "" {
			fr += "\n# Open challenges\n" + open
		}
		out = append(out, textBlock(fr))
	}
	return out
}

func turnMessages(t Turn) (assistant, user map[string]any) {
	content := make([]any, len(t.Content))
	for i, c := range t.Content {
		content[i] = c
	}
	if len(content) == 0 {
		content = []any{textBlock("(no output)")}
	}
	var res blocks
	for _, c := range t.Calls {
		res = append(res, map[string]any{"type": "tool_result", "tool_use_id": c.ID, "content": c.Output, "is_error": c.IsErr})
	}
	if t.Nudge != "" {
		res = append(res, textBlock(t.Nudge))
	}
	if len(res) == 0 {
		res = blocks{textBlock("Continue.")}
	}
	return map[string]any{"role": "assistant", "content": content}, map[string]any{"role": "user", "content": res}
}

// BuildRequest builds the model call of one turn of the given kind from the loop state and the state of the investigation. Nothing else
// goes in: the window holds only the last turns (Evict) and the uncommitted ones.
func BuildRequest(p Prompt, kind TurnKind, ls LoopState, s State) Request {
	registry := make([]string, 0, len(p.Catalog))
	for _, t := range p.Catalog {
		if !slices.Contains(p.External, t.Name) {
			registry = append(registry, t.Name)
		}
	}
	offered := OfferedTools(kind, p.External, registry)
	var tools []ToolSpec
	for _, t := range p.Catalog {
		if slices.Contains(offered, t.Name) {
			tools = append(tools, t)
		}
	}
	msgs := []any{map[string]any{"role": "user", "content": head(p, kind, ls, s)}}
	instr := textBlock(instruction(kind, ls, s))
	if len(ls.Window) == 0 {
		msgs[0].(map[string]any)["content"] = append(msgs[0].(map[string]any)["content"].(blocks), instr)
	}
	for i, t := range ls.Window {
		a, u := turnMessages(t)
		if i == len(ls.Window)-1 {
			u["content"] = append(slices.Clone(u["content"].(blocks)), instr)
		}
		msgs = append(msgs, a, u)
	}
	req := Request{
		System:   systemText(p, kind),
		Tools:    tools,
		Messages: msgs,
	}
	if kind == TurnFinal {
		req.ForceTool = ToolFinalizeReport
	}
	return req
}

// Payload is the request as the Messages API takes it (with the model and max_tokens the caller adds).
func (r Request) Payload() map[string]any {
	specs := make([]any, len(r.Tools))
	for i, t := range r.Tools {
		specs[i] = map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Schema}
	}
	out := map[string]any{"messages": r.Messages}
	if r.System != "" {
		out["system"] = r.System
	}
	if len(specs) > 0 {
		out["tools"] = specs
		if r.ForceTool != "" {
			out["tool_choice"] = map[string]any{"type": "tool", "name": r.ForceTool}
		}
	}
	return out
}

// Size is the length in bytes of the request as JSON (for tests: the prompt does not grow with the history).
func (r Request) Size() int {
	b, _ := json.Marshal(r.Payload())
	return len(b)
}
