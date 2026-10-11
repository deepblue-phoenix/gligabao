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
)

func TestTrajectories(t *testing.T) { dbtest.Run(t, testTrajectories) }

func testTrajectories(t *testing.T, d *sql.DB) {
	ex := `{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"}],"edges":[{"source":"Alice","target":"Acme","relation":"works_at"}]}`
	llm := &recordingLLM{outs: []string{ex, `{"answer":"Acme.","entities":[1],"edges":[1]}`, ex}}
	dir := t.TempDir()
	ctx := context.Background()

	rem, err := memory.Remember(ctx, d, llm, "u1", "", "Alice works at Acme", time.Time{}, memory.WithTrajectoryDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if rem.TrajectoryID == "" || rem.Trajectory != nil {
		t.Fatalf("without eval: id=%q traj=%v", rem.TrajectoryID, rem.Trajectory)
	}
	rec, err := memory.Recall(ctx, d, llm, "u1", "", "Where does Alice work?", memory.WithTrajectoryDir(dir), memory.WithEval())
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
