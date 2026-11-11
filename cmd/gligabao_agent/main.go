// Command gligabao_agent is the deep-research agent of gligabao as a binary of its own: given a topic it searches the web, reads
// pages and prints a report. It is a stripped-down deployment option for small places (a Lambda, a container with nothing else): no
// server, no HTTP API, no admin UI, no training and no evals. What it does have is what the research jobs of the server have: the
// research manager (internal/agent_research) that runs the agent and its sub-agents and records every run and state in the database,
// and the knowledge base (the memory): the databases (SQLite or PostgreSQL, and the vector store) are opened in process, and the
// agents work with them through their remember and query tools. With --export it ends by writing everything it has (the database, the
// embeddings, the images, the trajectory) to one archive file, for a program that runs it where its storage is thrown away afterwards
// (the Modal runner): see internal/db/dbexport. TestMinimalDependencies lists what must stay out of the binary. See
// docs/deep-research-agent.md.
//
//	ANTHROPIC_API_KEY=... OPENAI_API_KEY=... gligabao_agent "How do solid-state batteries compare with lithium-ion?"
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

	"github.com/deepblue-phoenix/gligabao/internal/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/dbexport"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/buildinfo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	log "github.com/sirupsen/logrus"
)

// version is set at build time by run_build.sh (-ldflags "-X main.version=$(cat VERSION)").
var version = ""

const (
	maxStepsLimit = agent_research.MaxMaxSteps // the same limits as the research job of the server
	maxTopicChars = agent_research.MaxTopicLen
	envDB         = "GLIGABAO_DB" // the SQLite file, when --db is not given
	defaultUser   = "agent"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1 // the agent failed
	exitUsage = 2 // bad flags, no topic, no API key
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// output is what --json prints.
type output struct {
	Report string `json:"report"`
	// RunID is the id of the run of the agent in the database (gb_agent_research_runs); Subagents is how many sub-agents it spawned.
	RunID     int64 `json:"run_id"`
	Subagents int   `json:"subagents"`
	// The totals are those of the whole tree of agents: the agent and its sub-agents.
	Steps        int     `json:"steps"`
	ToolCalls    int     `json:"tool_calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	// Trajectory is the ATIF trajectory of the research, with the steps of all the sub-agents merged in; only with --atif.
	Trajectory *atif.Trajectory `json:"trajectory,omitempty"`
	// Export says what was written to the archive of --export.
	Export *exportSummary `json:"export,omitempty"`
}

// exportSummary is what the archive of --export holds.
type exportSummary struct {
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Tables  int    `json:"tables"`
	Rows    int64  `json:"rows"`
	Vectors int64  `json:"vectors"`
	Images  int    `json:"images"`
	// Skipped is what is not in the archive because it is remote and persisted by its own server: "tables" (PostgreSQL), "vectors" (Pinecone).
	Skipped []string `json:"skipped,omitempty"`
}

// trajectoryFile is the name of the ATIF trajectory in the archive of --export ("extra/trajectory.json").
const trajectoryFile = "trajectory.json"

// run is main without the process: it returns the exit code. The report goes to stdout; everything else (progress, cost, errors) to
// stderr.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gligabao_agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		maxSteps    = fs.Int("max-steps", 0, fmt.Sprintf("most model calls, the last of which writes the report (1 to %d; default: the agent's, 20 for the default agent)", maxStepsLimit))
		agentName   = fs.String("agent", "", "the agent definition to run (default: \"default\", the built-in research agent; with --agent-bundle, the agent the bundle is for)")
		defsDir     = fs.String("agent-defs-dir", "", "directory the agent definitions (TOML or JSON files) are loaded from (default: "+agent_defs.DirName+" next to the binary; it need not exist)")
		bundlePath  = fs.String("agent-bundle", "", "JSON file with the agent definitions to use instead of a directory (what a server sends to an agent that runs where it has no agent_defs directory, e.g. in Modal); not with --agent-defs-dir")
		maxCost     = fs.Float64("max-cost-usd", 0, "cap on what the whole research may spend in USD, sub-agents included; it lowers the caps of the agent's definition (0: only those apply)")
		model       = fs.String("model", "", "model to run on; it must support Anthropic's web search tool (default: the built-in default)")
		timeout     = fs.Duration("timeout", 0, "give up after this long, e.g. 10m (default: no limit)")
		asJSON      = fs.Bool("json", false, "print the report and the totals as JSON instead of the plain report")
		withATIF    = fs.Bool("atif", false, "add the ATIF trajectory of the research (the sub-agents' steps merged into it) to the --json output as \"trajectory\"")
		atifDir     = fs.String("atif-dir", "", "store the ATIF trajectory of the research in this directory as <trajectory_id>.json")
		exportPath  = fs.String("export", "", "after the research (also if it failed) write what the agent has to this one archive file: the whole SQLite database (knowledge base, runs, states, costs), the embeddings of the Chromem vector store, the images and the ATIF trajectory; a PostgreSQL database or a Pinecone index is not exported, it persists its data itself; the format is described in internal/db/dbexport")
		verbose     = fs.Bool("verbose", false, "log every step and tool call to stderr")
		events      = fs.Bool("events", false, "write the progress to stderr as JSON lines ({\"event\":\"state\"|\"usage\",...}), for the program that runs the agent (the Modal runner); not with --verbose")
		showVersion = fs.Bool("version", false, "print the version and exit")
		configPath  = fs.String("config", "", "gligabao.toml to read the settings from (API keys, db_backend, postgres_dsn, vector_store, ...); default: the environment only")
		dbPath      = fs.String("db", "", "SQLite database file (sqlite backend), with the vector store and the images next to it; default: $"+envDB+", else "+db.DefaultSQLitePath)
		vectorDir   = fs.String("vector-dir", "", "chromem vector store directory (default: next to the database)")
		user        = fs.String("user", defaultUser, "the user whose knowledge base the agent works with")
		dataset     = fs.String("dataset", memory_core.DefaultDataset, "the dataset of the user's knowledge base the agent queries and remembers into")
	)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Usage: gligabao_agent [flags] TOPIC | -

Deep-research agent: searches the web, reads pages and prints a report on TOPIC (- reads it from stdin). It may spawn sub-agents
for the parts of a broad topic. --agent picks the agent definition (prompt, tools, search and knowledge settings, cost caps, sub-agents) from the
agent_defs directory next to the binary (see docs/agents/agent-defs.md). Each run costs money (model calls and web searches, sub-agents included).

It works with the knowledge base (the memory of gligabao) in process, through its remember and query tools, and records every run
and its states in the database, as the research jobs of the server do: the database is opened here (SQLite file, or PostgreSQL with
--config), and nothing needs to be running. That needs %s and %s (embeddings) in the environment or the config file.

--export FILE keeps what the agent learned when its storage is not kept: it writes the local state (every table of the SQLite database,
the embeddings of the Chromem vector store, the images, the trajectory) to one ZIP archive, JSON lines inside, that another gligabao
can load. A PostgreSQL database or a Pinecone index persists its data itself, so those are left out.

Flags:
`, config.EnvAnthropicKey, config.EnvOpenAIKey)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if *showVersion {
		fmt.Fprintln(stdout, versionString())
		return exitOK
	}

	if *events && *verbose {
		fmt.Fprintln(stderr, "gligabao_agent: --events and --verbose both write to stderr: use one")
		return exitUsage
	}
	if *withATIF && !*asJSON {
		fmt.Fprintln(stderr, "gligabao_agent: --atif adds the trajectory to the --json output: use it with --json (or --atif-dir to store it in a file)")
		return exitUsage
	}
	topic, err := readTopic(fs.Args(), stdin)
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
		return exitUsage
	}
	if *maxSteps < 0 || *maxSteps > maxStepsLimit {
		fmt.Fprintf(stderr, "gligabao_agent: --max-steps must be between 1 and %d\n", maxStepsLimit)
		return exitUsage
	}
	if *maxCost < 0 {
		fmt.Fprintln(stderr, "gligabao_agent: --max-cost-usd must be 0 (no cap) or a positive number of USD")
		return exitUsage
	}
	reg, bundleAgent, err := loadAgents(*bundlePath, *defsDir)
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
		return exitUsage
	}
	agentToRun := *agentName
	if agentToRun == "" {
		agentToRun = bundleAgent
	}
	if _, err := reg.Resolve(agentToRun, ""); err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: --agent: %v\n", err)
		return exitUsage
	}
	userID, err := memory_core.ResolveUserID(*user)
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
		return exitUsage
	}
	datasetName, err := memory_core.ResolveDatasetName(*dataset)
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
		return exitUsage
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
		return exitUsage
	}
	client, err := ai.New(cfg.AnthropicKey, ai.WithPrices(ai.NewPriceTable(cfg.AIPrices())))
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
		return exitUsage
	}
	emb, err := ai.NewEmbedder(cfg.OpenAIKey, ai.WithEmbedPrices(ai.NewPriceTable(cfg.AIPrices())), ai.WithEmbedDimensions(cfg.EmbeddingDimensions))
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: embedder: %v (set %s or openai_api_key)\n", err, config.EnvOpenAIKey)
		return exitUsage
	}

	log.SetOutput(stderr)
	if *verbose {
		log.SetLevel(log.InfoLevel)
	} else {
		log.SetLevel(log.WarnLevel)
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	path := *dbPath
	if path == "" {
		path = os.Getenv(envDB)
	}
	a, err := openApp(ctx, cfg, client, emb, path, *vectorDir)
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
		return exitUsage
	}
	defer a.close()
	if err := a.research.UseAgentDefs(reg); err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
		return exitUsage
	}

	var onState func(stateEvent)
	var onUsage func(totals)
	switch {
	case *events:
		onState, onUsage = jsonEvents(stderr)
	case *verbose:
		onState = textProgress(stderr)
	}
	var trajectory func() *atif.Trajectory
	if *withATIF || *atifDir != "" || *exportPath != "" {
		trajectory = a.captureTrajectory()
	}
	out, err := a.runResearch(ctx, userID, agent_research.Options{Topic: topic, MaxSteps: *maxSteps, Model: *model, Dataset: datasetName, Agent: agentToRun, MaxCostUSD: *maxCost}, onState, onUsage)
	if err != nil {
		fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
		return exitError
	}
	if out.DrainErr != nil {
		fmt.Fprintf(stderr, "gligabao_agent: some text that was queued for the knowledge base was not absorbed: %v\n", out.DrainErr)
	}
	var traj *atif.Trajectory
	if trajectory != nil {
		// Also for a research that failed: that is when it is most wanted.
		if traj = trajectory(); traj == nil {
			if *withATIF || *atifDir != "" {
				fmt.Fprintln(stderr, "gligabao_agent: no ATIF trajectory was recorded")
			}
		} else if *atifDir != "" {
			if path, err := atif.WriteFile(*atifDir, traj); err != nil {
				fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
			} else {
				fmt.Fprintf(stderr, "ATIF trajectory: %s\n", path)
			}
		}
	}
	var export *exportSummary
	if *exportPath != "" {
		// Also for a research that failed or was stopped: what it learned until then is worth keeping.
		var err error
		if export, err = fullExport(ctx, a, *exportPath, traj, stderr); err != nil {
			fmt.Fprintf(stderr, "gligabao_agent: export: %v\n", err)
		}
	}
	steps, toolCalls, in, outTokens, cost := out.totals()
	fmt.Fprintf(stderr, "run %d, sub-agents: %d, steps: %d, tool calls: %d, tokens: %d in / %d out, cost: $%.4f\n", out.Root.ID, len(out.Kids), steps, toolCalls, in, outTokens, cost)
	exportFailed := *exportPath != "" && export == nil // the archive is what the program that runs this wants: it is a failure without it
	if out.Root.Status != core.StatusCompleted {
		reason := out.Root.Error
		if ctx.Err() != nil {
			reason = fmt.Sprintf("%s (%v)", reason, context.Cause(ctx))
		}
		fmt.Fprintf(stderr, "gligabao_agent: the research %s: %s\n", out.Root.Status, reason)
		return exitError
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(output{out.Root.Report, out.Root.ID, len(out.Kids), steps, toolCalls, in, outTokens, cost, trajectoryIf(*withATIF, traj), export}); err != nil {
			fmt.Fprintf(stderr, "gligabao_agent: %v\n", err)
			return exitError
		}
	} else {
		fmt.Fprintln(stdout, out.Root.Report)
	}
	if exportFailed {
		return exitError
	}
	return exitOK
}

// fullExport writes the archive of --export (the agent's whole state, with the trajectory if there is one) and says so on stderr.
func fullExport(ctx context.Context, a *app, path string, traj *atif.Trajectory, stderr io.Writer) (*exportSummary, error) {
	var extra []dbexport.Extra
	if traj != nil {
		b, err := atif.Marshal(traj)
		if err != nil {
			return nil, fmt.Errorf("the trajectory: %w", err)
		}
		extra = append(extra, dbexport.Extra{Name: trajectoryFile, Data: b})
	}
	m, err := a.fullExport(ctx, path, extra...)
	if err != nil {
		return nil, err
	}
	sum := &exportSummary{Path: path, Tables: len(m.Tables), Images: len(m.Images)}
	for _, t := range m.Tables {
		sum.Rows += t.Rows
	}
	for _, v := range m.Vectors {
		sum.Vectors += v.Count
	}
	for _, sk := range m.Skipped {
		sum.Skipped = append(sum.Skipped, sk.What)
		fmt.Fprintf(stderr, "export: no %s: %s\n", sk.What, sk.Reason)
	}
	if info, err := os.Stat(path); err == nil {
		sum.Bytes = info.Size()
	}
	fmt.Fprintf(stderr, "export: %s (%d bytes: %d rows in %d tables, %d vectors, %d images)\n", path, sum.Bytes, sum.Rows, sum.Tables, sum.Vectors, sum.Images)
	return sum, nil
}

// trajectoryIf is t if on, else nil (left out of the JSON).
func trajectoryIf(on bool, t *atif.Trajectory) *atif.Trajectory {
	if on {
		return t
	}
	return nil
}

// loadAgents loads the agent definitions: from the bundle file, or from the directory (default: next to the binary; a directory that does not
// exist leaves only the built-in default agent). It returns the registry and the agent the bundle is for ("" without a bundle).
func loadAgents(bundlePath, dir string) (*agent_defs.Registry, string, error) {
	switch {
	case bundlePath != "" && dir != "":
		return nil, "", errors.New("--agent-bundle and --agent-defs-dir both say where the agent definitions are: use one")
	case bundlePath != "":
		return agent_defs.LoadBundle(bundlePath)
	}
	if dir == "" {
		dir = agent_defs.DefaultDir()
	}
	reg, err := agent_defs.Load(dir)
	return reg, "", err
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

// readTopic takes the topic from the only argument, or from stdin if it is "-".
func readTopic(args []string, stdin io.Reader) (string, error) {
	if len(args) != 1 {
		return "", errors.New("give exactly one topic as an argument, or - to read it from stdin (see --help)")
	}
	topic := args[0]
	if topic == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		topic = string(b)
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return "", errors.New("empty topic")
	}
	if n := len([]rune(topic)); n > maxTopicChars {
		return "", fmt.Errorf("topic too long (%d characters, at most %d)", n, maxTopicChars)
	}
	return topic, nil
}

func versionString() string {
	v := version
	if v == "" {
		v = "dev"
	}
	s := "gligabao_agent " + v
	if c := buildinfo.Commit(); c != "" {
		s += " (" + c + ")"
	}
	return s
}
