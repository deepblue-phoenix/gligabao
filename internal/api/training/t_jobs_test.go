package training_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/ml_sidecar"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

type fakeMLSidecar struct {
	reqs      []ml_sidecar.StartTrainingJobRequest
	err       error
	cancelled []string
	cancelErr error
	infers    []ml_sidecar.InferRequest
	inferResp ml_sidecar.InferResponse
	inferErr  error
}

func (f *fakeMLSidecar) Infer(_ context.Context,
	req ml_sidecar.InferRequest) (ml_sidecar.InferResponse, error) {
	f.infers = append(f.infers, req)
	return f.inferResp, f.inferErr
}

func (f *fakeMLSidecar) CancelTrainingJob(_ context.Context,
	jobID string) error {
	f.cancelled = append(f.cancelled, jobID)
	return f.cancelErr
}

func (f *fakeMLSidecar) StartTrainingJob(_ context.Context,
	req ml_sidecar.StartTrainingJobRequest) (ml_sidecar.TrainingJob, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return ml_sidecar.TrainingJob{}, f.err
	}
	size, steps := req.ModelSize, req.NumTrainSteps
	if size == "" {
		size = ml_sidecar.DefaultModelSize
	}
	if steps == 0 {
		steps = ml_sidecar.DefaultNumTrainSteps
	}
	return ml_sidecar.TrainingJob{
		JobID:         "fc-" + size,
		ProviderJobID: "prov-" + size,
		Status:        "started",
		ModelSize:     size,
		NumTrainSteps: steps,
		Dataset:       req.Dataset,
		StartedAt:     time.Now().Add(-time.Second),
	}, nil
}

// TestStartTrainingJob checks the endpoint that starts a training job on the ML sidecar and records it.
func TestStartTrainingJob(t *testing.T) { dbtest.Run(t, testStartTrainingJob) }

func testStartTrainingJob(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	dsDir := core.DirStore{t.TempDir()}
	sc := &fakeMLSidecar{}
	h := training.StartJobOf(d,
		loraBackends(d, dsDir, sc),
		nil)
	if rec := upload(training.UploadDatasetOf(d, dsDir),
		"/x",
		"file",
		"mine.jsonl",
		goodDataset,
		nil); rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s",
			rec.Code,
			rec.Body)
	}
	post := func(h http.HandlerFunc,
		url,
		body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, userReq("POST",
			url,
			strings.NewReader(body)))
		return rec
	}

	rec := post(h,
		"/x",
		`{"num_train_steps": 7, "model_size": "12B", "dataset": " mine "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start: %d %s",
			rec.Code,
			rec.Body)
	}
	if len(sc.reqs) != 1 || sc.reqs[0].NumTrainSteps != 7 || sc.reqs[0].ModelSize != "12b" || sc.reqs[0].Dataset != "mine.jsonl" {
		t.Fatalf("ml_sidecar request: %+v", sc.reqs)
	}
	run, err := trainingdb.GetTrainingJobRun(ctx,
		d,
		1)
	if err != nil || run.JobID != "fc-12b" || run.ProviderJobID != "prov-12b" || run.Status != "started" || run.ModelSize != "12b" ||
		run.NumTrainSteps != 7 || run.Dataset != "mine.jsonl" || run.FinishedAt != nil {
		t.Fatalf("stored run: %+v, %v",
			run,
			err)
	}
	for _, want := range []string{`"id":1`, `"job_id":"fc-12b"`, `"num_train_steps":7`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("response lacks %s: %s",
				want,
				rec.Body)
		}
	}

	// Only the dataset is required; the rest takes the ML sidecar's defaults. Every started job is a new record.
	if rec := post(h,
		"/x",
		`{"dataset":"mine"}`); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"id":2`) ||
		!strings.Contains(rec.Body.String(), `"model_size":"4b"`) {
		t.Fatalf("defaults: %d %s",
			rec.Code,
			rec.Body)
	}

	// Bad input never reaches the ML sidecar.
	n := len(sc.reqs)
	for _, body := range []string{`{`, ``, `{}`, `{"model_size":"99b","dataset":"mine"}`, `{"num_train_steps":-1,"dataset":"mine"}`,
		`{"dataset":"../x"}`, `{"dataset":"a/b"}`, `{"dataset":"mine.jsonl"}`, `{"dataset":"unknown"}`} {
		if rec := post(h,
			"/x",
			body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d",
				body,
				rec.Code)
		}
	}
	if len(sc.reqs) != n {
		t.Fatalf("ml_sidecar called for bad input: %+v", sc.reqs[n:])
	}

	// An ML sidecar failure records nothing; without an ML sidecar the endpoint is unavailable.
	sc.err = errors.New("boom")
	if rec := post(h,
		"/x",
		`{"dataset":"mine"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("ml_sidecar error: %d", rec.Code)
	}
	if _, err := trainingdb.GetTrainingJobRun(ctx,
		d,
		3); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("a failed start was recorded: %v", err)
	}
	if rec := post(training.StartJobOf(d,
		loraBackends(d, dsDir, nil),
		nil),
		"/x",
		""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no ML sidecar: %d", rec.Code)
	}
}

// A request the ML sidecar rejects as invalid is the caller's error (400, with the ML sidecar's message), not a bad gateway.
func TestStartTrainingJobMLSidecarRejects(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	dir := t.TempDir()
	addDataset(t,
		dir,
		"mine")
	sc := &fakeMLSidecar{
		err: &ml_sidecar.StatusError{
			Method: "POST",
			Path:   "/training/job_start",
			Code:   400,
			Body:   `{"error":"num_train_steps must be a positive integer"}`,
		},
	}
	rec := httptest.NewRecorder()
	training.StartJobOf(d,
		loraBackends(d, core.DirStore{dir}, sc),
		nil)(rec, userReq("POST",
		"/x",
		strings.NewReader(`{"dataset":"mine","num_train_steps":5}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "num_train_steps must be a positive integer") {
		t.Fatalf("got %d %s",
			rec.Code,
			rec.Body)
	}
}

// TestStopTrainingJob checks the endpoint that asks the ML sidecar to stop a job.
func TestStopTrainingJob(t *testing.T) { dbtest.Run(t, testStopTrainingJob) }

func testStopTrainingJob(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	sc := &fakeMLSidecar{}
	for _, jobID := range []string{"j1", "j2"} {
		if _, err := trainingdb.RecordTrainingJobRun(ctx,
			d,
			trainingdb.TrainingJobRun{
				UserID:        testUser,
				JobID:         jobID,
				Status:        "training",
				ModelSize:     "4b",
				NumTrainSteps: 10,
				StartedAt:     time.Now(),
			}); err != nil {
			t.Fatal(err)
		}
	}
	end := time.Now()
	if err := trainingdb.UpdateTrainingJobRun(ctx,
		d,
		2,
		trainingdb.TrainingJobRunUpdate{
			Status:     trainingdb.TrainingJobCancelled,
			FinishedAt: &end,
		}); err != nil {
		t.Fatal(err)
	}
	stop := func(h http.HandlerFunc,
		id string) *httptest.ResponseRecorder {
		req := userReq("POST",
			"/x",
			nil)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	h := training.StopJobOf(d, loraBackends(d, nil, sc), nil)

	if rec := stop(h, "1"); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"stopping"`) {
		t.Fatalf("stop: %d %s",
			rec.Code,
			rec.Body)
	}
	if len(sc.cancelled) != 1 || sc.cancelled[0] != "j1" {
		t.Fatalf("ml_sidecar asked to stop: %v", sc.cancelled)
	}
	// A job that is over, an unknown one and a bad id never reach the ML sidecar.
	for id, want := range map[string]int{"2": http.StatusConflict, "99": http.StatusNotFound, "x": http.StatusBadRequest} {
		if rec := stop(h, id); rec.Code != want {
			t.Fatalf("id %s: %d, want %d",
				id,
				rec.Code,
				want)
		}
	}
	if len(sc.cancelled) != 1 {
		t.Fatalf("ml_sidecar called: %v", sc.cancelled)
	}
	// The ML sidecar's own answers: over already (409), or a failure (502).
	sc.cancelErr = &ml_sidecar.StatusError{
		Method: "POST",
		Path:   "/training/job_cancel",
		Code:   409,
		Body:   `{"error":"already over"}`,
	}
	if rec := stop(h, "1"); rec.Code != http.StatusConflict {
		t.Fatalf("ml_sidecar 409: %d", rec.Code)
	}
	sc.cancelErr = errors.New("boom")
	if rec := stop(h, "1"); rec.Code != http.StatusBadGateway {
		t.Fatalf("ml_sidecar error: %d", rec.Code)
	}
	if rec := stop(training.StopJobOf(d, core.Backends{}, nil), "1"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no ML sidecar: %d", rec.Code)
	}
}

// loraBackends is the backends of a server whose only backend is the ML sidecar sc (nil: none) with the datasets of ds.
func loraBackends(d *sql.DB,
	ds core.DatasetStore,
	sc core.MLSidecarJobs) core.Backends {
	if sc == nil || (reflect.ValueOf(sc).Kind() == reflect.Ptr && reflect.ValueOf(sc).IsNil()) {
		return core.Backends{}
	}
	return core.NewBackends(core.NewMLSidecarBackend(d, ds, sc))
}

func (f *fakeMLSidecar) StreamTrainingJobEvents(context.Context,
	string,
	int,
	func(ml_sidecar.TrainingEvent) error) error {
	return nil
}
