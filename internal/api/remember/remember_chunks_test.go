package remember

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

func TestRememberLongText(t *testing.T) { dbtest.Run(t, testRememberLongText) }

func testRememberLongText(t *testing.T, d *sql.DB) {
	llm := fakeLLM{`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm, memory_core.WithChunking(memory_chunk.Config{Size: 40, OverlapPercent: 20})))

	var paras []string
	for i := 0; i < 8; i++ {
		paras = append(paras, "Alice walked along the river and talked about the weather and her plans for the summer holidays.")
	}
	body, _ := json.Marshal(map[string]string{"user_id": "u1", "text": strings.Join(paras, "\n\n")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(string(body))))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res struct {
		ArtifactID int64 `json:"artifact_id"`
		Chunks     []struct {
			ArtifactID int64 `json:"artifact_id"`
			Index      int   `json:"index"`
			Tokens     int   `json:"tokens"`
			Summary    struct {
				ArtifactID  int64  `json:"artifact_id"`
				Text        string `json:"text"`
				Model       string `json:"model"`
				Reasoning   string `json:"reasoning"`
				InferenceID int64  `json:"inference_id"`
			} `json:"summary"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) < 4 {
		t.Fatalf("chunks = %d: %s", len(res.Chunks), rec.Body)
	}
	dsID, _ := relational.DatasetID(context.Background(), d, "u1", relational.DefaultDataset)
	arts, _ := graph.ListArtifacts(context.Background(), d, dsID)
	if len(arts) != 2*len(res.Chunks)+1 || arts[0].ID != res.ArtifactID {
		t.Errorf("artifacts = %d, parent %d", len(arts), res.ArtifactID)
	}
	byID := map[int64]graph.Artifact{}
	var chunks []graph.Artifact
	for _, a := range arts {
		byID[a.ID] = a
		if a.IsChunk() {
			chunks = append(chunks, a)
		}
	}
	for i, c := range res.Chunks {
		if c.Index != i || c.Tokens > 40 || chunks[i].ID != c.ArtifactID || chunks[i].ParentID != res.ArtifactID {
			t.Errorf("chunk %d = %+v, artifact %+v", i, c, chunks[i])
		}
		// Its summary is an artifact of its own, a child of the chunk, that references the inference (model and reasoning level) that wrote it.
		s := byID[c.Summary.ArtifactID]
		if s.Kind != graph.ArtifactChunkSummary || s.ParentID != c.ArtifactID || s.Text != c.Summary.Text || s.Text == "" || s.IsChunk() || !s.IsDerived() ||
			s.InferenceID == 0 || s.InferenceID != c.Summary.InferenceID || c.Summary.Reasoning != "none" {
			t.Errorf("chunk %d: summary %+v, artifact %+v", i, c.Summary, s)
		}
	}
}
