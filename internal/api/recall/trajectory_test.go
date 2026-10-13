package recall

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

func TestTrajectories(t *testing.T) { dbtest.Run(t, testTrajectories) }

func testTrajectories(t *testing.T, d *sql.DB) {
	ex := `{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"}],"edges":[{"source":"Alice","target":"Acme","relation":"works_at"}]}`
	llm := &recordingLLM{outs: []string{ex, `{"answer":"Acme.","entities":[1],"edges":[1]}`, ex}}
	dir := t.TempDir()
	ctx := context.Background()

	// Without eval nothing is generated or stored.
	rem, err := memory.Remember(ctx, d, llm, "u1", "", "", "Alice works at Acme", time.Time{}, memory_core.WithTrajectoryDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if rem.TrajectoryID != "" || rem.Trajectory != nil {
		t.Fatalf("without eval: id=%q traj=%v", rem.TrajectoryID, rem.Trajectory)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatalf("without eval: %d files stored", len(files))
	}
	llm.outs = append([]string{ex}, llm.outs...) // the eval Remember below needs its own extraction
	rem, err = memory.Remember(ctx, d, llm, "u1", "", "", "Alice works at Acme", time.Time{}, memory_core.WithTrajectoryDir(dir), memory_core.WithEval())
	if err != nil {
		t.Fatal(err)
	}
	if rem.Trajectory == nil || rem.TrajectoryID != rem.Trajectory.TrajectoryID {
		t.Fatalf("with eval: %+v", rem)
	}
	rec, err := memory.Recall(ctx, d, llm, "u1", "", "Where does Alice work?", memory_core.WithTrajectoryDir(dir), memory_core.WithEval())
	if err != nil {
		t.Fatal(err)
	}
	if rec.TrajectoryID == "" || rec.Trajectory == nil || rec.Trajectory.TrajectoryID != rec.TrajectoryID {
		t.Fatalf("with eval: %+v", rec)
	}
	if n := len(rec.Trajectory.Steps); n < 4 { // user, tool, system+user, agent
		t.Fatalf("recall steps: %d", n)
	}
	for _, id := range []string{rem.TrajectoryID, rec.TrajectoryID} {
		if _, err := os.Stat(filepath.Join(dir, id+".json")); err != nil {
			t.Fatal(err)
		}
	}
}
