package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_memory"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/dbexport"
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

// openApp opens the databases described by cfg (the SQLite file at sqlitePath, or its default; the chromem vector store next to it, or at
// vectorDir) and builds the app over them. Call close when done.
func openApp(ctx context.Context, cfg *config.Config, llm *ai.Client, emb memory_core.Embedder, sqlitePath, vectorDir string) (*app, error) {
	dbCfg := db.ConfigFrom(cfg, sqlitePath, vectorDir, "")
	dbCfg.Geo = cfg.Geo
	dbCfg.Models = ai.SupportedModels(ai.NewPriceTable(cfg.AIPrices()))
	if dbCfg.SQLitePath != "" {
		// A fresh Lambda has an empty /tmp: the directory of the file may not exist yet.
		if err := os.MkdirAll(filepath.Dir(dbCfg.SQLitePath), 0o755); err != nil {
			return nil, err
		}
	}
	database, err := db.Open(dbCfg)
	if err != nil {
		return nil, err
	}
	log.WithFields(log.Fields{"backend": database.Backend, "sqlite_path": database.SQLitePath, "vector_store": database.VectorStore, "vector_dir": database.VectorDir}).Info("databases opened")

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

// close stops the managers and closes the databases (for SQLite, the file is then complete and can be copied). The calls should be over.
func (a *app) close() {
	a.stop()
	a.remember.Wait()
	if err := a.database.Close(); err != nil {
		log.WithError(err).Warn("closing the databases")
	}
}

// fullExport writes the whole local state (SQLite tables, chromem embeddings, images) to one archive at path (see package dbexport).
func (a *app) fullExport(ctx context.Context, path string) (*dbexport.Manifest, error) {
	if err := a.mem.Drain(ctx); err != nil {
		log.WithError(err).Warn("export: some text that was queued for the knowledge base was not absorbed")
	}
	return dbexport.WriteFile(context.WithoutCancel(ctx), path, dbexport.Source{
		Relational:          a.database.Relational,
		Backend:             a.database.Backend,
		Vector:              a.database.Vector,
		VectorStore:         a.database.VectorStore,
		EmbeddingDimensions: a.database.EmbeddingDimensions,
		ImageDir:            a.database.ImageDir,
	}, dbexport.Options{Source: "gligabao_pipelines", Version: version})
}
