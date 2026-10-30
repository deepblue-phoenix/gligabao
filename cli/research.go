package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// researchPollInterval is how often `research start --wait` asks the server how the agent is doing.
var researchPollInterval = 2 * time.Second

func (c *CLI) researchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "research",
		Short: "Start, list and follow deep-research agents",
		Long: `A research agent searches the web and reads pages, over many steps, in the background, and ends with a report on a topic. Every
state it goes through (searching, reading, thinking, ...) is recorded, so that you can follow what it does (docs/research.md). Each one
costs money: model calls and web searches (it needs an Anthropic key).` + clientHelp,
	}
	cmd.AddCommand(c.researchStartCmd(), c.researchListCmd(), c.researchGetCmd())
	return cmd
}

func (c *CLI) researchStartCmd() *cobra.Command {
	var (
		jsonBody string
		maxSteps int
		model    string
		dataset  string
		wait     bool
		raw      bool
	)
	cmd := &cobra.Command{
		Use:   "start [TOPIC | -]",
		Short: "Start a research agent on a topic (POST /v1/gb/research)",
		Long: `Start a deep-research agent on a topic and print the run (JSON of POST /v1/gb/research, see docs/research.md) right away: the agent
goes on in the background. Follow it with "research get ID"; with --wait the command waits for the agent to end, and prints the finished
run (with its report). The topic is the argument, or stdin if the argument is "-". Or give the whole request with --json.
` + clientHelp,
		Example: `  gligabao research start "How do solid-state batteries compare with lithium-ion in 2025?"
  gligabao research start "EU AI Act obligations for startups" --max-steps 10 --wait --raw
  gligabao research ls --active`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if raw && !wait {
				return usageErr("--raw needs --wait: the report only exists when the agent has ended")
			}
			m, err := bodyFromFlags(cmd, jsonBody)
			if err != nil {
				return err
			}
			b := &body{m: m}
			b.default_("user_id", c.flags.user)
			switch {
			case len(args) == 1 && args[0] == "-":
				text, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return usageErr("read stdin: %v", err)
				}
				b.set("topic", string(text))
			case len(args) == 1:
				b.set("topic", args[0])
			}
			if t, _ := b.m["topic"].(string); strings.TrimSpace(t) == "" {
				return usageErr("no topic: give it as an argument, on stdin (-) or with --json")
			}
			if cmd.Flags().Changed("max-steps") {
				b.set("max_steps", maxSteps)
			}
			if cmd.Flags().Changed("model") {
				b.set("model", model)
			}
			if cmd.Flags().Changed("dataset") {
				b.set("dataset", dataset)
			}
			cl, err := c.client(cmd)
			if err != nil {
				return err
			}
			out, err := cl.call(cmd.Context(), "POST", "/v1/gb/research", nil, b.bytes())
			if err != nil {
				return err
			}
			if !wait {
				return printResearch(cmd, out, raw)
			}
			var started struct {
				ID     int64  `json:"id"`
				UserID string `json:"user_id"`
			}
			if err := json.Unmarshal(out, &started); err != nil || started.ID == 0 {
				return fmt.Errorf("unexpected response of the server: %s", strings.TrimSpace(string(out)))
			}
			for {
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(researchPollInterval):
				}
				out, err = cl.call(cmd.Context(), "GET", "/v1/gb/research/"+strconv.FormatInt(started.ID, 10), url.Values{"user_id": {started.UserID}}, nil)
				if err != nil {
					return err
				}
				var run struct {
					Status string `json:"status"`
					Error  string `json:"error"`
				}
				if err := json.Unmarshal(out, &run); err != nil {
					return err
				}
				if run.Status != "running" {
					if err := printResearch(cmd, out, raw); err != nil {
						return err
					}
					if run.Status == "failed" {
						return &exitError{code: exitOther, msg: "the research failed: " + run.Error}
					}
					return nil
				}
			}
		},
	}
	f := cmd.Flags()
	f.StringVar(&jsonBody, "json", "", `the request body as JSON ('{"topic": "..."}', @file, or - for stdin); the flags below override its fields`)
	f.IntVar(&maxSteps, "max-steps", 0, "the most model calls the agent makes (default 20, at most 40); more research more, and cost more")
	f.StringVar(&model, "model", "", "the model the agent runs on (must support web search; default: the server's)")
	f.StringVarP(&dataset, "dataset", "d", "", `the dataset of your knowledge base the agent queries and remembers into (default "main")`)
	f.BoolVar(&wait, "wait", false, "wait until the agent ends, and print the finished run (exit code 1 if it failed)")
	f.BoolVar(&raw, "raw", false, "with --wait: print only the report, as text")
	return cmd
}

func (c *CLI) researchListCmd() *cobra.Command {
	var (
		active bool
		status string
		limit  int
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the research agents of the user (GET /v1/gb/research)",
		Long:  "List the research agents of the user, newest first, with their status, state, steps and cost. With --active, only those working now." + clientHelp,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{"user_id": {c.flags.user}}
			if active {
				q.Set("active", "true")
			}
			if status != "" {
				q.Set("status", status)
			}
			if cmd.Flags().Changed("limit") {
				q.Set("limit", strconv.Itoa(limit))
			}
			return c.send(cmd, "GET", "/v1/gb/research", q, nil)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&active, "active", false, "only the agents that are working now")
	f.StringVar(&status, "status", "", "only the agents with this status: running, completed or failed")
	f.IntVar(&limit, "limit", 0, "the most agents listed (default 50, at most 500)")
	return cmd
}

func (c *CLI) researchGetCmd() *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "get ID",
		Short: "Show a research agent, its states and its report (GET /v1/gb/research/{id})",
		Long: `Show a research agent: its status, state, cost and every state it went through, in order, with what each was about (the query, the
URL); the report is in it once the status is completed. With --raw only the report is printed, as text.` + clientHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id < 1 {
				return usageErr("ID must be a positive integer")
			}
			cl, err := c.client(cmd)
			if err != nil {
				return err
			}
			out, err := cl.call(cmd.Context(), "GET", "/v1/gb/research/"+args[0], url.Values{"user_id": {c.flags.user}}, nil)
			if err != nil {
				return err
			}
			return printResearch(cmd, out, raw)
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "print only the report, as text")
	return cmd
}

// printResearch prints a run: as JSON, or with raw only its report (an error if it has none yet).
func printResearch(cmd *cobra.Command, out []byte, raw bool) error {
	if !raw {
		emit(cmd, out)
		return nil
	}
	var run struct {
		Status string `json:"status"`
		Report string `json:"report"`
	}
	if err := json.Unmarshal(out, &run); err != nil {
		return err
	}
	if run.Report == "" {
		return &exitError{code: exitOther, msg: fmt.Sprintf("no report: the research is %s", run.Status)}
	}
	fmt.Fprintln(cmd.OutOrStdout(), run.Report)
	return nil
}
