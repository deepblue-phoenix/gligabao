// Command gligabao_lambda runs one gligabao operation (remember, query or research) in process and exits: the databases (SQLite and the
// chromem vector store, or PostgreSQL / Pinecone with --config) are opened, the operation runs, the databases are closed and the process
// ends. It is for places that start for one job and are thrown away (an AWS Lambda, a container job): no server, HTTP API, admin UI,
// training, evals or daemon, so the binary is small and starts fast. The state is the SQLite file and the vector directory (point --db
// to a writable path such as /tmp/gligabao.db); getting them in before the run and out after it is the caller's job, or --export writes
// them to one archive. See docs/lambda.md.
//
//	OPENAI_API_KEY=... ANTHROPIC_API_KEY=... gligabao_lambda remember "Alice joined Acme in 2021."
//	OPENAI_API_KEY=... ANTHROPIC_API_KEY=... gligabao_lambda query "Where does Alice work?"
//	OPENAI_API_KEY=... ANTHROPIC_API_KEY=... gligabao_lambda research "Solid-state batteries"
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	"github.com/deepblue-phoenix/gligabao/internal/sys/buildinfo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	log "github.com/sirupsen/logrus"
)

// version is set at build time by run_build.sh (-ldflags "-X main.version=$(cat VERSION)").
var version = ""

const (
	envDB       = "GLIGABAO_DB" // the SQLite file, when --db is not given
	defaultUser = "lambda"
	maxTextLen  = 1 << 20 // the text of remember, in bytes
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1 // the operation failed
	exitUsage = 2 // bad flags or input, no API key, a database that cannot be opened
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usage = `Usage: gligabao_lambda COMMAND [flags] ARG | -

Runs one gligabao operation in process, then exits. Commands:

  remember [flags] TEXT | -     store TEXT (- reads it from stdin) in the knowledge base, and wait until it is absorbed
  query    [flags] QUESTION | - answer QUESTION from the knowledge base
  research [flags] TOPIC | -    run a research agent on TOPIC and print its report
  version                       print the version

The result goes to stdout; logs and totals go to stderr. Every command takes the flags of "gligabao_lambda COMMAND -h".
Needs ANTHROPIC_API_KEY and OPENAI_API_KEY (embeddings) in the environment or the --config file.
`

// common are the flags every command has.
type common struct {
	config, db, vectorDir, user, dataset, export string
	timeout                                      time.Duration
	json, verbose                                bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.config, "config", "", "gligabao.toml to read the settings from (API keys, db_backend, postgres_dsn, vector_store, ...); default: the environment only")
	fs.StringVar(&c.db, "db", "", "SQLite database file, with the vector store and the images next to it; default: $"+envDB+", else "+db.DefaultSQLitePath)
	fs.StringVar(&c.vectorDir, "vector-dir", "", "chromem vector store directory (default: next to the database)")
	fs.StringVar(&c.user, "user", defaultUser, "the user whose knowledge base is used")
	fs.StringVar(&c.dataset, "dataset", memory_core.DefaultDataset, "the dataset of the user's knowledge base")
	fs.StringVar(&c.export, "export", "", "when done (also if the operation failed) write the whole local state (SQLite tables, embeddings, images) to this ZIP archive")
	fs.DurationVar(&c.timeout, "timeout", 0, "give up after this long, e.g. 90s (default: no limit)")
	fs.BoolVar(&c.json, "json", false, "print the result as JSON")
	fs.BoolVar(&c.verbose, "verbose", false, "log what the databases and pipelines do to stderr")
}

// run is main without the process: it returns the exit code.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "version", "--version":
		fmt.Fprintln(stdout, versionString())
		return exitOK
	case "remember", "query", "research":
	default:
		fmt.Fprintf(stderr, "gligabao_lambda: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}

	fs := flag.NewFlagSet("gligabao_lambda "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	var (
		kind, at, session, agentName, defsDir, model string
		maxSteps                                     int
		maxCost                                      float64
	)
	switch cmd {
	case "remember":
		fs.StringVar(&kind, "kind", "", "kind of the text: knowledge (default) or episode")
		fs.StringVar(&at, "at", "", "event time of the text, RFC 3339 (default: now)")
		fs.StringVar(&session, "session", "", "session ID to attach to what is remembered")
	case "query":
		fs.StringVar(&session, "session", "", "session ID to label the query with")
	case "research":
		fs.StringVar(&agentName, "agent", "", "the agent definition to run (default: the built-in research agent)")
		fs.StringVar(&defsDir, "agent-defs-dir", "", "directory the agent definitions are loaded from (default: "+agent_defs.DirName+" next to the binary; it need not exist)")
		fs.StringVar(&model, "model", "", "model to run on; it must support Anthropic's web search tool (default: the built-in default)")
		fs.IntVar(&maxSteps, "max-steps", 0, fmt.Sprintf("most model calls (1 to %d; default: the agent's)", agent_research.MaxMaxSteps))
		fs.Float64Var(&maxCost, "max-cost-usd", 0, "cap on what the whole research may spend in USD (0: only the agent's caps apply)")
	}
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: gligabao_lambda %s [flags] ARG | -\n\nFlags:\n", cmd)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	fail := func(code int, err error) int {
		fmt.Fprintf(stderr, "gligabao_lambda: %v\n", err)
		return code
	}

	input, err := readInput(cmd, fs.Args(), stdin)
	if err != nil {
		return fail(exitUsage, err)
	}
	userID, err := memory_core.ResolveUserID(c.user)
	if err != nil {
		return fail(exitUsage, err)
	}
	datasetName, err := memory_core.ResolveDatasetName(c.dataset)
	if err != nil {
		return fail(exitUsage, err)
	}
	var ref time.Time
	if at != "" {
		if ref, err = time.Parse(time.RFC3339, at); err != nil {
			return fail(exitUsage, fmt.Errorf("--at: %w", err))
		}
	}
	if session != "" {
		if session, err = memory_core.ResolveSessionID(session); err != nil {
			return fail(exitUsage, err)
		}
	}
	if maxSteps < 0 || maxSteps > agent_research.MaxMaxSteps {
		return fail(exitUsage, fmt.Errorf("--max-steps must be between 1 and %d", agent_research.MaxMaxSteps))
	}
	if maxCost < 0 {
		return fail(exitUsage, errors.New("--max-cost-usd must be 0 (no cap) or a positive number of USD"))
	}
	var reg *agent_defs.Registry
	if cmd == "research" {
		dir := defsDir
		if dir == "" {
			dir = agent_defs.DefaultDir()
		}
		if reg, err = agent_defs.Load(dir); err != nil {
			return fail(exitUsage, err)
		}
		if _, err = reg.Resolve(agentName, ""); err != nil {
			return fail(exitUsage, fmt.Errorf("--agent: %w", err))
		}
	}

	cfg, err := loadConfig(c.config)
	if err != nil {
		return fail(exitUsage, err)
	}
	client, err := ai.New(cfg.AnthropicKey, ai.WithPrices(ai.NewPriceTable(cfg.AIPrices())))
	if err != nil {
		return fail(exitUsage, err)
	}
	emb, err := ai.NewEmbedder(cfg.OpenAIKey, ai.WithEmbedPrices(ai.NewPriceTable(cfg.AIPrices())), ai.WithEmbedDimensions(cfg.EmbeddingDimensions))
	if err != nil {
		return fail(exitUsage, fmt.Errorf("embedder: %w (set %s or openai_api_key)", err, config.EnvOpenAIKey))
	}

	log.SetOutput(stderr)
	if c.verbose {
		log.SetLevel(log.InfoLevel)
	} else {
		log.SetLevel(log.WarnLevel)
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	path := c.db
	if path == "" {
		path = os.Getenv(envDB)
	}
	a, err := openApp(ctx, cfg, client, emb, path, c.vectorDir, cmd == "research")
	if err != nil {
		return fail(exitUsage, err)
	}
	defer a.close()

	var code int
	switch cmd {
	case "remember":
		code = a.cmdRemember(ctx, stdout, stderr, c.json, memory_remember.Request{
			UserID: userID, Dataset: datasetName, Kind: kind, Text: input, Ref: ref,
			Opts: append(a.rememberOpts[:len(a.rememberOpts):len(a.rememberOpts)], sessionOpt(session)...),
		})
	case "query":
		code = a.cmdQuery(ctx, stdout, stderr, c.json, userID, datasetName, input, sessionOpt(session))
	case "research":
		if err := a.research.UseAgentDefs(reg); err != nil {
			return fail(exitUsage, err)
		}
		code = a.cmdResearch(ctx, stdout, stderr, c.json, userID, agent_research.Options{Topic: input, MaxSteps: maxSteps, Model: model, Dataset: datasetName, Agent: agentName, MaxCostUSD: maxCost})
	}
	if c.export != "" {
		// Also when the operation failed: what was learned until then is worth keeping.
		m, err := a.fullExport(ctx, c.export)
		if err != nil {
			fmt.Fprintf(stderr, "gligabao_lambda: export: %v\n", err)
			if code == exitOK {
				code = exitError
			}
		} else {
			fmt.Fprintf(stderr, "export: %s (%d tables, %d vector datasets, %d images)\n", c.export, len(m.Tables), len(m.Vectors), len(m.Images))
		}
	}
	return code
}

func sessionOpt(session string) []memory_core.Option {
	if session == "" {
		return nil
	}
	return []memory_core.Option{memory_core.WithSessionID(session)}
}

// cmdRemember stores the text and waits for the run to end, so the text is in the databases when the process exits.
func (a *app) cmdRemember(ctx context.Context, stdout, stderr io.Writer, asJSON bool, req memory_remember.Request) int {
	outcome := make(chan memory_remember.Outcome, 1)
	req.Ctx, req.Outcome = ctx, outcome
	runID, err := a.remember.Submit(req)
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_lambda: could not queue the text: %v\n", err)
		return exitError
	}
	var out memory_remember.Outcome
	select {
	case out = <-outcome:
	case <-ctx.Done():
		fmt.Fprintf(stderr, "gligabao_lambda: remember run %d: %v\n", runID, context.Cause(ctx))
		return exitError
	}
	if out.Err != nil {
		fmt.Fprintf(stderr, "gligabao_lambda: remember run %d failed: %v\n", runID, out.Err)
		return exitError
	}
	if asJSON {
		return printJSON(stdout, stderr, out.Result)
	}
	fmt.Fprintf(stdout, "remembered (run %d): %d entities, %d facts, %d facts invalidated\n", runID, len(out.Result.Entities), len(out.Result.Edges), len(out.Result.InvalidatedEdges))
	return exitOK
}

// cmdQuery answers the question. A dataset that does not exist yet is not an error: the knowledge base is just empty.
func (a *app) cmdQuery(ctx context.Context, stdout, stderr io.Writer, asJSON bool, userID, dataset, question string, extra []memory_core.Option) int {
	opts := append(a.queryOpts[:len(a.queryOpts):len(a.queryOpts)], extra...)
	res, err := memory_query.Query(ctx, a.database.Relational, a.llm, userID, dataset, question, opts...)
	if errors.Is(err, memory_core.ErrDatasetNotFound) {
		res, err = &memory_query.QueryResult{Answer: "The knowledge base is empty: nothing was remembered yet."}, nil
	}
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_lambda: query: %v\n", err)
		return exitError
	}
	if asJSON {
		return printJSON(stdout, stderr, res)
	}
	fmt.Fprintln(stdout, strings.TrimSpace(res.Answer))
	return exitOK
}

// researchOutput is what research --json prints.
type researchOutput struct {
	Report       string  `json:"report"`
	RunID        int64   `json:"run_id"`
	Subagents    int     `json:"subagents"`
	Steps        int     `json:"steps"`
	ToolCalls    int     `json:"tool_calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// cmdResearch runs the agent, waits for it, its sub-agents and the text they remembered without waiting, and prints the report.
func (a *app) cmdResearch(ctx context.Context, stdout, stderr io.Writer, asJSON bool, userID string, opts agent_research.Options) int {
	root, err := a.research.Start(ctx, userID, opts)
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_lambda: %v\n", err)
		return exitError
	}
	a.research.Wait()
	if err := a.mem.Drain(ctx); err != nil {
		fmt.Fprintf(stderr, "gligabao_lambda: some text that was queued for the knowledge base was not absorbed: %v\n", err)
	}
	dbCtx := context.WithoutCancel(ctx) // the runs are ended and recorded even if ctx is done
	run, err := a.research.Get(dbCtx, userID, root.ID)
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_lambda: %v\n", err)
		return exitError
	}
	out := researchOutput{Report: run.Report, RunID: run.ID}
	queue := []int64{run.ID}
	all := []core.Run{*run}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		kids, err := a.research.List(dbCtx, userID, agent_research.ListOptions{ParentID: parent, Limit: agent_research.MaxListLimit})
		if err != nil {
			fmt.Fprintf(stderr, "gligabao_lambda: %v\n", err)
			return exitError
		}
		for _, k := range kids {
			all = append(all, k)
			queue = append(queue, k.ID)
		}
	}
	out.Subagents = len(all) - 1
	for _, r := range all {
		out.Steps += r.Steps
		out.ToolCalls += r.ToolCalls
		out.InputTokens += r.InputTokens
		out.OutputTokens += r.OutputTokens
		out.CostUSD += r.CostUSD
	}
	fmt.Fprintf(stderr, "run %d, sub-agents: %d, steps: %d, tool calls: %d, tokens: %d in / %d out, cost: $%.4f\n", out.RunID, out.Subagents, out.Steps, out.ToolCalls, out.InputTokens, out.OutputTokens, out.CostUSD)
	if run.Status != core.StatusCompleted {
		reason := run.Error
		if ctx.Err() != nil {
			reason = fmt.Sprintf("%s (%v)", reason, context.Cause(ctx))
		}
		fmt.Fprintf(stderr, "gligabao_lambda: the research %s: %s\n", run.Status, reason)
		return exitError
	}
	if asJSON {
		return printJSON(stdout, stderr, out)
	}
	fmt.Fprintln(stdout, run.Report)
	return exitOK
}

func printJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "gligabao_lambda: %v\n", err)
		return exitError
	}
	return exitOK
}

// readInput takes the text, question or topic from the only argument, or from stdin if it is "-".
func readInput(cmd string, args []string, stdin io.Reader) (string, error) {
	what := map[string]string{"remember": "text", "query": "question", "research": "topic"}[cmd]
	if len(args) != 1 {
		return "", fmt.Errorf("give exactly one %s as an argument, or - to read it from stdin (see -h)", what)
	}
	s := args[0]
	if s == "-" {
		b, err := io.ReadAll(io.LimitReader(stdin, maxTextLen+1))
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		s = string(b)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty %s", what)
	}
	if len(s) > maxTextLen {
		return "", fmt.Errorf("%s too long (at most %d bytes)", what, maxTextLen)
	}
	if cmd == "research" {
		if n := len([]rune(s)); n > agent_research.MaxTopicLen {
			return "", fmt.Errorf("topic too long (%d characters, at most %d)", n, agent_research.MaxTopicLen)
		}
	}
	return s, nil
}

// loadConfig reads the settings: the file at path, or with no path the defaults, which take the API keys and the PostgreSQL
// connection string from the environment.
func loadConfig(path string) (*config.Config, error) {
	if path != "" {
		return config.Load(path)
	}
	cfg := config.Default()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func versionString() string {
	v := version
	if v == "" {
		v = "dev"
	}
	s := "gligabao_lambda " + v
	if c := buildinfo.Commit(); c != "" {
		s += " (" + c + ")"
	}
	return s
}
