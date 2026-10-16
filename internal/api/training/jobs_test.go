package training_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
)

type fakeSidecar struct {
	reqs      []sidecar.StartTrainingJobRequest
	err       error
	cancelled []string
	cancelErr error
}

func (f *fakeSidecar) CancelTrainingJob(_ context.Context, jobID string) error {
	f.cancelled = append(f.cancelled, jobID)
	return f.cancelErr
}

func (f *fakeSidecar) StartTrainingJob(_ context.Context, req sidecar.StartTrainingJobRequest) (sidecar.TrainingJob, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return sidecar.TrainingJob{}, f.err
	}
	size, steps := req.ModelSize, req.NumTrainSteps
	if size == "" {
		size = sidecar.DefaultModelSize
	}
	if steps == 0 {
		steps = sidecar.DefaultNumTrainSteps
	}
	return sidecar.TrainingJob{JobID: "fc-" + size, ProviderJobID: "prov-" + size, Status: "started", ModelSize: size, NumTrainSteps: steps,
		Dataset: req.Dataset, StartedAt: time.Now().Add(-time.Second)}, nil
}

// TestStartTrainingJob checks the endpoint that starts a training job on the sidecar and records it.
func TestStartTrainingJob(t *testing.T) { dbtest.Run(t, testStartTrainingJob) }

func testStartTrainingJob(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: t.TempDir()}}
	sc := &fakeSidecar{}
	h := training.StartJobOf(dbs, sc, nil)
	if rec := upload(training.UploadDatasetOf(dbs), "/x", "file", "mine.jsonl", goodDataset, nil); rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	post := func(h http.HandlerFunc, url, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("POST", url, strings.NewReader(body)))
		return rec
	}

	rec := post(h, "/x", `{"num_train_steps": 7, "model_size": "12B", "dataset": " mine.jsonl "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if len(sc.reqs) != 1 || sc.reqs[0].NumTrainSteps != 7 || sc.reqs[0].ModelSize != "12b" || sc.reqs[0].Dataset != "mine.jsonl" {
		t.Fatalf("sidecar request: %+v", sc.reqs)
	}
	run, err := relational.GetTrainingJobRun(ctx, d, 1)
	if err != nil || run.JobID != "fc-12b" || run.ProviderJobID != "prov-12b" || run.Status != "started" || run.ModelSize != "12b" ||
		run.NumTrainSteps != 7 || run.Dataset != "mine.jsonl" || run.FinishedAt != nil {
		t.Fatalf("stored run: %+v, %v", run, err)
	}
	for _, want := range []string{`"id":1`, `"job_id":"fc-12b"`, `"num_train_steps":7`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("response lacks %s: %s", want, rec.Body)
		}
	}

	// An empty body takes the sidecar's defaults; every started job is a new record.
	if rec := post(h, "/x", ""); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"id":2`) ||
		!strings.Contains(rec.Body.String(), `"model_size":"4b"`) {
		t.Fatalf("defaults: %d %s", rec.Code, rec.Body)
	}

	// Bad input never reaches the sidecar.
	n := len(sc.reqs)
	for _, body := range []string{`{`, `{"model_size":"99b"}`, `{"num_train_steps":-1}`,
		`{"dataset":"../x.jsonl"}`, `{"dataset":"a/b.jsonl"}`, `{"dataset":"data.txt"}`} {
		if rec := post(h, "/x", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, rec.Code)
		}
	}
	if rec := post(h, "/x?db=nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown db: %d", rec.Code)
	}
	if len(sc.reqs) != n {
		t.Fatalf("sidecar called for bad input: %+v", sc.reqs[n:])
	}

	// A sidecar failure records nothing; without a sidecar the endpoint is unavailable.
	sc.err = errors.New("boom")
	if rec := post(h, "/x", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("sidecar error: %d", rec.Code)
	}
	if _, err := relational.GetTrainingJobRun(ctx, d, 3); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("a failed start was recorded: %v", err)
	}
	if rec := post(training.StartJobOf(dbs, nil, nil), "/x", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no sidecar: %d", rec.Code)
	}
}

// A request the sidecar rejects as invalid is the caller's error (400, with the sidecar's message), not a bad gateway.
func TestStartTrainingJobSidecarRejects(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	sc := &fakeSidecar{err: &sidecar.StatusError{Method: "POST", Path: "/training/job_start", Code: 400, Body: `{"error":"num_train_steps must be a positive integer"}`}}
	rec := httptest.NewRecorder()
	training.StartJobOf([]training.Database{{Name: "standard", DB: d}}, sc, nil)(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"num_train_steps":5}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "num_train_steps must be a positive integer") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// TestStopTrainingJob checks the endpoint that asks the sidecar to stop a job.
func TestStopTrainingJob(t *testing.T) { dbtest.Run(t, testStopTrainingJob) }

func testStopTrainingJob(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: t.TempDir()}}
	sc := &fakeSidecar{}
	for _, jobID := range []string{"j1", "j2"} {
		if _, err := relational.RecordTrainingJobRun(ctx, d, relational.TrainingJobRun{JobID: jobID, Status: "training", ModelSize: "4b", NumTrainSteps: 10, StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	end := time.Now()
	if err := relational.UpdateTrainingJobRun(ctx, d, 2, relational.TrainingJobRunUpdate{Status: relational.TrainingJobCancelled, FinishedAt: &end}); err != nil {
		t.Fatal(err)
	}
	stop := func(h http.HandlerFunc, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/x", nil)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	h := training.StopJobOf(dbs, sc)

	if rec := stop(h, "1"); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"stopping"`) {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body)
	}
	if len(sc.cancelled) != 1 || sc.cancelled[0] != "j1" {
		t.Fatalf("sidecar asked to stop: %v", sc.cancelled)
	}
	// A job that is over, an unknown one and a bad id never reach the sidecar.
	for id, want := range map[string]int{"2": http.StatusConflict, "99": http.StatusNotFound, "x": http.StatusBadRequest} {
		if rec := stop(h, id); rec.Code != want {
			t.Fatalf("id %s: %d, want %d", id, rec.Code, want)
		}
	}
	if len(sc.cancelled) != 1 {
		t.Fatalf("sidecar called: %v", sc.cancelled)
	}
	// The sidecar's own answers: over already (409), or a failure (502).
	sc.cancelErr = &sidecar.StatusError{Method: "POST", Path: "/training/job_cancel", Code: 409, Body: `{"error":"already over"}`}
	if rec := stop(h, "1"); rec.Code != http.StatusConflict {
		t.Fatalf("sidecar 409: %d", rec.Code)
	}
	sc.cancelErr = errors.New("boom")
	if rec := stop(h, "1"); rec.Code != http.StatusBadGateway {
		t.Fatalf("sidecar error: %d", rec.Code)
	}
	if rec := stop(training.StopJobOf(dbs, nil), "1"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no sidecar: %d", rec.Code)
	}
}
