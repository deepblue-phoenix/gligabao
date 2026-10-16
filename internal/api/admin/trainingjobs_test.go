package admin_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
)

type fakeSidecar struct {
	reqs []sidecar.StartTrainingJobRequest
	err  error
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
	return sidecar.TrainingJob{JobID: "fc-" + size, Status: "started", ModelSize: size, NumTrainSteps: steps,
		StartedAt: time.Now().Add(-time.Second)}, nil
}

// TestStartTrainingJob checks the admin endpoint that starts a training job on the sidecar and records it.
func TestStartTrainingJob(t *testing.T) { dbtest.Run(t, testStartTrainingJob) }

func testStartTrainingJob(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []admin.Named{{Name: "standard", DB: d}}
	sc := &fakeSidecar{}
	h := admin.StartTrainingJobOf(dbs, sc)
	post := func(h http.HandlerFunc, url, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("POST", url, strings.NewReader(body)))
		return rec
	}

	rec := post(h, "/x", `{"num_train_steps": 7, "model_size": "12B"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if len(sc.reqs) != 1 || sc.reqs[0].NumTrainSteps != 7 || sc.reqs[0].ModelSize != "12b" {
		t.Fatalf("sidecar request: %+v", sc.reqs)
	}
	run, err := relational.GetTrainingJobRun(ctx, d, 1)
	if err != nil || run.JobID != "fc-12b" || run.Status != "started" || run.ModelSize != "12b" || run.NumTrainSteps != 7 {
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
	for _, body := range []string{`{`, `{"model_size":"99b"}`, `{"num_train_steps":-1}`} {
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
	if rec := post(admin.StartTrainingJobOf(dbs, nil), "/x", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no sidecar: %d", rec.Code)
	}
}
