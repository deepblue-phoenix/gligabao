// Package server wires up and runs the HTTP server.
package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/ivan/gligabao/internal/ai"
	"github.com/ivan/gligabao/internal/api"
	"github.com/ivan/gligabao/internal/db"
	"github.com/ivan/gligabao/internal/memory"
)

const shutdownTimeout = 5 * time.Second

//---------------------------------------------------------------------------------------------

// Options configures the server.
type Options struct {
	// Addr is the listen address, e.g. "127.0.0.1:8080".
	Addr string
	// DBPath is the SQLite database file path.
	DBPath string
	// AnthropicKey is the Anthropic API key. If empty, LLM endpoints return 503.
	AnthropicKey string
	// Prices overrides/extends the built-in model price table used for call costs. May be nil.
	Prices map[string]ai.Price
}

//---------------------------------------------------------------------------------------------

// NewHandler builds the HTTP handler for the given database and LLM.
// llm may be nil, in which case endpoints requiring it return 503.
func NewHandler(database *sql.DB, llm memory.LLM) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "gligabao is running")
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := database.PingContext(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	api.Register(mux, database, llm)
	return mux
}

//---------------------------------------------------------------------------------------------

// Run opens the database, starts the server and blocks until ctx is
// cancelled or SIGINT/SIGTERM is received, then shuts down gracefully.
func Run(ctx context.Context, opts Options) error {
	database, err := db.Open(opts.DBPath)
	if err != nil {
		return err
	}
	defer database.Close()

	var llm memory.LLM
	if c, err := ai.New(opts.AnthropicKey, ai.WithPrices(ai.NewPriceTable(opts.Prices))); err != nil {
		log.Printf("warning: %v; /v1/gb/remember and /v1/gb/recall will return 503", err)
	} else {
		llm = c
	}

	srv := &http.Server{Addr: opts.Addr, Handler: NewHandler(database, llm)}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s (db: %s)", opts.Addr, opts.DBPath)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Println("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}
