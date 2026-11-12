package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/dbexport"
	"github.com/deepblue-phoenix/gligabao/internal/db/dbfull"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
)

// The export of an agent that ran in Modal (its databases opened as gligabao_agent does) is loaded into the databases of the server
// (opened as the server does), as the run of the user's research.
func TestDBImporterLoadsTheExportOfAnAgent(t *testing.T) {
	ctx := context.Background()
	const ts = "2025-03-01T10:00:00Z"

	agentDB, err := db.Open(db.Config{
		SQLitePath:          filepath.Join(t.TempDir(), "agent.db"),
		EmbeddingDimensions: 3,
		Models:              ai.SupportedModels(nil),
		Schemas:             []db.Schema{agent_research.Open},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agentDB.Close()
	for _, q := range []string{
		`INSERT INTO gb_datasets (user_id, name, created_at) VALUES ('default', 'main', '` + ts + `')`,
		`INSERT INTO gb_artifacts (dataset_id, kind, text, session_id, valid_at, created_at) VALUES (1, 'knowledge', 'Acme is in Paris.', 'research-1', '` + ts + `', '` + ts + `')`,
		`INSERT INTO gb_entities (dataset_id, name, name_key, type, created_at, updated_at) VALUES (1, 'Acme', 'acme', 'org', '` + ts + `', '` + ts + `')`,
		`INSERT INTO gb_agent_research_runs (user_id, topic, status, state, created_at) VALUES ('default', 'the topic', 'completed', 'completed', '` + ts + `')`,
	} {
		if _, err := agentDB.Relational.Exec(q); err != nil {
			t.Fatalf("%s: %v",
				q,
				err)
		}
	}
	if err := agentDB.Vector.Upsert(ctx,
		"dataset-1-entities",
		[]vector.Vector{{
			ID:       "entity-1",
			Values:   []float32{1, 0, 0},
			Metadata: map[string]any{"entity_id": float64(1)},
		}}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "export.zip")
	if _, err := dbexport.WriteFile(ctx,
		archive,
		dbexport.Source{
			Relational:          agentDB.Relational,
			Backend:             agentDB.Backend,
			Vector:              agentDB.Vector,
			VectorStore:         agentDB.VectorStore,
			EmbeddingDimensions: agentDB.EmbeddingDimensions,
			ImageDir:            agentDB.ImageDir,
		},
		dbexport.Options{}); err != nil {
		t.Fatal(err)
	}

	srv, err := dbfull.Open(db.Config{
		SQLitePath:          filepath.Join(t.TempDir(), "server.db"),
		EmbeddingDimensions: 3,
		Models:              ai.SupportedModels(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if _, err := srv.Relational.Exec(`INSERT INTO gb_agent_research_runs (id, user_id, topic, status, state, runner, created_at) VALUES (5, 'u1', 'the topic', 'running', 'thinking', ?, ?)`,
		core.RunnerModal,
		ts); err != nil {
		t.Fatal(err)
	}

	summary, err := dbImporter{srv}.Import(ctx,
		archive,
		agent_research.ImportRun{
			UserID: "u1",
			RunID:  5,
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(summary)

	var user, session string
	var vectors int
	if err := srv.Relational.QueryRow(`SELECT d.user_id, a.session_id FROM gb_artifacts a JOIN gb_datasets d ON d.id = a.dataset_id`).Scan(&user, &session); err != nil {
		t.Fatal(err)
	}
	if user != "u1" || session != "research-5" {
		t.Errorf("artifact of %q in session %q",
			user,
			session)
	}
	var n int
	if err := srv.Relational.QueryRow(`SELECT COUNT(*) FROM gb_agent_research_runs`).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d runs (the run of the main agent is the server's own): %v",
			n,
			err)
	}
	if st, ok := srv.Vector.(vector.Statter); ok {
		stats, err := st.Stats(ctx, "dataset-1-entities")
		if err != nil {
			t.Fatal(err)
		}
		vectors = stats.Count
	}
	if vectors != 1 {
		t.Errorf("%d vectors", vectors)
	}
}

func TestRemoteEventOfCarriesTheExport(t *testing.T) {
	ev := remoteEventOf(sidecar.ResearchEvent{
		Seq:    3,
		Type:   sidecar.ResearchEventStatus,
		Status: sidecar.ResearchCompleted,
		Export: &sidecar.ResearchExport{
			Bytes:  24,
			SHA256: "abc",
		},
	})
	if ev.Export == nil || ev.Export.Bytes != 24 || ev.Export.SHA256 != "abc" || ev.Seq != 3 {
		t.Fatalf("%+v", ev)
	}
	if ev := remoteEventOf(sidecar.ResearchEvent{
		Type: sidecar.ResearchEventStatus,
	}); ev.Export != nil {
		t.Fatalf("%+v", ev)
	}
}
