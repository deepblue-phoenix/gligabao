package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db/sqlite"
)

func TestSynth(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	s := sqlite.Store
	ev := &core.EvalDef{UserID: "u1", Name: "e", Type: core.TypeKnowledge}
	if err := s.CreateEvalDef(ctx, d, ev); err != nil {
		t.Fatal(err)
	}
	sd := &core.SynthDef{
		EvalDefID: ev.ID,
		SynthSettings: core.SynthSettings{
			Prompt: "p", Model: "m", NumExamples: 3, Mode: core.SynthBulk, BulkSize: 2,
		},
	}
	if err := s.CreateSynthDef(ctx, d, "u1", sd); err != nil || sd.Version != 1 {
		t.Fatal(err, sd)
	}
	// another owner sees nothing
	if _, err := s.GetSynthDef(ctx, d, "u2", ev.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("other owner: %v", err)
	}
	// the same settings are no new version, a change is
	same, err := s.UpdateSynthDef(ctx, d, "u1", ev.ID, sd.SynthSettings)
	if err != nil || same.Version != 1 {
		t.Fatal(err, same)
	}
	next := sd.SynthSettings
	next.Prompt = "q"
	upd, err := s.UpdateSynthDef(ctx, d, "u1", ev.ID, next)
	if err != nil || upd.Version != 2 || upd.Prompt != "q" {
		t.Fatal(err, upd)
	}
	vs, err := s.ListSynthVersions(ctx, d, "u1", ev.ID)
	if err != nil || len(vs) != 2 || vs[0].Version != 2 || vs[1].Prompt != "p" {
		t.Fatal(err, vs)
	}
	// one active generation at a time
	g := &core.SynthGeneration{EvalDefID: ev.ID, Version: 2, Model: "m", Mode: core.SynthBulk, Requested: 3}
	if err := s.CreateSynthGeneration(ctx, d, "u1", g); err != nil || g.ID == 0 || g.Status != core.SynthPending {
		t.Fatal(err, g)
	}
	other := &core.SynthGeneration{EvalDefID: ev.ID, Version: 2, Model: "m", Mode: core.SynthBulk}
	if err := s.CreateSynthGeneration(ctx, d, "u1", other); !errors.Is(err, core.ErrSynthConflict) {
		t.Errorf("second active generation: %v", err)
	}
	// the stale one ends errored
	n, err := s.FailStaleSynthGenerations(ctx, d, time.Now().Add(time.Minute), "lost", time.Now())
	if err != nil || n != 1 {
		t.Fatal(err, n)
	}
	got, err := s.GetSynthGeneration(ctx, d, "u1", g.ID)
	if err != nil || got.Status != core.SynthErrored || got.Error != "lost" || got.FinishedAt == nil {
		t.Fatal(err, got)
	}
	if err := s.CreateSynthGeneration(ctx, d, "u1", other); err != nil {
		t.Errorf("after the first ended: %v", err)
	}
	// cases
	cases := []core.SynthCase{{CaseName: "case-001", GenerationID: g.ID, Version: 2, ContentHash: "h"}}
	if err := s.SetSynthCases(ctx, d, "u1", ev.ID, cases); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSynthCases(ctx, d, "u2", ev.ID, cases); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("cases of another owner: %v", err)
	}
	rows, err := s.ListSynthCases(ctx, d, "u1", ev.ID)
	if err != nil || len(rows) != 1 || rows[0].ContentHash != "h" {
		t.Fatal(err, rows)
	}
	list, err := s.ListSynthDefs(ctx, d, "u1")
	if err != nil || len(list) != 1 {
		t.Fatal(err, list)
	}
	// deleting the eval deletes everything of the synthetic eval
	if _, err := s.DeleteEvalDef(ctx, d, "u1", ev.ID); err != nil {
		t.Fatal(err)
	}
	if got := count(t, d, `SELECT COUNT(*) FROM gb_synth_defs`) + count(t, d, `SELECT COUNT(*) FROM gb_synth_generations`) +
		count(t, d, `SELECT COUNT(*) FROM gb_synth_cases`) + count(t, d, `SELECT COUNT(*) FROM gb_synth_def_versions`); got != 0 {
		t.Errorf("rows left: %d", got)
	}
}
