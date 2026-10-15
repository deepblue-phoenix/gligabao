package remember

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_image"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

const imageJSON = `{"theme":"Plot","title":"Revenue","text_blocks":[
	{"kind":"title","text":"Revenue"},{"kind":"axis","text":"Year"},{"kind":"legend","text":"EU"}]}`

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 3))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRememberImage(t *testing.T) { dbtest.Run(t, testRememberImage) }

func testRememberImage(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	imgDir := filepath.Join(t.TempDir(), "images")
	data := pngBytes(t)
	b64 := base64.StdEncoding.EncodeToString(data)

	var calls int
	llm := llmFunc(func(req ai.Request) (*ai.Response, error) {
		calls++
		if len(req.Messages) != 1 || len(req.Messages[0].Images) != 1 {
			t.Errorf("the image was not sent: %+v", req.Messages)
		}
		return &ai.Response{Text: imageJSON, StopReason: "end_turn", Provider: "anthropic", Model: "claude-haiku-5-5", InputTokens: 100, OutputTokens: 20, CostUSD: 0.001}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm, memory_image.WithImageDir(imgDir)))
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		return rec
	}

	rec := post(`{"user_id":"u1","dataset":"imgs","image":"` + b64 + `","reference_time":"2025-01-31T09:00:00Z"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res memory_image.ImageResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Pipeline != "image" || res.Dataset != "imgs" || res.MediaType != "image/png" || res.SizeBytes != len(data) ||
		res.Theme != "plot" || res.Title != "Revenue" || len(res.TextBlocks) != 3 || res.Text != "Revenue\nYear\nEU" ||
		res.ValidAt.Format("2006-01-02T15:04:05Z") != "2025-01-31T09:00:00Z" || res.SHA256 == "" {
		t.Errorf("result: %+v", res)
	}

	dsID, err := relational.DatasetID(ctx, d, "u1", "imgs")
	if err != nil {
		t.Fatal(err)
	}
	arts, err := graph.ListImageArtifacts(ctx, d, dsID)
	if err != nil || len(arts) != 1 || arts[0].ID != res.ImageArtifactID || arts[0].SHA256 != res.SHA256 || arts[0].SizeBytes != int64(len(data)) || arts[0].MediaType != "image/png" {
		t.Fatalf("image artifacts: %+v, %v", arts, err)
	}
	// The bytes are on the file system, in the image directory; the row only tracks the path.
	wantPath := fmt.Sprintf("%d/%s.png", dsID, res.SHA256)
	if arts[0].Path != wantPath || res.Path != wantPath {
		t.Errorf("path = %q (result %q), want %q", arts[0].Path, res.Path, wantPath)
	}
	if got, err := os.ReadFile(filepath.Join(imgDir, filepath.FromSlash(arts[0].Path))); err != nil || !bytes.Equal(got, data) {
		t.Errorf("image file: %v", err)
	}
	if got, err := memory_image.LoadImage(ctx, d, "u1", "imgs", arts[0].ID, memory_image.WithImageDir(imgDir)); err != nil || !bytes.Equal(got, data) {
		t.Errorf("LoadImage: %v", err)
	}
	if _, err := memory_image.LoadImage(ctx, d, "u1", "imgs", 999, memory_image.WithImageDir(imgDir)); err == nil {
		t.Error("LoadImage of a missing artifact: no error")
	}
	if n := countFiles(t, imgDir); n != 1 {
		t.Errorf("files = %d, want 1", n)
	}
	xs, err := graph.ListImageExtractions(ctx, d, dsID)
	if err != nil || len(xs) != 1 {
		t.Fatalf("extractions: %+v, %v", xs, err)
	}
	x := xs[0]
	if x.ID != res.ExtractionID || x.ImageArtifactID != arts[0].ID || x.Theme != "plot" || x.Title != "Revenue" || x.Model != "claude-haiku-5-5" ||
		x.InputTokens != 100 || x.OutputTokens != 20 || x.CostUSD != 0.001 || x.Raw != imageJSON ||
		len(x.TextBlocks) != 3 || x.TextBlocks[1].Kind != "axis" || x.TextBlocks[1].Text != "Year" {
		t.Errorf("stored extraction: %+v", x)
	}

	// The image pipeline is separate: nothing went into the text artifacts or the graph.
	if c, err := graph.DatasetCounts(ctx, d, dsID); err != nil || c.Artifacts != 0 || c.Entities != 0 || c.Edges != 0 {
		t.Errorf("counts: %+v, %v", c, err)
	}
	// Its LLM call is recorded under its own type.
	if stats, err := relational.ModelRunStats(ctx, d); err != nil || stats[memory_image.CallRememberImage].Runs != 1 || stats[memory_remember.CallRemember].Runs != 0 {
		t.Errorf("model run stats: %+v, %v", stats, err)
	}

	// Rejected before any LLM call, with nothing stored.
	for name, body := range map[string]string{
		"not base64":   `{"user_id":"u1","dataset":"imgs","image":"@@@"}`,
		"not an image": `{"user_id":"u1","dataset":"imgs","image":"` + base64.StdEncoding.EncodeToString([]byte("hello")) + `"}`,
		"with text":    `{"user_id":"u1","dataset":"imgs","image":"` + b64 + `","text":"hi"}`,
		"with kind":    `{"user_id":"u1","dataset":"imgs","image":"` + b64 + `","kind":"episode"}`,
		"no user":      `{"dataset":"imgs","image":"` + b64 + `"}`,
		"bad time":     `{"user_id":"u1","dataset":"imgs","image":"` + b64 + `","reference_time":"yesterday"}`,
	} {
		if rec := post(body); rec.Code != 400 {
			t.Errorf("%s: status %d, want 400: %s", name, rec.Code, rec.Body)
		}
	}
	if calls != 1 {
		t.Errorf("LLM calls = %d, want 1", calls)
	}
	if arts, _ := graph.ListImageArtifacts(ctx, d, dsID); len(arts) != 1 || countFiles(t, imgDir) != 1 {
		t.Errorf("a rejected request stored an image: %d artifacts, %d files", len(arts), countFiles(t, imgDir))
	}

	// The same image again (even with another reference time) is not read again: the stored result comes back, with
	// nothing sent to the LLM and nothing new stored.
	if res.Cached {
		t.Error("the first result is cached")
	}
	rec = post(`{"user_id":"u1","dataset":"imgs","image":"` + b64 + `","reference_time":"2030-01-01T00:00:00Z"}`)
	if rec.Code != 200 {
		t.Fatalf("second image: status %d: %s", rec.Code, rec.Body)
	}
	var again memory_image.ImageResult
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if !again.Cached || again.ImageArtifactID != res.ImageArtifactID || again.ExtractionID != res.ExtractionID || again.Path != res.Path ||
		again.Theme != "plot" || again.Title != "Revenue" || len(again.TextBlocks) != 3 || again.Text != res.Text ||
		again.Model != "claude-haiku-5-5" || again.InputTokens != 100 || !again.ValidAt.Equal(res.ValidAt) || !again.CreatedAt.Equal(res.CreatedAt) {
		t.Errorf("cached result: %+v\nfirst: %+v", again, res)
	}
	if calls != 1 {
		t.Errorf("LLM calls = %d after the repeat, want 1", calls)
	}
	if arts, _ := graph.ListImageArtifacts(ctx, d, dsID); len(arts) != 1 {
		t.Errorf("a repeated image stored another artifact: %d", len(arts))
	}
	if xs, _ := graph.ListImageExtractions(ctx, d, dsID); len(xs) != 1 {
		t.Errorf("a repeated image stored another extraction: %d", len(xs))
	}
	if stats, _ := relational.ModelRunStats(ctx, d); stats[memory_image.CallRememberImage].Runs != 1 {
		t.Errorf("model run stats: %+v", stats)
	}

	// A file that went missing is restored from the repeated upload.
	if err := os.Remove(filepath.Join(imgDir, filepath.FromSlash(res.Path))); err != nil {
		t.Fatal(err)
	}
	if rec := post(`{"user_id":"u1","dataset":"imgs","image":"` + b64 + `"}`); rec.Code != 200 {
		t.Fatalf("third image: status %d: %s", rec.Code, rec.Body)
	}
	if got, err := os.ReadFile(filepath.Join(imgDir, filepath.FromSlash(res.Path))); err != nil || !bytes.Equal(got, data) {
		t.Errorf("file not restored: %v", err)
	}

	// Another dataset does not know the image: it is read (and stored) there.
	rec = post(`{"user_id":"u1","dataset":"other","image":"` + b64 + `"}`)
	var other memory_image.ImageResult
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &other) != nil || other.Cached || calls != 2 {
		t.Errorf("other dataset: status %d, %+v, calls %d", rec.Code, other, calls)
	}
	// So does another user's dataset of the same name.
	rec = post(`{"user_id":"u2","dataset":"imgs","image":"` + b64 + `"}`)
	if rec.Code != 200 || calls != 3 {
		t.Errorf("other user: status %d, calls %d", rec.Code, calls)
	}

	// A different image is read.
	var b2 bytes.Buffer
	png.Encode(&b2, image.NewRGBA(image.Rect(0, 0, 5, 5)))
	rec = post(`{"user_id":"u1","dataset":"imgs","image":"` + base64.StdEncoding.EncodeToString(b2.Bytes()) + `"}`)
	var diff memory_image.ImageResult
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &diff) != nil || diff.Cached || diff.SHA256 == res.SHA256 || calls != 4 {
		t.Errorf("different image: status %d, %+v, calls %d", rec.Code, diff, calls)
	}
	if arts, _ := graph.ListImageArtifacts(ctx, d, dsID); len(arts) != 2 {
		t.Errorf("artifacts = %d, want 2", len(arts))
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestRememberImageLLMFailure(t *testing.T) { dbtest.Run(t, testRememberImageLLMFailure) }

func testRememberImageLLMFailure(t *testing.T, d *sql.DB) {
	llm := llmFunc(func(ai.Request) (*ai.Response, error) {
		return &ai.Response{Text: "I cannot read this.", StopReason: "end_turn"}, nil
	})
	mux := http.NewServeMux()
	imgDir := t.TempDir()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm, memory_image.WithImageDir(imgDir)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember",
		strings.NewReader(`{"user_id":"u1","image":"`+base64.StdEncoding.EncodeToString(pngBytes(t))+`"}`)))
	if rec.Code != 502 {
		t.Fatalf("status %d, want 502: %s", rec.Code, rec.Body)
	}
	if _, err := relational.DatasetID(context.Background(), d, "u1", relational.DefaultDataset); err == nil {
		t.Error("a failed request created the dataset")
	}
	if n := countFiles(t, imgDir); n != 0 {
		t.Errorf("a failed request left %d files", n)
	}
}

type llmFunc func(ai.Request) (*ai.Response, error)

func (f llmFunc) Complete(_ context.Context, req ai.Request) (*ai.Response, error) { return f(req) }

func TestRememberImageEval(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	llm := llmFunc(func(ai.Request) (*ai.Response, error) {
		return &ai.Response{Text: imageJSON, StopReason: "end_turn", Model: "m"}, nil
	})
	rec := httptest.NewRecorder()
	testEvalHandler(t, d, llm, memory_image.WithImageDir(t.TempDir())).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/eval/remember",
		strings.NewReader(`{"user_id":"u1","image":"`+base64.StdEncoding.EncodeToString(pngBytes(t))+`"}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res memory_image.ImageResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.TrajectoryID == "" || res.Trajectory == nil {
		t.Fatalf("no trajectory: %s", rec.Body)
	}
	if b, _ := json.Marshal(res.Trajectory); !strings.Contains(string(b), "[image: image/png, ") || strings.Contains(string(b), "iVBOR") {
		t.Errorf("the trajectory should describe the image, not embed it: %s", b)
	}
}

func TestRememberImageSession(t *testing.T) { dbtest.Run(t, testRememberImageSession) }

func testRememberImageSession(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	imgDir := filepath.Join(t.TempDir(), "images")
	b64 := base64.StdEncoding.EncodeToString(pngBytes(t))
	llm := llmFunc(func(ai.Request) (*ai.Response, error) {
		return &ai.Response{Text: imageJSON, StopReason: "end_turn", Model: "claude-haiku-5-5"}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm, memory_image.WithImageDir(imgDir)))
	post := func(body string) memory_image.ImageResult {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var res memory_image.ImageResult
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	first := post(`{"user_id":"u1","dataset":"imgs","image":"` + b64 + `","session_id":"sess-a"}`)
	if first.SessionID != "sess-a" || first.Cached {
		t.Errorf("first: session %q, cached %v", first.SessionID, first.Cached)
	}
	// The same image from another session is the cached one, which keeps the session that stored it.
	again := post(`{"user_id":"u1","dataset":"imgs","image":"` + b64 + `","session_id":"sess-b"}`)
	if !again.Cached || again.ImageArtifactID != first.ImageArtifactID || again.SessionID != "sess-a" {
		t.Errorf("repeat: %+v", again)
	}
	dsID, err := relational.DatasetID(ctx, d, "u1", "imgs")
	if err != nil {
		t.Fatal(err)
	}
	if arts, err := graph.ListImageArtifacts(ctx, d, dsID); err != nil || len(arts) != 1 || arts[0].SessionID != "sess-a" {
		t.Errorf("image artifacts: %+v, %v", arts, err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(`{"user_id":"u1","dataset":"imgs","image":"`+b64+`","session_id":"a\u0000b"}`)))
	if rec.Code != 400 {
		t.Errorf("invalid session status = %d, want 400: %s", rec.Code, rec.Body)
	}
}
