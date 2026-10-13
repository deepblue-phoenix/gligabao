// Package server wires up and runs the HTTP server.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	adminui "github.com/deepblue-phoenix/gligabao/admin_ui"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/api"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/buildinfo"
	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/jev"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
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

// adminURL returns the URL at which the admin UI is reachable for the listen address addr.
func adminURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr + "/v1/gb/admin/"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/v1/gb/admin/"
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
	// Prices overrides/extends the built-in model price table used for call costs. May be nil.
	Prices map[string]ai.Price
	// TrajectoryDir is where the ATIF trajectories of the eval endpoints are stored as JSON.
	// Default: an "atif" directory next to the SQLite file (memory_core.TrajectoryDir if there is none).
	TrajectoryDir string
	// Eval registers the eval endpoints (/v1/gb/eval/remember and /v1/gb/eval/recall), which return ATIF
	// trajectories with their results. Off by default.
	Eval bool
	// EvalDBPath is a second SQLite file, the eval database (see evals/main.go): the evaluation datasets, the
	// experiments and their runs. The admin UI shows it next to the main database and reads the Evals block (and the
	// ATIF files, from an "atif" directory next to it) from it. Opened for the admin UI only; ignored (with a warning)
	// unless the main database is SQLite. Empty = not shown, and the Evals routes are not registered.
	EvalDBPath string
	// AdminUI registers the admin UI handlers (served at /v1/gb/admin/ from the assets embedded in the binary).
	AdminUI bool
}

//---------------------------------------------------------------------------------------------

// EvalDBName is the name of the eval database in the admin UI (see Options.EvalDBPath). It holds the evaluation
// datasets, the experiments and their runs, kept apart from the main database.
const EvalDBName = "eval"

// NewHandler builds the HTTP handler for the given databases and LLM.
// llm may be nil, in which case endpoints requiring it return 503. emb may be nil, in which case remember does not embed entities.
// jv may be nil, in which case remember skips the steps that run on Jev. ws may be nil, in which case web search is not available
// (a remember request with web_search returns 503); it is the Anthropic web search tool, so it is only set with an Anthropic API key. The eval endpoints are registered only if eval is true,
// the admin UI (/v1/gb/admin/) only if adminUI is true and its assets were built into the binary.
func NewHandler(d *db.DB, llm memory_core.LLM, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, eval, adminUI bool, extraAdminDBs ...admin.Named) http.Handler {
	mux := http.NewServeMux()
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
	api.Register(mux, d, llm, emb, jv, ws)
	if eval {
		api.RegisterEval(mux, d, llm, emb, jv, ws)
	}
	if adminUI {
		registerAdminUI(mux, d, extraAdminDBs)
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

// registerAdminUI serves the embedded admin UI assets under /v1/gb/admin/. It is a no-op (with a warning) if the
// assets were not built into the binary (see admin_ui/ and run_build.sh).
func registerAdminUI(mux *http.ServeMux, d *db.DB, extra []admin.Named) {
	if !adminui.Available() {
		log.Warn("admin_ui is enabled but the web assets are not embedded in this binary; build with ./run_build.sh (admin UI build enabled)")
		return
	}
	dbs := append([]admin.Named{{Name: "standard", DB: d.Relational, Vectors: d.Vector, ImageDir: d.ImageDir}}, extra...)
	mux.HandleFunc("GET /v1/gb/admin/api/databases", admin.Databases(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/overview", admin.OverviewOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/graph", admin.GraphOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/vectors", admin.VectorsOf(dbs))
	mux.HandleFunc("GET /v1/gb/admin/api/images", admin.ImagesOf(dbs))
	mux.HandleFunc("DELETE /v1/gb/admin/api/datasets", admin.DeleteDatasetOf(dbs))
	// The experiments live in the eval database; without one there are none to show.
	for _, e := range extra {
		if e.Name != EvalDBName {
			continue
		}
		mux.HandleFunc("GET /v1/gb/admin/api/evals", admin.EvalOverview(e.DB))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/experiments/{id}", admin.EvalExperiment(e.DB))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/experiments/{id}/history", admin.EvalHistory(e.DB))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/runs/{id}", admin.EvalRun(e.DB))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/run-evals/{id}/graph", admin.EvalRunEvalGraph(e.DB))
		mux.HandleFunc("GET /v1/gb/admin/api/evals/trajectories/{id}", admin.EvalTrajectory(e.DB, e.TrajectoryDir))
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
	return rel, nil
}

//---------------------------------------------------------------------------------------------

// Run opens the database, starts the server and blocks until ctx is
// cancelled or SIGINT/SIGTERM is received, then shuts down gracefully.
func Run(ctx context.Context, opts Options) error {
	fmt.Fprint(os.Stderr, banner)
	lib := buildinfo.Library()
	fields := log.Fields{"version": lib.Version, "commit": lib.Commit}
	if app, ok := buildinfo.App(); ok {
		fields["app"], fields["app_version"], fields["app_commit"] = app.Name, app.Version, app.Commit
	}
	log.WithFields(fields).Info("starting gligabao")
	opts.DB.Models = ai.SupportedModels(ai.NewPriceTable(opts.Prices)) // the models gligabao supports are stored on startup
	database, err := db.Open(opts.DB)
	if err != nil {
		return err
	}
	defer database.Close()
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
	if opts.Eval {
		log.Warn("eval mode: /v1/gb/eval/remember and /v1/gb/eval/recall are enabled")
	}
	if opts.Eval {
		log.WithField("trajectory_dir", memory_core.TrajectoryDir).Info("ATIF trajectories are stored here")
	}

	if opts.AdminUI && adminui.Available() {
		log.WithField("url", adminURL(opts.Addr)).Info("admin UI is active")
	} else if opts.AdminUI {
		log.Warn("admin UI is enabled but not active: web assets are not embedded in this binary")
	} else {
		log.Info("admin UI is not active (enable with admin_ui = true or --admin-ui)")
	}

	var llm memory_core.LLM
	// Web search is Anthropic's cloud-based web search tool, so it needs the Anthropic API key and only exists with it.
	var ws memory_core.WebSearcher
	if c, err := ai.New(opts.AnthropicKey, ai.WithPrices(ai.NewPriceTable(opts.Prices))); err != nil {
		log.WithError(err).Warn("LLM client unavailable; /v1/gb/remember and /v1/gb/recall will return 503")
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
	if opts.AdminUI && opts.EvalDBPath != "" {
		if database.Backend != config.DBBackendSQLite {
			log.Warn("eval database not shown in the admin UI: only supported with the sqlite backend")
		} else if evalDB, err := openAdminDB(opts.EvalDBPath); err != nil {
			log.WithError(err).WithField("path", opts.EvalDBPath).Warn("eval database not shown in the admin UI")
		} else {
			defer evalDB.Close()
			log.WithField("path", opts.EvalDBPath).Info("eval database is shown in the admin UI")
			// The eval runner (a separate process) keeps the eval database's vectors next to it, always in chromem with the
			// default dimensions (see evals/main.go), so open that directory afresh whenever it is needed.
			vecDir := db.DefaultVectorDir(opts.EvalDBPath)
			extra = append(extra, admin.Named{
				Name:          EvalDBName,
				DB:            evalDB,
				TrajectoryDir: filepath.Join(filepath.Dir(opts.EvalDBPath), "atif"),
				OpenVectors: func() (vector.Store, error) {
					return vector.NewChromem(vecDir, config.DefaultEmbeddingDimensions)
				},
			})
		}
	}

	srv := &http.Server{Addr: opts.Addr, Handler: NewHandler(database, llm, emb, jv, ws, opts.Eval, opts.AdminUI, extra...)}

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
		return srv.Shutdown(shutdownCtx)
	}
	return nil
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
