// Command gligabao_worker runs one gligabao memory pipeline (remember or query) in process and exits, over the PostgreSQL database and the
// remote vector store (Pinecone or S3 Vectors) of the main gligabao instance, so both see the same data at once and nothing is synced
// or exported. It is for places that start for one job and are thrown away (an AWS Lambda, a container job, a sub-process of an
// orchestrator agent): no server, HTTP API, admin UI, agents, training, evals or daemon, so the binary is small and starts fast. There is
// no SQLite and no local vector store. See docs/worker_binary.md.
//
//	OPENAI_API_KEY=... ANTHROPIC_API_KEY=... gligabao_worker remember "Alice joined Acme in 2021."
//	OPENAI_API_KEY=... ANTHROPIC_API_KEY=... gligabao_worker query "Where does Alice work?"
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

	"github.com/deepblue-phoenix/gligabao/internal/ai"
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
	defaultUser = "worker"
	maxTextLen  = 1 << 20 // the text of remember, in bytes
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1 // the operation failed
	exitUsage = 2 // bad flags or input, no API key, a database that cannot be opened
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt,
		syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx,
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr))
}

const usage = `Usage: gligabao_worker COMMAND [flags] ARG | -

Runs one gligabao memory pipeline in process, then exits. Commands:

  remember [flags] TEXT | -     store TEXT (- reads it from stdin) in the knowledge base, and wait until it is absorbed
  query    [flags] QUESTION | - answer QUESTION from the knowledge base
  version                       print the version

The result goes to stdout; logs and totals go to stderr. Every command takes the flags of "gligabao_worker COMMAND -h".
Needs ANTHROPIC_API_KEY and OPENAI_API_KEY (embeddings) in the environment or the --config file.
`

// common are the flags every command has.
type common struct {
	config, user, dataset string
	timeout               time.Duration
	json, verbose         bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.config,
		"config",
		"",
		"gligabao.toml to read the settings from (API keys, db_backend = \"postgres\", postgres_dsn, vector_store = \"pinecone\" or \"s3vectors\", ...); default: the environment only (POSTGRES_DSN, and the Pinecone or S3 Vectors variables)")
	fs.StringVar(&c.user,
		"user",
		defaultUser,
		"the user whose knowledge base is used")
	fs.StringVar(&c.dataset,
		"dataset",
		memory_core.DefaultDataset,
		"the dataset of the user's knowledge base")
	fs.DurationVar(&c.timeout,
		"timeout",
		0,
		"give up after this long, e.g. 90s (default: no limit)")
	fs.BoolVar(&c.json,
		"json",
		false,
		"print the result as JSON")
	fs.BoolVar(&c.verbose,
		"verbose",
		false,
		"log what the databases and pipelines do to stderr")
}

// run is main without the process: it returns the exit code.
func run(ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout,
	stderr io.Writer) int {
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
	case "remember", "query":
	default:
		fmt.Fprintf(stderr,
			"gligabao_worker: unknown command %q\n\n%s",
			cmd,
			usage)
		return exitUsage
	}

	fs := flag.NewFlagSet("gligabao_worker "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	var kind, at, session string
	switch cmd {
	case "remember":
		fs.StringVar(&kind,
			"kind",
			"",
			"kind of the text: knowledge (default) or episode")
		fs.StringVar(&at,
			"at",
			"",
			"event time of the text, RFC 3339 (default: now)")
		fs.StringVar(&session,
			"session",
			"",
			"session ID to attach to what is remembered")
	case "query":
		fs.StringVar(&session,
			"session",
			"",
			"session ID to label the query with")
	}
	fs.Usage = func() {
		fmt.Fprintf(stderr,
			"Usage: gligabao_worker %s [flags] ARG | -\n\nFlags:\n",
			cmd)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	fail := func(code int,
		err error) int {
		fmt.Fprintf(stderr,
			"gligabao_worker: %v\n",
			err)
		return code
	}

	input, err := readInput(cmd,
		fs.Args(),
		stdin)
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

	cfg, err := loadConfig(c.config)
	if err != nil {
		return fail(exitUsage, err)
	}
	client, err := ai.New(cfg.AnthropicKey, ai.WithPrices(ai.NewPriceTable(cfg.AIPrices())))
	if err != nil {
		return fail(exitUsage, err)
	}
	emb, err := ai.NewEmbedder(cfg.OpenAIKey,
		ai.WithEmbedPrices(ai.NewPriceTable(cfg.AIPrices())),
		ai.WithEmbedDimensions(cfg.EmbeddingDimensions))
	if err != nil {
		return fail(exitUsage, fmt.Errorf("embedder: %w (set %s or openai_api_key)",
			err,
			config.EnvOpenAIKey))
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

	a, err := openApp(ctx,
		cfg,
		client,
		emb)
	if err != nil {
		return fail(exitUsage, err)
	}
	defer a.close()

	var code int
	switch cmd {
	case "remember":
		code = a.cmdRemember(ctx,
			stdout,
			stderr,
			c.json,
			memory_remember.Request{
				UserID:  userID,
				Dataset: datasetName,
				Kind:    kind,
				Text:    input,
				Ref:     ref,
				Opts:    append(a.rememberOpts[:len(a.rememberOpts):len(a.rememberOpts)], sessionOpt(session)...),
			})
	case "query":
		code = a.cmdQuery(ctx,
			stdout,
			stderr,
			c.json,
			userID,
			datasetName,
			input,
			sessionOpt(session))
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
func (a *app) cmdRemember(ctx context.Context,
	stdout,
	stderr io.Writer,
	asJSON bool,
	req memory_remember.Request) int {
	outcome := make(chan memory_remember.Outcome, 1)
	req.Ctx, req.Outcome = ctx, outcome
	runID, err := a.remember.Submit(req)
	if err != nil {
		fmt.Fprintf(stderr,
			"gligabao_worker: could not queue the text: %v\n",
			err)
		return exitError
	}
	var out memory_remember.Outcome
	select {
	case out = <-outcome:
	case <-ctx.Done():
		fmt.Fprintf(stderr,
			"gligabao_worker: remember run %d: %v\n",
			runID,
			context.Cause(ctx))
		return exitError
	}
	if out.Err != nil {
		fmt.Fprintf(stderr,
			"gligabao_worker: remember run %d failed: %v\n",
			runID,
			out.Err)
		return exitError
	}
	if asJSON {
		return printJSON(stdout,
			stderr,
			out.Result)
	}
	fmt.Fprintf(stdout,
		"remembered (run %d): %d entities, %d facts, %d facts invalidated\n",
		runID,
		len(out.Result.Entities),
		len(out.Result.Edges),
		len(out.Result.InvalidatedEdges))
	return exitOK
}

// cmdQuery answers the question. A dataset that does not exist yet is not an error: the knowledge base is just empty.
func (a *app) cmdQuery(ctx context.Context,
	stdout,
	stderr io.Writer,
	asJSON bool,
	userID,
	dataset,
	question string,
	extra []memory_core.Option) int {
	opts := append(a.queryOpts[:len(a.queryOpts):len(a.queryOpts)], extra...)
	res, err := memory_query.Query(ctx,
		a.database.Relational,
		a.llm,
		userID,
		dataset,
		question,
		opts...)
	if errors.Is(err, memory_core.ErrDatasetNotFound) {
		res, err = &memory_query.QueryResult{
			Answer: "The knowledge base is empty: nothing was remembered yet.",
		}, nil
	}
	if err != nil {
		fmt.Fprintf(stderr,
			"gligabao_worker: query: %v\n",
			err)
		return exitError
	}
	if asJSON {
		return printJSON(stdout,
			stderr,
			res)
	}
	fmt.Fprintln(stdout, strings.TrimSpace(res.Answer))
	return exitOK
}

func printJSON(stdout,
	stderr io.Writer,
	v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr,
			"gligabao_worker: %v\n",
			err)
		return exitError
	}
	return exitOK
}

// readInput takes the text or question from the only argument, or from stdin if it is "-".
func readInput(cmd string,
	args []string,
	stdin io.Reader) (string, error) {
	what := map[string]string{"remember": "text", "query": "question"}[cmd]
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
		return "", fmt.Errorf("%s too long (at most %d bytes)",
			what,
			maxTextLen)
	}
	return s, nil
}

// loadConfig reads the settings: the file at path (which must say db_backend and vector_store itself), or with no path the defaults of
// the environment: PostgreSQL, and the vector store whose settings are in the environment (S3 Vectors if its bucket is set, else Pinecone).
func loadConfig(path string) (*config.Config, error) {
	if path != "" {
		cfg, err := config.Load(path)
		if err != nil {
			return nil, err
		}
		return cfg, checkRemote(cfg)
	}
	cfg := config.Default()
	cfg.DBBackend = config.DBBackendPostgres
	cfg.VectorStore = vectorStoreFromEnv(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, checkRemote(cfg)
}

// vectorStoreFromEnv is the remote vector store the environment configures: S3 Vectors with a bucket, else Pinecone.
func vectorStoreFromEnv(cfg *config.Config) string {
	if cfg.S3VectorsBucket != "" {
		return config.VectorStoreS3Vectors
	}
	return config.VectorStorePinecone
}

func versionString() string {
	v := version
	if v == "" {
		v = "dev"
	}
	s := "gligabao_worker " + v
	if c := buildinfo.Commit(); c != "" {
		s += " (" + c + ")"
	}
	return s
}
