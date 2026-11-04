package dbimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/dbexport"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

const (
	dims = 3
	ts   = "2025-03-01T10:00:00Z"
	sha  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func ex(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func one[T any](t *testing.T, d *sql.DB, q string, args ...any) T {
	t.Helper()
	var v T
	if err := d.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// agentArchive writes the archive of an agent that ran in a container: the database of the agent (a main agent, one sub-agent that
// did not end, a dataset that was deleted) with its vectors and one image, and returns the path of the archive.
func agentArchive(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	d := dbtest.OpenSQLite(t)
	vs, err := vector.NewChromem(filepath.Join(t.TempDir(), "vectors"), dims)
	if err != nil {
		t.Fatal(err)
	}
	imageDir := filepath.Join(t.TempDir(), "images")

	ex(t, d, `INSERT INTO gb_datasets (user_id, name, created_at) VALUES ('agent', 'main', ?)`, ts)
	ex(t, d, `INSERT INTO gb_datasets (user_id, name, created_at, deleted, deleted_at) VALUES ('agent', 'gone', ?, 1, ?)`, ts, ts)
	ex(t, d, `INSERT INTO gb_models (id, name, provider, kind, supported, created_at) VALUES ('m1', 'model-one', 'anthropic', 'llm', 1, ?)`, ts)
	ex(t, d, `INSERT INTO gb_inferences (type, stage, model_id, reasoning, input_tokens, output_tokens, cost_usd, created_at) VALUES ('remember', 'extract_graph', 'm1', 'none', 10, 5, 0.25, ?)`, ts)
	ex(t, d, `INSERT INTO gb_inferences (type, stage, model_id, reasoning, input_tokens, output_tokens, cost_usd, created_at) VALUES ('remember', 'summarize_chunk', 'm1', 'low', 20, 7, 0.5, ?)`, ts)

	// The main agent (run 1) and a sub-agent (run 2) remembered under their own sessions.
	ex(t, d, `INSERT INTO gb_artifacts (dataset_id, kind, text, session_id, valid_at, created_at) VALUES (1, 'knowledge', 'Acme is in Paris. Berlin too.', 'research-1', ?, ?)`, ts, ts)
	ex(t, d, `INSERT INTO gb_artifacts (dataset_id, kind, text, session_id, parent_id, chunk_index, start_offset, end_offset, tokens, valid_at, created_at) VALUES (1, 'knowledge', 'Acme is in Paris.', 'research-2', 1, 0, 0, 17, 4, ?, ?)`, ts, ts)
	ex(t, d, `INSERT INTO gb_artifacts (dataset_id, kind, text, session_id, parent_id, inference_id, valid_at, created_at) VALUES (1, 'chunk_summary', 'A summary', 'research-2', 2, 2, ?, ?)`, ts, ts)
	ex(t, d, `INSERT INTO gb_artifacts (dataset_id, kind, text, valid_at, created_at) VALUES (2, 'knowledge', 'in a dataset that was deleted', ?, ?)`, ts, ts)
	for _, name := range []string{"Acme", "Paris", "Berlin"} {
		ex(t, d, `INSERT INTO gb_entities (dataset_id, name, name_key, type, description, model_id, created_at, updated_at) VALUES (1, ?, ?, ?, 'agent knows it', 'm1', ?, ?)`, name, strings.ToLower(name), map[string]string{"Acme": "org"}[name]+"x", ts, ts)
	}
	ex(t, d, `UPDATE gb_entities SET type = 'org' WHERE name = 'Acme'`)
	ex(t, d, `UPDATE gb_entities SET type = 'place' WHERE name <> 'Acme'`)
	ex(t, d, `INSERT INTO gb_edges (dataset_id, source_id, target_id, relation, description, valid_at, created_at, artifact_id, model_id) VALUES (1, 1, 2, 'located_in', 'Acme is in Paris', ?, ?, 2, 'm1')`, ts, ts)
	ex(t, d, `INSERT INTO gb_edge_artifacts (edge_id, artifact_id) VALUES (1, 2)`)
	ex(t, d, `INSERT INTO gb_entity_artifacts (entity_id, artifact_id) VALUES (1, 2)`)
	ex(t, d, `INSERT INTO gb_entity_artifacts (entity_id, artifact_id) VALUES (2, 2)`)
	ex(t, d, `INSERT INTO gb_entity_sessions (entity_id, session_id) VALUES (1, 'research-2')`)
	ex(t, d, `INSERT INTO gb_edge_sessions (edge_id, session_id) VALUES (1, 'research-2')`)
	ex(t, d, `INSERT INTO gb_edge_events (dataset_id, edge_id, artifact_id, event, created_at) VALUES (1, 1, 2, 'created', ?)`, ts)
	ex(t, d, `INSERT INTO gb_artifact_classes (dataset_id, artifact_id, class, inference_id, created_at) VALUES (1, 2, 'company', 1, ?)`, ts)
	ex(t, d, `INSERT INTO gb_entity_geo (entity_id, kind, latitude, longitude, provider, created_at, updated_at) VALUES (2, 'city', 48.85, 2.35, 'test', ?, ?)`, ts, ts)

	ex(t, d, `INSERT INTO gb_series (dataset_id, key, field, name, point_count, first_ts, last_ts, stats, created_at, updated_at) VALUES (1, 'AAPL', 'value', 'Apple', 2, '2025-01-01T00:00:00Z', '2025-01-02T00:00:00Z', '{}', ?, ?)`, ts, ts)
	ex(t, d, `INSERT INTO gb_series (dataset_id, key, field, name, point_count, first_ts, last_ts, stats, created_at, updated_at) VALUES (1, 'GOOG', 'value', 'Google', 1, '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z', '{}', ?, ?)`, ts, ts)
	for _, p := range []struct {
		series int
		ts     string
		v      float64
	}{{1, "2025-01-01T00:00:00Z", 10}, {1, "2025-01-02T00:00:00Z", 11}, {2, "2025-01-01T00:00:00Z", 5}} {
		ex(t, d, `INSERT INTO gb_series_points (series_id, ts, value) VALUES (?, ?, ?)`, p.series, p.ts, p.v)
	}
	ex(t, d, `INSERT INTO gb_series_anchors (series_id, entity_id, role, created_at) VALUES (1, 1, 'subject', ?)`, ts)
	ex(t, d, `INSERT INTO gb_series_anchors (series_id, artifact_id, role, created_at) VALUES (2, 1, '', ?)`, ts)

	ex(t, d, `INSERT INTO gb_image_artifacts (dataset_id, session_id, media_type, sha256, size_bytes, path, valid_at, created_at) VALUES (1, 'research-1', 'image/png', ?, 4, ?, ?, ?)`, sha, "1/"+sha+".png", ts, ts)
	ex(t, d, `INSERT INTO gb_image_extractions (image_artifact_id, provider, model, theme, title, text, created_at) VALUES (1, 'p', 'm', 'chart', 'T', 'text', ?)`, ts)
	ex(t, d, `INSERT INTO gb_image_text_blocks (extraction_id, position, kind, text) VALUES (1, 0, 'title', 'T')`)
	if err := os.MkdirAll(filepath.Join(imageDir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "1", sha+".png"), []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatal(err)
	}

	ex(t, d, `INSERT INTO gb_remember_runs (user_id, dataset, kind, params, artifact_id, status, started_at) VALUES ('agent', 'main', 'knowledge', '{"session":"research-2","other":"x"}', 1, 'succeeded', ?)`, ts)
	ex(t, d, `INSERT INTO gb_remember_run_stages (run_id, stage_type, status, inference_ids) VALUES (1, 'extract_graph', 'succeeded', '[1,99]'), (1, 'summarize_chunk', 'succeeded', '[2]')`)
	ex(t, d, `INSERT INTO gb_remember_run_stage_outputs (run_id, stage, name, label, output, created_at) VALUES (1, 'extract_graph', 'extract_graph', '', '{"a":1}', ?)`, ts)
	ex(t, d, `INSERT INTO gb_query_runs (user_id, dataset, question, session_id, status, started_at) VALUES ('agent', 'main', 'what?', 'research-1', 'succeeded', ?)`, ts)
	ex(t, d, `INSERT INTO gb_graph_in_ctx_counters (dataset_id, kind, item_id, pipeline, stage, run_id, created_at) VALUES (1, 'entity', 1, 'remember', 'resolve_edges', 1, ?)`, ts)
	ex(t, d, `INSERT INTO gb_graph_in_ctx_counters (dataset_id, kind, item_id, pipeline, stage, run_id, created_at) VALUES (1, 'edge', 1, 'query', 'query', 1, ?)`, ts)

	ex(t, d, `INSERT INTO gb_agent_research_runs (user_id, topic, status, state, runner, report, created_at, finished_at) VALUES ('agent', 'the topic', 'completed', 'completed', 'local', 'REPORT', ?, ?)`, ts, ts)
	ex(t, d, `INSERT INTO gb_agent_research_runs (user_id, parent_id, name, reason, topic, status, state, runner, created_at) VALUES ('agent', 1, 'worker', 'why', 'the sub-task', 'running', 'searching', 'local', ?)`, ts)
	ex(t, d, `INSERT INTO gb_agent_research_states (run_id, seq, state, step, detail, started_at) VALUES (1, 1, 'thinking', 1, 'main state', ?)`, ts)
	ex(t, d, `INSERT INTO gb_agent_research_states (run_id, seq, state, step, detail, started_at) VALUES (2, 1, 'searching', 1, 'sub state', ?)`, ts)

	for ds, vs2 := range map[string][]vector.Vector{
		"dataset-1-entities": {
			{ID: "entity-1", Values: []float32{1, 0, 0}, Metadata: map[string]any{"kind": "entity", "entity_id": float64(1), "name": "Acme", "type": "org"}},
			{ID: "entity-2", Values: []float32{0, 1, 0}, Metadata: map[string]any{"kind": "entity", "entity_id": float64(2), "name": "Paris", "type": "place"}},
			{ID: "entity-3", Values: []float32{0, 0, 1}, Metadata: map[string]any{"kind": "entity", "entity_id": float64(3), "name": "Berlin", "type": "place"}},
		},
		"dataset-1-edges":     {{ID: "edge-1", Values: []float32{1, 1, 0}, Metadata: map[string]any{"kind": "edge", "edge_id": float64(1), "relation": "located_in"}}},
		"dataset-1-chunks":    {{ID: "chunk-2", Values: []float32{1, 0, 1}, Metadata: map[string]any{"kind": "chunk", "artifact_id": float64(2), "parent_id": float64(1), "index": float64(0)}}},
		"dataset-1-summaries": {{ID: "summary-3", Values: []float32{0, 1, 1}, Metadata: map[string]any{"kind": "summary", "artifact_id": float64(3), "chunk_id": float64(2), "parent_id": float64(1), "index": float64(0)}}},
		"dataset-2-entities":  {{ID: "entity-1", Values: []float32{1, 1, 1}}},
	} {
		if err := vs.Upsert(ctx, ds, vs2); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(t.TempDir(), "export.zip")
	if _, err := dbexport.WriteFile(ctx, path, dbexport.Source{Relational: d, Backend: config.DBBackendSQLite, Vector: vs, VectorStore: config.VectorStoreChromem, EmbeddingDimensions: dims, ImageDir: imageDir}, dbexport.Options{Source: "test"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func backendOf(d *sql.DB) string {
	if _, err := d.Exec(`SELECT sqlite_version()`); err != nil {
		return config.DBBackendPostgres
	}
	return config.DBBackendSQLite
}

// server is the database of the gligabao that started the agent: it has a dataset "main" with an entity and a series of its own, and
// the run of the main agent.
type server struct {
	d            *sql.DB
	vs           vector.Store
	imageDir     string
	ds, acme     int64
	rootRun      int64
	acmeVector   []float32
	existingGoog int64
}

func newServer(t *testing.T, d *sql.DB) *server {
	t.Helper()
	ctx := context.Background()
	s := &server{d: d, imageDir: filepath.Join(t.TempDir(), "images"), acmeVector: []float32{0.5, 0.5, 0.5}}
	var err error
	if s.vs, err = vector.NewChromem(filepath.Join(t.TempDir(), "vectors"), dims); err != nil {
		t.Fatal(err)
	}
	if s.ds, err = relational.EnsureDataset(ctx, d, "u1", "main"); err != nil {
		t.Fatal(err)
	}
	now := relational.Now()
	if err := d.QueryRow(`INSERT INTO gb_entities (dataset_id, name, name_key, type, description, created_at, updated_at) VALUES ($1, 'Acme Inc', 'acme', 'org', 'the server knows it', $2, $2) RETURNING id`, s.ds, now).Scan(&s.acme); err != nil && backendOf(d) == config.DBBackendPostgres {
		t.Fatal(err)
	} else if err != nil {
		res, err := d.Exec(`INSERT INTO gb_entities (dataset_id, name, name_key, type, description, created_at, updated_at) VALUES (?, 'Acme Inc', 'acme', 'org', 'the server knows it', ?, ?)`, s.ds, relational.FormatTime(now), relational.FormatTime(now))
		if err != nil {
			t.Fatal(err)
		}
		s.acme, _ = res.LastInsertId()
	}
	if err := s.vs.Upsert(ctx, fmt.Sprintf("dataset-%d-entities", s.ds), []vector.Vector{{ID: fmt.Sprintf("entity-%d", s.acme), Values: s.acmeVector}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *server) target() Target {
	return Target{Relational: s.d, Backend: backendOf(s.d), Vector: s.vs, EmbeddingDimensions: dims, ImageDir: s.imageDir}
}

func TestImport(t *testing.T) {
	archive := agentArchive(t)
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		ctx := context.Background()
		if backendOf(d) != config.DBBackendSQLite {
			t.Skip("the fixture of the server is written for SQLite")
		}
		s := newServer(t, d)
		ex(t, d, `INSERT INTO gb_series (dataset_id, key, field, name, point_count, first_ts, last_ts, stats, created_at, updated_at) VALUES (?, 'AAPL', 'value', 'Apple', 1, '2025-01-02T00:00:00Z', '2025-01-02T00:00:00Z', '{}', ?, ?)`, s.ds, ts, ts)
		ex(t, d, `INSERT INTO gb_series_points (series_id, ts, value) VALUES (1, '2025-01-02T00:00:00Z', 99)`)
		// The run of the main agent, as the server recorded it from the events of the container.
		ex(t, d, `INSERT INTO gb_agent_research_runs (id, user_id, topic, status, state, runner, created_at) VALUES (7, 'u1', 'the topic', 'running', 'searching', 'modal', ?)`, ts)
		ex(t, d, `INSERT INTO gb_agent_research_states (run_id, seq, state, detail, started_at) VALUES (7, 1, 'searching', 'the server timeline', ?)`, ts)

		r, err := dbexport.Open(archive)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		res, err := Import(ctx, r, s.target(), Options{UserID: "u1", RootRunID: 7, SessionPrefix: "research-", Runner: "modal"})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Warnings) != 0 {
			t.Errorf("warnings: %v", res.Warnings)
		}
		t.Log(res.Summary())

		// datasets: matched by name, the deleted one is left out with its rows
		if res.Datasets["main"] != s.ds || len(res.Datasets) != 1 {
			t.Errorf("datasets: %v", res.Datasets)
		}
		if n := one[int](t, d, `SELECT COUNT(*) FROM gb_datasets`); n != 1 {
			t.Errorf("%d datasets", n)
		}
		if n := one[int](t, d, `SELECT COUNT(*) FROM gb_artifacts WHERE text LIKE '%deleted%'`); n != 0 {
			t.Error("the artifact of the deleted dataset was imported")
		}

		// models and inferences, with their costs
		if n := one[int](t, d, `SELECT supported FROM gb_models WHERE id = 'm1'`); n != 0 {
			t.Errorf("supported = %d", n)
		}
		if c := one[float64](t, d, `SELECT SUM(cost_usd) FROM gb_inferences`); c != 0.75 {
			t.Errorf("cost %v", c)
		}

		// entities: Acme merged into the stored one, the others added; the stored row is kept
		if n := one[int](t, d, `SELECT COUNT(*) FROM gb_entities WHERE name_key = 'acme'`); n != 1 {
			t.Errorf("%d Acme entities", n)
		}
		if got := one[string](t, d, `SELECT description FROM gb_entities WHERE id = ?`, s.acme); got != "the server knows it" {
			t.Errorf("description %q", got)
		}
		if n := one[int](t, d, `SELECT COUNT(*) FROM gb_entities WHERE dataset_id = ?`, s.ds); n != 3 {
			t.Errorf("%d entities", n)
		}
		paris := one[int64](t, d, `SELECT id FROM gb_entities WHERE name = 'Paris'`)
		berlin := one[int64](t, d, `SELECT id FROM gb_entities WHERE name = 'Berlin'`)
		if tb := res.Tables["gb_entities"]; tb.Added != 2 || tb.Merged != 1 {
			t.Errorf("entities: %+v", tb)
		}

		// artifacts: parents, datasets and sessions of the runs mapped
		sub := one[int64](t, d, `SELECT id FROM gb_agent_research_runs WHERE parent_id = 7`)
		if one[int64](t, d, `SELECT COUNT(*) FROM gb_agent_research_runs`) != 2 {
			t.Error("the main agent's run must not be imported")
		}
		doc := one[int64](t, d, `SELECT id FROM gb_artifacts WHERE kind = 'knowledge' AND parent_id IS NULL`)
		chunk := one[int64](t, d, `SELECT id FROM gb_artifacts WHERE kind = 'knowledge' AND parent_id = ?`, doc)
		summary := one[int64](t, d, `SELECT id FROM gb_artifacts WHERE kind = 'chunk_summary' AND parent_id = ?`, chunk)
		if got := one[string](t, d, `SELECT session_id FROM gb_artifacts WHERE id = ?`, doc); got != "research-7" {
			t.Errorf("session of the main agent: %q", got)
		}
		if got := one[string](t, d, `SELECT session_id FROM gb_artifacts WHERE id = ?`, chunk); got != fmt.Sprintf("research-%d", sub) {
			t.Errorf("session of the sub-agent: %q (run %d)", got, sub)
		}
		if got := one[int64](t, d, `SELECT dataset_id FROM gb_artifacts WHERE id = ?`, doc); got != s.ds {
			t.Errorf("dataset %d", got)
		}
		if inf := one[int64](t, d, `SELECT inference_id FROM gb_artifacts WHERE id = ?`, summary); one[string](t, d, `SELECT stage FROM gb_inferences WHERE id = ?`, inf) != "summarize_chunk" {
			t.Errorf("the summary's inference is %d", inf)
		}

		// facts and links: the fact points at the stored entity; the stored entity is linked to the new artifact and session
		if src := one[int64](t, d, `SELECT source_id FROM gb_edges`); src != s.acme {
			t.Errorf("source of the fact: %d, want the stored entity %d", src, s.acme)
		}
		edge := one[int64](t, d, `SELECT id FROM gb_edges`)
		if one[int64](t, d, `SELECT target_id FROM gb_edges`) != paris || one[int64](t, d, `SELECT artifact_id FROM gb_edges`) != chunk {
			t.Error("the fact is not mapped")
		}
		if one[int](t, d, `SELECT COUNT(*) FROM gb_entity_artifacts WHERE entity_id = ? AND artifact_id = ?`, s.acme, chunk) != 1 {
			t.Error("the stored entity is not linked to the artifact")
		}
		if one[string](t, d, `SELECT session_id FROM gb_entity_sessions WHERE entity_id = ?`, s.acme) != fmt.Sprintf("research-%d", sub) ||
			one[string](t, d, `SELECT session_id FROM gb_edge_sessions WHERE edge_id = ?`, edge) != fmt.Sprintf("research-%d", sub) {
			t.Error("the sessions of the links are not renamed")
		}
		if one[int64](t, d, `SELECT edge_id FROM gb_edge_events`) != edge || one[int64](t, d, `SELECT artifact_id FROM gb_artifact_classes`) != chunk {
			t.Error("events or classes not mapped")
		}
		if one[int64](t, d, `SELECT entity_id FROM gb_entity_geo`) != paris {
			t.Error("the place is not mapped")
		}

		// series: merged by key; the stored series gets the missing points (the one it has stays), the summary is recomputed
		if one[int](t, d, `SELECT COUNT(*) FROM gb_series WHERE key = 'AAPL'`) != 1 || one[int](t, d, `SELECT COUNT(*) FROM gb_series WHERE key = 'GOOG'`) != 1 {
			t.Error("series not merged or not added")
		}
		aapl := one[int64](t, d, `SELECT id FROM gb_series WHERE key = 'AAPL'`)
		if n := one[int](t, d, `SELECT COUNT(*) FROM gb_series_points WHERE series_id = ?`, aapl); n != 2 {
			t.Errorf("%d points", n)
		}
		if v := one[float64](t, d, `SELECT value FROM gb_series_points WHERE series_id = ? AND ts = '2025-01-02T00:00:00Z'`, aapl); v != 99 {
			t.Errorf("the stored point was replaced: %v", v)
		}
		if one[int](t, d, `SELECT point_count FROM gb_series WHERE id = ?`, aapl) != 2 || one[string](t, d, `SELECT first_ts FROM gb_series WHERE id = ?`, aapl) != "2025-01-01T00:00:00Z" {
			t.Error("the summary of the merged series is stale")
		}
		var stats struct{ Count int }
		if err := json.Unmarshal([]byte(one[string](t, d, `SELECT stats FROM gb_series WHERE id = ?`, aapl)), &stats); err != nil || stats.Count != 2 {
			t.Errorf("stats %v %v", stats, err)
		}
		if one[int64](t, d, `SELECT entity_id FROM gb_series_anchors WHERE series_id = ?`, aapl) != s.acme {
			t.Error("the anchor is not on the stored entity")
		}

		// images: the row and the file are under the directory of the dataset in the target
		wantPath := fmt.Sprintf("%d/%s.png", s.ds, sha)
		if got := one[string](t, d, `SELECT path FROM gb_image_artifacts`); got != wantPath {
			t.Errorf("image path %q", got)
		}
		if b, err := os.ReadFile(filepath.Join(s.imageDir, filepath.FromSlash(wantPath))); err != nil || len(b) != 4 {
			t.Errorf("image file: %v %v", b, err)
		}
		if one[string](t, d, `SELECT title FROM gb_image_extractions`) != "T" || one[string](t, d, `SELECT text FROM gb_image_text_blocks`) != "T" || res.Images != 1 {
			t.Error("image extraction not imported")
		}

		// the runs of the pipelines: artifact, inferences (the unknown one is dropped) and the session in the params are mapped
		var artifactID int64
		var params, ids1, ids2, user string
		if err := d.QueryRow(`SELECT user_id, artifact_id, params FROM gb_remember_runs`).Scan(&user, &artifactID, &params); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`SELECT inference_ids FROM gb_remember_run_stages WHERE stage_type = 'extract_graph'`).Scan(&ids1); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`SELECT inference_ids FROM gb_remember_run_stages WHERE stage_type = 'summarize_chunk'`).Scan(&ids2); err != nil {
			t.Fatal(err)
		}
		if user != "u1" || artifactID != doc {
			t.Errorf("remember run: %s %d", user, artifactID)
		}
		var list1, list2 []int64
		json.Unmarshal([]byte(ids1), &list1)
		json.Unmarshal([]byte(ids2), &list2)
		if len(list1) != 1 || one[string](t, d, `SELECT stage FROM gb_inferences WHERE id = ?`, list1[0]) != "extract_graph" || len(list2) != 1 || one[string](t, d, `SELECT stage FROM gb_inferences WHERE id = ?`, list2[0]) != "summarize_chunk" {
			t.Errorf("inference ids %s %s", ids1, ids2)
		}
		if !strings.Contains(params, fmt.Sprintf(`"session":"research-%d"`, sub)) || !strings.Contains(params, `"other":"x"`) {
			t.Errorf("params %s", params)
		}
		run := one[int64](t, d, `SELECT id FROM gb_remember_runs`)
		if one[int64](t, d, `SELECT run_id FROM gb_remember_run_stage_outputs`) != run {
			t.Error("stage output not mapped")
		}
		if one[string](t, d, `SELECT session_id FROM gb_query_runs`) != "research-7" || one[string](t, d, `SELECT user_id FROM gb_query_runs`) != "u1" {
			t.Error("query run")
		}
		qrun := one[int64](t, d, `SELECT id FROM gb_query_runs`)
		if one[int64](t, d, `SELECT item_id FROM gb_graph_in_ctx_counters WHERE kind = 'entity'`) != s.acme || one[int64](t, d, `SELECT run_id FROM gb_graph_in_ctx_counters WHERE kind = 'entity'`) != run ||
			one[int64](t, d, `SELECT item_id FROM gb_graph_in_ctx_counters WHERE kind = 'edge'`) != edge || one[int64](t, d, `SELECT run_id FROM gb_graph_in_ctx_counters WHERE kind = 'edge'`) != qrun {
			t.Error("counters not mapped")
		}

		// the sub-agent: a child of the server's run, its state, and its end (it was still running when the container ended)
		var status, runner, user2, name, errText string
		if err := d.QueryRow(`SELECT status, runner, user_id, name, error FROM gb_agent_research_runs WHERE id = ?`, sub).Scan(&status, &runner, &user2, &name, &errText); err != nil {
			t.Fatal(err)
		}
		if status != "failed" || runner != "modal" || user2 != "u1" || name != "worker" || !strings.Contains(errText, "interrupted") {
			t.Errorf("sub-agent: %s %s %s %s %q", status, runner, user2, name, errText)
		}
		if one[int](t, d, `SELECT COUNT(*) FROM gb_agent_research_states WHERE run_id = 7`) != 1 || one[string](t, d, `SELECT detail FROM gb_agent_research_states WHERE run_id = ?`, sub) != "sub state" {
			t.Error("states")
		}
		if one[string](t, d, `SELECT status FROM gb_agent_research_runs WHERE id = 7`) != "running" {
			t.Error("the run of the server was changed")
		}

		// vectors: new ones under the mapped IDs and metadata; the vector of the merged entity is not copied over the stored one
		ents := fmt.Sprintf("dataset-%d-entities", s.ds)
		got := map[string]vector.Match{}
		ms, err := s.vs.Query(ctx, ents, []float32{1, 1, 1}, vector.QueryOptions{TopK: 10, IncludeValues: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			got[m.ID] = m
		}
		if len(got) != 3 || got[fmt.Sprintf("entity-%d", paris)].Metadata["entity_id"] != float64(paris) || got[fmt.Sprintf("entity-%d", berlin)].ID == "" {
			t.Errorf("entity vectors: %+v", got)
		}
		if a := got[fmt.Sprintf("entity-%d", s.acme)]; len(a.Values) != dims || a.Values[0] < 0.57 || a.Values[0] > 0.58 { // (0.5, 0.5, 0.5) normalized
			t.Errorf("the stored vector of the merged entity was replaced: %v", a.Values)
		}
		edges, _ := s.vs.Query(ctx, fmt.Sprintf("dataset-%d-edges", s.ds), []float32{1, 1, 0}, vector.QueryOptions{TopK: 5})
		if len(edges) != 1 || edges[0].ID != fmt.Sprintf("edge-%d", edge) || edges[0].Metadata["edge_id"] != float64(edge) {
			t.Errorf("edge vectors: %+v", edges)
		}
		chunks, _ := s.vs.Query(ctx, fmt.Sprintf("dataset-%d-chunks", s.ds), []float32{1, 0, 1}, vector.QueryOptions{TopK: 5})
		if len(chunks) != 1 || chunks[0].ID != fmt.Sprintf("chunk-%d", chunk) || chunks[0].Metadata["artifact_id"] != float64(chunk) || chunks[0].Metadata["parent_id"] != float64(doc) {
			t.Errorf("chunk vectors: %+v", chunks)
		}
		sums, _ := s.vs.Query(ctx, fmt.Sprintf("dataset-%d-summaries", s.ds), []float32{0, 1, 1}, vector.QueryOptions{TopK: 5})
		if len(sums) != 1 || sums[0].ID != fmt.Sprintf("summary-%d", summary) || sums[0].Metadata["chunk_id"] != float64(chunk) {
			t.Errorf("summary vectors: %+v", sums)
		}
		if res.Vectors != 5 || res.VectorsMerged != 1 || res.VectorsSkipped != 1 { // the vector of the deleted dataset is not imported
			t.Errorf("vectors: %+v", res)
		}
	})
}

func TestImportWithoutRootRun(t *testing.T) {
	archive := agentArchive(t)
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		if backendOf(d) != config.DBBackendSQLite {
			t.Skip("SQLite fixture")
		}
		s := newServer(t, d)
		r, err := dbexport.Open(archive)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		res, err := Import(context.Background(), r, s.target(), Options{UserID: "u1", SessionPrefix: "research-"})
		if err != nil {
			t.Fatal(err)
		}
		if n := one[int](t, d, `SELECT COUNT(*) FROM gb_agent_research_runs`); n != 0 {
			t.Errorf("%d agent runs", n)
		}
		if res.Tables["gb_agent_research_runs"].Skipped != 2 {
			t.Errorf("%+v", res.Tables["gb_agent_research_runs"])
		}
		// the sessions are left as they were
		if one[int](t, d, `SELECT COUNT(*) FROM gb_artifacts WHERE session_id = 'research-1'`) != 1 {
			t.Error("sessions were renamed without runs to map them to")
		}
	})
}

func TestImportVectorsNeedTheSameEmbeddingSize(t *testing.T) {
	archive := agentArchive(t)
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		if backendOf(d) != config.DBBackendSQLite {
			t.Skip("SQLite fixture")
		}
		s := newServer(t, d)
		r, err := dbexport.Open(archive)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		tg := s.target()
		tg.EmbeddingDimensions = 8
		res, err := Import(context.Background(), r, tg, Options{UserID: "u1"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Vectors != 0 || res.VectorsSkipped != 7 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "8") {
			t.Errorf("%+v", res)
		}
		if one[int](t, d, `SELECT COUNT(*) FROM gb_entities`) != 3 {
			t.Error("the rows must be imported anyway")
		}
	})
}

func TestImportFailureLeavesTheDatabaseAsItWas(t *testing.T) {
	archive := agentArchive(t)
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		if backendOf(d) != config.DBBackendSQLite {
			t.Skip("SQLite fixture")
		}
		s := newServer(t, d)
		// A table of the target that lacks a NOT NULL column the archive has no value for makes an insert fail late in the import.
		ex(t, d, `DROP TABLE gb_image_text_blocks`)
		ex(t, d, `CREATE TABLE gb_image_text_blocks (id INTEGER PRIMARY KEY AUTOINCREMENT, extraction_id INTEGER NOT NULL REFERENCES gb_image_extractions(id), position INTEGER NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, must_have TEXT NOT NULL)`)
		r, err := dbexport.Open(archive)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if _, err := Import(context.Background(), r, s.target(), Options{UserID: "u1"}); err == nil || !strings.Contains(err.Error(), "gb_image_text_blocks") {
			t.Fatalf("got %v", err)
		}
		if n := one[int](t, d, `SELECT COUNT(*) FROM gb_artifacts`) + one[int](t, d, `SELECT COUNT(*) FROM gb_inferences`) + one[int](t, d, `SELECT COUNT(*) FROM gb_series`); n != 0 {
			t.Errorf("%d rows were left behind", n)
		}
		if n := one[int](t, d, `SELECT COUNT(*) FROM gb_entities`); n != 1 {
			t.Errorf("%d entities", n)
		}
	})
}

func TestImportNeedsAUser(t *testing.T) {
	if _, err := Import(context.Background(), nil, Target{Relational: &sql.DB{}, Backend: config.DBBackendSQLite}, Options{}); err == nil {
		t.Fatal("expected an error")
	}
}
