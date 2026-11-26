package memory_image

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

func TestImageGraphExtractorHook(t *testing.T) { dbtest.Run(t, testImageGraphExtractorHook) }

// The graph extractor hook gets the graph step of an image as ContentImage, with the image itself and the description the LLM read; when it
// handles the request, the LLM is called only to read the image (not to extract the graph).
func testImageGraphExtractorHook(t *testing.T,
	d *sql.DB) {
	var seen llm.Request
	data := testPNG(t)
	lm := imageLLM{`{"theme":"graph","title":"T","description":"A calls B.","text_blocks":[]}`, &seen}
	var got []memory_core.GraphExtractRequest
	hook := memory_core.WithGraphExtractor(func(_ context.Context,
		req memory_core.GraphExtractRequest) (memory_core.GraphExtractResult, error) {
		got = append(got, req)
		return memory_core.GraphExtractResult{
			Entities: []graph.Entity{{Name: "A", Type: "thing"}, {Name: "B", Type: "thing"}},
			Edges:    []graph.Edge{{Source: "A", Target: "B", Relation: "calls"}},
		}, nil
	})
	res, err := RememberImage(context.Background(),
		d,
		lm,
		"u1",
		"imgs",
		data,
		time.Time{},
		hook,
		WithImageDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ContentType != memory_core.ContentImage || got[0].Image == nil || !bytes.Equal(got[0].Image.Data, data) ||
		got[0].Image.MediaType != "image/png" || got[0].Image.SHA256 != res.SHA256 || got[0].Text == "" {
		t.Fatalf("hook requests = %+v", got)
	}
	if len(res.Entities) != 2 || len(res.Edges) != 1 || res.ArtifactID == 0 {
		t.Errorf("result = %+v", res)
	}
}
