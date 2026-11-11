package investigate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// LoopRegistry is the registry of the investigative loop: the tools of stage 1 and the two that end a phase. The loop (loop.go) decides
// from a successful call of finish_exploration or finalize_report that the phase ended; the handlers only run the gates.
func LoopRegistry() Registry {
	r := InvestigativeRegistry()
	return Registry{tools: append(r.Tools(), finishExplorationTool, finalizeReportTool)}
}

func problemsError(head string, problems []string) error {
	return errors.New(head + ":\n- " + strings.Join(problems, "\n- "))
}

var finishExplorationTool = Tool{
	Name:        ToolFinishExploration,
	Description: "End the exploration and start writing the report. The code checks the exploration gate: the root question resolved, no actionable pending question of high priority, enough sources and domains, every resolved question backed by a VERIFIED claim (or a stated reason), no unaddressed contradiction, no open challenge, and a conclusion that survived a challenge. If something is missing the call fails and lists it.",
	Schema:      obj(props{}),
	Handler: func(s State, _ json.RawMessage) (Result, error) {
		if missing := ExplorationGate(s); len(missing) > 0 {
			return Result{}, problemsError("the exploration is not finished", missing)
		}
		return Result{State: s, Text: "exploration finished: the report is written next, from the notes and the frontier"}, nil
	},
}

var finalizeReportTool = Tool{
	Name:        ToolFinalizeReport,
	Description: "Submit the report: the only way a run ends. Summary first, then the details, a section on gaps and assumptions (every unresolvable question by its id), the verification status of the key claims, and the sources. Cite claim ids and source ids; the code checks that they exist, that none cited claim is REFUTED and that every unresolvable question is in the report.",
	Schema:      obj(props{"report": str("the whole report, in Markdown")}, "report"),
	Handler: func(s State, raw json.RawMessage) (Result, error) {
		var a struct {
			Report string `json:"report"`
		}
		if err := decode(raw, &a); err != nil {
			return Result{}, err
		}
		if problems := FinalizationGate(s, a.Report); len(problems) > 0 {
			return Result{}, problemsError("the report cannot be accepted", problems)
		}
		return Result{State: s, Text: fmt.Sprintf("report accepted (%d characters)", len([]rune(a.Report)))}, nil
	},
}
