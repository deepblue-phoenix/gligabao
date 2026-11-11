package main

import (
	"context"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_memory"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	log "github.com/sirupsen/logrus"
)

// app is what the server builds to serve remember and query, without the server: the databases, the remember manager and the
// knowledge base over them.
type app struct {
	database *db.DB
	mem      *agent_memory.Memory
	remember *memory_remember.Manager
	llm      *ai.Client
	stop     context.CancelFunc
	// rememberOpts and queryOpts are the options of every remember and query call (vectors, chunking, geo).
	rememberOpts, queryOpts []memory_core.Option
}

// openApp opens the databases described by cfg (PostgreSQL, and a remote vector store: Pinecone or S3 Vectors; see checkRemote) and builds
// the app over them. Call close when done.
func openApp(ctx context.Context, cfg *config.Config, llm *ai.Client, emb memory_core.Embedder) (*app, error) {
	if err := checkRemote(cfg); err != nil {
		return nil, err
	}
	dbCfg := db.ConfigFrom(cfg, "", "", "")
	dbCfg.Geo = cfg.Geo
	dbCfg.Models = ai.SupportedModels(ai.NewPriceTable(cfg.AIPrices()))
	database, err := db.Open(dbCfg)
	if err != nil {
		return nil, err
	}
	log.WithFields(log.Fields{"backend": database.Backend, "vector_store": database.VectorStore}).Info("databases opened")

	var geo memory_geo.Resolver
	if cfg.Geo {
		geo = memory_geo.Default()
	}
	// The same options as the remember and query endpoints of the server (internal/api), less what a command line cannot ask for.
	a := &app{database: database, llm: llm}
	a.rememberOpts = []memory_core.Option{memory.WithVectors(database.Vector, emb), memory_core.WithChunking(database.Chunking), memory_core.WithGeoResolver(geo)}
	a.queryOpts = []memory_core.Option{memory.WithVectors(database.Vector, emb), memory_core.WithGeoResolver(geo), memory_core.WithGeoRadius(cfg.GeoRadiusKm)}

	mgrCtx, stop := context.WithCancel(ctx)
	a.stop = stop
	a.remember = memory_remember.NewManager(mgrCtx, database.Relational, llm)
	a.mem = &agent_memory.Memory{Remember: a.remember, DB: database.Relational, LLM: llm, RememberOpts: a.rememberOpts, QueryOpts: a.queryOpts}
	return a, nil
}

// close stops the managers and closes the databases. The calls should be over.
func (a *app) close() {
	a.stop()
	a.remember.Wait()
	if err := a.database.Close(); err != nil {
		log.WithError(err).Warn("closing the databases")
	}
}

// checkRemote fails unless cfg uses PostgreSQL and a remote vector store (Pinecone or S3 Vectors). The worker keeps no state of its own: it
// works on the databases of the main gligabao instance, which see every write at once, so nothing has to be synced.
func checkRemote(cfg *config.Config) error {
	if cfg.DBBackend != config.DBBackendPostgres {
		return fmt.Errorf("db_backend %q is not supported: the worker needs %q (set db_backend and postgres_dsn, or %s)", cfg.DBBackend, config.DBBackendPostgres, config.EnvPostgresDSN)
	}
	switch cfg.VectorStore {
	case config.VectorStorePinecone, config.VectorStoreS3Vectors:
		return nil
	}
	return fmt.Errorf("vector_store %q is not supported: the worker needs %q or %q", cfg.VectorStore, config.VectorStorePinecone, config.VectorStoreS3Vectors)
}
