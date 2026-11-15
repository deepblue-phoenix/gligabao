package agent_research_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// Tests of the migration "reports table" (migrate_reports.go). Remove this file with it.

// oldReportsDatabase is a database of the version before the reports table: the fresh schema plus the column report of the runs, with a
// research and its worker that have reports, and a failed research without one.
func oldReportsDatabase(t *testing.T,
	d *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`ALTER TABLE gb_agent_research_runs ADD COLUMN report TEXT NOT NULL DEFAULT ''`,
		`INSERT INTO gb_agent_research_runs (id, user_id, topic, status, state, report, created_at, finished_at) VALUES (1, 'u1', 'batteries', 'completed', 'completed', 'The old report.', '2025-01-01T00:00:00.000Z', '2025-01-01T00:10:00.000Z')`,
		`INSERT INTO gb_agent_research_runs (id, user_id, parent_id, name, topic, status, state, report, created_at, finished_at) VALUES (2, 'u1', 1, 'q_002', 'cost', 'completed', 'completed', '{"status":"answered"}', '2025-01-01T00:01:00.000Z', '2025-01-01T00:02:00.000Z')`,
		`INSERT INTO gb_agent_research_runs (id, user_id, topic, status, state, created_at, finished_at) VALUES (3, 'u1', 'failed one', 'failed', 'failed', '2025-01-01T00:00:00.000Z', '2025-01-01T00:10:00.000Z')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestMigrateReportsMovesTheReportsAndDropsTheColumn(t *testing.T) {
	dbtest.Run(t, func(t *testing.T,
		d *sql.DB) {
		backend := config.DBBackendSQLite
		if strings.HasSuffix(t.Name(), "postgres") {
			backend = config.DBBackendPostgres
		}
		ctx := context.Background()
		oldReportsDatabase(t, d)
		fake := &fakeAnthropic{
			summary: "A summary.",
		}
		client := fake.start(t)

		// Without a client nothing happens, the column stays.
		if n, err := agent_research.MigrateReports(ctx, d, backend, nil); err != nil || n != 0 || !hasColumn(t, d, backend, "gb_agent_research_runs", "report") {
			t.Fatalf("no client: %d %v", n, err)
		}

		n, err := agent_research.MigrateReports(ctx, d, backend, client)
		if err != nil || n != 2 {
			t.Fatalf("migrate: %d %v", n, err)
		}
		if hasColumn(t, d, backend, "gb_agent_research_runs", "report") {
			t.Error("the column was not dropped")
		}
		// Only the research is summarized (one call), its worker (findings) is not, and the run without a report has no row.
		if fake.summaries != 1 {
			t.Errorf("%d summary calls", fake.summaries)
		}
		rows, err := d.Query(`SELECT run_id, report, summary FROM gb_agent_research_reports ORDER BY run_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var id int64
			var report, summary string
			if err := rows.Scan(&id, &report, &summary); err != nil {
				t.Fatal(err)
			}
			got = append(got, strings.Join([]string{string(rune('0' + id)), report, summary}, "|"))
		}
		if want := `1|The old report.|A summary.,2|{"status":"answered"}|`; strings.Join(got, ",") != want {
			t.Errorf("rows %q, want %q", strings.Join(got, ","), want)
		}

		// It is over: another run does nothing, with or without a client.
		if n, err := agent_research.MigrateReports(ctx, d, backend, client); err != nil || n != 0 || fake.summaries != 1 {
			t.Errorf("second run: %d %v (%d summary calls)", n, err, fake.summaries)
		}

		// The runs read with their reports.
		m := agent_research.NewManager(ctx,
			d,
			mustOps(t, backend),
			client)
		defer m.Wait()
		run, err := m.Get(ctx,
			"u1",
			1)
		if err != nil || run.Report != "The old report." || run.Summary != "A summary." {
			t.Errorf("run: %+v %v", run, err)
		}
	})
}

func TestMigrateReportsStopsAtAFailedSummaryAndKeepsTheColumn(t *testing.T) {
	dbtest.Run(t, func(t *testing.T,
		d *sql.DB) {
		backend := config.DBBackendSQLite
		if strings.HasSuffix(t.Name(), "postgres") {
			backend = config.DBBackendPostgres
		}
		ctx := context.Background()
		oldReportsDatabase(t, d)
		fake := &fakeAnthropic{} // its summary call fails
		if n, err := agent_research.MigrateReports(ctx, d, backend, fake.start(t)); err == nil || n != 0 {
			t.Fatalf("migrate: %d %v", n, err)
		}
		if !hasColumn(t, d, backend, "gb_agent_research_runs", "report") {
			t.Fatal("the column was dropped before the reports were all migrated")
		}
		// The next start, when the model answers, finishes it.
		fake.mu.Lock()
		fake.summary = "Later."
		fake.mu.Unlock()
		if n, err := agent_research.MigrateReports(ctx, d, backend, fake.start(t)); err != nil || n != 2 {
			t.Fatalf("second migrate: %d %v", n, err)
		}
	})
}

func mustOps(t *testing.T,
	backend string) agent_research.Ops {
	t.Helper()
	ops, err := agent_research.OpsFor(backend)
	if err != nil {
		t.Fatal(err)
	}
	return ops
}
