package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStartResearchJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/research/job_start" {
			http.NotFound(w, r)
			return
		}
		var in StartResearchJobRequest
		json.NewDecoder(r.Body).Decode(&in)
		if in.Topic != "batteries" || in.MaxSteps != 7 || in.Model != "m" || in.Dataset != "d" {
			http.Error(w,
				"bad body",
				http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"job_id":"abc","provider_job_id":"fc-1","status":"started"}`))
	}))
	defer srv.Close()

	job, err := New(srv.URL).StartResearchJob(context.Background(), StartResearchJobRequest{
		Topic:    "batteries",
		MaxSteps: 7,
		Model:    "m",
		Dataset:  "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.JobID != "abc" || job.ProviderJobID != "fc-1" || job.Status != "started" {
		t.Fatalf("got %+v", job)
	}
	if _, err := New(srv.URL).StartResearchJob(context.Background(), StartResearchJobRequest{}); err == nil {
		t.Fatal("expected an error for no topic")
	}
}

func TestStartResearchJobUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		http.Error(w,
			`{"error":"the agent is not deployed"}`,
			http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := New(srv.URL).StartResearchJob(context.Background(), StartResearchJobRequest{
		Topic: "x",
	})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 503 || se.Message() != "the agent is not deployed" {
		t.Fatalf("got %v", err)
	}
}

func TestCancelResearchJob(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		if r.URL.Path != "/research/job_cancel" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"job_id":"abc","status":"stopping"}`))
	}))
	defer srv.Close()
	if err := New(srv.URL).CancelResearchJob(context.Background(), "abc"); err != nil || got["job_id"] != "abc" {
		t.Fatalf("err %v, body %v",
			err,
			got)
	}
	if err := New(srv.URL).CancelResearchJob(context.Background(), " "); err == nil {
		t.Fatal("expected an error for no job id")
	}
}

func TestStreamResearchJobEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		if r.URL.Path != "/research/job_events" || r.URL.Query().Get("job_id") != "abc" || r.URL.Query().Get("from") != "2" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": connected\n\n")
		fmt.Fprint(w, "id: 2\nevent: state\ndata: {\"type\":\"state\",\"agent\":\"main\",\"state_seq\":1,\"state\":\"searching\",\"step\":1,\"detail\":\"q\"}\n\n")
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, "id: 3\nevent: status\ndata: {\"type\":\"status\",\"status\":\"completed\",\"report\":\"R\",\"usage\":{\"steps\":3,\"cost_usd\":0.5,\"subagents\":1},\"export\":{\"bytes\":24,\"sha256\":\"abc\"}}\n\n")
	}))
	defer srv.Close()

	var got []ResearchEvent
	err := New(srv.URL).StreamResearchJobEvents(context.Background(),
		"abc",
		2,
		func(ev ResearchEvent) error {
			got = append(got, ev)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 2 || got[0].State != "searching" || got[0].Agent != "main" || got[0].Finished() {
		t.Fatalf("got %+v", got)
	}
	if last := got[1]; last.Seq != 3 || !last.Finished() || last.Report != "R" || last.Usage == nil || last.Usage.Steps != 3 || last.Usage.CostUSD != 0.5 || last.Usage.Subagents != 1 || last.Export == nil || last.Export.Bytes != 24 || last.Export.SHA256 != "abc" {
		t.Fatalf("last: %+v", got[1])
	}
}

func TestDownloadResearchExport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		if r.URL.Path != "/research/job_export" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("job_id") {
		case "abc":
			w.Header().Set("Content-Type", "application/zip")
			w.Write([]byte("PK-the-archive"))
		case "short":
			w.Header().Set("Content-Length", "100")
			w.Write([]byte("PK"))
		default:
			http.Error(w,
				`{"error":"job has no export"}`,
				http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)

	var buf bytes.Buffer
	n, err := c.DownloadResearchExport(context.Background(),
		"abc",
		&buf)
	if err != nil || n != 14 || buf.String() != "PK-the-archive" {
		t.Fatalf("n %d, err %v, got %q",
			n,
			err,
			buf.String())
	}
	_, err = c.DownloadResearchExport(context.Background(),
		"none",
		&buf)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound || se.Message() != "job has no export" {
		t.Fatalf("got %v", err)
	}
	if _, err := c.DownloadResearchExport(context.Background(),
		"short",
		&bytes.Buffer{}); err == nil {
		t.Fatal("a body that ends early must be an error")
	}
	if _, err := c.DownloadResearchExport(context.Background(),
		" ",
		&buf); err == nil {
		t.Fatal("expected an error for no job id")
	}
}
