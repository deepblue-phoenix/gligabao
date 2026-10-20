package admin_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/evals"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// storeOf is the evals Store of the backend d is open on (PostgreSQL if its driver is pgx's).
func storeOf(t *testing.T, d *sql.DB) evalsdb.Store {
	t.Helper()
	backend := config.DBBackendSQLite
	if strings.Contains(fmt.Sprintf("%T", d.Driver()), "stdlib") {
		backend = config.DBBackendPostgres
	}
	s, err := evals.StoreFor(backend)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// caseExecutor runs the one case of an eval: it records it as it is told.
type caseExecutor struct {
	run func(ctx context.Context, rec *runner.EvalRecorder, def *core.EvalDef) error
}

func (c caseExecutor) Execute(ctx context.Context, rec *runner.EvalRecorder, def *core.EvalDef) error {
	return c.run(ctx, rec, def)
}

// recorded is what recordCase made.
type recorded struct {
	ExperimentID core.ExperimentDefID
	RunID        core.ExperimentRunID
	CaseRunID    core.CaseRunID
}

// recordCase records, as the knowledge runner does, a run of the experiment with one eval of one case that passed, with the ATIF
// trajectories trajs (stored in dir) as its calls and the dataset datasetID (0: none) as its graph.
func recordCase(t *testing.T, d *sql.DB, experiment, eval, name string, datasetID int64, dir string, trajs ...*atif.Trajectory) recorded {
	t.Helper()
	ctx := context.Background()
	r := runner.New(d, storeOf(t, d))
	def, err := r.SyncEval(ctx, runner.EvalSpec{Name: eval, Type: core.TypeKnowledge, Cases: []runner.CaseSpec{{Name: name, Meta: &core.KnowledgeCaseDefMeta{}}}})
	if err != nil {
		t.Fatal(err)
	}
	xd, err := r.EnsureExperiment(ctx, experiment, "", []core.EvalDefID{def.ID})
	if err != nil {
		t.Fatal(err)
	}
	xr, err := r.StartExperimentRun(ctx, xd, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out recorded
	out.ExperimentID, out.RunID = xd.ID, xr.Run().ID
	ex := caseExecutor{func(ctx context.Context, rec *runner.EvalRecorder, def *core.EvalDef) error {
		cr, err := rec.StartCase(ctx, def.Cases[0])
		if err != nil {
			return err
		}
		meta := &core.KnowledgeCaseRunMeta{PassedChecks: []string{"it works"}}
		if datasetID != 0 {
			meta.DatasetID = &datasetID
		}
		for i, tr := range trajs {
			meta.Steps += len(tr.Steps)
			meta.Trajectories = append(meta.Trajectories, core.KnowledgeTrajectory{Seq: i, Kind: strings.TrimPrefix(tr.Agent.Name, "gligabao-"),
				TrajectoryID: tr.TrajectoryID, AgentName: tr.Agent.Name, Steps: len(tr.Steps), Path: filepath.Join(dir, tr.TrajectoryID+".json")})
		}
		cr.Status, cr.Verdict = core.StatusCompleted, core.VerdictPass
		out.CaseRunID = cr.ID
		return rec.FinishCase(ctx, cr, meta)
	}}
	run, err := r.RunEval(ctx, def, ex, runner.EvalOptions{ExperimentRunID: &out.RunID, Meta: &core.KnowledgeEvalRunMeta{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := xr.Add(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := xr.Finish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	return out
}
