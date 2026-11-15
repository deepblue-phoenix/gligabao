package remember

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_image"

	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil/httputiltest"
)

const imageGraphJSON = `{"theme":"graph","title":"Org","description":"Alice reports to Bob.","text_blocks":[{"kind":"label","text":"Alice"},{"kind":"label","text":"Bob"}]}`

const imageGraphExtractionJSON = `{"entities":[{"name":"Alice","type":"person"},{"name":"Bob","type":"person"}],
	"edges":[{"source":"Alice","target":"Bob","relation":"reports to"}]}`

func TestRememberImageGraph(t *testing.T) { dbtest.Run(t, testRememberImageGraph) }

func testRememberImageGraph(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	imgDir := filepath.Join(t.TempDir(), "images")
	b64 := base64.StdEncoding.EncodeToString(pngBytes(t))

	var imageCalls, textCalls int
	lm := llmFunc(func(req llm.Request) (*llm.Response, error) {
		text := imageGraphExtractionJSON
		if len(req.Messages) > 0 && len(req.Messages[0].Images) > 0 {
			imageCalls++
			text = imageGraphJSON
		} else {
			textCalls++
			if !strings.Contains(req.Messages[0].Content, "Alice reports to Bob.") {
				t.Errorf("the graph was not extracted from the description: %q", req.Messages[0].Content)
			}
		}
		return &llm.Response{
			Text:       text,
			StopReason: "end_turn",
			Provider:   "anthropic",
			Model:      "m",
		}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t,
		d,
		lm,
		memory_image.WithImageDir(imgDir)))
	post := func() memory_image.ImageResult {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httputiltest.NewRequest("POST",
			"/v1/gb/remember",
			strings.NewReader(`{"user_id":"u1","dataset":"imgs","image":"`+b64+`"}`)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s",
				rec.Code,
				rec.Body)
		}
		var res memory_image.ImageResult
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	res := post()
	if res.Description != "Alice reports to Bob." || res.ArtifactID == 0 || len(res.Entities) != 2 || len(res.Edges) != 1 || res.Edges[0].Relation != "reports_to" {
		t.Fatalf("result: %+v", res)
	}
	dsID, err := relational.DatasetID(ctx,
		d,
		"u1",
		"imgs")
	if err != nil {
		t.Fatal(err)
	}
	// The graph is stored and belongs to the text artifact the description became.
	if c, err := graph.DatasetCounts(ctx,
		d,
		dsID); err != nil || c.Artifacts != 1 || c.Entities != 2 || c.Edges != 1 {
		t.Errorf("counts: %+v, %v",
			c,
			err)
	}
	g, err := graph.GetImageGraph(ctx,
		d,
		res.ExtractionID)
	if err != nil || g.ArtifactID != res.ArtifactID || g.Description != "Alice reports to Bob." {
		t.Errorf("image graph: %+v, %v",
			g,
			err)
	}

	// A repeat is answered from the store: same artifact, no LLM call.
	again := post()
	if !again.Cached || again.ArtifactID != res.ArtifactID || again.Description != res.Description || imageCalls != 1 || textCalls != 1 {
		t.Errorf("cached: %+v, image calls %d, text calls %d",
			again,
			imageCalls,
			textCalls)
	}
}
