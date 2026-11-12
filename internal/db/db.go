// Package db bundles the clients of every database gligabao uses (relational, vector and graph) and the settings
// that describe them into one DB value. Open it once at startup and pass that single *DB around instead of the
// individual clients.
//
// The per-kind packages (internal/db/relational, internal/db/graph, internal/db/vector) hold the operations and the
// backends; this package only opens them, keeps them together and closes them.
//
// Open creates only the tables of the knowledge base (relational and graph). The tables of the other features that live in the
// relational database (training, evals, training audits, research agent runs) are created by the Schemas of Config: package dbfull
// opens a database with all of them (what the server uses), and a program that needs none (cmd/gligabao_agent) does not link them.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"

	log "github.com/sirupsen/logrus"
)

//---------------------------------------------------------------------------------------------

// Config selects and locates the databases to open. The zero value is a valid default: sqlite at DefaultSQLitePath
// with a chromem vector store next to it.
type Config struct {
	// Backend is the relational (and, for now, graph) database type: config.DBBackendSQLite (default if empty) or
	// config.DBBackendPostgres.
	Backend string
	// Geo creates the geo index of the geo entities (the R*Tree of SQLite, the PostGIS index of PostgreSQL) when the database is
	// opened: set it when geo is on (graph.EnableGeoIndex). Without it the index is not created, and nothing indexes the
	// places stored then; turning geo on later does not index the places stored before (SQLite).
	Geo bool
	// SQLitePath is the SQLite database file (sqlite backend). Default: DefaultSQLitePath.
	SQLitePath string
	// PostgresDSN is the PostgreSQL connection URL (postgres backend).
	PostgresDSN string

	// VectorStore is the vector database type: config.VectorStoreChromem (default if empty) or
	// config.VectorStorePinecone or config.VectorStoreS3Vectors.
	VectorStore string
	// VectorDir is the chromem directory (chromem store only). Default: next to SQLitePath.
	VectorDir string
	// ImageDir is the directory the image files of the image pipeline are kept in. Default: next to SQLitePath.
	ImageDir string
	// EmbeddingDimensions is the embedding size. Default: config.DefaultEmbeddingDimensions.
	EmbeddingDimensions int
	// PineconeKey and PineconeIndexHost configure the Pinecone store.
	PineconeKey       string
	PineconeIndexHost string
	// S3Vectors configures the S3 vector store (config.VectorStoreS3Vectors); its Dimensions default to EmbeddingDimensions.
	S3Vectors vector.S3VectorsConfig
	// Chunking is the chunk size and overlap remember uses for a long text. The zero value means chunk.DefaultConfig().
	Chunking memory_chunk.Config

	// Models are the models gligabao supports, inserted into gb_models on every Open (see relational.SyncModels).
	// Default (nil): ai.SupportedModels(nil), i.e. the built-in price table; pass the models of the configured price
	// table (ai.SupportedModels(ai.NewPriceTable(prices))) to include the models of its overrides.
	Models []ai.ModelInfo

	// Schemas create the tables of the features that are not part of the knowledge base (see Schema), in order, once the relational
	// and graph schemas exist. See package dbfull for the full set. Default (nil): none.
	Schemas []Schema
}

// Schema creates the tables of one feature in the relational database rel of the given backend (config.DBBackendSQLite or
// config.DBBackendPostgres). It must be safe to run on every Open (create if not exists).
type Schema func(backend string,
	rel *sql.DB) error

// DefaultSQLitePath is used when Config.SQLitePath is empty.
const DefaultSQLitePath = "build/gligabao.db"

// ConfigFrom builds a Config from the application config plus the settings that are not part of it
// (sqlitePath, vectorDir and imageDir may be empty for their defaults).
func ConfigFrom(c *config.Config,
	sqlitePath,
	vectorDir,
	imageDir string) Config {
	return Config{
		Backend:             c.DBBackend,
		SQLitePath:          sqlitePath,
		PostgresDSN:         c.PostgresDSN,
		VectorStore:         c.VectorStore,
		VectorDir:           vectorDir,
		ImageDir:            imageDir,
		EmbeddingDimensions: c.EmbeddingDimensions,
		PineconeKey:         c.PineconeKey,
		PineconeIndexHost:   c.PineconeIndexHost,
		S3Vectors: vector.S3VectorsConfig{
			Bucket:     c.S3VectorsBucket,
			Region:     c.S3VectorsRegion,
			Prefix:     c.S3VectorsPrefix,
			Dimensions: c.EmbeddingDimensions,
		},
		Chunking: c.Chunking(),
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
	// VectorStore is the vector store in use (config.VectorStoreChromem, config.VectorStorePinecone or config.VectorStoreS3Vectors).
	VectorStore string
	// EmbeddingDimensions is the size of the stored embeddings.
	EmbeddingDimensions int
	// SQLitePath is the sqlite database file ("" for postgres).
	SQLitePath string
	// VectorDir is the chromem directory ("" for pinecone and s3vectors).
	VectorDir string
	// ImageDir is the directory of the image files (created when the first image is stored). The relational
	// database only tracks their paths.
	ImageDir string
	// Chunking is the chunk size and overlap remember uses for a long text.
	Chunking memory_chunk.Config
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
	if cfg.Chunking == (memory_chunk.Config{}) {
		cfg.Chunking = memory_chunk.DefaultConfig()
	}

	if cfg.ImageDir == "" {
		cfg.ImageDir = DefaultImageDir(cfg.SQLitePath)
	}

	d := &DB{
		ImageDir:            cfg.ImageDir,
		Backend:             cfg.Backend,
		VectorStore:         cfg.VectorStore,
		EmbeddingDimensions: cfg.EmbeddingDimensions,
		Chunking:            cfg.Chunking,
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
	if cfg.Geo {
		ok, err := graph.EnableGeoIndex(context.Background(), rel)
		if err != nil {
			rel.Close()
			return nil, err
		}
		if !ok {
			log.WithField("backend", cfg.Backend).Warn("geo is on but the database has no geo index: the PostGIS extension is not enabled " +
				"(run CREATE EXTENSION postgis, then restart); the places are stored, but proximity queries cannot use an index")
		}
	}
	if err := gberrors.Init(cfg.Backend, rel); err != nil { // the GBerrors are always set up
		rel.Close()
		return nil, err
	}
	for _, schema := range cfg.Schemas {
		if err := schema(cfg.Backend, rel); err != nil {
			rel.Close()
			return nil, err
		}
	}

	models := cfg.Models
	if models == nil {
		models = ai.SupportedModels(nil)
	}
	rows := make([]relational.Model,
		0,
		len(models))
	for _, m := range models {
		rows = append(rows, relational.Model{
			Name:     m.Name,
			Provider: m.Provider,
			Kind:     m.Kind,
		})
	}
	if err := relational.SyncModels(context.Background(),
		rel,
		rows); err != nil {
		rel.Close()
		return nil, err
	}

	switch cfg.VectorStore {
	case config.VectorStoreChromem:
		dir := cfg.VectorDir
		if dir == "" {
			dir = DefaultVectorDir(cfg.SQLitePath)
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
	case config.VectorStoreS3Vectors:
		s3cfg := cfg.S3Vectors
		if s3cfg.Dimensions == 0 {
			s3cfg.Dimensions = cfg.EmbeddingDimensions
		}
		s, err := vector.NewS3Vectors(context.Background(), s3cfg)
		if err != nil {
			rel.Close()
			return nil, err
		}
		d.Vector = s
	default:
		rel.Close()
		return nil, fmt.Errorf("unknown vector store %q", cfg.VectorStore)
	}
	return d, nil
}

// DefaultVectorDir is the chromem directory used for the SQLite file sqlitePath when none is configured: the file's
// path without extension plus ".vectors".
func DefaultVectorDir(sqlitePath string) string {
	return strings.TrimSuffix(sqlitePath, filepath.Ext(sqlitePath)) + ".vectors"
}

// DefaultImageDir is the image directory used for the SQLite file sqlitePath when none is configured: the file's
// path without extension plus ".images".
func DefaultImageDir(sqlitePath string) string {
	return strings.TrimSuffix(sqlitePath, filepath.Ext(sqlitePath)) + ".images"
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
	return fmt.Sprintf("relational/graph: %s; vector: %s (%d dims); images: %s",
		rel,
		vec,
		d.EmbeddingDimensions,
		d.ImageDir)
}
