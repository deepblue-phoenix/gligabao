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
	"net/http"
	"sync"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/buildinfo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/sys/server"
	"github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
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

// TrainedModelExporter is a named hook that exports trained models (see ServeOptions.TrainedModelExporters and
// docs/hooks.md): gligabao fetches the weights of the model to a local directory and calls Export with it; what Export does
// with the files (upload them to a third party, copy them, ...) is up to the embedding program.
type TrainedModelExporter = training.TrainedModelExporter

// ExportTrainedModelFunc is the Export callback of a TrainedModelExporter.
type ExportTrainedModelFunc = training.ExportTrainedModelFunc

// ExportTrainedModelRequest is what a TrainedModelExporter's Export callback is asked to export: the trained model, the local
// directory with its files, and the options of the request.
type ExportTrainedModelRequest = training.ExportTrainedModelRequest

// ExportTrainedModelResult is what an Export callback reports: a reference to what it made and optional details.
type ExportTrainedModelResult = training.ExportTrainedModelResult

// TrainedModelInferenceHook is a named hook that runs the prompts of one trained model somewhere other than the sidecar (Modal): see
// ServeOptions.TrainedModelInferenceHooks and docs/hooks.md. Model is the name of the trained model it targets; when that model is
// prompted, gligabao calls Infer instead of the sidecar. Several hooks can be supplied, each for a different model.
type TrainedModelInferenceHook = training.TrainedModelInferenceHook

// InferTrainedModelFunc is the Infer callback of a TrainedModelInferenceHook.
type InferTrainedModelFunc = training.InferTrainedModelFunc

// InferTrainedModelRequest is what a hook's Infer callback is asked to run: the trained model, the prompt and the limits chosen by the caller.
type InferTrainedModelRequest = training.InferTrainedModelRequest

// InferTrainedModelResult is what an Infer callback answers: the response and, optionally, the token counts.
type InferTrainedModelResult = training.InferTrainedModelResult

// TrainedModel is a trained model, the result of a completed training job (a LoRA adapter on the sidecar's models volume).
type TrainedModel = trainingdb.TrainedModel

// TrainedModelFile is one file of a trained model's weights.
type TrainedModelFile = trainingdb.TrainedModelFile

// AdminExtension adds views and UI to the admin UI (see ServeOptions.AdminExtensions and docs/admin-extensions.md): a Name, the
// browser files in Assets (index.js is required) and, optionally, an HTTP Handler that is the extension's own backend API.
type AdminExtension = admin.Extension

// ServeOptions holds the settings that are not part of the Config file.
type ServeOptions struct {
	// DBPath is the SQLite database file (sqlite backend). Default: DefaultDBPath.
	DBPath string
	// VectorDir is the directory of the chromem vector store. Default: next to DBPath.
	VectorDir string
	// ImageDir is the directory the image files of the image pipeline are kept in (the database only tracks their
	// paths). Default: next to DBPath, as <name>.images.
	ImageDir string
	// TrainingDatasetDirs are the training datasets directories, searched in order: one subdirectory with a dataset.toml manifest
	// per dataset (the datasets uploaded in the admin UI are saved in the first). If empty, Config.DatasetsTrainingDirs; if that
	// is empty too, a "datasets_training" directory next to DBPath.
	TrainingDatasetDirs []string
	// TrainingEvalDirs are the directories of the evals of trained models, searched in order: one JSON file per eval, a map
	// {"name", "cases"} (the training evals the admin UI and the API run). If empty, Config.EvalsTrainingDirs; if that is empty
	// too, an "evals_training" directory next to DBPath.
	TrainingEvalDirs []string
	// OntologyDirs are the directories ontology documents (OWL, RDF/XML) are loaded from by file name, searched in order (a
	// request to POST or PUT /v1/gb/ontologies with "file" instead of "content"). Nothing is loaded at startup. If empty,
	// Config.OntologyDirs; if that is empty too, there are none and only "content" can be sent.
	OntologyDirs []string
	// TrainedModelExportDir is the directory the files of trained models are fetched to for an export, one subdirectory per training
	// job (gligabao never deletes them). Default: a "trained_models" directory next to DBPath.
	TrainedModelExportDir string
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
	// TrainedModelExporters are the hooks that export trained models (POST /v1/gb/training/models/{id}/exports names the one to
	// use): gligabao fetches the model's files from the sidecar to a local directory and calls the exporter with it. Each has a
	// Name and the Export callback. They are validated at startup: Serve returns the error if one is malformed.
	TrainedModelExporters []TrainedModelExporter
	// TrainedModelInferenceHooks run the prompts of trained models without the sidecar: each has a Name, the Model it targets (the
	// name of a trained model, as the admin UI lists it) and the Infer callback. A model that has a hook is prompted through it (in the
	// admin UI's prompt dialog and through POST /v1/gb/training/models/{id}/infer); a model that has none still runs on the sidecar.
	// The training evals and audits are not affected: they always run on the sidecar (Modal), where the models are trained. Several
	// hooks can be supplied, for different models. They are validated at startup: Serve returns the error if one is malformed.
	TrainedModelInferenceHooks []TrainedModelInferenceHook
	// AdminExtensions add views, and buttons and blocks in the existing views, to the admin UI (served only with
	// admin_ui = true): each is a Name, the browser files in Assets (an index.js ES module is required, style.css is optional) and an
	// optional HTTP Handler, its own backend API. See docs/admin-extensions.md. Validated at startup: Serve returns the error if one
	// is malformed.
	AdminExtensions []AdminExtension
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
	serverOpts, restore := serverOptions(cfg, opts)
	defer restore()
	return server.Run(ctx, serverOpts)
}

// MountOptions says where Mount registers gligabao's handlers.
type MountOptions struct {
	// Mux is the mux of the host program (required). gligabao registers all its routes on it as one subtree, MountPrefix
	// ("/v1/gb/"), so the host must not have routes of its own under that prefix (Mount returns an error if it has).
	Mux *http.ServeMux
	// Wrap, if set, wraps gligabao's handler before it is registered: use it to put the host's authentication or other middleware
	// in front of the API and the admin UI (gligabao has no access control of its own).
	Wrap func(http.Handler) http.Handler
}

// MountPrefix is the path prefix of every route gligabao serves; Mount registers them on the host's mux under it.
const MountPrefix = server.MountPrefix

// Mount is the alternative to Serve for a program that already runs its own HTTP server: instead of starting a server on
// Config.Port, gligabao registers its handlers (the API, and the admin UI, eval and training endpoints if the config enables
// them) on the supplied mux, under MountPrefix, and the host serves them on its own port. Serve behaves as before.
//
// Mount does the whole setup (opens the databases, checks the sidecar, ...) and returns when the handlers are registered; register
// them before the host starts serving. The returned release function stops the remember manager and closes the databases; it is
// safe to call more than once and is also called when ctx is cancelled. Unlike Serve, Mount does not handle SIGINT/SIGTERM: the
// host owns the process and should call release (or cancel ctx) after its own server has shut down. Config.Port is not used. The
// hooks and App of opts are installed until release is called.
//
// gligabao's own "/", "/health" and "/version" routes are not registered, they belong to the host.
// A nil cfg means DefaultConfig().
func Mount(ctx context.Context, cfg *Config, opts ServeOptions, mount MountOptions) (release func(), err error) {
	serverOpts, restore := serverOptions(cfg, opts)
	serverOpts.Addr = ""
	rel, err := server.Mount(ctx, serverOpts, mount.Mux, mount.Wrap)
	if err != nil {
		restore()
		return nil, err
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			rel()
			restore()
		})
	}
	go func() { // server.Mount releases its part when ctx is done; the process-wide settings go with it
		<-ctx.Done()
		release()
	}()
	return release, nil
}

// serverOptions turns the config and the ServeOptions into the server's options. It also installs the process-wide settings
// of opts (App, Hooks); restore undoes them.
func serverOptions(cfg *Config, opts ServeOptions) (serverOpts server.Options, restore func()) {
	if cfg == nil {
		cfg = config.Default()
	}
	if opts.DBPath == "" {
		opts.DBPath = DefaultDBPath
	}
	restore = func() {
		monitoring.SetHooks(nil)
	}
	if opts.App != nil {
		buildinfo.SetApp(&buildinfo.Component{Name: opts.App.Name, Version: opts.App.Version, Commit: opts.App.Commit})
		restore = func() {
			buildinfo.SetApp(nil)
			monitoring.SetHooks(nil)
		}
	}
	monitoring.SetHooks(opts.Hooks)
	dbConfig := db.ConfigFrom(cfg, opts.DBPath, opts.VectorDir, opts.ImageDir)
	trainingDirs := training.Dirs{Datasets: opts.TrainingDatasetDirs, Evals: opts.TrainingEvalDirs, ModelExport: opts.TrainedModelExportDir}
	if len(trainingDirs.Datasets) == 0 {
		trainingDirs.Datasets = cfg.DatasetsTrainingDirs
	}
	if len(trainingDirs.Evals) == 0 {
		trainingDirs.Evals = cfg.EvalsTrainingDirs
	}
	ontologyDirs := opts.OntologyDirs
	if len(ontologyDirs) == 0 {
		ontologyDirs = cfg.OntologyDirs
	}
	if trainingDirs.ModelExport == "" {
		trainingDirs.ModelExport = cfg.TrainedModelExportDir
	}
	return server.Options{
		Addr:         cfg.Addr(),
		DB:           dbConfig,
		TrainingDirs: trainingDirs,
		OntologyDirs: ontologyDirs,
		AnthropicKey: cfg.AnthropicKey,
		OpenAIKey:    cfg.OpenAIKey,
		JevKey:       cfg.JevKey,
		JevBaseURL:   cfg.JevBaseURL,
		JevModel:     cfg.JevModel,
		SidecarURL:   cfg.SidecarURL,
		Prices:       aiPrices(cfg),

		TimeseriesProviders:   opts.TimeseriesProviders,
		TrainedModelExporters: opts.TrainedModelExporters,

		TrainedModelInferenceHooks: opts.TrainedModelInferenceHooks,
		AdminExtensions:            opts.AdminExtensions,

		TrajectoryDir:      opts.TrajectoryDir,
		EvalDBPath:         opts.EvalDBPath,
		EvalModeEnable:     cfg.EvalModeEnable,
		TrainingModeEnable: cfg.TrainingModeEnable,
		AdminUIenable:      cfg.AdminUIenable,
	}, restore
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
