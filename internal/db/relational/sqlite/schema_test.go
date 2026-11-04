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

var statusColumn = regexp.MustCompile(`(?m)^\t\w+_status `)

func quoted(names []string) string { return "'" + strings.Join(names, "', '") + "'" }

// The stage columns of gb_remember_runs and gb_query_runs are written out in the .sql files, not generated: check that they match the
// stage lists of package model.
func TestRunTablesMatchStages(t *testing.T) {
	statuses := quoted(model.RememberStatuses)
	check := func(table, sql string, stages []stage) {
		body := tableBody(t, sql, table)
		if !strings.Contains(body, "status      TEXT NOT NULL CHECK (status IN ("+statuses+"))") {
			t.Errorf("%s: the status CHECK is not the list of model.RememberStatuses", table)
		}
		n := 0
		for _, s := range stages {
			for _, c := range []string{"_status ", "_error ", "_started_at ", "_finished_at "} {
				if !strings.Contains(body, "\n\t"+s.Name+c) {
					t.Errorf("%s: no column %s%s", table, s.Name, strings.TrimSpace(c))
				}
			}
			if !strings.Contains(body, fmt.Sprintf("CHECK (%[1]s_status IN (%[2]s))", s.Name, statuses)) {
				t.Errorf("%s: the CHECK of %s_status is not the list of model.RememberStatuses", table, s.Name)
			}
			if has := strings.Contains(body, "\n\t"+s.Name+"_inference_ids "); has != s.Model {
				t.Errorf("%s: %s_inference_ids present = %v, want %v (Stage.Model)", table, s.Name, has, s.Model)
			}
			n++
		}
		if got := len(statusColumn.FindAllString(body, -1)); got != n {
			t.Errorf("%s: %d columns named *_status, want %d (one for each stage)", table, got, n)
		}
	}
	var remember, query []stage
	for _, s := range model.RememberStages {
		remember = append(remember, stage{s.Name, s.Model})
	}
	for _, s := range model.QueryStages {
		query = append(query, stage{s.Name, s.Model})
	}
	check("gb_remember_runs", rememberRunsSchema, remember)
	check("gb_query_runs", queryRunsSchema, query)

	names := make([]string, len(model.RememberStages))
	for i, s := range model.RememberStages {
		names[i] = s.Name
	}
	if out := tableBody(t, rememberRunsSchema, "gb_remember_run_stage_outputs"); !strings.Contains(out, "CHECK (stage IN ("+quoted(names)+"))") {
		t.Errorf("gb_remember_run_stage_outputs: the stage CHECK is not the list of model.RememberStages")
	}
}
