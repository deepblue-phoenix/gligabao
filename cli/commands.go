package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// The client commands: they talk to a gligabao server over the REST API (the local daemon unless --server is given), so they are
// exactly as the API is: same fields, same validation, same errors. Output is JSON on stdout; an error is {"error", "status"} on
// stderr and a non-zero exit code (see the exit constants).

const clientHelp = `
Where it runs:
  By default the command uses a local gligabao daemon (gligabao serve, on 127.0.0.1, no authentication), which it starts in the
  background if none is running and leaves running. With --server URL (or GLIGABAO_URL) it uses that server instead and starts nothing;
  GLIGABAO_API_KEY is sent as the API key.
  The daemon uses the config gligabao.toml of the current directory if there is one; otherwise ~/.gligabao (config
  ~/.gligabao/gligabao.toml if it exists, database ~/.gligabao/gligabao.db). Its log is ~/.gligabao/daemon-<port>.log.
  It needs ANTHROPIC_API_KEY and OPENAI_API_KEY in the environment (or in the config) to start.

Output and exit codes:
  JSON on stdout. Errors go to stderr as {"error": "...", "status": 400}. Exit codes: 0 ok, 2 usage or invalid request (400),
  3 not found, 4 conflict, 5 server or LLM not available, 1 other.`

// addClientCommands adds the commands that use the API.
func (c *CLI) addClientCommands(o Options) {
	cmds := []*cobra.Command{c.rememberCmd(), c.queryCmd(), c.datasetCmd(), c.apiCmd(), c.statusCmd(), c.stopCmd()}
	for _, cmd := range cmds {
		cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
			// The arguments are parsed by now: an error from here on is not a usage error, so the help is not printed for it.
			cmd.SilenceUsage = true
			// The config is loaded by the daemon, not here; the program's own PreRun (e.g. loading its environment) still runs.
			if o.PreRun != nil {
				return o.PreRun(cmd)
			}
			return nil
		}
		silenceErrors(cmd)
		c.Root.AddCommand(cmd)
	}
}

// silenceErrors makes cmd and its subcommands leave printing their errors to Execute (as JSON).
func silenceErrors(cmd *cobra.Command) {
	cmd.SilenceErrors = true
	for _, sub := range cmd.Commands() {
		silenceErrors(sub)
	}
}

// body is the request body being built: the fields of --json, then the flags the user set on top of them.
type body struct {
	m map[string]any
}

func (b *body) set(key string, v any) { b.m[key] = v }

func (b *body) default_(key string, v any) {
	if _, ok := b.m[key]; !ok {
		b.m[key] = v
	}
}

func (b *body) bytes() []byte { out, _ := json.Marshal(b.m); return out }

// commonFlags are the flags of remember and query that map to the request fields of the same name.
type commonFlags struct {
	jsonBody  string
	dataset   string
	session   string
	models    []string
	reasoning []string
}

func (f *commonFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.jsonBody, "json", "", `the request body as JSON ('{"..."}', @file, or - for stdin); the flags below override its fields`)
	fl.StringVarP(&f.dataset, "dataset", "d", "", `dataset (default "main")`)
	fl.StringVar(&f.session, "session", "", "session_id: connects the calls of one agent session")
	fl.StringArrayVar(&f.models, "model", nil, "LLM model of a stage, STAGE=MODEL (repeatable)")
	fl.StringArrayVar(&f.reasoning, "reasoning", nil, "reasoning level of a stage, STAGE=none|low|medium|high (repeatable)")
}

// build starts the request body: the --json fields, the user and the flags that were set.
func (f *commonFlags) build(cmd *cobra.Command, c *CLI) (*body, error) {
	m, err := bodyFromFlags(cmd, f.jsonBody)
	if err != nil {
		return nil, err
	}
	b := &body{m: m}
	b.default_("user_id", c.flags.user)
	if cmd.Flags().Changed("dataset") {
		b.set("dataset", f.dataset)
	}
	if cmd.Flags().Changed("session") {
		b.set("session_id", f.session)
	}
	models, err := pairs("model", f.models)
	if err != nil {
		return nil, err
	}
	if models != nil {
		b.set("models", models)
	}
	reasoning, err := pairs("reasoning", f.reasoning)
	if err != nil {
		return nil, err
	}
	if reasoning != nil {
		b.set("reasoning", reasoning)
	}
	return b, nil
}

//---------------------------------------------------------------------------------------------

func (c *CLI) rememberCmd() *cobra.Command {
	var (
		cf        commonFlags
		file      string
		kind      string
		at        string
		image     string
		series    string
		webSearch bool
	)
	cmd := &cobra.Command{
		Use:   "remember [TEXT | -]",
		Short: "Remember a text or an image (POST /v1/gb/remember)",
		Long: `Remember a text (or an image) in the knowledge graph of a dataset: an LLM extracts its entities and facts, which are stored with
their embeddings. The command returns when the run is complete, with the JSON response of POST /v1/gb/remember (see docs/remember.md).

The text is the argument, the contents of --file, or stdin if the argument is "-". Or give the whole request with --json.
` + clientHelp,
		Example: `  gligabao remember "Alice joined Acme in March 2024." -d work --kind episode --at 2024-03-01T09:00:00Z
  gligabao remember -f notes.txt --session chat-42
  echo "Bob lives in Rome." | gligabao remember -
  gligabao remember --image receipt.png -d finance
  gligabao remember --json '{"text":"...","web_search":true}'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := cf.build(cmd, c)
			if err != nil {
				return err
			}
			switch {
			case len(args) == 1 && args[0] == "-":
				text, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return usageErr("read stdin: %v", err)
				}
				b.set("text", string(text))
			case len(args) == 1:
				b.set("text", args[0])
			case file != "":
				text, err := os.ReadFile(file)
				if err != nil {
					return usageErr("--file: %v", err)
				}
				b.set("text", string(text))
			}
			if image != "" {
				img, err := os.ReadFile(image)
				if err != nil {
					return usageErr("--image: %v", err)
				}
				b.set("image", base64.StdEncoding.EncodeToString(img))
			}
			if cmd.Flags().Changed("kind") {
				b.set("kind", kind)
			}
			if cmd.Flags().Changed("at") {
				b.set("reference_time", at)
			}
			if webSearch {
				b.set("web_search", true)
			}
			if series != "" {
				raw, err := os.ReadFile(series)
				if err != nil {
					return usageErr("--series: %v", err)
				}
				var specs []any
				if err := json.Unmarshal(raw, &specs); err != nil {
					return usageErr("--series: %s is not a JSON array of series: %v", series, err)
				}
				ts, _ := b.m["timeseries"].(map[string]any)
				if ts == nil {
					ts = map[string]any{}
				}
				ts["series"] = specs
				b.set("timeseries", ts)
			}
			if _, hasText := b.m["text"]; !hasText && b.m["image"] == nil {
				return usageErr("nothing to remember: give the text as an argument, with --file or on stdin (-), or an --image")
			}
			return c.send(cmd, "POST", "/v1/gb/remember", nil, b.bytes())
		},
	}
	cf.add(cmd)
	f := cmd.Flags()
	f.StringVarP(&file, "file", "f", "", "read the text from this file")
	f.StringVar(&kind, "kind", "", `artifact kind: "knowledge" (default) or "episode" (anchored in time)`)
	f.StringVar(&at, "at", "", "reference_time (RFC 3339): when the text was written; relative dates in it are resolved against it (default now)")
	f.StringVar(&image, "image", "", "remember this image file (JPEG, PNG, GIF or WebP) instead of a text")
	f.StringVar(&series, "series", "", "file with a JSON array of timeseries to store with the text (timeseries.series, see docs/timeseries.md)")
	f.BoolVar(&webSearch, "web-search", false, "also search the web for the key entities of the text (needs an Anthropic key)")
	return cmd
}

//---------------------------------------------------------------------------------------------

func (c *CLI) queryCmd() *cobra.Command {
	var (
		cf           commonFlags
		geoNearby    bool
		geoRadius    float64
		evidenceOnly bool
		raw          bool
	)
	cmd := &cobra.Command{
		Use:   "query QUESTION",
		Short: "Answer a question from the knowledge graph (POST /v1/gb/query)",
		Long: `Answer a question from the knowledge graph of a dataset. The response (see docs/query.md) has the answer and its evidence: the
entities, edges and passages it relied on.

With --evidence-only nothing is answered: the command only retrieves, and returns as evidence everything it found (no LLM call writes
an answer), for an agent that answers with its own LLM.
` + clientHelp,
		Example: `  gligabao query "Where does Alice work?"
  gligabao query "What happened in March?" -d work --session chat-42
  gligabao query "Who lives near Rome?" --geo-radius 80
  gligabao query "Where does Alice work?" --evidence-only
  gligabao query "Where does Alice work?" --raw      # only the answer, as text`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := cf.build(cmd, c)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				b.set("query", args[0])
			}
			if q, _ := b.m["query"].(string); strings.TrimSpace(q) == "" {
				return usageErr("no question: give it as an argument (or --json)")
			}
			if geoNearby {
				b.set("geo_nearby", true)
			}
			if cmd.Flags().Changed("geo-radius") {
				b.set("geo_radius_km", geoRadius)
			}
			if evidenceOnly {
				b.set("evidence_only", true)
			}
			if !raw {
				return c.send(cmd, "POST", "/v1/gb/query", nil, b.bytes())
			}
			cl, err := c.client(cmd)
			if err != nil {
				return err
			}
			out, err := cl.call(cmd.Context(), "POST", "/v1/gb/query", nil, b.bytes())
			if err != nil {
				return err
			}
			var res struct {
				Answer string `json:"answer"`
			}
			if err := json.Unmarshal(out, &res); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), res.Answer)
			return nil
		},
	}
	cf.add(cmd)
	f := cmd.Flags()
	f.BoolVar(&geoNearby, "geo-nearby", false, "also search the geo entities near the places of the question (needs geo on in the config)")
	f.Float64Var(&geoRadius, "geo-radius", 0, "radius in km of the geo search (turns --geo-nearby on)")
	f.BoolVar(&evidenceOnly, "evidence-only", false, "retrieve only: no answer is written, the evidence is everything retrieved")
	f.BoolVar(&raw, "raw", false, "print only the answer, as text")
	return cmd
}

//---------------------------------------------------------------------------------------------

func (c *CLI) datasetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dataset",
		Short: "List, create, delete and export datasets",
		Long:  "Datasets are the separate knowledge graphs of a user (docs/datasets.md)." + clientHelp,
	}
	user := func() url.Values { return url.Values{"user_id": {c.flags.user}} }

	cmd.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "List the datasets of the user (GET /v1/gb/datasets)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.send(cmd, "GET", "/v1/gb/datasets", user(), nil)
		},
	})

	var description string
	var tags []string
	create := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a dataset (POST /v1/gb/datasets)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b := map[string]any{"user_id": c.flags.user, "name": args[0]}
			if description != "" {
				b["description"] = description
			}
			if len(tags) > 0 {
				b["tags"] = tags
			}
			out, _ := json.Marshal(b)
			return c.send(cmd, "POST", "/v1/gb/datasets", nil, out)
		},
	}
	create.Flags().StringVar(&description, "description", "", "what the dataset is for")
	create.Flags().StringArrayVar(&tags, "tag", nil, "a tag (repeatable)")
	cmd.AddCommand(create)

	cmd.AddCommand(&cobra.Command{
		Use:   "rm NAME",
		Short: "Delete a dataset (DELETE /v1/gb/datasets/{name})",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.send(cmd, "DELETE", "/v1/gb/datasets/"+url.PathEscape(args[0]), user(), nil)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "graph NAME",
		Short: "Export the knowledge graph of a dataset (GET /v1/gb/datasets/{name}/graph)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.send(cmd, "GET", "/v1/gb/datasets/"+url.PathEscape(args[0])+"/graph", user(), nil)
		},
	})
	return cmd
}

//---------------------------------------------------------------------------------------------

func (c *CLI) apiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "api METHOD PATH [JSON | -]",
		Short: "Call any endpoint of the REST API",
		Long: `Send a request to any endpoint (timeseries, ontologies, ...) and print the JSON response. The body is JSON text, @file, or - for
stdin. The query string can be part of PATH. No user_id is added: send it yourself where the endpoint wants one.
` + clientHelp,
		Example: `  gligabao api GET "/v1/gb/timeseries?user_id=local_user"
  gligabao api POST /v1/gb/ontologies '{"user_id":"local_user","name":"x","file":"x.owl"}'`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			method, path := strings.ToUpper(args[0]), args[1]
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			var b []byte
			if len(args) == 3 {
				var err error
				if b, err = readBody(cmd, args[2]); err != nil {
					return usageErr("body: %v", err)
				}
			}
			return c.send(cmd, method, path, nil, b)
		},
	}
}

//---------------------------------------------------------------------------------------------

func (c *CLI) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the target of the commands and whether its daemon is running",
		Long: `Print, as JSON, what the commands use (the --server, or the local daemon with its URL, config, database and log) and whether it
answers. It never starts the daemon.` + clientHelp,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := map[string]any{"user": c.flags.user}
			var base string
			if c.flags.server != "" {
				base = strings.TrimRight(c.flags.server, "/")
				out["mode"], out["url"] = "remote", base
			} else {
				d, err := c.localDaemon(cmd)
				if err != nil {
					return err
				}
				out["mode"], out["url"] = "local", d.url()
				out["project"] = d.layout.Project
				out["config"] = d.layout.ConfigPath
				db := d.layout.DBPath
				if db == "" {
					db = "(default of serve, relative to the working directory)"
				}
				out["db"], out["log"] = db, d.logPath()
				base = d.url()
			}
			cl := &apiClient{base: base, user: c.flags.user}
			status, b, err := cl.do(cmd.Context(), "GET", "/version", nil, nil)
			out["running"] = err == nil && status == 200
			if out["running"] == true {
				var v any
				if json.Unmarshal(b, &v) == nil {
					out["server_version"] = v
				}
			}
			b, _ = json.Marshal(out)
			emit(cmd, b)
			return nil
		},
	}
}

func (c *CLI) stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the local daemon this CLI started",
		Long:  "Ask the local daemon to shut down gracefully (it is started again by the next command that needs it)." + clientHelp,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := c.localDaemon(cmd)
			if err != nil {
				return err
			}
			stopped, err := d.stop()
			if err != nil {
				return err
			}
			b, _ := json.Marshal(map[string]any{"stopped": stopped})
			emit(cmd, b)
			return nil
		},
	}
}

// send calls the API and prints the response.
func (c *CLI) send(cmd *cobra.Command, method, path string, query url.Values, body []byte) error {
	cl, err := c.client(cmd)
	if err != nil {
		return err
	}
	out, err := cl.call(cmd.Context(), method, path, query, body)
	if err != nil {
		return err
	}
	emit(cmd, out)
	return nil
}
