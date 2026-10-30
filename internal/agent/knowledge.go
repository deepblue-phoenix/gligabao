package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// maxRememberChars is the longest text one remember call takes: a snippet, not a whole page.
const maxRememberChars = 20000

// Knowledge is the internal knowledge base (the memory of gligabao) a research agent works with, for one user and one dataset. It
// gives the agent its remember and query tools; an agent without one has neither.
type Knowledge interface {
	// Remember adds text to the knowledge base. With wait it blocks until the text is absorbed (the graph is updated, so that a
	// query can find it) and returns what was stored. Without it, it only queues the text and returns at once, with how to find
	// the run; if the run fails later nobody is told (it is in the logs and the run log of the remember pipeline).
	Remember(ctx context.Context, text string, wait bool) (string, error)
	// Query answers the question from the knowledge base and returns the answer with the facts it is based on.
	Query(ctx context.Context, question string) (string, error)
}

// rememberTool is the tool that submits a snippet to the knowledge base.
func rememberTool(kb Knowledge) Tool {
	return Tool{
		Name: "remember",
		Description: "Store an important snippet (a fact, a figure, a quote, with its date and context) in the knowledge base, so that it " +
			"can be found later with query. Call it for the useful parts of what you find, not for whole pages. " +
			"By default (sync=false) it only queues the text and returns at once: use this when you can go on without having it in the " +
			"knowledge base. Set sync=true only when you need it absorbed first, e.g. to query for it right after.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text":   map[string]any{"type": "string", "description": "The snippet to remember: self-contained, with names, dates and numbers (at most 20000 characters)."},
				"source": map[string]any{"type": "string", "description": "Where it comes from, normally the URL of the page (optional)."},
				"sync":   map[string]any{"type": "boolean", "description": "true: wait until the text is absorbed into the knowledge base. false (default): queue it and go on."},
			},
			"required": []string{"text"},
		},
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in struct {
				Text   string `json:"text"`
				Source string `json:"source"`
				Sync   bool   `json:"sync"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			text := strings.TrimSpace(in.Text)
			if text == "" {
				return "", errors.New(`missing "text"`)
			}
			if len([]rune(text)) > maxRememberChars {
				return "", fmt.Errorf("text is too long (at most %d characters): remember the important parts only, or in several calls", maxRememberChars)
			}
			if src := strings.TrimSpace(in.Source); src != "" {
				text = "Source: " + src + "\n\n" + text
			}
			return kb.Remember(ctx, text, in.Sync)
		},
	}
}

// queryTool is the tool that asks the knowledge base.
func queryTool(kb Knowledge) Tool {
	return Tool{
		Name: "query",
		Description: "Ask the internal knowledge base (what was remembered before, in earlier researches or earlier in this one). Returns " +
			"an answer from it and the facts it is based on. Use it besides web_search and fetch_url, before searching for something that " +
			"may be known already and to check what you found. Text queued with remember (sync=false) may not be there yet.",
		Schema: objectSchema("question", "What to ask, as a question or a few keywords."),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			q, err := stringArg(input, "question")
			if err != nil {
				return "", err
			}
			return kb.Query(ctx, q)
		},
	}
}
