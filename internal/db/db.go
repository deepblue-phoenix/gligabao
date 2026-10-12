// Package db bundles the clients of every database gligabao uses (relational, vector and graph) and the settings
// that describe them into one DB value. Open it once at startup and pass that single *DB around instead of the
// individual clients.
//
// The per-kind packages (internal/db/relational, internal/db/graph, internal/db/vector) hold the operations and the
// backends; this package only opens them, keeps them together and closes them.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
)

//---------------------------------------------------------------------------------------------

// Config selects and locates the databases to open. The zero value is a valid default: sqlite at DefaultSQLitePath
// with a chromem vector store next to it.
type Config struct {
	// Backend is the relational (and, for now, graph) database type: config.DBBackendSQLite (default if empty) or
	// config.DBBackendPostgres.
	Backend string
	// SQLitePath is the SQLite database file (sqlite backend). Default: DefaultSQLitePath.
	SQLitePath string
	// PostgresDSN is the PostgreSQL connection URL (postgres backend).
	PostgresDSN string

	// VectorStore is the vector database type: config.VectorStoreChromem (default if empty) or
	// config.VectorStorePinecone.
	VectorStore string
	// VectorDir is the chromem directory (chromem store only). Default: next to SQLitePath.
	VectorDir string
	// EmbeddingDimensions is the embedding size. Default: config.DefaultEmbeddingDimensions.
	EmbeddingDimensions int
	// PineconeKey and PineconeIndexHost configure the Pinecone store.
	PineconeKey       string
	PineconeIndexHost string
}

// DefaultSQLitePath is used when Config.SQLitePath is empty.
const DefaultSQLitePath = "bin/gligabao.db"

// ConfigFrom builds a Config from the application config plus the settings that are not part of it
// (sqlitePath and vectorDir may be empty for their defaults).
func ConfigFrom(c *config.Config, sqlitePath, vectorDir string) Config {
	return Config{
		Backend:             c.DBBackend,
		SQLitePath:          sqlitePath,
		PostgresDSN:         c.PostgresDSN,
		VectorStore:         c.VectorStore,
		VectorDir:           vectorDir,
		EmbeddingDimensions: c.EmbeddingDimensions,
		PineconeKey:         c.PineconeKey,
		PineconeIndexHost:   c.PineconeIndexHost,
	}
}

//---------------------------------------------------------------------------------------------

// DB holds the open clients of all databases and the settings shared across gligabao. Create it with Open and
// release it with Close. It is safe for concurrent use; do not copy it.
type DB struct {
	// Relational is the relational database (datasets, ontology metadata, LLM call log). Pass it to the
	// operations of package relational.
	Relational *sql.DB
	// Graph is the client the graph operations of package graph run on. The graph is currently stored in tables of
	// the relational database, so this is the same handle as Relational; a dedicated graph database will get its
	// own client here.
	Graph *sql.DB
	// Vector is the embedding store (isolated per dataset). Never nil on an opened DB.
	Vector vector.Store

	// Backend is the relational/graph backend in use (config.DBBackendSQLite or config.DBBackendPostgres).
	Backend string
	// VectorStore is the vector store in use (config.VectorStoreChromem or config.VectorStorePinecone).
	VectorStore string
	// EmbeddingDimensions is the size of the stored embeddings.
	EmbeddingDimensions int
	// SQLitePath is the sqlite database file ("" for postgres).
	SQLitePath string
	// VectorDir is the chromem directory ("" for pinecone).
	VectorDir string
}

// Open opens the relational, graph and vector databases described by cfg and applies their schemas.
// On error nothing stays open.
func Open(cfg Config) (*DB, error) {
	if cfg.Backend == "" {
		cfg.Backend = config.DBBackendSQLite
	}
	if cfg.VectorStore == "" {
		cfg.VectorStore = config.VectorStoreChromem
	}
	if cfg.EmbeddingDimensions == 0 {
		cfg.EmbeddingDimensions = config.DefaultEmbeddingDimensions
	}
	if cfg.SQLitePath == "" {
		cfg.SQLitePath = DefaultSQLitePath
	}

	d := &DB{
		Backend:             cfg.Backend,
		VectorStore:         cfg.VectorStore,
		EmbeddingDimensions: cfg.EmbeddingDimensions,
	}

	target := cfg.SQLitePath
	if cfg.Backend == config.DBBackendPostgres {
		target = cfg.PostgresDSN
	} else {
		d.SQLitePath = cfg.SQLitePath
	}
	rel, err := relational.Open(cfg.Backend, target)
	if err != nil {
		return nil, err
	}
	d.Relational = rel
	if err := graph.Open(cfg.Backend, rel); err != nil {
		rel.Close()
		return nil, err
	}
	d.Graph = rel

	switch cfg.VectorStore {
	case config.VectorStoreChromem:
		dir := cfg.VectorDir
		if dir == "" {
			dir = strings.TrimSuffix(cfg.SQLitePath, filepath.Ext(cfg.SQLitePath)) + ".vectors"
		}
		s, err := vector.NewChromem(dir, cfg.EmbeddingDimensions)
		if err != nil {
			rel.Close()
			return nil, err
		}
		d.Vector, d.VectorDir = s, dir
	case config.VectorStorePinecone:
		p, err := vector.NewPinecone(cfg.PineconeKey, cfg.PineconeIndexHost)
		if err != nil {
			rel.Close()
			return nil, err
		}
		d.Vector = p
	default:
		rel.Close()
		return nil, fmt.Errorf("unknown vector store %q", cfg.VectorStore)
	}
	return d, nil
}

// Close closes every client. It is safe to call on a nil DB.
func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	var errs []error
	if d.Relational != nil {
		errs = append(errs, d.Relational.Close())
	}
	if d.Graph != nil && d.Graph != d.Relational {
		errs = append(errs, d.Graph.Close())
	}
	if c, ok := d.Vector.(io.Closer); ok {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// Ping checks that the databases that can be checked are reachable.
func (d *DB) Ping(ctx context.Context) error {
	return d.Relational.PingContext(ctx)
}

// String describes the databases in use, without secrets (it is safe to log).
func (d *DB) String() string {
	rel := d.Backend
	if d.SQLitePath != "" {
		rel += " " + d.SQLitePath
	}
	vec := d.VectorStore
	if d.VectorDir != "" {
		vec += " " + d.VectorDir
	}
	return fmt.Sprintf("relational/graph: %s; vector: %s (%d dims)", rel, vec, d.EmbeddingDimensions)
}
