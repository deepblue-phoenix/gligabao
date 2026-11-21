package gligabao

import (
	evalsroutes "github.com/deepblue-phoenix/gligabao/internal/api/evals"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runmanager"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
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
			FileLimits: fileLimits(env.Prices,
				env.Settings),
			Limits: runLimits(env.Settings),
		})
	if err != nil {
		log.WithError(err).Error("evals are not available: the run manager could not be made")
		return nil
	}
	return m
}

// fileLimits are the limits of an eval file: the run manager's defaults, with the evals_max_* settings that are set. The file size
// limit is always set, so the run manager keeps these limits (it only fills them in when it has none).
func fileLimits(prices *llm.PriceTable,
	s server.EvalSettings) evalfile.Limits {
	lim := runmanager.DefaultFileLimits(prices)
	lim.MaxCases = orInt(s.MaxCases, lim.MaxCases)
	lim.MaxFileBytes = orInt(s.MaxFileBytes, lim.MaxFileBytes)
	lim.MaxTextBytes = orInt(s.MaxTextBytes, lim.MaxTextBytes)
	return lim
}

// runLimits are the limits of the runs; a zero field takes the run manager's default.
func runLimits(s server.EvalSettings) runmanager.Limits {
	return runmanager.Limits{
		MaxRunsPerUser:       s.MaxRunsPerUser,
		MaxRuns:              s.MaxRuns,
		MaxRunCostUSD:        s.MaxRunCostUSD,
		MaxUserCostUSDPerDay: s.MaxUserCostUSDPerDay,
		DatasetRetention:     s.DatasetRetention,
		HeartbeatInterval:    s.HeartbeatInterval,
		HeartbeatStale:       s.HeartbeatStale,
	}
}

// orInt is v if it is set (positive), else def.
func orInt(v,
	def int) int {
	if v > 0 {
		return v
	}
	return def
}
