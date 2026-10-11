// Package server wires up and runs the HTTP server.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	adminui "github.com/deepblue-phoenix/gligabao/admin_ui"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/api"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	log "github.com/sirupsen/logrus"
)

const shutdownTimeout = 5 * time.Second

//---------------------------------------------------------------------------------------------

// Options configures the server.
type Options struct {
	// Addr is the listen address, e.g. "127.0.0.1:8080".
	Addr string
	// DB selects and locates the databases (relational, graph, vector).
	DB db.Config
	// AnthropicKey is the Anthropic API key. If empty, LLM endpoints return 503.
	AnthropicKey string
	// Prices overrides/extends the built-in model price table used for call costs. May be nil.
	Prices map[string]ai.Price
	// TrajectoryDir is where the ATIF trajectories of the eval endpoints are stored as JSON.
	// Default: an "atif" directory next to the SQLite file (memory.TrajectoryDir if there is none).
	TrajectoryDir string
	// Eval registers the eval endpoints (/v1/gb/eval/remember and /v1/gb/eval/recall), which return ATIF
	// trajectories with their results. Off by default.
	Eval bool
	// AdminUI registers the admin UI handlers (served at /admin/ from the assets embedded in the binary).
	AdminUI bool
}

//---------------------------------------------------------------------------------------------

// NewHandler builds the HTTP handler for the given databases and LLM.
// llm may be nil, in which case endpoints requiring it return 503. The eval endpoints are registered only if eval is true,
// the admin UI (/admin/) only if adminUI is true and its assets were built into the binary.
func NewHandler(d *db.DB, llm memory.LLM, eval, adminUI bool) http.Handler {
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
	api.Register(mux, d, llm)
	if eval {
		api.RegisterEval(mux, d, llm)
	}
	if adminUI {
		registerAdminUI(mux)
	}
	return mux
}

// registerAdminUI serves the embedded admin UI assets under /admin/. It is a no-op (with a warning) if the
// assets were not built into the binary (see admin_ui/ and run_build.sh).
func registerAdminUI(mux *http.ServeMux) {
	if !adminui.Available() {
		log.Warn("admin_ui is enabled but the web assets are not embedded in this binary; build with ./run_build.sh (admin UI build enabled)")
		return
	}
	mux.Handle("GET /admin/", http.StripPrefix("/admin/", http.FileServerFS(adminui.Assets())))
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
}

//---------------------------------------------------------------------------------------------

// Run opens the database, starts the server and blocks until ctx is
// cancelled or SIGINT/SIGTERM is received, then shuts down gracefully.
func Run(ctx context.Context, opts Options) error {
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
		"embedding_dimensions": database.EmbeddingDimensions,
	}).Info("databases opened")

	switch {
	case opts.TrajectoryDir != "":
		memory.TrajectoryDir = opts.TrajectoryDir
	case database.SQLitePath != "":
		memory.TrajectoryDir = filepath.Join(filepath.Dir(database.SQLitePath), "atif")
	}
	if opts.Eval {
		log.Warn("eval mode: /v1/gb/eval/remember and /v1/gb/eval/recall are enabled")
	}
	if opts.Eval {
		log.WithField("trajectory_dir", memory.TrajectoryDir).Info("ATIF trajectories are stored here")
	}

	if opts.AdminUI {
		log.WithField("path", "/admin/").Info("admin UI enabled")
	}

	var llm memory.LLM
	if c, err := ai.New(opts.AnthropicKey, ai.WithPrices(ai.NewPriceTable(opts.Prices))); err != nil {
		log.WithError(err).Warn("LLM client unavailable; /v1/gb/remember and /v1/gb/recall will return 503")
	} else {
		llm = c
	}

	srv := &http.Server{Addr: opts.Addr, Handler: NewHandler(database, llm, opts.Eval, opts.AdminUI)}

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
