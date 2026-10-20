package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/db/postgres"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// open returns a database with the schema applied in a temporary schema of the database named by POSTGRES_TEST_DSN (the test
// is skipped if it is not set).
func open(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("gbneotest_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
	})
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(postgres.Schema); err != nil {
		t.Fatal(err)
	}
	return d
}

func count(t *testing.T, d *sql.DB, query string, args ...any) (n int) {
	t.Helper()
	if err := d.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestCreate(t *testing.T) {
	ctx := context.Background()
	d := open(t)

	// Definitions.
	ev := &core.EvalDef{Name: "kn", Type: core.TypeKnowledge, Tags: []string{"a"}, Params: core.Params{"judge": "x"}}
	if err := postgres.CreateEvalDef(ctx, d, ev); err != nil || ev.ID == 0 || ev.CreatedAt.IsZero() {
		t.Fatalf("CreateEvalDef: %+v %v", ev, err)
	}
	if err := postgres.CreateEvalDef(ctx, d, &core.EvalDef{Name: "kn", Type: core.TypeKnowledge}); err == nil {
		t.Error("accepted a second eval def with the same name")
	}
	tr := &core.EvalDef{Name: "tr", Type: core.TypeTraining}
	if err := postgres.CreateEvalDef(ctx, d, tr); err != nil {
		t.Fatal(err)
	}

	kc := &core.CaseDef{EvalDefID: ev.ID, Type: core.TypeKnowledge, Name: "c0"}
	if err := postgres.CreateCaseDef(ctx, d, kc); err != nil || kc.ID == 0 {
		t.Fatalf("CreateCaseDef: %+v %v", kc, err)
	}
	if err := postgres.CreateCaseDef(ctx, d, &core.CaseDef{EvalDefID: ev.ID, Type: core.TypeTraining, Name: "bad", Position: 1}); err == nil {
		t.Error("accepted a case def whose type is not that of its eval")
	}
	tc := &core.CaseDef{EvalDefID: tr.ID, Type: core.TypeTraining, Name: "c0"}
	if err := postgres.CreateCaseDef(ctx, d, tc); err != nil {
		t.Fatal(err)
	}

	km := &core.KnowledgeCaseDefMeta{CaseDefID: kc.ID}
	km.Remember = []core.KnowledgeArtifact{{Text: "Alice works at Acme."}}
	km.Query = "Where does Alice work?"
	km.ExpectContains = []string{"Acme"}
	km.Models = map[string]string{"extract_graph": "m"}
	if err := postgres.CreateKnowledgeCaseDefMeta(ctx, d, km); err != nil {
		t.Fatalf("CreateKnowledgeCaseDefMeta: %v", err)
	}
	tm := &core.TrainingCaseDefMeta{CaseDefID: tc.ID, TrainingInput: core.TrainingInput{Instruction: "q"}, TrainingExpected: core.TrainingExpected{Output: "a"}}
	if err := postgres.CreateTrainingCaseDefMeta(ctx, d, tm); err != nil {
		t.Fatalf("CreateTrainingCaseDefMeta: %v", err)
	}
	if err := postgres.CreateTrainingCaseDefMeta(ctx, d, &core.TrainingCaseDefMeta{CaseDefID: kc.ID}); err == nil {
		t.Error("accepted training metadata on a knowledge case def")
	}

	ex := &core.ExperimentDef{Name: "x", Params: core.Params{"k": 1}}
	if err := postgres.CreateExperimentDef(ctx, d, ex); err != nil || ex.ID == 0 {
		t.Fatalf("CreateExperimentDef: %+v %v", ex, err)
	}
	if err := postgres.CreateExperimentDefEval(ctx, d, ex.ID, ev.ID, 0); err != nil {
		t.Fatalf("CreateExperimentDefEval: %v", err)
	}
	if err := postgres.CreateExperimentDefEval(ctx, d, ex.ID, tr.ID, 0); err == nil {
		t.Error("accepted two evals at one position")
	}

	// Runs.
	xr := &core.ExperimentRun{DefID: ex.ID, Params: core.Params{"only": "kn"}}
	if err := postgres.CreateExperimentRun(ctx, d, xr); err != nil || xr.ID == 0 || xr.Status != core.StatusPending {
		t.Fatalf("CreateExperimentRun: %+v %v", xr, err)
	}
	started := time.Now()
	er := &core.EvalRun{DefID: ev.ID, ExperimentRunID: &xr.ID, Status: core.StatusRunning, Counts: core.Counts{Total: 1}, Timing: core.Timing{StartedAt: &started}}
	if err := postgres.CreateEvalRun(ctx, d, er); err != nil || er.ID == 0 {
		t.Fatalf("CreateEvalRun: %+v %v", er, err)
	}
	alone := &core.EvalRun{DefID: tr.ID}
	if err := postgres.CreateEvalRun(ctx, d, alone); err != nil {
		t.Fatalf("CreateEvalRun on its own: %v", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM gb_neo_eval_runs WHERE experiment_run_id IS NULL`); n != 1 {
		t.Fatalf("eval runs on their own: %d", n)
	}

	score := 1.0
	cr := &core.CaseRun{DefID: kc.ID, EvalRunID: er.ID, Type: core.TypeKnowledge, Status: core.StatusCompleted, Verdict: core.VerdictPass, Score: &score}
	if err := postgres.CreateCaseRun(ctx, d, cr); err != nil || cr.ID == 0 {
		t.Fatalf("CreateCaseRun: %+v %v", cr, err)
	}
	if err := postgres.CreateCaseRun(ctx, d, &core.CaseRun{DefID: kc.ID, EvalRunID: er.ID, Type: core.TypeKnowledge}); err == nil {
		t.Error("accepted a second run of one case in an eval run")
	}
	if err := postgres.CreateCaseRun(ctx, d, &core.CaseRun{DefID: kc.ID, EvalRunID: alone.ID, Type: core.TypeKnowledge, Verdict: core.VerdictFail}); err == nil {
		t.Error("accepted a verdict on a case run that did not complete")
	}
	dataset := int64(7)
	kr := &core.KnowledgeCaseRunMeta{CaseRunID: cr.ID, Input: km.KnowledgeInput, Answer: "Acme", PassedChecks: []string{"contains"}, DatasetID: &dataset, Steps: 2}
	if err := postgres.CreateKnowledgeCaseRunMeta(ctx, d, kr); err != nil {
		t.Fatalf("CreateKnowledgeCaseRunMeta: %v", err)
	}
	traj := &core.KnowledgeTrajectory{Seq: 0, Kind: "remember", TrajectoryID: "t1", Steps: 2, ArtifactIDs: []int64{1, 2}}
	if err := postgres.CreateKnowledgeTrajectory(ctx, d, cr.ID, traj); err != nil || traj.CreatedAt.IsZero() {
		t.Fatalf("CreateKnowledgeTrajectory: %+v %v", traj, err)
	}
	if err := postgres.CreateKnowledgeTrajectory(ctx, d, cr.ID, &core.KnowledgeTrajectory{Seq: 1, Kind: "other", TrajectoryID: "t2"}); err == nil {
		t.Error("accepted a trajectory of an unknown kind")
	}

	tcr := &core.CaseRun{DefID: tc.ID, EvalRunID: alone.ID, Type: core.TypeTraining}
	if err := postgres.CreateCaseRun(ctx, d, tcr); err != nil {
		t.Fatal(err)
	}
	js := 4
	if err := postgres.CreateTrainingCaseRunMeta(ctx, d, &core.TrainingCaseRunMeta{CaseRunID: tcr.ID, Instruction: "q", Response: "a", Batch: 1, JudgeScore: &js}); err != nil {
		t.Fatalf("CreateTrainingCaseRunMeta: %v", err)
	}
	if err := postgres.CreateTrainingCaseRunMeta(ctx, d, &core.TrainingCaseRunMeta{CaseRunID: cr.ID, Instruction: "q"}); err == nil {
		t.Error("accepted training metadata on a knowledge case run")
	}

	// What was stored.
	if n := count(t, d, `SELECT COUNT(*) FROM gb_neo_case_def_meta_knowledge WHERE case_def_id = $1 AND remember->0->>'text' = 'Alice works at Acme.' AND expect_edges = '[]'::jsonb`, kc.ID); n != 1 {
		t.Errorf("knowledge case def meta: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM gb_neo_knowledge_case_run_meta WHERE case_run_id = $1 AND dataset_id = 7 AND answer = 'Acme'`, cr.ID); n != 1 {
		t.Errorf("knowledge case run meta: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM gb_neo_eval_runs WHERE id = $1 AND experiment_run_id = $2 AND counts_total = 1 AND started_at IS NOT NULL AND finished_at IS NULL`, er.ID, xr.ID); n != 1 {
		t.Errorf("eval run: %d", n)
	}

	// A transaction can be rolled back.
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.CreateEvalDef(ctx, tx, &core.EvalDef{Name: "tmp", Type: core.TypeKnowledge}); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if n := count(t, d, `SELECT COUNT(*) FROM gb_neo_eval_defs WHERE name = 'tmp'`); n != 0 {
		t.Errorf("rolled back eval def: %d", n)
	}
}
