package readstest

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
)

// owned is everything one user has in the isolation suite.
type owned struct {
	user     string
	def      *core.EvalDef
	cdef     *core.CaseDef
	xdef     *core.ExperimentDef
	xrun     *core.ExperimentRun
	run      *core.EvalRun
	crun     *core.CaseRun
	trajID   string
	commID   int64
	standalo *core.EvalRun
}

func writeOwned(t *testing.T,
	d *sql.DB,
	s db.Store,
	user string,
	at time.Time) owned {
	t.Helper()
	ctx := context.Background()
	o := owned{
		user:   user,
		trajID: "traj-" + user,
	}
	// The same names for every user: they are unique per user.
	o.def = &core.EvalDef{
		UserID: user,
		Name:   "same",
		Type:   core.TypeKnowledge,
	}
	must(t, s.CreateEvalDef(ctx,
		d,
		o.def))
	o.cdef = &core.CaseDef{
		EvalDefID: o.def.ID,
		Type:      core.TypeKnowledge,
		Name:      "c0",
	}
	must(t, s.CreateCaseDef(ctx,
		d,
		o.cdef))
	must(t, s.CreateCaseDefMeta(ctx,
		d,
		&core.KnowledgeCaseDefMeta{
			CaseDefID: o.cdef.ID,
		}))
	o.xdef = &core.ExperimentDef{
		UserID: user,
		Name:   "same",
	}
	must(t, s.CreateExperimentDef(ctx,
		d,
		o.xdef))
	must(t, s.CreateExperimentDefEval(ctx,
		d,
		o.xdef.ID,
		o.def.ID,
		0))
	o.xrun = &core.ExperimentRun{
		UserID: user,
		DefID:  o.xdef.ID,
		Status: core.StatusCompleted,
		Timing: core.Timing{
			CreatedAt: at,
		},
	}
	must(t, s.CreateExperimentRun(ctx,
		d,
		o.xrun))
	o.run = &core.EvalRun{
		UserID:          user,
		DefID:           o.def.ID,
		Type:            core.TypeKnowledge,
		ExperimentRunID: &o.xrun.ID,
		Status:          core.StatusCompleted,
		CostUSD:         1.5,
		Timing: core.Timing{
			CreatedAt: at,
		},
	}
	must(t, s.CreateEvalRun(ctx,
		d,
		o.run))
	o.standalo = &core.EvalRun{
		UserID:   user,
		DefID:    o.def.ID,
		Type:     core.TypeKnowledge,
		Status:   core.StatusPending,
		RunnerID: "host:1:1",
		Timing: core.Timing{
			CreatedAt: at,
		},
	}
	must(t, s.CreateEvalRun(ctx,
		d,
		o.standalo))
	o.crun = &core.CaseRun{
		UserID:    user,
		DefID:     o.cdef.ID,
		EvalRunID: o.run.ID,
		Type:      core.TypeKnowledge,
		Status:    core.StatusCompleted,
		Timing: core.Timing{
			CreatedAt: at,
		},
	}
	must(t, s.CreateCaseRun(ctx,
		d,
		o.crun))
	must(t, s.CreateCaseRunMeta(ctx,
		d,
		&core.KnowledgeCaseRunMeta{
			CaseRunID: o.crun.ID,
			Trajectories: []core.KnowledgeTrajectory{{
				Seq:          0,
				Kind:         "remember",
				TrajectoryID: o.trajID,
				CreatedAt:    at,
			}},
		}))
	c := &core.CaseCommentary{
		CaseRunID: o.crun.ID,
		Model:     "m",
		Text:      "text",
	}
	must(t, s.CreateCaseCommentary(ctx,
		d,
		user,
		c))
	o.commID = c.ID
	return o
}

// RunIsolation is the isolation suite: for each read of the store, the data of user a is invisible to user b (db.ErrNotFound or an
// empty list), an empty owner is db.ErrInvalidOwner, a record of one owner under a parent of another is refused by the database, and
// the names are unique per user and not globally.
func RunIsolation(t *testing.T,
	d *sql.DB,
	s db.Store) {
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	a := writeOwned(t, d, s, "a", at)
	b := writeOwned(t, d, s, "b", at)
	if a.def.ID == b.def.ID {
		t.Fatal("two users, one eval def")
	}

	t.Run("own data is visible", func(t *testing.T) {
		got, err := s.GetEvalDefByName(ctx,
			d,
			"a",
			"same")
		must(t, err)
		if got.ID != a.def.ID || got.UserID != "a" || len(got.Cases) != 1 {
			t.Fatalf("eval def of a = %+v", got)
		}
		got, err = s.GetEvalDefByName(ctx,
			d,
			"b",
			"same")
		must(t, err)
		if got.ID != b.def.ID {
			t.Fatalf("eval def of b = %+v", got)
		}
		run, err := s.GetEvalRun(ctx,
			d,
			"a",
			a.run.ID)
		must(t, err)
		if run.UserID != "a" {
			t.Fatalf("eval run owner = %q", run.UserID)
		}
	})

	notFound := func(name string,
		err error) {
		t.Helper()
		if !errors.Is(err, db.ErrNotFound) {
			t.Errorf("%s of another user: err = %v, want ErrNotFound", name, err)
		}
	}
	t.Run("another user's data is invisible", func(t *testing.T) {
		_, err := s.GetEvalDefByName(ctx,
			d,
			"b",
			"missing")
		notFound("GetEvalDefByName", err)
		_, err = s.GetExperimentDefByName(ctx,
			d,
			"b",
			"missing")
		notFound("GetExperimentDefByName", err)
		_, err = s.GetExperimentDef(ctx,
			d,
			"b",
			a.xdef.ID)
		notFound("GetExperimentDef", err)
		_, err = s.GetEvalDef(ctx,
			d,
			"b",
			a.def.ID)
		notFound("GetEvalDef", err)
		_, err = s.GetExperimentRun(ctx,
			d,
			"b",
			a.xrun.ID)
		notFound("GetExperimentRun", err)
		_, err = s.GetEvalRun(ctx,
			d,
			"b",
			a.run.ID)
		notFound("GetEvalRun", err)
		_, err = s.GetCaseRun(ctx,
			d,
			"b",
			a.crun.ID)
		notFound("GetCaseRun", err)
		_, err = s.FindKnowledgeTrajectory(ctx,
			d,
			"b",
			a.trajID)
		notFound("FindKnowledgeTrajectory", err)
		_, err = s.FindKnowledgeTrajectory(ctx,
			d,
			"a",
			a.trajID)
		must(t, err)

		empties := map[string]int{}
		list := func(name string,
			n int,
			err error) {
			t.Helper()
			must(t, err)
			empties[name] = n
		}
		defs, err := s.ListExperimentDefs(ctx,
			d,
			"b")
		must(t, err)
		if len(defs) != 1 || defs[0].ID != b.xdef.ID || len(defs[0].EvalDefIDs) != 1 || defs[0].EvalDefIDs[0] != b.def.ID {
			t.Errorf("ListExperimentDefs of b = %+v", defs)
		}
		cdefs, err := s.ListCaseDefs(ctx,
			d,
			"b",
			a.def.ID)
		list("ListCaseDefs", len(cdefs), err)
		xruns, err := s.ListExperimentRuns(ctx,
			d,
			"b",
			a.xdef.ID)
		list("ListExperimentRuns", len(xruns), err)
		eruns, err := s.ListEvalRunsOfExperimentRun(ctx,
			d,
			"b",
			a.xrun.ID)
		list("ListEvalRunsOfExperimentRun", len(eruns), err)
		eruns, err = s.ListEvalRunsOfExperiment(ctx,
			d,
			"b",
			a.xdef.ID)
		list("ListEvalRunsOfExperiment", len(eruns), err)
		cruns, err := s.ListCaseRuns(ctx,
			d,
			"b",
			a.run.ID)
		list("ListCaseRuns", len(cruns), err)
		hist, err := s.ListCaseHistory(ctx,
			d,
			"b",
			a.xdef.ID,
			"same",
			"c0")
		list("ListCaseHistory", len(hist), err)
		comms, err := s.ListCaseCommentaries(ctx,
			d,
			"b",
			a.crun.ID)
		list("ListCaseCommentaries", len(comms), err)
		refs, err := s.ListExperimentCases(ctx,
			d,
			"b",
			a.xdef.ID)
		list("ListExperimentCases", len(refs), err)
		odefs, err := s.ListEvalRunsOfDef(ctx,
			d,
			"b",
			a.def.ID,
			0)
		list("ListEvalRunsOfDef", len(odefs), err)
		hist, err = s.ListCaseHistoryOfEval(ctx,
			d,
			"b",
			a.def.ID,
			"c0")
		list("ListCaseHistoryOfEval", len(hist), err)
		for name, n := range empties {
			if n != 0 {
				t.Errorf("%s of another user returned %d records", name, n)
			}
		}
		// And the same reads for the owner are not empty.
		own, err := s.ListCaseRuns(ctx,
			d,
			"a",
			a.run.ID)
		must(t, err)
		if len(own) != 1 {
			t.Errorf("ListCaseRuns of a = %d", len(own))
		}
		own2, err := s.ListCaseHistoryOfEval(ctx,
			d,
			"a",
			a.def.ID,
			"c0")
		must(t, err)
		if len(own2) != 1 {
			t.Errorf("ListCaseHistoryOfEval of a = %d", len(own2))
		}
		cs, err := s.ListCaseCommentaries(ctx,
			d,
			"a",
			a.crun.ID)
		must(t, err)
		if len(cs) != 1 {
			t.Errorf("ListCaseCommentaries of a = %d", len(cs))
		}
		if err := s.CreateCaseCommentary(ctx,
			d,
			"b",
			&core.CaseCommentary{
				CaseRunID: a.crun.ID,
				Model:     "m",
				Text:      "x",
			}); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("CreateCaseCommentary on a case run of another user: err = %v", err)
		}
	})

	t.Run("user lists", func(t *testing.T) {
		defs, err := s.ListEvalDefs(ctx,
			d,
			"a")
		must(t, err)
		if len(defs) != 1 || defs[0].ID != a.def.ID {
			t.Errorf("ListEvalDefs of a = %+v", defs)
		}
		defs, err = s.ListEvalDefs(ctx,
			d,
			"nobody")
		must(t, err)
		if len(defs) != 0 {
			t.Errorf("ListEvalDefs of nobody = %+v", defs)
		}
		runs, err := s.ListEvalRunsOfDef(ctx,
			d,
			"a",
			a.def.ID,
			0)
		must(t, err)
		if len(runs) != 2 || runs[0].ID != a.standalo.ID || runs[1].ID != a.run.ID {
			t.Errorf("ListEvalRunsOfDef of a = %+v", runs)
		}
		runs, err = s.ListEvalRunsOfDef(ctx,
			d,
			"a",
			a.def.ID,
			1)
		must(t, err)
		if len(runs) != 1 {
			t.Errorf("ListEvalRunsOfDef limit 1 = %d", len(runs))
		}
		n, err := s.CountActiveEvalRuns(ctx,
			d,
			"a")
		must(t, err)
		if n != 1 {
			t.Errorf("CountActiveEvalRuns = %d, want 1", n)
		}
		cost, err := s.SumCostSince(ctx,
			d,
			"a",
			at.Add(-time.Minute))
		must(t, err)
		if cost != 1.5 {
			t.Errorf("SumCostSince = %v, want 1.5", cost)
		}
		cost, err = s.SumCostSince(ctx,
			d,
			"a",
			at.Add(time.Minute))
		must(t, err)
		if cost != 0 {
			t.Errorf("SumCostSince after the runs = %v, want 0", cost)
		}
	})

	t.Run("empty owner is refused", func(t *testing.T) {
		check := func(name string,
			err error) {
			t.Helper()
			if !errors.Is(err, db.ErrInvalidOwner) {
				t.Errorf("%s with an empty owner: err = %v, want ErrInvalidOwner", name, err)
			}
		}
		_, err := s.GetEvalDefByName(ctx, d, "", "same")
		check("GetEvalDefByName", err)
		_, err = s.GetExperimentDefByName(ctx, d, "", "same")
		check("GetExperimentDefByName", err)
		_, err = s.ListExperimentDefs(ctx, d, "")
		check("ListExperimentDefs", err)
		_, err = s.GetExperimentDef(ctx, d, "", a.xdef.ID)
		check("GetExperimentDef", err)
		_, err = s.GetEvalDef(ctx, d, "", a.def.ID)
		check("GetEvalDef", err)
		_, err = s.ListCaseDefs(ctx, d, "", a.def.ID)
		check("ListCaseDefs", err)
		_, err = s.ListExperimentCases(ctx, d, "", a.xdef.ID)
		check("ListExperimentCases", err)
		_, err = s.ListExperimentRuns(ctx, d, "", a.xdef.ID)
		check("ListExperimentRuns", err)
		_, err = s.GetExperimentRun(ctx, d, "", a.xrun.ID)
		check("GetExperimentRun", err)
		_, err = s.GetEvalRun(ctx, d, "", a.run.ID)
		check("GetEvalRun", err)
		_, err = s.ListEvalRunsOfExperimentRun(ctx, d, "", a.xrun.ID)
		check("ListEvalRunsOfExperimentRun", err)
		_, err = s.ListEvalRunsOfExperiment(ctx, d, "", a.xdef.ID)
		check("ListEvalRunsOfExperiment", err)
		_, err = s.ListCaseRuns(ctx, d, "", a.run.ID)
		check("ListCaseRuns", err)
		_, err = s.GetCaseRun(ctx, d, "", a.crun.ID)
		check("GetCaseRun", err)
		_, err = s.FindKnowledgeTrajectory(ctx, d, "", a.trajID)
		check("FindKnowledgeTrajectory", err)
		_, err = s.ListCaseHistory(ctx, d, "", a.xdef.ID, "same", "c0")
		check("ListCaseHistory", err)
		_, err = s.ListCaseCommentaries(ctx, d, "", a.crun.ID)
		check("ListCaseCommentaries", err)
		check("CreateCaseCommentary", s.CreateCaseCommentary(ctx, d, "", &core.CaseCommentary{CaseRunID: a.crun.ID}))
		_, err = s.ListEvalDefs(ctx, d, "")
		check("ListEvalDefs", err)
		_, err = s.ListEvalRunsOfDef(ctx, d, "", a.def.ID, 0)
		check("ListEvalRunsOfDef", err)
		_, err = s.CountActiveEvalRuns(ctx, d, "")
		check("CountActiveEvalRuns", err)
		_, err = s.SumCostSince(ctx, d, "", at)
		check("SumCostSince", err)
		_, err = s.ListCaseHistoryOfEval(ctx, d, "", a.def.ID, "c0")
		check("ListCaseHistoryOfEval", err)
		_, err = s.DeleteEvalDef(ctx, d, "", a.def.ID)
		check("DeleteEvalDef", err)
		check("RequestCancel", s.RequestCancel(ctx, d, "", a.standalo.ID))
		// The creates.
		check("CreateEvalDef", s.CreateEvalDef(ctx, d, &core.EvalDef{Name: "n", Type: core.TypeKnowledge}))
		check("CreateExperimentDef", s.CreateExperimentDef(ctx, d, &core.ExperimentDef{Name: "n"}))
		check("CreateExperimentRun", s.CreateExperimentRun(ctx, d, &core.ExperimentRun{DefID: a.xdef.ID}))
		check("CreateEvalRun", s.CreateEvalRun(ctx, d, &core.EvalRun{DefID: a.def.ID, Type: core.TypeKnowledge}))
		check("CreateCaseRun", s.CreateCaseRun(ctx, d, &core.CaseRun{DefID: a.cdef.ID, EvalRunID: a.run.ID, Type: core.TypeKnowledge}))
	})

	t.Run("a cross-owner insert is refused by the composite keys", func(t *testing.T) {
		refused := func(name string,
			err error) {
			t.Helper()
			if err == nil {
				t.Errorf("%s: the database accepted a record of one owner under a parent of another", name)
			}
		}
		refused("eval run of b under the eval def of a", s.CreateEvalRun(ctx,
			d,
			&core.EvalRun{
				UserID: "b",
				DefID:  a.def.ID,
				Type:   core.TypeKnowledge,
			}))
		refused("experiment run of b under the experiment def of a", s.CreateExperimentRun(ctx,
			d,
			&core.ExperimentRun{
				UserID: "b",
				DefID:  a.xdef.ID,
			}))
		refused("eval run of b in the experiment run of a", s.CreateEvalRun(ctx,
			d,
			&core.EvalRun{
				UserID:          "b",
				DefID:           b.def.ID,
				Type:            core.TypeKnowledge,
				ExperimentRunID: &a.xrun.ID,
			}))
		cdef2 := &core.CaseDef{
			EvalDefID: b.def.ID,
			Type:      core.TypeKnowledge,
			Name:      "c1",
			Position:  1,
		}
		must(t, s.CreateCaseDef(ctx,
			d,
			cdef2))
		refused("case run of b in the eval run of a", s.CreateCaseRun(ctx,
			d,
			&core.CaseRun{
				UserID:    "b",
				DefID:     cdef2.ID,
				EvalRunID: a.run.ID,
				Type:      core.TypeKnowledge,
			}))
	})

	t.Run("names are unique per user", func(t *testing.T) {
		if err := s.CreateEvalDef(ctx,
			d,
			&core.EvalDef{
				UserID: "a",
				Name:   "same",
				Type:   core.TypeKnowledge,
			}); err == nil {
			t.Error("two eval defs of one user with one name")
		}
		if err := s.CreateExperimentDef(ctx,
			d,
			&core.ExperimentDef{
				UserID: "a",
				Name:   "same",
			}); err == nil {
			t.Error("two experiment defs of one user with one name")
		}
	})

	t.Run("bookkeeping", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		// a.standalo is pending, created an hour ago, has a runner and no beat: stale. a's other run and b's runs are final or fresh.
		must(t, s.ClaimRun(ctx,
			d,
			b.standalo.ID,
			"host:2:2",
			now))
		if err := s.ClaimRun(ctx,
			d,
			b.standalo.ID,
			"host:2:2",
			now); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("claiming a run twice: err = %v", err)
		}
		got, err := s.GetEvalRun(ctx,
			d,
			"b",
			b.standalo.ID)
		must(t, err)
		if got.Status != core.StatusRunning || got.RunnerID != "host:2:2" || got.HeartbeatAt == nil || got.StartedAt == nil {
			t.Errorf("claimed run = %+v", got)
		}
		cancel, err := s.Heartbeat(ctx,
			d,
			b.standalo.ID,
			now)
		must(t, err)
		if cancel {
			t.Error("cancel requested before the request")
		}
		// Another user cannot cancel it; the owner can.
		notFound("RequestCancel", s.RequestCancel(ctx,
			d,
			"a",
			b.standalo.ID))
		must(t, s.RequestCancel(ctx,
			d,
			"b",
			b.standalo.ID))
		cancel, err = s.Heartbeat(ctx,
			d,
			b.standalo.ID,
			now)
		must(t, err)
		if !cancel {
			t.Error("cancel not seen at the beat")
		}
		got, err = s.GetEvalRun(ctx,
			d,
			"b",
			b.standalo.ID)
		must(t, err)
		if !got.CancelRequested {
			t.Error("CancelRequested not set")
		}
		if err := s.RequestCancel(ctx,
			d,
			"b",
			b.run.ID); !errors.Is(err, db.ErrRunFinished) {
			t.Errorf("cancelling a finished run: err = %v, want ErrRunFinished", err)
		}
		if _, err := s.Heartbeat(ctx,
			d,
			b.run.ID,
			now); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("beat of a finished run: err = %v", err)
		}

		// Stale: only a.standalo (b's has a fresh beat).
		n, err := s.FailStale(ctx,
			d,
			core.TypeKnowledge,
			now.Add(-time.Minute),
			"crashed",
			now)
		must(t, err)
		if n != 1 {
			t.Fatalf("FailStale ended %d runs, want 1", n)
		}
		got, err = s.GetEvalRun(ctx,
			d,
			"a",
			a.standalo.ID)
		must(t, err)
		if got.Status != core.StatusErrored || got.Error != "crashed" || got.FinishedAt == nil {
			t.Errorf("stale run = %+v", got)
		}
		got, err = s.GetEvalRun(ctx,
			d,
			"b",
			b.standalo.ID)
		must(t, err)
		if got.Status != core.StatusRunning {
			t.Errorf("a run with a fresh beat was failed: %+v", got)
		}
		// A CLI run (no runner) is never stale.
		cli := &core.EvalRun{
			UserID: "a",
			DefID:  a.def.ID,
			Type:   core.TypeKnowledge,
			Status: core.StatusRunning,
			Timing: core.Timing{
				CreatedAt: at,
			},
		}
		must(t, s.CreateEvalRun(ctx,
			d,
			cli))
		n, err = s.FailStale(ctx,
			d,
			core.TypeKnowledge,
			now.Add(time.Minute),
			"crashed",
			now)
		must(t, err)
		if n != 1 { // b's run, whose beat is older than the cutoff now
			t.Errorf("second FailStale ended %d runs, want 1 (b's)", n)
		}
		got, err = s.GetEvalRun(ctx,
			d,
			"a",
			cli.ID)
		must(t, err)
		if got.Status != core.StatusRunning {
			t.Errorf("a CLI run was failed as stale: %+v", got)
		}
	})

	t.Run("admin reads span users", func(t *testing.T) {
		defs, err := s.AdminListEvalDefs(ctx,
			d,
			db.AdminFilter{})
		must(t, err)
		if len(defs) != 2 || defs[0].UserID != "a" || defs[1].UserID != "b" {
			t.Errorf("AdminListEvalDefs = %+v", defs)
		}
		defs, err = s.AdminListEvalDefs(ctx,
			d,
			db.AdminFilter{
				UserID: "b",
			})
		must(t, err)
		if len(defs) != 1 || defs[0].UserID != "b" {
			t.Errorf("AdminListEvalDefs of b = %+v", defs)
		}
		runs, err := s.AdminListEvalRuns(ctx,
			d,
			db.AdminFilter{},
			0)
		must(t, err)
		if len(runs) != 5 {
			t.Errorf("AdminListEvalRuns = %d runs, want 5", len(runs))
		}
		runs, err = s.AdminListEvalRuns(ctx,
			d,
			db.AdminFilter{
				UserID: "a",
			},
			1)
		must(t, err)
		if len(runs) != 1 || runs[0].UserID != "a" {
			t.Errorf("AdminListEvalRuns of a, limit 1 = %+v", runs)
		}
	})

	t.Run("delete eval def", func(t *testing.T) {
		if _, err := s.DeleteEvalDef(ctx,
			d,
			"b",
			a.def.ID); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("deleting the eval of another user: err = %v", err)
		}
		// It is in an experiment: the database refuses, and nothing is lost.
		if _, err := s.DeleteEvalDef(ctx,
			d,
			"a",
			a.def.ID); err == nil {
			t.Error("deleted an eval that an experiment includes")
		}
		if _, err := s.GetEvalRun(ctx,
			d,
			"a",
			a.run.ID); err != nil {
			t.Errorf("the failed delete lost a run: %v", err)
		}
		// A def that no experiment includes goes with its runs.
		lone := &core.EvalDef{
			UserID: "a",
			Name:   "lone",
			Type:   core.TypeKnowledge,
		}
		must(t, s.CreateEvalDef(ctx,
			d,
			lone))
		lc := &core.CaseDef{
			EvalDefID: lone.ID,
			Type:      core.TypeKnowledge,
			Name:      "c",
		}
		must(t, s.CreateCaseDef(ctx,
			d,
			lc))
		must(t, s.CreateCaseDefMeta(ctx,
			d,
			&core.KnowledgeCaseDefMeta{
				CaseDefID: lc.ID,
			}))
		lr := &core.EvalRun{
			UserID: "a",
			DefID:  lone.ID,
			Type:   core.TypeKnowledge,
		}
		must(t, s.CreateEvalRun(ctx,
			d,
			lr))
		lcr := &core.CaseRun{
			UserID:    "a",
			DefID:     lc.ID,
			EvalRunID: lr.ID,
			Type:      core.TypeKnowledge,
		}
		must(t, s.CreateCaseRun(ctx,
			d,
			lcr))
		ids, err := s.DeleteEvalDef(ctx,
			d,
			"a",
			lone.ID)
		must(t, err)
		if len(ids) != 1 || ids[0] != lr.ID {
			t.Errorf("deleted run ids = %v, want [%d]", ids, lr.ID)
		}
		if _, err := s.GetEvalDef(ctx,
			d,
			"a",
			lone.ID); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("the def is still there: %v", err)
		}
		if _, err := s.GetCaseRun(ctx,
			d,
			"a",
			lcr.ID); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("the case run is still there: %v", err)
		}
	})
}
