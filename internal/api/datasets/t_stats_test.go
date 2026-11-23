package datasets_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/datasets"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil/httputiltest"
)

// TestStats checks GET /v1/gb/datasets/{name}/stats: counts of the artifacts per kind and the depth of the artifact hierarchy.
func TestStats(t *testing.T) { dbtest.Run(t, testStats) }

func testStats(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ds, err := relational.CreateDataset(ctx,
		d,
		userID,
		"statsds",
		"",
		nil)
	must(err)
	now := time.Date(2025,
		3,
		1,
		9,
		0,
		0,
		0,
		time.UTC)
	tx, err := graph.Begin(ctx, d)
	must(err)
	defer tx.Rollback()
	// a root with a chunk that has a summary (depths 1, 2, 3) and a lone root (depth 1)
	root, err := tx.InsertArtifact(ctx,
		ds.ID,
		graph.ArtifactEpisode,
		"long text",
		"",
		now,
		now)
	must(err)
	chunk, err := tx.InsertChunkArtifact(ctx,
		graph.NewChunkArtifact{
			DatasetID: ds.ID,
			ParentID:  root,
			Kind:      graph.ArtifactEpisode,
			Text:      "chunk",
			Tokens:    3,
			ValidAt:   now,
			CreatedAt: now,
		})
	must(err)
	_, err = tx.InsertChunkSummaryArtifact(ctx,
		graph.NewChunkSummaryArtifact{
			DatasetID: ds.ID,
			ParentID:  chunk,
			Text:      "summary",
			ValidAt:   now,
			CreatedAt: now,
		})
	must(err)
	_, err = tx.InsertArtifact(ctx,
		ds.ID,
		graph.ArtifactEpisode,
		"short",
		"",
		now,
		now)
	must(err)
	must(tx.Commit())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/gb/datasets/{name}/stats", datasets.Stats(d))
	get := func(path string,
		want int) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httputiltest.NewRequest("GET",
			path,
			nil))
		if rec.Code != want {
			t.Fatalf("GET %s: status %d, want %d: %s",
				path,
				rec.Code,
				want,
				rec.Body)
		}
		return rec
	}
	var got struct {
		Dataset   string
		Artifacts struct {
			Total, Roots, Chunks, Summaries int
			MaxDepth                        int     `json:"max_depth"`
			AvgDepth                        float64 `json:"avg_depth"`
			ByKind                          []struct {
				Kind  string
				Count int
			} `json:"by_kind"`
		}
	}
	must(json.Unmarshal(get("/v1/gb/datasets/statsds/stats?user_id="+userID, 200).Body.Bytes(), &got))
	a := got.Artifacts
	if got.Dataset != "statsds" || a.Total != 4 || a.Roots != 2 || a.Chunks != 1 || a.Summaries != 1 || a.MaxDepth != 3 || a.AvgDepth != 1.75 {
		t.Errorf("stats = %+v", got)
	}
	if len(a.ByKind) != 2 || a.ByKind[0].Kind != "episode" || a.ByKind[0].Count != 3 {
		t.Errorf("by kind = %+v", a.ByKind)
	}
	get("/v1/gb/datasets/nope/stats?user_id="+userID, 404)
}
