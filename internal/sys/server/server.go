// Package server wires up and runs the HTTP server.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	adminui "github.com/deepblue-phoenix/gligabao/admin_ui"
	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent/code_sandbox"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate"
	"github.com/deepblue-phoenix/gligabao/internal/api"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	evalsroutes "github.com/deepblue-phoenix/gligabao/internal/api/evals"
	modelapi "github.com/deepblue-phoenix/gligabao/internal/api/model"
	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/atif/atifstore"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/dbfull"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/evals"
	evalstraining "github.com/deepblue-phoenix/gligabao/internal/evals/training"
	evalsapi "github.com/deepblue-phoenix/gligabao/internal/evals/training/api"
	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	"github.com/deepblue-phoenix/gligabao/internal/sys/buildinfo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/sys/jev"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	"github.com/deepblue-phoenix/gligabao/internal/sys/web"
	coretraining "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits"
	auditsapi "github.com/deepblue-phoenix/gligabao/internal/training_audits/api"
	log "github.com/sirupsen/logrus"
)

const shutdownTimeout = 5 * time.Second

const banner = `
                              GLIGABAO
   .+------+     +------+     +------+     +------+     +------+.
 .' |      |    /|      |     |      |     |      |\    |      | ` + "`" + `.
+   |      |   + |      |     +      +     |      | +   |      |   +
|   |      |   | |      |     |      |     |      | |   |      |   |
|  .+------+   | +------+     +------+     +------+ |   +------+.  |
|.'      .'    |/      /      |      |      \      \|    ` + "`" + `.      ` + "`" + `.|
+------+'      +------+       +------+       +------+      ` + "`+------+" + `
`

// serverURL returns the base URL at which the server is reachable for the listen address addr.
func serverURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// adminURL returns the URL at which the admin UI is reachable for the listen address addr.
func adminURL(addr string) string {
	return serverURL(addr) + "/v1/gb/admin/"
}

//---------------------------------------------------------------------------------------------

// Options configures the server.
type Options struct {
	// Addr is the listen address, e.g. "127.0.0.1:7878".
	Addr string
	// DB selects and locates the databases (relational, graph, vector).
	DB db.Config
	// AnthropicKey is the Anthropic API key. If empty, LLM endpoints return 503.
	AnthropicKey string
	// LinkupKey is the Linkup API key (research agents with the linkup provider).
	LinkupKey string
	// OpenAIKey is the OpenAI API key used to embed entities (dimensions: DB.EmbeddingDimensions). If empty,
	// remember stores no embeddings.
	OpenAIKey string
	// JevKey, JevBaseURL and JevModel configure Jev, the system 1 model. Without a key remember skips
	// every step that needs it.
	JevKey, JevBaseURL, JevModel string
	// SidecarURL is the base URL of the Python sidecar's REST API, which the training endpoint that starts jobs
	// (POST /v1/gb/training/jobs) calls. Default: sidecar.DefaultBaseURL.
	SidecarURL string
	// Prices overrides/extends the built-in model price table used for call costs. May be nil.
	Prices map[string]llm.Price
	// TrajectoryDir is where the ATIF trajectories of the eval calls are stored as JSON.
	// Default: an "atif" directory next to the SQLite file (memory_core.TrajectoryDir if there is none).
	TrajectoryDir string
	// TrainingModeEnable registers the training handlers (/v1/gb/training/...) and starts the training monitor. Off by default.
	TrainingModeEnable bool
	// TrainingDirs are the directories the training endpoints keep files in. An empty field gets its default, next to the SQLite
	// file (DB.SQLitePath, or db.DefaultSQLitePath if empty): see coretraining.DefaultDirs.
	TrainingDirs coretraining.Dirs
	// OntologyDirs are the directories ontology documents are loaded from by file name (see api.WithOntologyDirs). Default: none.
	OntologyDirs []string
	// AgentDefsDir is the directory the definitions of the research agents are loaded from at startup (agent_defs.Load; a definition that is
	// not valid stops the server). Default: an agent_defs directory next to the binary; if it does not exist only the built-in agent is there.
	AgentDefsDir string
	// ResearchMaxCostUSD is the most one research may spend, all its agents together (0: no cap of the server).
	ResearchMaxCostUSD float64
	// ResearchSearchProvider and ResearchFetchProvider are the default providers of web_search and fetch_url ("": anthropic, local).
	ResearchSearchProvider, ResearchFetchProvider string
	// ResearchNotesDir is the directory of the notes files of the investigative research loop ("": research_notes in the working directory).
	ResearchNotesDir string
	// Monty is where the code sandbox of the investigative agents is (see agent_research.Manager.Monty).
	Monty code_sandbox.Config
	// ResearchNotesS3 ("" bucket: off) also stores the notes files of the investigative loop in the research S3 bucket.
	ResearchNotesS3 investigate.NotesS3Config
	// TrajectoryS3 ("" bucket: off) stores the ATIF traces in an Amazon S3 bucket instead of the trajectory directory, one directory per
	// user (see package atifstore). With it the research agents also record a trace of every research.
	TrajectoryS3 atifstore.S3Config
	// OntologyS3 ("" bucket: off) also stores the ontology documents in an Amazon S3 bucket, one directory per user (see memory_ontology.Archive).
	OntologyS3 memory_ontology.S3Config
	// Evals are the settings of the user evals (the evals_* config keys): the store of the eval files, the limits and the liveness of
	// the runs. They reach the EvalManager hook in EvalEnv.Settings.
	Evals EvalSettings
	// EvalManager is an INJECTED HOOK: a factory for the run manager of the user evals (package evals/runmanager), which starts and
	// supervises their runs, reruns case runs and cleans up their datasets. The routes of the evals (/v1/gb/evals, and the evals views of
	// the admin UI) work through it. Nil means the server has no evals: no eval route is registered.
	//
	// Why it is injected and not just called: it was added because of an IMPORT CYCLE. Running an eval case is the job of package
	// internal/evals/knowledge (Executor, Rerunner), which runs every case through the typed layer of package api (remember.Remember, query.Run), and the run manager
	// that uses it imports it. The server imports api and the admin package, which serve the routes, so the server (or the packages it
	// imports) cannot import the manager: server -> runmanager -> knowledge -> api would not be a cycle by itself, but runmanager and
	// knowledge also import this package (for EvalEnv). The way out is to invert the dependency: package api/evals defines what the
	// handlers need (the interface evals.Manager), this package the factory type (given an EvalEnv), and a package above all of them,
	// the root package (gligabao.go, serverOptions), which may import everything, plugs the implementation in.
	//
	// prepare calls it once at startup with the server's own clients (EvalEnv). It returns the manager, or nil for none. If the
	// returned value also has a Wait() method, the server calls it on shutdown, after cancelling EvalEnv.Ctx, so the runs in progress can
	// record how they ended before the databases close.
	EvalManager func(EvalEnv) evalsroutes.Manager
	// Auth, if set, makes the user routes of the API (remember, query, datasets, timeseries, ontologies) require an API key
	// ("Authorization: Bearer <key>"): it is called with the key and returns the ID of the user it belongs to, which the request then
	// acts as; an error rejects the request (401). See docs/identity.md. Nil: the routes act as the user in the "gb-user-id" header, trusted as is.
	Auth httputil.Authenticator
	// TimeseriesProviders are the sources of timeseries that remember can fetch from (see docs/timeseries.md). Each has a
	// natural-language description of when it applies, and a callback that does the fetching. Validated at startup.
	TimeseriesProviders []memory_core.SeriesProvider
	// GeoResolver finds the coordinates of the places remember extracts as entities (see package memory_geo). Nil: the Nominatim resolver
	// of OpenStreetMap if Geo is true, else remember does not look at places.
	GeoResolver memory_geo.Resolver
	// GraphEmbedder is the graph embedder hook (see memory_core.GraphEmbedder): called with the graph of every remember once it is fully
	// constructed and with the graph context of every query once it is fetched. Nil: off. IMPORTANT: its embeddings are not stored in the
	// vector store yet; they must be.
	GraphEmbedder memory_core.GraphEmbedder
	// ClassifyingTrainingHook is the classifying training hook (see memory_core.ClassifyingTrainingHook), served as
	// POST /v1/gb/datasets/{name}/training. Nil: the route is not mounted.
	ClassifyingTrainingHook memory_core.ClassifyingTrainingHook
	// GCNII serves the per-user model routes (POST and GET /v1/gb/model/train, POST /v1/gb/model/infer and /embed): it is sargo, the Rust
	// server gligabao started, which trains and runs the models over HTTP (a *sargo.Client). Nil: the routes are not mounted.
	GCNII modelapi.Service
	// Geo turns the geo stage of remember on (with GeoResolver, or Nominatim if there is none); a GeoResolver turns it on by itself.
	Geo bool
	// GeoRadiusKm is the default radius, in kilometers, within which query looks for the geo entities near the places of a question
	// (a request can override it with geo_radius_km). 0: memory_core.DefaultGeoRadiusKm. It only matters with geo on.
	GeoRadiusKm float64
	// TrainedModelDeployers are the hooks that deploy trained models (POST /v1/gb/training/models/{id}/deployments): each is given
	// the model's files in a local directory and does with them what it wants. Validated at startup.
	TrainedModelDeployers []coretraining.TrainedModelDeployer
	// TrainedModelInferenceHooks run the prompts of the trained models they target (by name) instead of the sidecar: a model prompted
	// in the admin UI or through POST /v1/gb/training/models/{id}/infer that has a hook is run by it, the others on the sidecar. The
	// training evals and audits always run on the sidecar, hooks or not. Validated at startup.
	TrainedModelInferenceHooks []coretraining.TrainedModelInferenceHook
	// AdminExtensions add views and UI to the admin UI (see docs/admin-extensions.md); only used with AdminUIenable. Validated at startup.
	AdminExtensions []admin.Extension
	// AdminUIenable registers the admin UI handlers (served at /v1/gb/admin/ from the assets embedded in the binary).
	AdminUIenable bool
	// MCPEnable registers the MCP endpoint (/v1/gb/mcp) with the tools remember and query (see package api/mcp). Off by default.
	// It is not covered by Auth.
	MCPEnable bool
}

//---------------------------------------------------------------------------------------------

// SidecarActive is the flag set on startup (see Run): true if the sidecar answered its health check. Code that needs the
// sidecar and does not get it injected can check it instead of calling the sidecar and handling the error.
var SidecarActive atomic.Bool

// sidecarClient is the client of the configured sidecar, kept even if the startup check failed, so that the admin UI can
// show live whether the sidecar is up (see sidecarPing).
var sidecarClient atomic.Pointer[sidecar.Client]

// sidecarPing is a live health check of the sidecar, for the admin UI's health indicator.
func sidecarPing(ctx context.Context) error {
	c := sidecarClient.Load()
	if c == nil {
		return errors.New("sidecar not configured")
	}
	_, err := c.Health(ctx)
	return err
}

// sidecarBackends asks the sidecar for its training backends and their status, for the admin UI.
func sidecarBackends(ctx context.Context) ([]sidecar.BackendStatus, error) {
	c := sidecarClient.Load()
	if c == nil {
		return nil, errors.New("sidecar not configured")
	}
	return c.Backends(ctx)
}

// sidecarOpen sends a request to the sidecar and returns its raw answer (nil function if there is no client: the proxy
// then answers 503), for the backends' actions in the admin UI.
func sidecarOpen(ctx context.Context,
	method,
	path,
	rawQuery string,
	body io.Reader) (*http.Response, error) {
	c := sidecarClient.Load()
	if c == nil {
		return nil, errors.New("sidecar not configured")
	}
	return c.Open(ctx,
		method,
		path,
		rawQuery,
		body)
}

// sidecarPingTimeout bounds the sidecar health check on startup.
const sidecarPingTimeout = 5 * time.Second

// connectSidecar pings the sidecar at url (default if empty). It returns the client and true if the sidecar is operating
// correctly; otherwise it logs a warning and returns a nil client (a true nil interface, so callers can check == nil) and false.
func connectSidecar(ctx context.Context,
	url string) (coretraining.Sidecar, bool) {
	c := sidecar.New(url)
	sidecarClient.Store(c)
	pingCtx, cancel := context.WithTimeout(ctx, sidecarPingTimeout)
	defer cancel()
	if _, err := c.Health(pingCtx); err != nil {
		log.WithError(err).WithField("url", url).Warn("sidecar is not active; training endpoints will return 503")
		return nil, false
	}
	log.WithField("url", url).Info("sidecar is active")
	return c, true
}

// EvalSettings are the settings of the user evals. A zero field takes the default of the run manager (see package runmanager); the
// config file sets them all (the evals_* keys).
type EvalSettings struct {
	// S3 is the meta bucket of the eval files; with no bucket (S3.Bucket empty) the files are in the local directory LocalDir.
	S3 userfiles.S3Config
	// LocalDir is the directory of the eval files without a bucket, one subdirectory per user. Default: an evals_user directory next to
	// the SQLite file.
	LocalDir string
	// MaxCases, MaxFileBytes and MaxTextBytes are the limits of an eval: cases per eval, bytes of its JSON file, bytes of a text or a
	// corpus file.
	MaxCases, MaxFileBytes, MaxTextBytes int
	// MaxRunsPerUser and MaxRuns are the runs in progress at once, per user and on this server.
	MaxRunsPerUser, MaxRuns int
	// MaxRunCostUSD stops a run whose cases cost more; MaxUserCostUSDPerDay refuses the runs of a user past it in the last 24 hours.
	MaxRunCostUSD, MaxUserCostUSDPerDay float64
	// DatasetRetention is how long the eval datasets of a finished run are kept.
	DatasetRetention time.Duration
	// HeartbeatInterval is how often a running run records that it is alive; HeartbeatStale is when a run without a beat is stale.
	HeartbeatInterval, HeartbeatStale time.Duration
}

// EvalEnv is what the injected hook Options.EvalManager is given to build the run manager with: the server's own clients (so the evals
// use the same LLM, price table, embedder, web search and geo resolver as the server, configured once), the database and where the eval
// files and the trajectories are kept. It exists so that the package that implements the manager (internal/evals/runmanager) can get these
// without the server importing that package (see Options.EvalManager for the import cycle this avoids). It holds only plain values and
// the server's interfaces, never types of the implementing package.
type EvalEnv struct {
	// Ctx ends when the server shuts down: the runs in progress stop with it.
	Ctx context.Context
	// DB is the main database, shared (never closed by the manager).
	DB *db.DB
	// LLM, Embedder and WebSearch are the server's; LLM is nil without an Anthropic API key (no run starts then). Geo is its geo
	// resolver (nil: geo is off).
	LLM       memory_core.LLM
	Embedder  memory_core.Embedder
	WebSearch memory_core.WebSearcher
	Geo       memory_geo.Resolver
	// Prices is the price table, whose models are the ones a run can pick.
	Prices *llm.PriceTable
	// Files holds the eval files of the users (the S3 meta bucket, or a local directory).
	Files userfiles.Store
	// Trajectories is where the trajectories are stored (nil: the directory TrajectoryDir), TrajectoryDir the directory of the file store.
	Trajectories  atifstore.Store
	TrajectoryDir string
	// Settings are the settings of the user evals (Options.Evals).
	Settings EvalSettings
}

// NewHandler builds the HTTP handler for the given databases and LLM. ctx bounds the remember manager (see memory_remember.Manager).
// lm may be nil, in which case endpoints requiring it return 503. emb may be nil, in which case remember does not embed entities.
// jv may be nil, in which case remember skips the steps that run on Jev. ws may be nil, in which case web search is not available
// (a remember request with web_search returns 503); it is the Anthropic web search tool, so it is only set with an Anthropic API key.
// geoResolver turns geo on (remember and query find the places of a text, see package memory_geo); nil: geo is off.
// providers are the registered timeseries providers (already validated; may be nil: a remember request that fetches series then returns 503). hooks are the registered hooks of the training API (already validated; the zero value has none): the trained model deployers (with none a deployment request answers 400) and the inference hooks of trained models. dirs are the directories of the training endpoints (all fields must be set when trainingMode is true; see coretraining.Dirs.WithDefaults). ontologyDirs are the directories ontology files are loaded from by name (may be nil). sc starts the training jobs (/v1/gb/training/) on the sidecar, lists its datasets and follows the jobs' progress (may be nil: the endpoints then return 503). The training endpoints (/v1/gb/training/) are registered only if trainingMode is true,
// the admin UI (/v1/gb/admin/) only if adminUI is true and its assets were built into the binary; adminExts are the registered admin UI extensions
// (already validated; may be nil), mounted with it.
func NewHandler(ctx context.Context,
	d *db.DB,
	lm memory_core.LLM,
	emb memory_core.Embedder,
	jv memory_core.Classifier,
	ws memory_core.WebSearcher,
	geoResolver memory_geo.Resolver,
	providers []memory_core.SeriesProvider,
	hooks coretraining.Hooks,
	dirs coretraining.Dirs,
	ontologyDirs []string,
	sc coretraining.Sidecar,
	trainingMode,
	adminUI bool,
	adminExts []admin.Extension,
	extraAdminDBs ...admin.Named) http.Handler {
	return newMux(ctx,
		true,
		nil,
		geoResolver,
		0,
		nil,
		nil,
		false,
		newResearch(ctx,
			d,
			lm),
		d,
		lm,
		emb,
		jv,
		ws,
		providers,
		hooks,
		dirs,
		ontologyDirs,
		sc,
		trainingMode,
		adminUI,
		adminExts,
		nil,
		nil,
		nil,
		extraAdminDBs...)
}

// StartGeoManager is startGeoManager for the packages that run the pipelines in process (the evals).
func StartGeoManager(ctx context.Context,
	r memory_geo.Resolver,
	d *db.DB) memory_geo.Resolver {
	return startGeoManager(ctx,
		r,
		d)
}

// startGeoManager puts the geo resolver behind the geo manager of the server (see memory_geo.Manager): ONE goroutine that asks the
// resolver for one place at a time, serving query before remember, and the sweeper that queues the places whose resolving was lost (or
// done by a process without a manager: the worker). Both run until ctx is done. nil (geo off) and a resolver that already is a manager
// are returned as they are.
func startGeoManager(ctx context.Context,
	r memory_geo.Resolver,
	d *db.DB) memory_geo.Resolver {
	if r == nil {
		return nil
	}
	if _, ok := r.(memory_geo.Queue); ok {
		return r
	}
	m := memory_geo.NewManager(ctx, r)
	go memory_remember.RunGeoSweeper(ctx,
		d.Relational,
		m,
		m.Name(),
		0)
	return m
}

// newMux is NewHandler; standalone is false when the routes are mounted on a host's mux (see Mount), where "/", "/health" and
// "/version" belong to the host, so they are not registered: an unknown path under /v1/gb/ is a plain 404.
func newMux(ctx context.Context,
	standalone bool,
	auth httputil.Authenticator,
	geoResolver memory_geo.Resolver,
	geoRadiusKm float64,
	graphEmbedder memory_core.GraphEmbedder,
	trainingHook memory_core.ClassifyingTrainingHook,
	mcpEnable bool,
	research *agent_research.Manager,
	d *db.DB,
	lm memory_core.LLM,
	emb memory_core.Embedder,
	jv memory_core.Classifier,
	ws memory_core.WebSearcher,
	providers []memory_core.SeriesProvider,
	hooks coretraining.Hooks,
	dirs coretraining.Dirs,
	ontologyDirs []string,
	sc coretraining.Sidecar,
	trainingMode,
	adminUI bool,
	adminExts []admin.Extension,
	prices *llm.PriceTable,
	evalSvc *evalsroutes.Service,
	gcniiSvc modelapi.Service,
	extraAdminDBs ...admin.Named) http.Handler {
	geoResolver = startGeoManager(ctx,
		geoResolver,
		d)
	mux := http.NewServeMux()
	if standalone {
		mux.HandleFunc("GET /", func(w http.ResponseWriter,
			r *http.Request) {
			fmt.Fprintln(w, "gligabao is running")
		})
		mux.HandleFunc("GET /health", func(w http.ResponseWriter,
			r *http.Request) {
			if err := d.Ping(r.Context()); err != nil {
				http.Error(w,
					"db unavailable",
					http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintln(w, "ok")
		})
		mux.HandleFunc("GET /version", versionHandler)
	}
	mux.HandleFunc("GET /v1/gb/status", statusHandler(d, sc != nil))
	// The remember manager runs every remember pipeline run, one at a time, until ctx is done.
	errSink, sinkErr := gberrors.SinkFor(d.Backend, d.Relational)
	if sinkErr != nil {
		errSink = nil
	}
	mgr := memory_remember.NewManager(ctx,
		d.Relational,
		lm,
		memory_remember.WithErrSink(errSink))
	api.Register(mux,
		mgr,
		d,
		lm,
		emb,
		jv,
		ws,
		providers,
		api.WithOntologyDirs(ontologyDirs),
		api.WithAuth(auth),
		api.WithGeoResolver(geoResolver),
		api.WithGeoRadius(geoRadiusKm),
		api.WithGraphEmbedder(graphEmbedder),
		api.WithClassifyingTrainingHook(trainingHook),
		api.WithResearch(research),
		api.WithEvals(evalSvc),
		api.WithGCNII(gcniiSvc))
	if mcpEnable {
		api.RegisterMCP(mux,
			mgr,
			d,
			lm,
			emb,
			jv,
			ws,
			providers,
			buildinfo.Library().Version,
			api.WithGeoResolver(geoResolver),
			api.WithGeoRadius(geoRadiusKm),
			api.WithGraphEmbedder(graphEmbedder),
			api.WithResearch(research))
	}
	dbs := append([]admin.Named{{
		Name:     "standard",
		DB:       d.Relational,
		Vectors:  d.Vector,
		ImageDir: d.ImageDir,
		Images:   d.ImageStore,
		Evals:    evalSvc,
	}}, extraAdminDBs...)
	if trainingMode {
		registerTraining(ctx,
			mux,
			d.Backend,
			d.Relational,
			dirs,
			hooks,
			sc,
			lm)
	}
	if adminUI {
		registerAdminUI(mux,
			dbs,
			geoRadiusKm,
			sc,
			adminExts,
			lm,
			research,
			api.QueryHandler(d,
				lm,
				emb,
				api.WithGeoResolver(geoResolver),
				api.WithGeoRadius(geoRadiusKm),
				api.WithGraphEmbedder(graphEmbedder)),
			prices)
	}
	return mux
}

// newResearch makes the manager of the research agents (POST /v1/gb/research), which runs them until ctx is done. They need the Anthropic
// client: with any other lm (or none) starting a research answers 503 (but for one with the modal runner, which runs with the keys of the
// Modal app and needs the sidecar).
func newResearch(ctx context.Context,
	d *db.DB,
	lm memory_core.LLM) *agent_research.Manager {
	ops, err := agent_research.OpsFor(d.Backend)
	if err != nil {
		log.WithError(err).Fatal("agent research")
	}
	client, _ := lm.(*llm.Client)
	m := agent_research.NewManager(ctx,
		d.Relational,
		ops,
		client)
	m.UseRemote(sidecarRemote{}) // researches started with runner "modal" run in Modal, through the sidecar
	m.UseImporter(dbImporter{d}) // and what their agents learned there is loaded into the databases of the server when they end
	return m
}

// statusHandler serves GET /v1/gb/status: the version of gligabao, whether the database answers ("db") and the sidecar's
// state (a live check, like the admin UI's, and whether it was active on startup). It answers 503 when the database does not
// answer; a sidecar that is down is only reported, it does not change the status.
func statusHandler(d *db.DB,
	sidecarConfigured bool) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		out := map[string]any{"gligabao": buildinfo.Library(), "commit": buildinfo.Revision(), "status": "ok", "db": "ok"}
		code := http.StatusOK
		if err := d.Ping(r.Context()); err != nil {
			out["status"], out["db"] = "unavailable", "unavailable"
			code = http.StatusServiceUnavailable
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		out["sidecar_up"] = sidecarPing(ctx) == nil
		out["sidecar_active"] = sidecarConfigured
		httputil.WriteJSON(w,
			code,
			out)
	}
}

// versionHandler reports the version (and git commit) of gligabao and, if registered, of the embedding program.
func versionHandler(w http.ResponseWriter,
	r *http.Request) {
	out := map[string]any{"gligabao": buildinfo.Library()}
	if app, ok := buildinfo.App(); ok {
		out["app"] = app
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// registerTraining registers the training endpoints under /v1/gb/training/ (they are part of the API, not of the admin UI,
// and are served whether or not the admin UI is on, but only in training mode): the jobs, the datasets and the feed of the jobs' progress. They work with trainingDB; dirs are the directories they keep files in (the datasets, the evals, the deployed models). The training monitor follows the progress the sidecar
// reports for the jobs (it resumes the unfinished ones now) and feeds it to the SSE endpoint. lm judges the evals and the training data audits of the
// trained models (nil: starting one answers 503). Trained models are deployed through hooks.Deployers, after their files are fetched to dirs.ModelDeploy.
func registerTraining(ctx context.Context,
	mux *http.ServeMux,
	backend string,
	trainingDB *sql.DB,
	dirs coretraining.Dirs,
	hooks coretraining.Hooks,
	sc coretraining.Sidecar,
	lm memory_core.LLM) {
	mon := coretraining.NewMonitor(ctx,
		trainingDB,
		sc)
	// The evals and the training data audits are those of internal/evals and internal/training_audits (their own tables).
	evalStore, err := evals.StoreFor(backend)
	if err != nil {
		log.WithError(err).Fatal("training evals")
	}
	var evalInf evalstraining.Inferer // not sc itself: a nil Sidecar must become a nil Inferer
	if sc != nil {
		evalInf = sc
	}
	evals := evalstraining.NewManager(ctx,
		trainingDB,
		evalStore,
		dirs.Evals,
		evalInf,
		lm)
	var auditInf training_audits.Inferer // likewise nil without a sidecar
	if sc != nil {
		auditInf = sc
	}
	auditOps, err := training_audits.OpsFor(backend)
	if err != nil {
		log.WithError(err).Fatal("training audits")
	}
	audits := training_audits.NewManager(ctx,
		trainingDB,
		auditOps,
		dirs.Datasets,
		auditInf,
		lm)
	mux.HandleFunc("POST /v1/gb/training/jobs", training.StartJobOf(trainingDB,
		dirs.Datasets,
		sc,
		mon))
	mux.HandleFunc("POST /v1/gb/training/jobs/{id}/stop", training.StopJobOf(trainingDB, sc))
	mux.HandleFunc("GET /v1/gb/training/jobs", training.ListJobsOf(trainingDB))
	mux.HandleFunc("GET /v1/gb/training/models", training.ListModelsOf(trainingDB))
	mux.HandleFunc("GET /v1/gb/training/models/{id}", training.GetModelOf(trainingDB))
	var inferSidecar coretraining.Inferer // likewise nil without a sidecar
	if sc != nil {
		inferSidecar = sc
	}
	mux.HandleFunc("POST /v1/gb/training/models/{id}/infer", training.InferOf(trainingDB, coretraining.NewInferRouter(inferSidecar, hooks.Inference)))
	mux.HandleFunc("GET /v1/gb/training/inference-hooks", training.ListInferenceHooksOf(hooks.Inference))
	mux.HandleFunc("GET /v1/gb/training/models/{id}/inferences", training.ListInferencesOf(trainingDB))
	var fetcher coretraining.ModelFileFetcher // not sc itself: a nil Sidecar must become a nil fetcher
	if f, ok := sc.(coretraining.ModelFileFetcher); ok {
		fetcher = f
	}
	mux.HandleFunc("POST /v1/gb/training/models/{id}/deployments", training.DeployOf(trainingDB,
		fetcher,
		hooks.Deployers,
		dirs.ModelDeploy))
	mux.HandleFunc("GET /v1/gb/training/models/{id}/deployments", training.ListDeploymentsOf(trainingDB))
	mux.HandleFunc("GET /v1/gb/training/deployers", training.ListDeployersOf(hooks.Deployers))
	evalsapi.New(evals).Register(mux, "/v1/gb/training") // the evals, their definitions and the training experiments
	auditsapi.New(audits).Register(mux, "/v1/gb/training")
	mux.HandleFunc("GET /v1/gb/training/datasets", training.ListDatasetsOf(trainingDB, dirs.Datasets))
	mux.HandleFunc("POST /v1/gb/training/datasets", training.UploadDatasetOf(trainingDB, dirs.Datasets))
	mux.HandleFunc("GET /v1/gb/training/datasets/preview", training.PreviewDatasetOf(dirs.Datasets))
	mux.HandleFunc("GET /v1/gb/training/events", training.EventsOf(trainingDB, mon))
}

// registerAdminUI serves the embedded admin UI assets under /v1/gb/admin/. It is a no-op (with a warning) if the
// assets were not built into the binary (see admin_ui/ and run_build.sh).
func registerAdminUI(mux *http.ServeMux,
	dbs []admin.Named,
	geoRadiusKm float64,
	sc coretraining.Sidecar,
	exts []admin.Extension,
	lm memory_core.LLM,
	research *agent_research.Manager,
	query http.HandlerFunc,
	prices *llm.PriceTable) {
	if !adminui.Available() {
		log.Warn("admin_ui is enabled but the web assets are not embedded in this binary; build with ./run_build.sh (admin UI build enabled)")
		return
	}
	admin.RegisterExtensions(mux, exts) // the extensions' list, assets and APIs (docs/admin-extensions.md)
	mux.HandleFunc("GET /v1/gb/admin/api/databases", admin.Databases(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/overview", admin.OverviewOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/graph", admin.GraphOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/ontologies", admin.OntologiesOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/vectors", admin.VectorsOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/vectors/all", admin.VectorsAllOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/images", admin.ImagesOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/timeseries", admin.TimeseriesOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/inferences", admin.InferencesOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/remember-runs", admin.RememberRunsOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/remember-run", admin.RememberRunOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/remember-run-outputs", admin.RememberRunOutputsOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/remember-run-temporal", admin.RememberRunTemporalOf(dbs))
	// The query button of the top bar: the models and thinking levels to pick, and the query itself (the body of POST /v1/gb/query, with no API key).
	mux.HandleFunc("GET /v1/gb/admin/api/query-options", admin.QueryOptionsOf(llm.SupportedModels(prices)))
	mux.HandleFunc("POST /v1/gb/admin/api/query", query) // 503 without an LLM, as the public one
	mux.HandleFunc("GET /v1/gb/admin/api/query-runs", admin.QueryRunsOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/query-run", admin.QueryRunOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/web-searches", admin.WebSearchStatsOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/web-searches/dataset", admin.WebSearchDatasetOf(dbs))
	// The research agents of the standard database: the list, starting one, one agent, stopping an active one, and what an agent put in the knowledge base.
	if research != nil {
		mux.HandleFunc("GET /v1/gb/admin/api/research", admin.ResearchListOf(research))
		mux.HandleFunc("POST /v1/gb/admin/api/research", admin.ResearchStartOf(research))
		mux.HandleFunc("GET /v1/gb/admin/api/research/{id}", admin.ResearchGetOf(research))
		mux.HandleFunc("POST /v1/gb/admin/api/research/{id}/stop", admin.ResearchStopOf(research))
		mux.HandleFunc("GET /v1/gb/admin/api/research/{id}/provenance", admin.ResearchProvenanceOf(research))
		mux.HandleFunc("GET /v1/gb/admin/api/research/{id}/temporal", admin.ResearchTemporalOf(research))
		mux.HandleFunc("GET /v1/gb/admin/api/research/{id}/investigation", admin.ResearchInvestigationOf(research))
		mux.HandleFunc("GET /v1/gb/admin/api/research/agents", admin.ResearchAgentsOf(research))
		mux.HandleFunc("GET /v1/gb/admin/api/research/agent-defs", admin.ResearchAgentDefsOf(research))
	}
	mux.HandleFunc("DELETE /v1/gb/admin/api/datasets", admin.DeleteDatasetOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/geo-index", admin.GeoIndexOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/geo", admin.GeoOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/geo/nearby", admin.GeoNearbyOf(dbs, geoRadiusKm))
	mux.HandleFunc("GET /v1/gb/admin/api/sidecar/status", admin.SidecarStatusOf(sc != nil, sidecarPing))
	mux.HandleFunc("GET /v1/gb/admin/api/sidecar/backends", admin.SidecarBackendsOf(sidecarBackends))
	// A backend's long-running actions (e.g. deploy the Modal app): start, cancel, state with log, live log (SSE).
	const actions = "/v1/gb/admin/api/sidecar/backends/{name}/actions/{action}"
	mux.HandleFunc("POST "+actions, admin.SidecarBackendProxy(sidecarOpen, ""))
	mux.HandleFunc("GET "+actions, admin.SidecarBackendProxy(sidecarOpen, ""))
	mux.HandleFunc("GET "+actions+"/events", admin.SidecarBackendProxy(sidecarOpen, "/events"))
	mux.HandleFunc("POST "+actions+"/cancel", admin.SidecarBackendProxy(sidecarOpen, "/cancel"))
	mux.HandleFunc("GET /v1/gb/admin/api/sidecar/backends/{name}/volumes", admin.SidecarBackendProxy(sidecarOpen, "/volumes"))
	// The active apps of this system on a backend (Modal: the deployed ones), and the same action routes for each of them.
	mux.HandleFunc("GET /v1/gb/admin/api/sidecar/backends/{name}/apps", admin.SidecarBackendProxy(sidecarOpen, "/apps"))
	const appActions = "/v1/gb/admin/api/sidecar/backends/{name}/apps/{app}/actions/{action}"
	mux.HandleFunc("POST "+appActions, admin.SidecarBackendProxy(sidecarOpen, ""))
	mux.HandleFunc("GET "+appActions, admin.SidecarBackendProxy(sidecarOpen, ""))
	mux.HandleFunc("GET "+appActions+"/events", admin.SidecarBackendProxy(sidecarOpen, "/events"))
	mux.HandleFunc("POST "+appActions+"/cancel", admin.SidecarBackendProxy(sidecarOpen, "/cancel"))
	// The evals of every user live in the main database (the first Named); without them there is nothing to show.
	if len(dbs) > 0 && dbs[0].Evals != nil {
		e := dbs[0].Evals
		mux.HandleFunc("GET /v1/gb/admin/api/evals", admin.EvalOverview(e))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/defs/{id}", admin.EvalDef(e))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/defs/{id}/history", admin.EvalCaseHistory(e))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/runs/{id}", admin.EvalRun(e))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/runs/{id}/web-searches", admin.EvalRunWebSearches(e))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/case_runs/{id}/graph", admin.EvalCaseRunGraph(e))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/case_runs/{id}/temporal", admin.EvalCaseRunTemporal(e))
		// The debug view of a failed case: its checks, and the LLM commentary on it (made on request when the server has an LLM).
		mux.HandleFunc("GET /v1/gb/admin/api/evals/case_runs/{id}/debug", admin.EvalCaseRunDebug(e))
		mux.HandleFunc("POST /v1/gb/admin/api/evals/case_runs/{id}/commentary", admin.EvalCaseRunCommentary(e))
		// The rerun of a case with other models, made as the owner of the case run.
		mux.HandleFunc("POST /v1/gb/admin/api/evals/case_runs/{id}/rerun", admin.EvalCaseRunRerun(e))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/trajectories/{id}", admin.EvalTrajectory(e))
	}
	mux.Handle("GET /v1/gb/admin/", http.StripPrefix("/v1/gb/admin/", http.FileServerFS(adminui.Assets())))
	mux.HandleFunc("GET /v1/gb/admin", func(w http.ResponseWriter,
		r *http.Request) {
		http.Redirect(w,
			r,
			"/v1/gb/admin/",
			http.StatusMovedPermanently)
	})
}

//---------------------------------------------------------------------------------------------

// openAdminDB opens a SQLite file (creating it with the schema if missing) for the admin UI; vectors opened on demand (see admin.Named.OpenVectors).
func openAdminDB(path string) (*sql.DB, error) {
	rel, err := relational.Open(config.DBBackendSQLite, path)
	if err != nil {
		return nil, err
	}
	if err := graph.Open(config.DBBackendSQLite, rel); err != nil {
		rel.Close()
		return nil, err
	}
	if err := trainingdb.Open(config.DBBackendSQLite, rel); err != nil {
		rel.Close()
		return nil, err
	}
	if err := evals.Open(config.DBBackendSQLite, rel); err != nil {
		rel.Close()
		return nil, err
	}
	return rel, nil
}

//---------------------------------------------------------------------------------------------

// Run opens the database, starts the server and blocks until ctx is
// cancelled or SIGINT/SIGTERM is received, then shuts down gracefully.
func Run(ctx context.Context,
	opts Options) error {
	fmt.Fprint(os.Stderr, banner)
	yellow, reset := "", ""
	if st, err := os.Stderr.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == "" {
		yellow, reset = "\033[33m", "\033[0m"
	}
	fmt.Fprintf(os.Stderr,
		"\nServer: %s%s%s\n",
		yellow,
		serverURL(opts.Addr),
		reset)
	if opts.AdminUIenable && adminui.Available() {
		fmt.Fprintf(os.Stderr,
			"Admin UI: %s%s%s\n",
			yellow,
			adminURL(opts.Addr),
			reset)
	}
	fmt.Fprintln(os.Stderr)

	handler, closeAll, err := prepare(ctx,
		opts,
		true)
	if err != nil {
		return err
	}
	// The remember manager stops, and the databases close, only when Run returns, i.e. after the graceful shutdown has given the
	// runs in progress their time.
	defer closeAll()
	srv := &http.Server{
		Addr:    opts.Addr,
		Handler: handler,
	}

	ctx, stop := signal.NotifyContext(ctx,
		syscall.SIGINT,
		syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.WithField("addr", opts.Addr).Info("listening")
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			// Open connections (e.g. streams) did not finish in time: close them and quit.
			log.Warn("graceful shutdown timed out; closing the remaining connections")
			_ = srv.Close()
		}
		return nil
	}
	return nil
}

//---------------------------------------------------------------------------------------------

// MountPrefix is the path prefix every route of gligabao is under (the API, the admin UI, the eval and training endpoints).
const MountPrefix = "/v1/gb/"

// mountMethods are the methods gligabao's routes use. Mount registers the subtree once per method ("GET /v1/gb/"), not as a
// method-less "/v1/gb/": that one would conflict with a host's own "GET /" (ServeMux rejects patterns where neither is more specific).
var mountMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// Mount does what Run does, except that it does not listen: it sets everything up (databases, providers, managers, ...) and
// registers gligabao's handlers on mux, the mux of the program that hosts it, as the subtree MountPrefix ("/v1/gb/"). It returns
// as soon as the handlers are registered (so register them before the host starts serving) and the returned function releases
// what was set up (the remember manager and the databases); it is safe to call more than once. The same release happens by itself
// when ctx is cancelled. Call it only after the host's server has stopped serving: once released, the mounted routes answer 503.
//
// gligabao's own "/", "/health" and "/version" routes are not registered, they belong to the host. wrap, if not nil, wraps the
// handler (e.g. with the host's authentication) before it is registered. The Addr option is not used. Mount returns an error,
// not a panic, if the host already has a conflicting route under "/v1/gb/".
func Mount(ctx context.Context,
	opts Options,
	mux *http.ServeMux,
	wrap func(http.Handler) http.Handler) (release func(), err error) {
	if mux == nil {
		return nil, errors.New("gligabao: mount: mux is nil")
	}
	handler, closeAll, err := prepare(ctx,
		opts,
		false)
	if err != nil {
		return nil, err
	}
	var released atomic.Bool
	inner := handler
	handler = http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		if released.Load() {
			http.Error(w,
				"gligabao is shut down",
				http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	})
	if wrap != nil {
		handler = wrap(handler)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			released.Store(true)
			closeAll()
		})
	}
	for _, m := range mountMethods {
		if err := register(mux,
			m+" "+MountPrefix,
			handler); err != nil {
			release() // routes registered before the conflict stay on the mux, and answer 503
			return nil, err
		}
	}
	go func() {
		<-ctx.Done()
		release()
	}()
	log.WithField("prefix", MountPrefix).Info("gligabao handlers are mounted on the host's mux (gligabao does not listen on its own)")
	return release, nil
}

// register registers handler on mux under pattern; ServeMux panics on a conflicting route, which becomes an error here.
func register(mux *http.ServeMux,
	pattern string,
	handler http.Handler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("gligabao: mount: cannot register %q on the mux: %v",
				pattern,
				r)
		}
	}()
	mux.Handle(pattern, handler)
	return nil
}

//---------------------------------------------------------------------------------------------

// openTraceStore returns the S3 store of the ATIF traces, or nil (the local trajectory directory) when no bucket is configured.
func openTraceStore(ctx context.Context,
	cfg atifstore.S3Config) (atifstore.Store, error) {
	if cfg.Bucket == "" {
		return nil, nil
	}
	s, err := atifstore.NewS3(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// storeResearchTrace returns the function that stores the merged trace of each research in the directory of its user. A failed
// write is logged: the research is over and does not depend on it.
func storeResearchTrace(store atifstore.Store) agent_research.TrajectoryFunc {
	return func(runID int64,
		userID string,
		t *atif.Trajectory) {
		loc, err := store.Put(context.Background(),
			userID,
			t)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{"run_id": runID, "user_id": userID, "trajectory_id": t.TrajectoryID}).Error("store ATIF trace of research")
			return
		}
		log.WithFields(log.Fields{"run_id": runID, "location": loc}).Debug("ATIF trace of research stored")
	}
}

// prepare opens the databases, builds everything the server needs and returns its handler (the routes of NewHandler) and
// a function that releases it all (the remember manager, then the databases), which the caller must call when done. Nothing is
// listened on. On an error everything already set up is released.
func prepare(ctx context.Context,
	opts Options,
	standalone bool) (handler http.Handler, closeAll func(), err error) {
	var cleanups []func() // run in reverse order
	release := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
		cleanups = nil
	}
	closeAll = release
	defer func() {
		if err != nil { // the returns below set closeAll to nil, so release is what is called
			release()
			handler, closeAll = nil, nil
		}
	}()

	lib := buildinfo.Library()
	fields := log.Fields{"version": lib.Version, "commit": lib.Commit}
	if app, ok := buildinfo.App(); ok {
		fields["app"], fields["app_version"], fields["app_commit"] = app.Name, app.Version, app.Commit
	}
	log.WithFields(fields).Info("starting gligabao")
	opts.DB.Models = llm.SupportedModels(llm.NewPriceTable(opts.Prices)) // the models gligabao supports are stored on startup
	opts.DB.Geo = opts.GeoResolver != nil || opts.Geo                    // the geo index is only created when geo is on
	database, err := dbfull.Open(opts.DB)
	if err != nil {
		return nil, nil, err
	}
	cleanups = append(cleanups, func() { database.Close() })
	// Only non-secret fields are logged (never the DSN).
	log.WithFields(log.Fields{
		"backend":              database.Backend,
		"sqlite_path":          database.SQLitePath,
		"vector_store":         database.VectorStore,
		"vector_dir":           database.VectorDir,
		"image_store":          database.ImageStore.Location(),
		"embedding_dimensions": database.EmbeddingDimensions,
	}).Info("databases opened")

	switch {
	case opts.TrajectoryDir != "":
		memory_core.TrajectoryDir = opts.TrajectoryDir
	case database.SQLitePath != "":
		memory_core.TrajectoryDir = filepath.Join(filepath.Dir(database.SQLitePath), "atif")
	}
	traceStore, err := openTraceStore(ctx, opts.TrajectoryS3)
	if err != nil {
		return nil, nil, err
	}
	atifstore.Default = traceStore
	if traceStore != nil {
		log.WithField("atif_s3", opts.TrajectoryS3.Bucket+"/"+opts.TrajectoryS3.Prefix).Info("ATIF traces are stored in S3, one directory per user")
	}
	memory_ontology.Archive = nil
	if opts.OntologyS3.Bucket != "" {
		cfg := opts.OntologyS3
		ontoStore, ontoErr := memory_ontology.NewS3(ctx, cfg)
		if ontoErr != nil {
			return nil, nil, ontoErr
		}
		memory_ontology.Archive = ontoStore
		log.WithField("ontology_s3", ontoStore.Location()).Info("ontology documents are also stored in S3, one directory per user")
	}
	dbPath := opts.DB.SQLitePath
	if dbPath == "" {
		dbPath = db.DefaultSQLitePath
	}
	dirs := opts.TrainingDirs.WithDefaults(dbPath)
	if opts.TrainingModeEnable {
		log.WithFields(log.Fields{"datasets_dirs": dirs.Datasets, "evals_dirs": dirs.Evals, "model_deploy_dir": dirs.ModelDeploy}).Warn("training mode: the training endpoints (/v1/gb/training/) are enabled")
	} else {
		log.Info("training mode is off: the training endpoints are not registered (enable with training = true or --training)")
	}

	switch {
	case opts.MCPEnable && opts.Auth != nil:
		log.Warn("MCP is enabled: the MCP endpoint (/v1/gb/mcp, tools remember and query) has no authentication and is NOT covered by the Auth hook; callers choose the user_id themselves")
	case opts.MCPEnable:
		log.Warn("MCP is enabled: the MCP endpoint (/v1/gb/mcp, tools remember and query) has no authentication; callers choose the user_id themselves")
	default:
		log.Info("MCP is not active: the MCP endpoint is not registered (enable with mcp = true or --mcp)")
	}

	if opts.AdminUIenable && adminui.Available() {
		adminPath := adminURL(opts.Addr)
		if opts.Addr == "" { // mounted on a host's mux: the host's address is not known here
			adminPath = "/v1/gb/admin/"
		}
		log.WithField("url", adminPath).Info("admin UI is active")
	} else if opts.AdminUIenable {
		log.Warn("admin UI is enabled but not active: web assets are not embedded in this binary")
	} else {
		log.Info("admin UI is not active (enable with admin_ui = true or --admin-ui)")
	}

	providers, err := memory_core.ResolveSeriesProviders(opts.TimeseriesProviders)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range providers {
		log.WithField("provider", p.Name).Info("timeseries provider registered: remember can fetch series from it when asked (timeseries.fetch)")
	}

	var geoResolver memory_geo.Resolver
	switch {
	case opts.GeoResolver != nil:
		geoResolver = opts.GeoResolver
	case opts.Geo:
		geoResolver = memory_geo.Default()
	}
	if geoResolver != nil {
		log.WithField("resolver", geoResolver.Name()).Info("geo is active: remember finds the places among the entities of a text and stores their coordinates")
	} else {
		log.Info("geo is not active: remember does not look up places (enable with geo = true or --geo)")
	}

	deployers, err := coretraining.ResolveDeployers(opts.TrainedModelDeployers)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range deployers {
		log.WithField("deployer", e.Name).Info("trained model deployer registered: trained models can be deployed through it (POST /v1/gb/training/models/{id}/deployments)")
	}

	inferenceHooks, err := coretraining.ResolveInferenceHooks(opts.TrainedModelInferenceHooks)
	if err != nil {
		return nil, nil, err
	}
	for _, h := range inferenceHooks {
		log.WithFields(log.Fields{"hook": h.Name, "model": h.Model}).Info("trained model inference hook registered: prompts of the model bypass the sidecar and run on it")
	}
	hooks := coretraining.Hooks{
		Deployers: deployers,
		Inference: inferenceHooks,
	}

	adminExts, err := admin.ResolveExtensions(opts.AdminExtensions)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range adminExts {
		if opts.AdminUIenable {
			log.WithField("extension", e.Name).Info("admin UI extension registered: it is loaded by the admin UI")
		} else {
			log.WithField("extension", e.Name).Warn("admin UI extension not active: the admin UI is not enabled (admin_ui = true or --admin-ui)")
		}
	}

	var lm memory_core.LLM
	// Web search is Anthropic's cloud-based web search tool, so it needs the Anthropic API key and only exists with it.
	var ws memory_core.WebSearcher
	if c, err := llm.New(opts.AnthropicKey, llm.WithPrices(llm.NewPriceTable(opts.Prices))); err != nil {
		log.WithError(err).Warn("LLM client unavailable; /v1/gb/remember and /v1/gb/query will return 503")
		log.Info("web search is not available (no Anthropic API key); remember requests with web_search return 503")
	} else {
		lm, ws = c, c
		log.Info("web search is available: remember searches the web for the key entities of a text when asked (web_search)")
	}

	// The embedder is required: remember embeds what it stores with it and query embeds the question (similarity retrieval).
	emb, err := llm.NewEmbedder(opts.OpenAIKey,
		llm.WithEmbedPrices(llm.NewPriceTable(opts.Prices)),
		llm.WithEmbedDimensions(database.EmbeddingDimensions))
	if err != nil {
		return nil, nil, fmt.Errorf("embedder: %w (set openai_api_key or OPENAI_API_KEY)", err)
	}

	// Jev only runs when its API key is supplied.
	var jv memory_core.Classifier
	if opts.JevKey == "" {
		log.Info("Jev is not configured (no jev_api_key / JEV_API_KEY); remember skips the complexity classification")
	} else if c, err := newJev(opts); err != nil {
		log.WithError(err).Warn("Jev unavailable; remember skips the complexity classification")
	} else {
		jv = c
		log.WithField("model", jevModel(opts)).Info("Jev is active: remember classifies the complexity of each text")
	}

	var extra []admin.Named

	// The sidecar is pinged once on startup. If it is not operating correctly, the flag is off and sc stays nil, which
	// makes everything that needs the sidecar answer 503 (or do nothing) instead of failing.
	sc, sidecarActive := connectSidecar(ctx, opts.SidecarURL)
	SidecarActive.Store(sidecarActive)

	// The definitions of the research agents are read once, here: one that is not valid is an error, not an agent that is missing later.
	agentDefsDir := opts.AgentDefsDir
	if agentDefsDir == "" {
		agentDefsDir = agent_defs.DefaultDir()
	}
	agentDefs, err := agent_defs.Load(agentDefsDir)
	if err != nil {
		return nil, nil, err
	}

	// The remember manager stops when everything is released, i.e. after the graceful shutdown has given the runs in progress their time.
	mgrCtx, stopMgr := context.WithCancel(ctx)
	// The research agents are stopped with it, and given the time to write the end of their runs before the databases close.
	research := newResearch(mgrCtx,
		database,
		lm)
	research.MaxCostUSD = opts.ResearchMaxCostUSD
	research.NotesDir = opts.ResearchNotesDir
	research.LinkupKey = opts.LinkupKey
	research.SearchProvider = web.Provider(opts.ResearchSearchProvider)
	research.FetchProvider = web.Provider(opts.ResearchFetchProvider)
	research.InitSandbox(opts.Monty)
	if opts.ResearchNotesS3.Bucket != "" {
		notesS3, err := investigate.NewNotesS3(ctx, opts.ResearchNotesS3)
		if err != nil {
			stopMgr()
			return nil, nil, err
		}
		research.NotesS3 = notesS3
	}
	if traceStore != nil {
		research.UseTrajectories(storeResearchTrace(traceStore))
	}
	if err := research.UseAgentDefs(agentDefs); err != nil {
		stopMgr()
		return nil, nil, err
	}
	// The run manager of the evals: this is where the injected hook Options.EvalManager is called (it is injected because the package
	// that implements it imports this one, see its comment). It runs the evals of the users and their reruns, and is given the time to
	// record how the runs ended before the databases close, like the research agents: the cleanup below cancels mgrCtx (which the
	// manager got as EvalEnv.Ctx) and then waits for it, if it can be waited for.
	var evalSvc *evalsroutes.Service
	var evalWait interface{ Wait() }
	if opts.EvalManager != nil {
		files, filesErr := evalFiles(mgrCtx,
			opts.Evals,
			dbPath)
		if filesErr != nil {
			stopMgr()
			return nil, nil, filesErr
		}
		userfiles.Default = files
		evalStore, err := evals.StoreFor(database.Backend)
		if err != nil {
			stopMgr()
			return nil, nil, err
		}
		mgr := opts.EvalManager(EvalEnv{
			Ctx:           mgrCtx,
			DB:            database,
			LLM:           lm,
			Embedder:      emb,
			WebSearch:     ws,
			Geo:           geoResolver,
			Prices:        llm.NewPriceTable(opts.Prices),
			Files:         files,
			Trajectories:  traceStore,
			TrajectoryDir: memory_core.TrajectoryDir,
			Settings:      opts.Evals,
		})
		if mgr != nil {
			evalSvc = &evalsroutes.Service{
				DB:            database.Relational,
				Store:         evalStore,
				Mgr:           mgr,
				LLM:           lm,
				TrajectoryDir: memory_core.TrajectoryDir,
				Trajectories:  traceStore,
			}
			evalWait, _ = mgr.(interface{ Wait() })
			log.Info("evals are available: /v1/gb/evals")
		}
	}
	cleanups = append(cleanups, func() {
		stopMgr()
		research.Wait()
		if evalWait != nil {
			evalWait.Wait()
		}
	})
	mux := newMux(mgrCtx,
		standalone,
		opts.Auth,
		geoResolver,
		opts.GeoRadiusKm,
		opts.GraphEmbedder,
		opts.ClassifyingTrainingHook,
		opts.MCPEnable,
		research,
		database,
		lm,
		emb,
		jv,
		ws,
		providers,
		hooks,
		dirs,
		opts.OntologyDirs,
		sc,
		opts.TrainingModeEnable,
		opts.AdminUIenable,
		adminExts,
		llm.NewPriceTable(opts.Prices),
		evalSvc,
		opts.GCNII,
		extra...)
	// The agents that have a schedule start running by it now that their knowledge base is set up (by newMux). They stop with mgrCtx.
	if n := research.RunSchedules(); n > 0 {
		log.Infof("agent research: %d agent(s) run on a schedule", n)
	}
	return mux, closeAll, nil
}

// evalFiles is the store of the eval files of the users: the meta bucket (opts.S3) if it has one, else the local directory (opts.LocalDir,
// by default an evals_user directory next to the SQLite file dbPath).
func evalFiles(ctx context.Context,
	opts EvalSettings,
	dbPath string) (userfiles.Store, error) {
	if opts.S3.Bucket != "" {
		store, err := userfiles.NewS3(ctx, opts.S3)
		if err != nil {
			return nil, err
		}
		log.WithField("evals_s3", store.Location()).Info("eval files are stored in S3, one directory per user")
		return store, nil
	}
	dir := opts.LocalDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(dbPath), "evals_user")
	}
	log.WithField("evals_dir", dir).Info("eval files are stored in a local directory, one per user")
	return userfiles.FS{Dir: dir}, nil
}

// newJev builds the Jev client from the options.
func newJev(opts Options) (*jev.Client, error) {
	return jev.New(opts.JevKey,
		jev.WithBaseURL(opts.JevBaseURL),
		jev.WithModel(opts.JevModel))
}

func jevModel(opts Options) string {
	if opts.JevModel != "" {
		return opts.JevModel
	}
	return jev.DefaultModel
}
