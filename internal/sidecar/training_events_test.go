package sidecar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sseBody = ": connected\n\n" +
	"id: 3\nevent: status\ndata: {\"type\":\"status\",\"status\":\"running\"}\n\n" +
	": keepalive\n\n" +
	"id: 4\nevent: progress\r\ndata: {\"type\":\"progress\",\"step\":5,\"loss\":1.5,\"learning_rate\":0.0002}\r\n\r\n" +
	"event: status\ndata: {\"type\":\"status\",\"status\":\"completed\",\"final_loss\":0.5}\n\n"

func TestStreamTrainingJobEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/training/job_events" || r.URL.Query().Get("job_id") != "j1" || r.URL.Query().Get("from") != "3" {
			http.Error(w, "bad request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseBody))
	}))
	defer srv.Close()

	var got []TrainingEvent
	err := New(srv.URL).StreamTrainingJobEvents(context.Background(), "j1", 3, func(e TrainingEvent) error {
		got = append(got, e)
		return nil
	})
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d events, err %v", len(got), err)
	}
	if got[0].Seq != 3 || got[0].Status != "running" || got[0].Finished() {
		t.Fatalf("first: %+v", got[0])
	}
	if got[1].Seq != 4 || got[1].Type != EventProgress || *got[1].Step != 5 || *got[1].Loss != 1.5 || *got[1].LearningRate != 0.0002 {
		t.Fatalf("second: %+v", got[1])
	}
	// An event without an id follows the previous one.
	if got[2].Seq != 5 || !got[2].Finished() || *got[2].FinalLoss != 0.5 {
		t.Fatalf("third: %+v", got[2])
	}
}

func TestStreamTrainingJobEventsErrors(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"job_id is required"}`, http.StatusBadRequest)
	}))
	defer bad.Close()
	err := New(bad.URL).StreamTrainingJobEvents(context.Background(), "", 0, func(TrainingEvent) error { return nil })
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 400 || se.Message() != "job_id is required" {
		t.Fatalf("status error: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sseBody)) }))
	defer srv.Close()
	stop := errors.New("stop")
	if err := New(srv.URL).StreamTrainingJobEvents(context.Background(), "j", 0, func(TrainingEvent) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("callback error: %v", err)
	}

	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("data: not json\n\n")) }))
	defer junk.Close()
	if err := New(junk.URL).StreamTrainingJobEvents(context.Background(), "j", 0, func(TrainingEvent) error { return nil }); err == nil {
		t.Fatal("expected a decode error")
	}

	ctx, cancel := context.WithCancel(context.Background())
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer hang.Close()
	go cancel()
	if err := New(hang.URL).StreamTrainingJobEvents(ctx, "j", 0, func(TrainingEvent) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
