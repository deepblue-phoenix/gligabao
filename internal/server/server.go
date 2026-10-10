// Package server wires up and runs the HTTP server.
package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/api"
	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
)

const shutdownTimeout = 5 * time.Second

//---------------------------------------------------------------------------------------------

// Options configures the server.
type Options struct {
	// Addr is the listen address, e.g. "127.0.0.1:8080".
	Addr string
	// DBBackend is the main database type: config.DBBackendSQLite (default if empty) or config.DBBackendPostgres.
	DBBackend string
	// DBPath is the SQLite database file path (sqlite backend).
	DBPath string
	// PostgresDSN is the PostgreSQL connection string (postgres backend).
	PostgresDSN string
	// AnthropicKey is the Anthropic API key. If empty, LLM endpoints return 503.
	AnthropicKey string
	// VectorStore is the vector store type: config.VectorStoreChromem (default if empty) or config.VectorStorePinecone.
	VectorStore string
	// VectorDir is the chromem vector store directory (chromem store only). Default: next to DBPath.
	VectorDir string
	// EmbeddingDimensions is the embedding size (default config.DefaultEmbeddingDimensions).
	EmbeddingDimensions int
	// PineconeKey and PineconeIndexHost configure the Pinecone store.
	PineconeKey       string
	PineconeIndexHost string
	// Prices overrides/extends the built-in model price table used for call costs. May be nil.
	Prices map[string]ai.Price
}

//---------------------------------------------------------------------------------------------

// NewHandler builds the HTTP handler for the given database, LLM and vector store.
// llm may be nil, in which case endpoints requiring it return 503. vectors may be nil.
func NewHandler(database *sql.DB, llm memory.LLM, vectors vector.Store) http.Handler {
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
	api.Register(mux, database, llm, vectors)
	return mux
}

//---------------------------------------------------------------------------------------------

// NewVectorStore opens the vector store selected by opts.
func NewVectorStore(opts Options) (vector.Store, error) {
	dims := opts.EmbeddingDimensions
	if dims == 0 {
		dims = config.DefaultEmbeddingDimensions
	}
	switch opts.VectorStore {
	case "", config.VectorStoreChromem:
		dir := opts.VectorDir
		if dir == "" {
			dir = strings.TrimSuffix(opts.DBPath, filepath.Ext(opts.DBPath)) + ".vectors"
		}
		s, err := vector.NewChromem(dir, dims)
		if err != nil {
			return nil, err
		}
		log.Printf("vector store: chromem-go (%s, %d dims)", dir, dims)
		return s, nil
	case config.VectorStorePinecone:
		p, err := vector.NewPinecone(opts.PineconeKey, opts.PineconeIndexHost)
		if err != nil {
			return nil, err
		}
		log.Printf("vector store: Pinecone (%s)", opts.PineconeIndexHost)
		return p, nil
	default:
		return nil, fmt.Errorf("unknown vector store %q", opts.VectorStore)
	}
}

// Run opens the database, starts the server and blocks until ctx is
// cancelled or SIGINT/SIGTERM is received, then shuts down gracefully.
func Run(ctx context.Context, opts Options) error {
	dbTarget := opts.DBPath
	if opts.DBBackend == config.DBBackendPostgres {
		dbTarget = opts.PostgresDSN
	}
	database, err := relational.Open(opts.DBBackend, dbTarget)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := graph.Open(opts.DBBackend, database); err != nil {
		return err
	}

	var llm memory.LLM
	if c, err := ai.New(opts.AnthropicKey, ai.WithPrices(ai.NewPriceTable(opts.Prices))); err != nil {
		log.Printf("warning: %v; /v1/gb/remember and /v1/gb/recall will return 503", err)
	} else {
		llm = c
	}

	// One namespace per dataset (memory.VectorNamespace). Opening it here also reports a bad
	// configuration at startup.
	vectors, err := NewVectorStore(opts)
	if err != nil {
		return err
	}
	if c, ok := vectors.(io.Closer); ok {
		defer c.Close()
	}

	srv := &http.Server{Addr: opts.Addr, Handler: NewHandler(database, llm, vectors)}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if opts.DBBackend == config.DBBackendPostgres {
			log.Printf("listening on %s (db: postgres)", opts.Addr) // the DSN may hold a password
		} else {
			log.Printf("listening on %s (db: %s)", opts.Addr, opts.DBPath)
		}
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
