package agent_research

// The summary of a report (gb_agent_research_reports.summary): a shorter, higher-level text that says what the report found. It is made
// at the end of a research by one separate model call, with no tools and none of the agent's context: the call sees only the topic and
// the finished report, so it cannot change what the agent did and the agent does not know about it. A failed call never fails the
// research: the report is stored without a summary.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/llm"
)

// summaryTimeout is the longest the summary call of a research may take.
const summaryTimeout = 2 * time.Minute

// summaryMaxTokens is the longest summary the model may write; summaryMaxReportChars cuts the report that is sent (about 50k tokens) so
// that a very long one still fits the model's window.
const (
	summaryMaxTokens      = 1024
	summaryMaxReportChars = 200_000
)

// summarySystemPrompt tells the model what a summary of a report is.
const summarySystemPrompt = `You summarize research reports.
You are given the topic of a research and the report that was written for it. Write a summary of the report that is shorter and at a higher level than the report: the question, the main conclusions and, if the report says so, how reliable they are or what could not be found out. Keep the facts and numbers that matter most. Do not add anything the report does not say, do not mention that you are summarizing, and do not use headings.
Write one to three short paragraphs of plain text, at most 150 words in all.`

// reportSummary is the result of the summary call: the text and what the call spent.
type reportSummary struct {
	Text                      string
	InputTokens, OutputTokens int
	CostUSD                   float64
}

// summarizeReport asks the model (model, or the client's default if "") for the summary of report, the report of a research on topic.
// It is an isolated call: no tools, no history. The error says why there is no summary (no client, an API error, an empty or truncated
// answer); the usage of a call that did answer is returned also with an error.
func summarizeReport(ctx context.Context,
	client *llm.Client,
	model,
	topic,
	report string) (reportSummary, error) {
	if client == nil {
		return reportSummary{}, errors.New("no LLM client")
	}
	ctx, cancel := context.WithTimeout(ctx, summaryTimeout)
	defer cancel()
	resp, err := client.Complete(ctx, llm.Request{
		Model:  model,
		System: summarySystemPrompt,
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: "<topic>\n" + topic + "\n</topic>\n\n<report>\n" + truncate(strings.TrimSpace(report), summaryMaxReportChars) + "\n</report>",
		}},
		MaxTokens: summaryMaxTokens,
	})
	if err != nil {
		return reportSummary{}, err
	}
	out := reportSummary{
		Text:         strings.TrimSpace(resp.Text),
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		CostUSD:      resp.CostUSD,
	}
	switch {
	case resp.StopReason == "max_tokens":
		return out, errors.New("the summary was cut off")
	case out.Text == "":
		return out, errors.New("the summary is empty")
	}
	return out, nil
}
