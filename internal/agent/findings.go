package agent

import (
	"context"
	"encoding/json"
)

// ToolSubmitFindings is the terminal tool of a worker of the investigative loop (docs/agents/subagents.md): the only way it ends, and all that
// the supervisor sees of its work.
const ToolSubmitFindings = "submit_findings"

// SubmitFindingsTool is the submit_findings tool: validate checks the input (the findings, as schema describes them) and its error, which
// the worker reads, makes it fix them; with Agent.Terminal set to its name the run ends when it succeeds.
func SubmitFindingsTool(schema map[string]any, validate func(json.RawMessage) error) Tool {
	return Tool{
		Name: ToolSubmitFindings,
		Description: "End your work: hand your findings on the question to the supervisor. Call it once, when you have what you need or cannot get more. " +
			"It is checked: if something is wrong it tells you what, and you send it again.",
		Schema: schema,
		Run: func(_ context.Context, input json.RawMessage) (string, error) {
			if err := validate(input); err != nil {
				return "", err
			}
			return "findings received", nil
		},
	}
}
