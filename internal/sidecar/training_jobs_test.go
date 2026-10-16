package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStartTrainingJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/training/job_start" {
			http.NotFound(w, r)
			return
		}
		var in StartTrainingJobRequest
		json.NewDecoder(r.Body).Decode(&in)
		if in.ModelSize != "12b" || in.NumTrainSteps != 7 {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"job_id":"fc-1","status":"started","model_size":"12b","num_train_steps":7,"started_at":"2025-01-02T03:04:05.123456Z"}`))
	}))
	defer srv.Close()

	job, err := New(srv.URL).StartTrainingJob(context.Background(), StartTrainingJobRequest{NumTrainSteps: 7, ModelSize: ModelSize12B})
	if err != nil {
		t.Fatal(err)
	}
	if job.JobID != "fc-1" || job.Status != "started" || job.ModelSize != "12b" || job.NumTrainSteps != 7 ||
		!job.StartedAt.Equal(time.Date(2025, 1, 2, 3, 4, 5, 123456000, time.UTC)) {
		t.Fatalf("got %+v", job)
	}
}

func TestStartTrainingJobErrors(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Invalid model_size"}`, http.StatusBadRequest)
	}))
	defer bad.Close()
	if _, err := New(bad.URL).StartTrainingJob(context.Background(), StartTrainingJobRequest{ModelSize: "99b"}); err == nil {
		t.Fatal("expected sidecar error")
	}
	if _, err := New(bad.URL).StartTrainingJob(context.Background(), StartTrainingJobRequest{NumTrainSteps: -1}); err == nil {
		t.Fatal("expected error for negative steps")
	}

	noID := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"started"}`))
	}))
	defer noID.Close()
	if _, err := New(noID.URL).StartTrainingJob(context.Background(), StartTrainingJobRequest{}); err == nil {
		t.Fatal("expected error for a response without job_id")
	}
}
