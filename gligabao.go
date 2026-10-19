// Package gligabao is the public API for embedding gligabao in other Go programs.
//
// Everything else lives under internal/ and cannot be imported from outside this module.
// The standalone CLI (`cmd/gligabao`) uses this package too, so both behave identically.
//
//	cfg, err := gligabao.LoadConfig("gligabao.toml") // or gligabao.DefaultConfig()
//	...
//	err = gligabao.Serve(ctx, cfg, gligabao.ServeOptions{DBPath: "data/gligabao.db"})
package gligabao

import (
	"context"
	_ "embed"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/buildinfo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/sys/server"
	"github.com/deepblue-phoenix/gligabao/monitoring"
)

// VERSION is embedded at build time. It is edited with every change together with CHANGELOG.md, see AGENTS.md.
// (go:embed cannot reach outside the package directory, which is why VERSION lives at the repo root.)

//go:embed VERSION
var versionFile string

// App describes the program that embeds gligabao, so that the server can report its version next to gligabao's.
type App struct {
	Name    string
	Version string
	Commit  string // optional; see Commit()
}

func init() {
	buildinfo.Set(buildinfo.Component{
		Name:    "gligabao",
		Version: buildinfo.Parse(versionFile),
		Commit:  buildinfo.Commit(),

		CommitURL: buildinfo.CommitURL(),
	})
}

// Version returns the version of gligabao (the contents of the VERSION file embedded at build time).
func Version() string { return buildinfo.Library().Version }

// Commit returns the git revision this binary was built from ("+dirty" if the tree had uncommitted changes), or ""
// if the Go toolchain did not stamp one (e.g. a build outside a git checkout).
func Commit() string { return buildinfo.Commit() }

// Config is the gligabao configuration (the contents of gligabao.toml).
type Config = config.Config

// Price is a per-model price override, USD per 1M tokens.
type Price = config.Price

// DefaultDBPath is the SQLite database file used when ServeOptions.DBPath is empty.
const DefaultDBPath = db.DefaultSQLitePath

// Hooks is a set of callbacks gligabao invokes (e.g. for system events); see package monitoring.
type Hooks = monitoring.Hooks

// SystemEvent is the key/value data attached to a system event.
type SystemEvent = monitoring.Event

// TimeseriesProvider is a source of timeseries that remember can fetch from (see ServeOptions.TimeseriesProviders and
// docs/timeseries.md). Description says in natural language when the provider applies, so an LLM can decide which
// entities it is for; Fetch is the callback, with the logic of fetching.
type TimeseriesProvider = memory_core.SeriesProvider

// TimeseriesFetchRequest is what a TimeseriesProvider's Fetch callback is asked for: the entity, the query the LLM chose
// for it (following the provider's QueryFormat) and the period.
type TimeseriesFetchRequest = memory_core.SeriesFetchRequest

// TimeseriesEntity is the entity a series is fetched for.
type TimeseriesEntity = memory_core.FetchEntity

// TimeseriesData is a series returned by a Fetch callback.
type TimeseriesData = memory_core.SeriesData

// TimeseriesPoint is one value of a series at a time.
type TimeseriesPoint = memory_core.SeriesPoint

// ServeOptions holds the settings that are not part of the Config file.
type ServeOptions struct {
	// DBPath is the SQLite database file (sqlite backend). Default: DefaultDBPath.
	DBPath string
	// VectorDir is the directory of the chromem vector store. Default: next to DBPath.
	VectorDir string
	// ImageDir is the directory the image files of the image pipeline are kept in (the database only tracks their
	// paths). Default: next to DBPath, as <name>.images.
	ImageDir string
	// TrainingDatasetDir is the training datasets directory: one subdirectory with a dataset.toml manifest per dataset
	// (the datasets uploaded in the admin UI are saved there too). Default: a "datasets_training" directory next to DBPath.
	TrainingDatasetDir string
	// TrainingEvalDir is the directory of the evals of trained models: one JSON file per eval, a map {"name", "cases"} (the
	// training evals the admin UI and the API run). Default: an "evals_training" directory next to DBPath.
	TrainingEvalDir string
	// EvalDBPath is the eval database (the SQLite file ./run_evals.sh writes the datasets, experiments and runs to); with
	// the admin UI on it is shown next to the main database and feeds its Evals block.
	EvalDBPath string
	// TrajectoryDir is where the ATIF trajectories of the eval endpoints are stored as JSON. Default: "atif" next to DBPath.
	TrajectoryDir string
	// App identifies the embedding program; if set, the server reports its version next to gligabao's
	// (GET /version) and logs it at startup.
	App *App
	// TimeseriesProviders are the timeseries sources remember can fetch from when a request asks for it
	// (timeseries.fetch). Each has a Name, a natural-language Description of when it should be used (an LLM reads it to
	// decide which provider is for which entity of a text), an optional QueryFormat and the Fetch callback. They are
	// validated at startup: Serve returns the error if one is malformed.
	TimeseriesProviders []TimeseriesProvider
	// Hooks are installed for the lifetime of Serve; every one is called for each event.
	Hooks []Hooks
}

//---------------------------------------------------------------------------------------------

// DefaultConfig returns the default configuration; API keys are taken from the environment.
func DefaultConfig() *Config { return config.Default() }

// DefaultConfigPath returns the default config location (next to the running binary).
func DefaultConfigPath() string { return config.DefaultPath() }

// LoadConfig parses and validates the TOML config file at path.
// A missing file yields an error wrapping fs.ErrNotExist.
func LoadConfig(path string) (*Config, error) { return config.Load(path) }

//---------------------------------------------------------------------------------------------

// Serve starts the HTTP server and blocks until ctx is cancelled or SIGINT/SIGTERM is
// received, then shuts down gracefully. A nil cfg means DefaultConfig().
func Serve(ctx context.Context, cfg *Config, opts ServeOptions) error {
	if cfg == nil {
		cfg = config.Default()
	}
	if opts.DBPath == "" {
		opts.DBPath = DefaultDBPath
	}
	if opts.App != nil {
		buildinfo.SetApp(&buildinfo.Component{Name: opts.App.Name, Version: opts.App.Version, Commit: opts.App.Commit})
		defer buildinfo.SetApp(nil)
	}
	monitoring.SetHooks(opts.Hooks)
	defer monitoring.SetHooks(nil)
	dbConfig := db.ConfigFrom(cfg, opts.DBPath, opts.VectorDir, opts.ImageDir)
	dbConfig.TrainingDatasetDir = opts.TrainingDatasetDir
	dbConfig.TrainingEvalDir = opts.TrainingEvalDir
	return server.Run(ctx, server.Options{
		Addr:         cfg.Addr(),
		DB:           dbConfig,
		AnthropicKey: cfg.AnthropicKey,
		OpenAIKey:    cfg.OpenAIKey,
		JevKey:       cfg.JevKey,
		JevBaseURL:   cfg.JevBaseURL,
		JevModel:     cfg.JevModel,
		SidecarURL:   cfg.SidecarURL,
		Prices:       aiPrices(cfg),

		TimeseriesProviders: opts.TimeseriesProviders,

		TrajectoryDir: opts.TrajectoryDir,
		EvalDBPath:    opts.EvalDBPath,
		Eval:          cfg.EvalModeEnable,
		AdminUI:       cfg.AdminUIenable,
	})
}

// aiPrices converts the configured prices; omitted cache prices default to the input price.
func aiPrices(c *Config) map[string]ai.Price {
	out := make(map[string]ai.Price, len(c.Prices))
	for model, p := range c.Prices {
		ap := ai.Price{Input: p.Input, Output: p.Output, CacheWrite: p.Input, CacheRead: p.Input,
			LongThreshold: p.LongThreshold, LongInput: p.LongInput, LongOutput: p.LongOutput}
		if p.CacheWrite != nil {
			ap.CacheWrite = *p.CacheWrite
		}
		if p.CacheRead != nil {
			ap.CacheRead = *p.CacheRead
		}
		out[model] = ap
	}
	return out
}
