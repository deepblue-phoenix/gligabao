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
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/api"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/evals"
	evalstraining "github.com/deepblue-phoenix/gligabao/internal/evals/training"
	evalsapi "github.com/deepblue-phoenix/gligabao/internal/evals/training/api"
	"github.com/deepblue-phoenix/gligabao/internal/geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	"github.com/deepblue-phoenix/gligabao/internal/sys/buildinfo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/sys/jev"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
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
	Prices map[string]ai.Price
	// TrajectoryDir is where the ATIF trajectories of the eval endpoints are stored as JSON.
	// Default: an "atif" directory next to the SQLite file (memory_core.TrajectoryDir if there is none).
	TrajectoryDir string
	// EvalModeEnable registers the eval endpoints (/v1/gb/eval/remember and /v1/gb/eval/query), which return ATIF
	// trajectories with their results. Off by default.
	EvalModeEnable bool
	// TrainingModeEnable registers the training handlers (/v1/gb/training/...) and starts the training monitor. Off by default.
	TrainingModeEnable bool
	// TrainingDirs are the directories the training endpoints keep files in. An empty field gets its default, next to the SQLite
	// file (DB.SQLitePath, or db.DefaultSQLitePath if empty): see coretraining.DefaultDirs.
	TrainingDirs coretraining.Dirs
	// OntologyDirs are the directories ontology documents are loaded from by file name (see api.WithOntologyDirs). Default: none.
	OntologyDirs []string
	// EvalDBPath is a second SQLite file, the eval database (see internal/evals/knowledge/cmd): the evaluation datasets, the
	// experiments and their runs. The admin UI shows it next to the main database and reads the Evals block (and the
	// ATIF files, from an "atif" directory next to it) from it. Opened for the admin UI only; ignored (with a warning)
	// unless the main database is SQLite. Empty = not shown, and the Evals routes are not registered.
	EvalDBPath string
	// Auth, if set, makes the user routes of the API (remember, query, datasets, timeseries, ontologies) require an API key
	// ("Authorization: Bearer <key>"): it is called with the key and returns the ID of the user it belongs to, which the request then
	// acts as; an error rejects the request (401). See docs/identity.md. Nil: the routes trust the user_id of the request.
	Auth httputil.Authenticator
	// TimeseriesProviders are the sources of timeseries that remember can fetch from (see docs/timeseries.md). Each has a
	// natural-language description of when it applies, and a callback that does the fetching. Validated at startup.
	TimeseriesProviders []memory_core.SeriesProvider
	// GeoResolver finds the coordinates of the places remember extracts as entities (see package geo). Nil: the Nominatim resolver
	// of OpenStreetMap if Geo is true, else remember does not look at places.
	GeoResolver geo.Resolver
	// Geo turns the geo stage of remember on (with GeoResolver, or Nominatim if there is none); a GeoResolver turns it on by itself.
	Geo bool
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
func sidecarOpen(ctx context.Context, method, path, rawQuery string, body io.Reader) (*http.Response, error) {
	c := sidecarClient.Load()
	if c == nil {
		return nil, errors.New("sidecar not configured")
	}
	return c.Open(ctx, method, path, rawQuery, body)
}

// sidecarPingTimeout bounds the sidecar health check on startup.
const sidecarPingTimeout = 5 * time.Second

// connectSidecar pings the sidecar at url (default if empty). It returns the client and true if the sidecar is operating
// correctly; otherwise it logs a warning and returns a nil client (a true nil interface, so callers can check == nil) and false.
func connectSidecar(ctx context.Context, url string) (coretraining.Sidecar, bool) {
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

// EvalDBName is the name of the eval database in the admin UI (see Options.EvalDBPath). It holds the evaluation
// datasets, the experiments and their runs, kept apart from the main database.
const EvalDBName = "eval"

// NewHandler builds the HTTP handler for the given databases and LLM. ctx bounds the remember manager (see memory_remember.Manager).
// llm may be nil, in which case endpoints requiring it return 503. emb may be nil, in which case remember does not embed entities.
// jv may be nil, in which case remember skips the steps that run on Jev. ws may be nil, in which case web search is not available
// (a remember request with web_search returns 503); it is the Anthropic web search tool, so it is only set with an Anthropic API key.
// geoResolver turns geo on (remember and query find the places of a text, see package geo); nil: geo is off.
// providers are the registered timeseries providers (already validated; may be nil: a remember request that fetches series then returns 503). hooks are the registered hooks of the training API (already validated; the zero value has none): the trained model deployers (with none a deployment request answers 400) and the inference hooks of trained models. dirs are the directories of the training endpoints (all fields must be set when trainingMode is true; see coretraining.Dirs.WithDefaults). ontologyDirs are the directories ontology files are loaded from by name (may be nil). sc starts the training jobs (/v1/gb/training/) on the sidecar, lists its datasets and follows the jobs' progress (may be nil: the endpoints then return 503). The eval endpoints are registered only if eval is true, the training endpoints (/v1/gb/training/) only if trainingMode is true,
// the admin UI (/v1/gb/admin/) only if adminUI is true and its assets were built into the binary; adminExts are the registered admin UI extensions
// (already validated; may be nil), mounted with it.
func NewHandler(ctx context.Context, d *db.DB, llm memory_core.LLM, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, geoResolver geo.Resolver, providers []memory_core.SeriesProvider, hooks coretraining.Hooks, dirs coretraining.Dirs, ontologyDirs []string, sc coretraining.Sidecar, eval, trainingMode, adminUI bool, adminExts []admin.Extension, extraAdminDBs ...admin.Named) http.Handler {
	return newMux(ctx, true, nil, geoResolver, false, d, llm, emb, jv, ws, providers, hooks, dirs, ontologyDirs, sc, eval, trainingMode, adminUI, adminExts, extraAdminDBs...)
}

// newMux is NewHandler; standalone is false when the routes are mounted on a host's mux (see Mount), where "/", "/health" and
// "/version" belong to the host, so they are not registered: an unknown path under /v1/gb/ is a plain 404.
func newMux(ctx context.Context, standalone bool, auth httputil.Authenticator, geoResolver geo.Resolver, mcpEnable bool, d *db.DB, llm memory_core.LLM, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, providers []memory_core.SeriesProvider, hooks coretraining.Hooks, dirs coretraining.Dirs, ontologyDirs []string, sc coretraining.Sidecar, eval, trainingMode, adminUI bool, adminExts []admin.Extension, extraAdminDBs ...admin.Named) http.Handler {
	mux := http.NewServeMux()
	if standalone {
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "gligabao is running")
		})
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
			if err := d.Ping(r.Context()); err != nil {
				http.Error(w, "db unavailable", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintln(w, "ok")
		})
		mux.HandleFunc("GET /version", versionHandler)
	}
	// The remember manager runs every remember pipeline run, one at a time, until ctx is done.
	mgr := memory_remember.NewManager(ctx, d.Relational, llm)
	api.Register(mux, mgr, d, llm, emb, jv, ws, providers, api.WithOntologyDirs(ontologyDirs), api.WithAuth(auth), api.WithGeoResolver(geoResolver))
	if mcpEnable {
		api.RegisterMCP(mux, mgr, d, llm, emb, jv, ws, providers, buildinfo.Library().Version, api.WithGeoResolver(geoResolver))
	}
	if eval {
		api.RegisterEval(mux, mgr, d, llm, emb, jv, ws, providers, api.WithGeoResolver(geoResolver))
	}
	dbs := append([]admin.Named{{Name: "standard", DB: d.Relational, Vectors: d.Vector, ImageDir: d.ImageDir}}, extraAdminDBs...)
	if trainingMode {
		registerTraining(ctx, mux, d.Backend, d.Relational, dirs, hooks, sc, llm)
	}
	if adminUI {
		registerAdminUI(mux, dbs, sc, adminExts)
	}
	return mux
}

// versionHandler reports the version (and git commit) of gligabao and, if registered, of the embedding program.
func versionHandler(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"gligabao": buildinfo.Library()}
	if app, ok := buildinfo.App(); ok {
		out["app"] = app
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// registerTraining registers the training endpoints under /v1/gb/training/ (they are part of the API, not of the admin UI,
// and are served whether or not the admin UI is on, but only in training mode): the jobs, the datasets and the feed of the jobs' progress. They work with trainingDB; dirs are the directories they keep files in (the datasets, the evals, the deployed models). The training monitor follows the progress the sidecar
// reports for the jobs (it resumes the unfinished ones now) and feeds it to the SSE endpoint. llm judges the evals and the training data audits of the
// trained models (nil: starting one answers 503). Trained models are deployed through hooks.Deployers, after their files are fetched to dirs.ModelDeploy.
func registerTraining(ctx context.Context, mux *http.ServeMux, backend string, trainingDB *sql.DB, dirs coretraining.Dirs, hooks coretraining.Hooks, sc coretraining.Sidecar, llm memory_core.LLM) {
	mon := coretraining.NewMonitor(ctx, trainingDB, sc)
	// The evals and the training data audits are those of internal/evals and internal/training_audits (their own tables).
	evalStore, err := evals.StoreFor(backend)
	if err != nil {
		log.WithError(err).Fatal("training evals")
	}
	var evalInf evalstraining.Inferer // not sc itself: a nil Sidecar must become a nil Inferer
	if sc != nil {
		evalInf = sc
	}
	evals := evalstraining.NewManager(ctx, trainingDB, evalStore, dirs.Evals, evalInf, llm)
	var auditInf training_audits.Inferer // likewise nil without a sidecar
	if sc != nil {
		auditInf = sc
	}
	auditOps, err := training_audits.OpsFor(backend)
	if err != nil {
		log.WithError(err).Fatal("training audits")
	}
	audits := training_audits.NewManager(ctx, trainingDB, auditOps, dirs.Datasets, auditInf, llm)
	mux.HandleFunc("POST /v1/gb/training/jobs", training.StartJobOf(trainingDB, dirs.Datasets, sc, mon))
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
	mux.HandleFunc("POST /v1/gb/training/models/{id}/deployments", training.DeployOf(trainingDB, fetcher, hooks.Deployers, dirs.ModelDeploy))
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
func registerAdminUI(mux *http.ServeMux, dbs []admin.Named, sc coretraining.Sidecar, exts []admin.Extension) {
	if !adminui.Available() {
		log.Warn("admin_ui is enabled but the web assets are not embedded in this binary; build with ./run_build.sh (admin UI build enabled)")
		return
	}
	admin.RegisterExtensions(mux, exts) // the extensions' list, assets and APIs (docs/admin-extensions.md)
	mux.HandleFunc("GET /v1/gb/admin/api/databases", admin.Databases(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/overview", admin.OverviewOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/graph", admin.GraphOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/vectors", admin.VectorsOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/images", admin.ImagesOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/timeseries", admin.TimeseriesOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/inferences", admin.InferencesOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/remember-runs", admin.RememberRunsOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/web-searches", admin.WebSearchStatsOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/web-searches/dataset", admin.WebSearchDatasetOf(dbs))
	mux.HandleFunc("DELETE /v1/gb/admin/api/datasets", admin.DeleteDatasetOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/geo-index", admin.GeoIndexOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/geo", admin.GeoOf(dbs))
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
	// The experiments live in the eval database; without one there are none to show.
	for _, e := range dbs {
		if e.Name != EvalDBName || e.EvalStore == nil {
			continue
		}
		mux.HandleFunc("GET /v1/gb/admin/api/evals", admin.EvalOverview(e.DB, *e.EvalStore))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/experiments/{id}", admin.EvalExperiment(e.DB, *e.EvalStore))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/experiments/{id}/history", admin.EvalCaseHistory(e.DB, *e.EvalStore))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/runs/{id}", admin.EvalRun(e.DB, *e.EvalStore))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/runs/{id}/web-searches", admin.EvalRunWebSearches(e.DB, *e.EvalStore, e.TrajectoryDir))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/case_runs/{id}/graph", admin.EvalCaseRunGraph(e.DB, *e.EvalStore))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/trajectories/{id}", admin.EvalTrajectory(e.DB, *e.EvalStore, e.TrajectoryDir))
		break
	}
	mux.Handle("GET /v1/gb/admin/", http.StripPrefix("/v1/gb/admin/", http.FileServerFS(adminui.Assets())))
	mux.HandleFunc("GET /v1/gb/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v1/gb/admin/", http.StatusMovedPermanently)
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
func Run(ctx context.Context, opts Options) error {
	fmt.Fprint(os.Stderr, banner)
	yellow, reset := "", ""
	if st, err := os.Stderr.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == "" {
		yellow, reset = "\033[33m", "\033[0m"
	}
	fmt.Fprintf(os.Stderr, "\nServer: %s%s%s\n", yellow, serverURL(opts.Addr), reset)
	if opts.AdminUIenable && adminui.Available() {
		fmt.Fprintf(os.Stderr, "Admin UI: %s%s%s\n", yellow, adminURL(opts.Addr), reset)
	}
	fmt.Fprintln(os.Stderr)

	handler, closeAll, err := prepare(ctx, opts, true)
	if err != nil {
		return err
	}
	// The remember manager stops, and the databases close, only when Run returns, i.e. after the graceful shutdown has given the
	// runs in progress their time.
	defer closeAll()
	srv := &http.Server{Addr: opts.Addr, Handler: handler}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
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
func Mount(ctx context.Context, opts Options, mux *http.ServeMux, wrap func(http.Handler) http.Handler) (release func(), err error) {
	if mux == nil {
		return nil, errors.New("gligabao: mount: mux is nil")
	}
	handler, closeAll, err := prepare(ctx, opts, false)
	if err != nil {
		return nil, err
	}
	var released atomic.Bool
	inner := handler
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if released.Load() {
			http.Error(w, "gligabao is shut down", http.StatusServiceUnavailable)
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
		if err := register(mux, m+" "+MountPrefix, handler); err != nil {
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
func register(mux *http.ServeMux, pattern string, handler http.Handler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("gligabao: mount: cannot register %q on the mux: %v", pattern, r)
		}
	}()
	mux.Handle(pattern, handler)
	return nil
}

//---------------------------------------------------------------------------------------------

// prepare opens the databases, builds everything the server needs and returns its handler (the routes of NewHandler) and
// a function that releases it all (the remember manager, then the databases), which the caller must call when done. Nothing is
// listened on. On an error everything already set up is released.
func prepare(ctx context.Context, opts Options, standalone bool) (handler http.Handler, closeAll func(), err error) {
	var cleanups []func() // run in reverse order
	closeAll = func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
		cleanups = nil
	}
	defer func() {
		if err != nil {
			closeAll()
			handler, closeAll = nil, nil
		}
	}()

	lib := buildinfo.Library()
	fields := log.Fields{"version": lib.Version, "commit": lib.Commit}
	if app, ok := buildinfo.App(); ok {
		fields["app"], fields["app_version"], fields["app_commit"] = app.Name, app.Version, app.Commit
	}
	log.WithFields(fields).Info("starting gligabao")
	opts.DB.Models = ai.SupportedModels(ai.NewPriceTable(opts.Prices)) // the models gligabao supports are stored on startup
	database, err := db.Open(opts.DB)
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
		"image_dir":            database.ImageDir,
		"embedding_dimensions": database.EmbeddingDimensions,
	}).Info("databases opened")

	switch {
	case opts.TrajectoryDir != "":
		memory_core.TrajectoryDir = opts.TrajectoryDir
	case database.SQLitePath != "":
		memory_core.TrajectoryDir = filepath.Join(filepath.Dir(database.SQLitePath), "atif")
	}
	if opts.EvalModeEnable {
		log.Warn("eval mode: /v1/gb/eval/remember and /v1/gb/eval/query are enabled")
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
	if opts.EvalModeEnable {
		log.WithField("trajectory_dir", memory_core.TrajectoryDir).Info("ATIF trajectories are stored here")
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

	var geoResolver geo.Resolver
	switch {
	case opts.GeoResolver != nil:
		geoResolver = opts.GeoResolver
	case opts.Geo:
		geoResolver = geo.Default()
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
	hooks := coretraining.Hooks{Deployers: deployers, Inference: inferenceHooks}

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

	var llm memory_core.LLM
	// Web search is Anthropic's cloud-based web search tool, so it needs the Anthropic API key and only exists with it.
	var ws memory_core.WebSearcher
	if c, err := ai.New(opts.AnthropicKey, ai.WithPrices(ai.NewPriceTable(opts.Prices))); err != nil {
		log.WithError(err).Warn("LLM client unavailable; /v1/gb/remember and /v1/gb/query will return 503")
		log.Info("web search is not available (no Anthropic API key); remember requests with web_search return 503")
	} else {
		llm, ws = c, c
		log.Info("web search is available: remember searches the web for the key entities of a text when asked (web_search)")
	}

	var emb memory_core.Embedder
	if e, err := ai.NewEmbedder(opts.OpenAIKey, ai.WithEmbedPrices(ai.NewPriceTable(opts.Prices)), ai.WithEmbedDimensions(database.EmbeddingDimensions)); err != nil {
		log.WithError(err).Warn("embedder unavailable; /v1/gb/remember will not embed entities")
	} else {
		emb = e
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
	if opts.AdminUIenable && opts.EvalDBPath != "" {
		if database.Backend != config.DBBackendSQLite {
			log.Warn("eval database not shown in the admin UI: only supported with the sqlite backend")
		} else if evalDB, err := openAdminDB(opts.EvalDBPath); err != nil {
			log.WithError(err).WithField("path", opts.EvalDBPath).Warn("eval database not shown in the admin UI")
		} else {
			cleanups = append(cleanups, func() { evalDB.Close() })
			log.WithField("path", opts.EvalDBPath).Info("eval database is shown in the admin UI")
			// The eval runner (a separate process) keeps the eval database's vectors next to it, always in chromem with the
			// default dimensions (see internal/evals/knowledge/cmd), so open that directory afresh whenever it is needed.
			vecDir := db.DefaultVectorDir(opts.EvalDBPath)
			evalStore, err := evals.StoreFor(database.Backend)
			if err != nil {
				return nil, nil, err
			}
			extra = append(extra, admin.Named{
				Name:          EvalDBName,
				DB:            evalDB,
				EvalStore:     &evalStore,
				TrajectoryDir: filepath.Join(filepath.Dir(opts.EvalDBPath), "atif"),
				OpenVectors: func() (vector.Store, error) {
					return vector.NewChromem(vecDir, config.DefaultEmbeddingDimensions)
				},
			})
		}
	}

	// The sidecar is pinged once on startup. If it is not operating correctly, the flag is off and sc stays nil, which
	// makes everything that needs the sidecar answer 503 (or do nothing) instead of failing.
	sc, sidecarActive := connectSidecar(ctx, opts.SidecarURL)
	SidecarActive.Store(sidecarActive)

	// The remember manager stops when everything is released, i.e. after the graceful shutdown has given the runs in progress their time.
	mgrCtx, stopMgr := context.WithCancel(ctx)
	cleanups = append(cleanups, stopMgr)
	return newMux(mgrCtx, standalone, opts.Auth, geoResolver, opts.MCPEnable, database, llm, emb, jv, ws, providers, hooks, dirs, opts.OntologyDirs, sc, opts.EvalModeEnable, opts.TrainingModeEnable, opts.AdminUIenable, adminExts, extra...), closeAll, nil
}

// newJev builds the Jev client from the options.
func newJev(opts Options) (*jev.Client, error) {
	return jev.New(opts.JevKey, jev.WithBaseURL(opts.JevBaseURL), jev.WithModel(opts.JevModel))
}

func jevModel(opts Options) string {
	if opts.JevModel != "" {
		return opts.JevModel
	}
	return jev.DefaultModel
}
