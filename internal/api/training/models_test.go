package training_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
)

func TestTrainedModelEndpoints(t *testing.T) { dbtest.Run(t, testTrainedModelEndpoints) }

func testTrainedModelEndpoints(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	for _, m := range []trainingdb.TrainedModel{
		{JobID: "j1", ModelSize: "4b", NumTrainSteps: 10, Path: "trained/j1"},
		{JobID: "j2", ModelSize: "12b", NumTrainSteps: 20, Path: "trained/j2", Dataset: "mine.jsonl"},
	} {
		if _, _, err := trainingdb.RecordTrainedModel(ctx, d, m); err != nil {
			t.Fatal(err)
		}
	}
	get := func(h http.HandlerFunc, url, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", url, nil)
		if path != "" {
			req.SetPathValue("id", path)
		}
		h(rec, req)
		return rec
	}

	rec := get(training.ListModelsOf(d), "/x", "")
	var list struct{ Models []trainingdb.TrainedModel }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 || len(list.Models) != 2 ||
		list.Models[0].JobID != "j2" || list.Models[1].JobID != "j1" || list.Models[0].Files == nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(get(training.ListModelsOf(d), "/x?limit=1", "").Body.Bytes(), &list); err != nil || len(list.Models) != 1 {
		t.Fatalf("limit: %+v", list)
	}
	for url, want := range map[string]int{"/x?limit=0": 400, "/x?limit=a": 400} {
		if rec := get(training.ListModelsOf(d), url, ""); rec.Code != want {
			t.Fatalf("%s: %d", url, rec.Code)
		}
	}
	rec = get(training.GetModelOf(d), "/x", "2")
	var m trainingdb.TrainedModel
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m.JobID != "j2" || m.Dataset != "mine.jsonl" {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	for id, want := range map[string]int{"99": 404, "0": 400, "x": 400} {
		if rec := get(training.GetModelOf(d), "/x", id); rec.Code != want {
			t.Fatalf("get %s: %d", id, rec.Code)
		}
	}
}

func TestInferWithATrainedModel(t *testing.T) { dbtest.Run(t, testInferWithATrainedModel) }

func testInferWithATrainedModel(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	id, _, err := trainingdb.RecordTrainedModel(ctx, d, trainingdb.TrainedModel{JobID: "j2", ModelSize: "12b", NumTrainSteps: 20, Path: "trained/j2"})
	if err != nil {
		t.Fatal(err)
	}
	sc := &fakeSidecar{inferResp: sidecar.InferResponse{Response: "hello", InputTokens: 7, OutputTokens: 3}}
	post := func(sc core.Inferer, modelID, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		req.SetPathValue("id", modelID)
		training.InferOf(d, sc)(rec, req)
		return rec
	}

	rec := post(sc, "1", `{"prompt": "hi", "max_tokens": 50, "temperature": 0.2}`)
	if rec.Code != 200 || id != 1 {
		t.Fatalf("infer: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		ID       int    `json:"id"`
		ModelID  int64  `json:"model_id"`
		Response string `json:"response"`
		InTok    int    `json:"input_tokens"`
		OutTok   int    `json:"output_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ModelID != 1 || out.Response != "hello" || out.InTok != 7 || out.OutTok != 3 {
		t.Fatalf("response: %s", rec.Body)
	}
	// The weights and size to load come from the model's record, not from the client.
	if len(sc.infers) != 1 || sc.infers[0].ModelPath != "trained/j2" || sc.infers[0].ModelSize != "12b" || sc.infers[0].Prompt != "hi" ||
		sc.infers[0].MaxTokens != 50 || sc.infers[0].Temperature == nil || *sc.infers[0].Temperature != 0.2 {
		t.Fatalf("sidecar request: %+v", sc.infers)
	}

	for name, c := range map[string]struct {
		id, body string
		want     int
	}{
		"unknown model": {"9", `{"prompt":"hi"}`, 404},
		"bad id":        {"x", `{"prompt":"hi"}`, 400},
		"no prompt":     {"1", `{}`, 400},
		"bad json":      {"1", `{`, 400},
		"max tokens":    {"1", `{"prompt":"hi","max_tokens":999999}`, 400},
		"temperature":   {"1", `{"prompt":"hi","temperature":0}`, 400},
	} {
		if rec := post(sc, c.id, c.body); rec.Code != c.want {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := post(nil, "1", `{"prompt":"hi"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no sidecar: %d", rec.Code)
	}
	sc.inferErr = &sidecar.InferError{Message: "No trained model at 'trained/j2'"}
	if rec := post(sc, "1", `{"prompt":"hi"}`); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "No trained model") {
		t.Fatalf("generation failed: %d %s", rec.Code, rec.Body)
	}
	sc.inferErr = errors.New("connection refused")
	if rec := post(sc, "1", `{"prompt":"hi"}`); rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "refused") {
		t.Fatalf("sidecar down: %d %s", rec.Code, rec.Body)
	}

	// Every request that reached the sidecar is stored, with its outcome.
	list, err := trainingdb.ListTrainedModelInferences(ctx, d, 1, 0)
	if err != nil || len(list) != 3 {
		t.Fatalf("stored requests: %+v %v", list, err)
	}
	if r := list[2]; r.Status != trainingdb.TrainedModelInferenceCompleted || r.Response != "hello" || r.Prompt != "hi" || r.ModelPath != "trained/j2" ||
		r.ModelSize != "12b" || r.MaxTokens != 50 || r.Temperature == nil || *r.Temperature != 0.2 || r.ID != int64(out.ID) {
		t.Fatalf("completed request: %+v", r)
	}
	if r := list[1]; r.Status != trainingdb.TrainedModelInferenceFailed || !strings.Contains(r.Error, "No trained model") || r.Response != "" {
		t.Fatalf("failed request: %+v", r)
	}
	if r := list[0]; r.Status != trainingdb.TrainedModelInferenceFailed || !strings.Contains(r.Error, "refused") {
		t.Fatalf("sidecar down request: %+v", r)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x?limit=2", nil)
	req.SetPathValue("id", "1")
	training.ListInferencesOf(d)(rec, req)
	var listed struct {
		Inferences []trainingdb.TrainedModelInference `json:"inferences"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || rec.Code != 200 || len(listed.Inferences) != 2 || listed.Inferences[0].ID != list[0].ID {
		t.Fatalf("list inferences: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", "9")
	training.ListInferencesOf(d)(rec, req)
	if rec.Code != 404 {
		t.Fatalf("list inferences of unknown model: %d", rec.Code)
	}
}

type fakeFetcher struct{ files map[string]string }

func (f fakeFetcher) DownloadModelFile(ctx context.Context, modelPath, file string, w io.Writer) (int64, error) {
	content, ok := f.files[modelPath+"/"+file]
	if !ok {
		return 0, errors.New("no such file")
	}
	n, err := io.WriteString(w, content)
	return int64(n), err
}

func TestExportATrainedModel(t *testing.T) { dbtest.Run(t, testExportATrainedModel) }

func testExportATrainedModel(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	sum := sha256.Sum256([]byte("weights"))
	if _, _, err := trainingdb.RecordTrainedModel(ctx, d, trainingdb.TrainedModel{JobID: "job1", ModelSize: "4b", NumTrainSteps: 1, Path: "trained/job1",
		Files: []trainingdb.TrainedModelFile{{Path: "adapter_model.safetensors", SizeBytes: 7, SHA256: hex.EncodeToString(sum[:])}}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var got []core.ExportTrainedModelRequest
	var failWith error
	exporters := []core.TrainedModelExporter{
		{Name: "copy", Export: func(ctx context.Context, req core.ExportTrainedModelRequest) (core.ExportTrainedModelResult, error) {
			got = append(got, req)
			if failWith != nil {
				return core.ExportTrainedModelResult{}, failWith
			}
			if _, err := os.Stat(filepath.Join(req.Dir, "adapter_model.safetensors")); err != nil {
				return core.ExportTrainedModelResult{}, err
			}
			return core.ExportTrainedModelResult{Ref: "ref-" + req.Model.JobID, Details: map[string]any{"files": 1}}, nil
		}},
		{Name: "panics", Export: func(context.Context, core.ExportTrainedModelRequest) (core.ExportTrainedModelResult, error) {
			panic("kaboom")
		}},
	}
	fetcher := fakeFetcher{files: map[string]string{"trained/job1/adapter_model.safetensors": "weights"}}
	var events []string
	monitoring.SetHooks([]monitoring.Hooks{{SystemEvent: func(_ context.Context, typ string, _ monitoring.Event) { events = append(events, typ) }}})
	defer monitoring.SetHooks(nil)

	post := func(f core.ModelFileFetcher, modelID, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		req.SetPathValue("id", modelID)
		training.ExportOf(d, f, exporters, root)(rec, req)
		return rec
	}

	rec := post(fetcher, "1", `{"exporter":"copy","options":{"name":"x"}}`)
	var out trainingdb.TrainedModelExport
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	wantDir := filepath.Join(root, "job1")
	if out.Status != trainingdb.TrainedModelExportCompleted || out.Ref != "ref-job1" || out.Exporter != "copy" || out.TrainedModelID != 1 ||
		out.Dir != wantDir || string(out.Details) != `{"files":1}` || string(out.Options) != `{"name":"x"}` || out.CompletedAt == nil {
		t.Fatalf("export record: %+v", out)
	}
	if len(got) != 1 || got[0].ExportID != out.ID || got[0].Dir != wantDir || got[0].Model.JobID != "job1" || got[0].Options["name"] != "x" {
		t.Fatalf("exporter request: %+v", got)
	}
	// the files are left in place for later
	if b, err := os.ReadFile(filepath.Join(wantDir, "adapter_model.safetensors")); err != nil || string(b) != "weights" {
		t.Fatalf("fetched file: %q %v", b, err)
	}

	for name, c := range map[string]struct {
		f        core.ModelFileFetcher
		id, body string
		want     int
	}{
		"unknown model":    {fetcher, "9", `{"exporter":"copy"}`, 404},
		"bad id":           {fetcher, "x", `{"exporter":"copy"}`, 400},
		"no exporter":      {fetcher, "1", `{}`, 400},
		"unknown exporter": {fetcher, "1", `{"exporter":"nope"}`, 400},
		"bad json":         {fetcher, "1", `{`, 400},
		"no sidecar":       {nil, "1", `{"exporter":"copy"}`, 503},
	} {
		if rec := post(c.f, c.id, c.body); rec.Code != c.want {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// requests that never reached the exporter are not recorded
	if list, err := trainingdb.ListTrainedModelExports(ctx, d, 1, 0); err != nil || len(list) != 1 {
		t.Fatalf("stored exports: %+v %v", list, err)
	}

	// the exporter failing, panicking, and the files not being fetchable: 502, recorded as failed
	failWith = errors.New("upload refused")
	if rec := post(fetcher, "1", `{"exporter":"copy"}`); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "upload refused") {
		t.Fatalf("exporter error: %d %s", rec.Code, rec.Body)
	}
	if rec := post(fetcher, "1", `{"exporter":"panics"}`); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "kaboom") {
		t.Fatalf("exporter panic: %d %s", rec.Code, rec.Body)
	}
	os.RemoveAll(wantDir) // so the files have to be fetched again, which the fetcher cannot do
	if rec := post(fakeFetcher{}, "1", `{"exporter":"copy"}`); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "fetching") {
		t.Fatalf("fetch error: %d %s", rec.Code, rec.Body)
	}

	list, err := trainingdb.ListTrainedModelExports(ctx, d, 1, 0)
	if err != nil || len(list) != 4 {
		t.Fatalf("stored exports: %+v %v", list, err)
	}
	if e := list[0]; e.Status != trainingdb.TrainedModelExportFailed || !strings.Contains(e.Error, "fetching") || e.Dir != "" {
		t.Fatalf("fetch failure record: %+v", e)
	}
	if e := list[1]; e.Status != trainingdb.TrainedModelExportFailed || !strings.Contains(e.Error, "kaboom") || e.Dir == "" {
		t.Fatalf("panic record: %+v", e)
	}
	if e := list[2]; e.Status != trainingdb.TrainedModelExportFailed || !strings.Contains(e.Error, "upload refused") {
		t.Fatalf("failure record: %+v", e)
	}
	wantEvents := []string{"training.model_export.completed", "training.model_export.failed", "training.model_export.failed", "training.model_export.failed"}
	if strings.Join(events, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("system events: %v", events)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x?limit=2", nil)
	req.SetPathValue("id", "1")
	training.ListExportsOf(d)(rec, req)
	var listed struct {
		Exports []trainingdb.TrainedModelExport `json:"exports"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || rec.Code != 200 || len(listed.Exports) != 2 || listed.Exports[0].ID != list[0].ID {
		t.Fatalf("list exports: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", "9")
	training.ListExportsOf(d)(rec, req)
	if rec.Code != 404 {
		t.Fatalf("list exports of unknown model: %d", rec.Code)
	}
}

func TestListExporters(t *testing.T) {
	list := func(exporters []core.TrainedModelExporter) string {
		rec := httptest.NewRecorder()
		training.ListExportersOf(exporters)(rec, httptest.NewRequest("GET", "/x", nil))
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
		return strings.TrimSpace(rec.Body.String())
	}
	if got := list(nil); got != `{"exporters":[]}` {
		t.Fatalf("none: %s", got)
	}
	if got := list([]core.TrainedModelExporter{{Name: "a", Description: "does a"}, {Name: "b"}}); got != `{"exporters":[{"name":"a","description":"does a"},{"name":"b","description":""}]}` {
		t.Fatalf("two: %s", got)
	}
}
