package gligabao

import (
	evalsroutes "github.com/deepblue-phoenix/gligabao/internal/api/evals"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runmanager"
	"github.com/deepblue-phoenix/gligabao/internal/sys/server"
	log "github.com/sirupsen/logrus"
)

// newEvalManager is the factory of the server's injected hook server.Options.EvalManager: it makes the run manager of the user evals
// from the server's own clients. The server cannot import package runmanager (see server.Options.EvalManager for the import cycle), so
// this is the one place that plugs it in. It returns nil (no evals) if the manager cannot be made.
func newEvalManager(env server.EvalEnv) evalsroutes.Manager {
	m, err := runmanager.New(env.Ctx,
		runmanager.Config{
			DB:            env.DB,
			Files:         env.Files,
			LLM:           env.LLM,
			Embedder:      env.Embedder,
			WebSearch:     env.WebSearch,
			Geo:           env.Geo,
			Prices:        env.Prices,
			Trajectories:  env.Trajectories,
			TrajectoryDir: env.TrajectoryDir,
		})
	if err != nil {
		log.WithError(err).Error("evals are not available: the run manager could not be made")
		return nil
	}
	return m
}
