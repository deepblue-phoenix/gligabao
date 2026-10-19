package training_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

func TestTrainedModelEndpoints(t *testing.T) { dbtest.Run(t, testTrainedModelEndpoints) }

func testTrainedModelEndpoints(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d}}
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

	rec := get(training.ListModelsOf(dbs), "/x", "")
	var list struct{ Models []trainingdb.TrainedModel }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 || len(list.Models) != 2 ||
		list.Models[0].JobID != "j2" || list.Models[1].JobID != "j1" || list.Models[0].Files == nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(get(training.ListModelsOf(dbs), "/x?limit=1", "").Body.Bytes(), &list); err != nil || len(list.Models) != 1 {
		t.Fatalf("limit: %+v", list)
	}
	for url, want := range map[string]int{"/x?limit=0": 400, "/x?limit=a": 400, "/x?db=nope": 404} {
		if rec := get(training.ListModelsOf(dbs), url, ""); rec.Code != want {
			t.Fatalf("%s: %d", url, rec.Code)
		}
	}
	rec = get(training.GetModelOf(dbs), "/x", "2")
	var m trainingdb.TrainedModel
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m.JobID != "j2" || m.Dataset != "mine.jsonl" {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	for id, want := range map[string]int{"99": 404, "0": 400, "x": 400} {
		if rec := get(training.GetModelOf(dbs), "/x", id); rec.Code != want {
			t.Fatalf("get %s: %d", id, rec.Code)
		}
	}
}

func TestInferWithATrainedModel(t *testing.T) { dbtest.Run(t, testInferWithATrainedModel) }

func testInferWithATrainedModel(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d}}
	id, _, err := trainingdb.RecordTrainedModel(ctx, d, trainingdb.TrainedModel{JobID: "j2", ModelSize: "12b", NumTrainSteps: 20, Path: "trained/j2"})
	if err != nil {
		t.Fatal(err)
	}
	sc := &fakeSidecar{inferResp: sidecar.InferResponse{Response: "hello", InputTokens: 7, OutputTokens: 3}}
	post := func(sc training.Inferer, modelID, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		req.SetPathValue("id", modelID)
		training.InferOf(dbs, sc)(rec, req)
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
	training.ListInferencesOf(dbs)(rec, req)
	var listed struct {
		Inferences []trainingdb.TrainedModelInference `json:"inferences"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || rec.Code != 200 || len(listed.Inferences) != 2 || listed.Inferences[0].ID != list[0].ID {
		t.Fatalf("list inferences: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", "9")
	training.ListInferencesOf(dbs)(rec, req)
	if rec.Code != 404 {
		t.Fatalf("list inferences of unknown model: %d", rec.Code)
	}
}
