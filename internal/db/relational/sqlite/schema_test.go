package sqlite

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

// tableBody returns the text of the CREATE TABLE statement of the table in sql (up to its closing ");").
func tableBody(t *testing.T, sql, table string) string {
	t.Helper()
	_, rest, ok := strings.Cut(sql, "CREATE TABLE IF NOT EXISTS "+table+" (")
	if !ok {
		t.Fatalf("no CREATE TABLE for %s", table)
	}
	body, _, ok := strings.Cut(rest, "\n);")
	if !ok {
		t.Fatalf("CREATE TABLE %s is not closed", table)
	}
	return body
}

// stage is a stage of model.RememberStages or model.QueryStages.
type stage struct {
	Name  string
	Model bool
}

var (
	statusColumn = regexp.MustCompile(`(?m)^\t\w+_status `)
	stageCol     = regexp.MustCompile(`(?m)^\t\w+_(status|error|started_at|finished_at|inference_ids) `)
)

func quoted(names []string) string { return "'" + strings.Join(names, "', '") + "'" }

// The stage columns of gb_query_runs are written out in the .sql file, not generated: check that they match model.QueryStages.
// gb_remember_runs has no stage columns: its stages are rows of gb_remember_run_stages.
func TestRunTablesMatchStages(t *testing.T) {
	statuses := quoted(model.RememberStatuses)
	if body := tableBody(t, rememberRunsSchema, "gb_remember_runs"); !strings.Contains(body, "status      TEXT NOT NULL CHECK (status IN ("+statuses+"))") {
		t.Errorf("gb_remember_runs: the status CHECK is not the list of model.RememberStatuses")
	} else if stageCol.MatchString(body) {
		t.Errorf("gb_remember_runs: has per-stage columns, the stages are in gb_remember_run_stages")
	}

	body := tableBody(t, rememberRunsSchema, "gb_remember_run_stages")
	if !strings.Contains(body, "status        TEXT NOT NULL DEFAULT 'not_started' CHECK (status IN ("+statuses+"))") {
		t.Errorf("gb_remember_run_stages: the status CHECK is not the list of model.RememberStatuses")
	}
	for _, c := range []string{"run_id ", "stage_type ", "error ", "started_at ", "finished_at ", "inference_ids ", "PRIMARY KEY (run_id, stage_type)"} {
		if !strings.Contains(body, c) {
			t.Errorf("gb_remember_run_stages: no %s", strings.TrimSpace(c))
		}
	}

	var query []stage
	for _, s := range model.QueryStages {
		query = append(query, stage{s.Name, s.Model})
	}
	checkQueryRuns(t, query)

	names := make([]string, len(model.RememberStages))
	for i, s := range model.RememberStages {
		names[i] = s.Name
	}
	if out := tableBody(t, rememberRunsSchema, "gb_remember_run_stage_outputs"); !strings.Contains(out, "CHECK (stage IN ("+quoted(names)+"))") {
		t.Errorf("gb_remember_run_stage_outputs: the stage CHECK is not the list of model.RememberStages")
	}
}

// checkQueryRuns checks the stage columns of gb_query_runs against stages.
func checkQueryRuns(t *testing.T, stages []stage) {
	t.Helper()
	statuses := quoted(model.RememberStatuses)
	body := tableBody(t, queryRunsSchema, "gb_query_runs")
	if !strings.Contains(body, "status      TEXT NOT NULL CHECK (status IN ("+statuses+"))") {
		t.Errorf("gb_query_runs: the status CHECK is not the list of model.RememberStatuses")
	}
	n := 0
	for _, s := range stages {
		for _, c := range []string{"_status ", "_error ", "_started_at ", "_finished_at "} {
			if !strings.Contains(body, "\n\t"+s.Name+c) {
				t.Errorf("gb_query_runs: no column %s%s", s.Name, strings.TrimSpace(c))
			}
		}
		if !strings.Contains(body, fmt.Sprintf("CHECK (%[1]s_status IN (%[2]s))", s.Name, statuses)) {
			t.Errorf("gb_query_runs: the CHECK of %s_status is not the list of model.RememberStatuses", s.Name)
		}
		if has := strings.Contains(body, "\n\t"+s.Name+"_inference_ids "); has != s.Model {
			t.Errorf("gb_query_runs: %s_inference_ids present = %v, want %v (Stage.Model)", s.Name, has, s.Model)
		}
		n++
	}
	if got := len(statusColumn.FindAllString(body, -1)); got != n {
		t.Errorf("gb_query_runs: %d columns named *_status, want %d (one for each stage)", got, n)
	}
}
