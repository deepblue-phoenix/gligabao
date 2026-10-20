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

	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db/postgres"

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
	schema := fmt.Sprintf("gbauditstest_%d", time.Now().UnixNano())
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

	started := time.Now()
	run := &core.AuditRun{TrainedModelID: 3, Dataset: "mine", DatasetSHA256: "abc", Params: core.Params{"batch_size": 10},
		Status: core.StatusRunning, Counts: core.Counts{Total: 2, Pending: 2}, Timing: core.Timing{StartedAt: &started}}
	if err := postgres.CreateAuditRun(ctx, d, run); err != nil || run.ID == 0 || run.CreatedAt.IsZero() {
		t.Fatalf("CreateAuditRun: %+v %v", run, err)
	}
	pending := &core.AuditRun{TrainedModelID: 3, Dataset: "mine"}
	if err := postgres.CreateAuditRun(ctx, d, pending); err != nil || pending.Status != core.StatusPending {
		t.Fatalf("CreateAuditRun without a status: %+v %v", pending, err)
	}
	for name, bad := range map[string]*core.AuditRun{
		"no dataset":           {TrainedModelID: 3},
		"unknown status":       {TrainedModelID: 3, Dataset: "mine", Status: "judged"},
		"judged not split":     {TrainedModelID: 3, Dataset: "mine", AuditStats: core.AuditStats{NumJudged: 2, NumCorrect: 1}},
		"score sum over limit": {TrainedModelID: 3, Dataset: "mine", AuditStats: core.AuditStats{NumJudged: 1, NumCorrect: 1, ScoreSum: 6}},
	} {
		if err := postgres.CreateAuditRun(ctx, d, bad); err == nil {
			t.Errorf("accepted an audit run with %s", name)
		}
	}

	score, six := 4, 6
	c0 := &core.AuditCaseRun{RunID: run.ID, Idx: 0, Status: core.StatusCompleted, Instruction: "q0", Input: "ctx", Reference: "a0", Response: "a0",
		Batch: 1, JudgeScore: &score, JudgeReason: "same", InputTokens: 5, OutputTokens: 3, DurationMS: 120}
	if err := postgres.CreateAuditCaseRun(ctx, d, c0); err != nil || c0.ID == 0 || c0.CreatedAt.IsZero() {
		t.Fatalf("CreateAuditCaseRun: %+v %v", c0, err)
	}
	if got := c0.Score(); got == nil || *got != 0.8 {
		t.Errorf("score %v", got)
	}
	if (core.AuditCaseRun{}).Score() != nil {
		t.Error("an unjudged case has a score")
	}
	c1 := &core.AuditCaseRun{RunID: run.ID, Idx: 1, Status: core.StatusErrored, Error: "model failed", Instruction: "q1"}
	if err := postgres.CreateAuditCaseRun(ctx, d, c1); err != nil {
		t.Fatalf("CreateAuditCaseRun errored: %v", err)
	}
	for name, bad := range map[string]*core.AuditCaseRun{
		"a second case at one idx":       {RunID: run.ID, Idx: 1, Instruction: "q"},
		"an unknown run":                 {RunID: 999, Idx: 0, Instruction: "q"},
		"a negative idx":                 {RunID: run.ID, Idx: -1, Instruction: "q"},
		"a score on a case that errored": {RunID: run.ID, Idx: 2, Status: core.StatusErrored, Instruction: "q", JudgeScore: &score},
		"a score above 5":                {RunID: run.ID, Idx: 3, Status: core.StatusCompleted, Instruction: "q", JudgeScore: &six},
		"an unknown case status":         {RunID: run.ID, Idx: 4, Status: "judged", Instruction: "q"},
	} {
		if err := postgres.CreateAuditCaseRun(ctx, d, bad); err == nil {
			t.Errorf("accepted %s", name)
		}
	}

	if n := count(t, d, `SELECT COUNT(*) FROM gb_training_audit_runs WHERE id = $1 AND trained_model_id = 3 AND dataset_sha256 = 'abc' AND counts_total = 2 AND started_at IS NOT NULL AND finished_at IS NULL AND params = '{"batch_size": 10}'::jsonb`, run.ID); n != 1 {
		t.Errorf("audit run: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM gb_training_audit_case_runs WHERE run_id = $1 AND judge_score IS NULL`, run.ID); n != 1 {
		t.Errorf("case runs without a score: %d", n)
	}

	// Deleting the run deletes its case runs.
	if _, err := d.Exec(`DELETE FROM gb_training_audit_runs WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM gb_training_audit_case_runs`); n != 0 {
		t.Errorf("case runs left after their run was deleted: %d", n)
	}

	// A transaction can be rolled back.
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.CreateAuditRun(ctx, tx, &core.AuditRun{TrainedModelID: 77, Dataset: "tmp"}); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if n := count(t, d, `SELECT COUNT(*) FROM gb_training_audit_runs WHERE trained_model_id = 77`); n != 0 {
		t.Errorf("rolled back audit run: %d", n)
	}
}
