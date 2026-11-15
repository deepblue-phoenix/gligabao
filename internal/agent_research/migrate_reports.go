package agent_research

// =====================================================================================================================================
// MIGRATION "REPORTS TABLE" (one-off; REMOVE THIS WHOLE FILE ONCE EVERY DATABASE HAS RUN IT)
//
// Until 0.207.0 the report of a run was the column gb_agent_research_runs.report. From 0.207.0 it is a row of its own table,
// gb_agent_research_reports (run_id, report, summary, created_at), and the column does not exist in a new database.
//
// What it does, for a database that still has the column (that is how it is detected: no other state is kept):
//  1. For every run with a report that has no row in gb_agent_research_reports yet, it inserts the row, with the report, the time the run
//     ended as created_at and, for a research (a run without parent), the summary that summarizeReport makes with one model call. A
//     worker of the investigative loop (its report is its findings) gets no summary.
//  2. When every report has its row, it drops the column gb_agent_research_runs.report. Once the column is gone the migration never
//     runs again.
// It needs the LLM client for the summaries: without one (Manager.Client nil) it does nothing and the column stays, so that a later start
// with a client migrates (the reports of runs that were not migrated are not shown until then). It stops at the first summary that
// fails (an API error, a cancelled context: the server stopping), without dropping the column; the reports it had migrated are kept
// (step 1 skips them) and the next start goes on from there. It runs in the background from NewManager (MigrateReports is synchronous).
// With several processes on one PostgreSQL database two may run it at once: the inserts ignore a row that is there, so the result is the same
// (only the summaries are paid twice).
//
// To remove it: delete this file and t_migrate_reports_test.go, the call in NewManager (search "MigrateReports"), and the field Backend of
// Ops and its two values (only this file uses it). Do not change sql/schema*.sql (they already lack the column), nor
// sql/upgrade_status.sql of SQLite (it rebuilds an old table, with its report column, for this migration to find).
// =====================================================================================================================================

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	log "github.com/sirupsen/logrus"
)

// legacyReport is a report that is still in the column of its run.
type legacyReport struct {
	RunID  int64
	Topic  string
	Model  string
	Report string
	// Root is true for a research (a run without parent), whose report gets a summary.
	Root bool
}

// legacyReportColumn says whether gb_agent_research_runs still has the report column, the sign that the migration has to run.
func legacyReportColumn(ctx context.Context,
	d *sql.DB,
	backend string) (bool, error) {
	query := `SELECT COUNT(*) FROM pragma_table_info('gb_agent_research_runs') WHERE name = 'report'`
	if backend == config.DBBackendPostgres {
		query = `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'gb_agent_research_runs' AND column_name = 'report'`
	}
	var n int
	if err := d.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// legacyReports lists the reports of the column that have no row in the new table yet, oldest run first.
func legacyReports(ctx context.Context,
	d *sql.DB) ([]legacyReport, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT r.id, r.topic, r.model, r.report, CASE WHEN r.parent_id IS NULL THEN 1 ELSE 0 END
		FROM gb_agent_research_runs r
		WHERE r.report <> '' AND NOT EXISTS (SELECT 1 FROM gb_agent_research_reports p WHERE p.run_id = r.id)
		ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []legacyReport{}
	for rows.Next() {
		var l legacyReport
		var root int
		if err := rows.Scan(&l.RunID, &l.Topic, &l.Model, &l.Report, &root); err != nil {
			return nil, err
		}
		l.Root = root == 1
		out = append(out, l)
	}
	return out, rows.Err()
}

// insertLegacyReport copies the report of a run (the column) into its row of the new table, with summary; a row that is there is left alone.
func insertLegacyReport(ctx context.Context,
	d *sql.DB,
	backend string,
	runID int64,
	summary string) error {
	query := `
		INSERT INTO gb_agent_research_reports (run_id, report, summary, created_at)
		SELECT id, report, ?, COALESCE(finished_at, created_at) FROM gb_agent_research_runs WHERE id = ?
		ON CONFLICT (run_id) DO NOTHING`
	if backend == config.DBBackendPostgres {
		query = `
		INSERT INTO gb_agent_research_reports (run_id, report, summary, created_at)
		SELECT id, report, $1::text, COALESCE(finished_at, created_at) FROM gb_agent_research_runs WHERE id = $2
		ON CONFLICT (run_id) DO NOTHING`
	}
	_, err := d.ExecContext(ctx,
		query,
		summary,
		runID)
	return err
}

// MigrateReports is the migration described at the top of this file: it moves the reports of gb_agent_research_runs.report to
// gb_agent_research_reports, with their summaries made by client, and drops the column. It returns migrated, the number of reports it
// moved, and does nothing (0, nil) if the column is not there (the migration ran) or client is nil. backend is config.DBBackendSQLite
// (or empty) or config.DBBackendPostgres.
func MigrateReports(ctx context.Context,
	d *sql.DB,
	backend string,
	client *llm.Client) (migrated int, err error) {
	has, err := legacyReportColumn(ctx,
		d,
		backend)
	if err != nil || !has {
		return 0, err
	}
	if client == nil {
		log.Warn("agent research: the reports are still in the column of the runs and have no summary: they are migrated at the first start that has an LLM client")
		return 0, nil
	}
	todo, err := legacyReports(ctx, d)
	if err != nil {
		return 0, fmt.Errorf("migrate the reports of the research runs: %w", err)
	}
	for _, l := range todo {
		summary := ""
		if l.Root {
			sum, err := summarizeReport(ctx,
				client,
				l.Model,
				l.Topic,
				l.Report)
			if err != nil {
				return migrated, fmt.Errorf("migrate the reports of the research runs: summary of the report of run %d: %w",
					l.RunID,
					err)
			}
			summary = sum.Text
		}
		if err := insertLegacyReport(ctx,
			d,
			backend,
			l.RunID,
			summary); err != nil {
			return migrated, fmt.Errorf("migrate the reports of the research runs: run %d: %w",
				l.RunID,
				err)
		}
		migrated++
	}
	if _, err := d.ExecContext(ctx, `ALTER TABLE gb_agent_research_runs DROP COLUMN report`); err != nil {
		return migrated, fmt.Errorf("migrate the reports of the research runs: drop the column: %w", err)
	}
	return migrated, nil
}

// migrateReports runs MigrateReports for the manager, in the background, and logs how it went.
func (m *Manager) migrateReports() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		n, err := MigrateReports(m.ctx,
			m.DB,
			m.Ops.Backend,
			m.Client)
		switch {
		case err != nil:
			log.WithError(err).Errorf("agent research: the migration of the reports did not finish (%d moved); it goes on at the next start", n)
		case n > 0:
			log.Infof("agent research: %d report(s) moved to gb_agent_research_reports, with their summaries; the column report of the runs was dropped", n)
		}
	}()
}
