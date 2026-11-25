package sargo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// gcniiServer is a fake sargo: it records the last request path and body and answers status with body.
func gcniiServer(t *testing.T,
	status int,
	body string,
	gotPath *string,
	gotBody *map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*gotPath = r.URL.Path
		*gotBody = map[string]any{}
		_ = json.Unmarshal(raw, gotBody)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func trainRequest() *GCNIITrainRequest {
	return &GCNIITrainRequest{
		UserID: "alice",
		Graphs: []GCNIIGraph{
			{
				N:     2,
				Edges: [][2]int{{0, 1}},
				Class: "pair",
			},
		},
		Epochs: 3,
	}
}

func TestGCNIITrainSendsTheUserAndAcceptsA202(t *testing.T) {
	var path string
	var body map[string]any
	c := gcniiServer(t,
		http.StatusAccepted,
		`{"user_id":"alice","job":{"status":"running","graphs":1,"classes":["pair"],"epochs":3,"started_unix":5}}`,
		&path,
		&body)
	resp, err := c.GCNIITrain(context.Background(), trainRequest())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/gcnii/train" || body["user_id"] != "alice" || body["epochs"] != float64(3) {
		t.Fatalf("sent %s %v", path, body)
	}
	if resp.UserID != "alice" || resp.Job.Status != GCNIIJobRunning || resp.Job.Classes[0] != "pair" {
		t.Fatalf("got %+v", resp)
	}
}

func TestGCNIITrainStatusInferEmbed(t *testing.T) {
	var path string
	var body map[string]any
	graph := GCNIIGraph{
		N:     2,
		Edges: [][2]int{{0, 1}},
	}
	c := gcniiServer(t,
		http.StatusOK,
		`{"user_id":"alice","trained":true,"job":{"status":"done","accuracy":1,"loss":0.1}}`,
		&path,
		&body)
	status, err := c.GCNIITrainStatus(context.Background(), "alice")
	if err != nil || path != "/v1/gcnii/train/status" || body["user_id"] != "alice" || !status.Trained || *status.Job.Accuracy != 1 {
		t.Fatalf("status: %v %s %v %+v", err, path, body, status)
	}
	c = gcniiServer(t,
		http.StatusOK,
		`{"class":"pair","index":0,"logits":[1],"probabilities":{"pair":1},"nodes":2,"edges":1}`,
		&path,
		&body)
	inf, err := c.GCNIIInfer(context.Background(),
		&GCNIIInferRequest{
			UserID: "alice",
			Graph:  graph,
		})
	if err != nil || path != "/v1/gcnii/infer" || body["user_id"] != "alice" || inf.Class != "pair" {
		t.Fatalf("infer: %v %s %v %+v", err, path, body, inf)
	}
	c = gcniiServer(t,
		http.StatusOK,
		`{"embedding":[0.5,0.5],"dim":2}`,
		&path,
		&body)
	emb, err := c.GCNIIEmbed(context.Background(),
		&GCNIIEmbedRequest{
			UserID: "alice",
			Graph:  graph,
		})
	if err != nil || path != "/v1/gcnii/embed" || emb.Dim != 2 {
		t.Fatalf("embed: %v %s %+v", err, path, emb)
	}
}

func TestGCNIIErrors(t *testing.T) {
	var path string
	var body map[string]any
	c := gcniiServer(t,
		http.StatusConflict,
		`{"error":"already running"}`,
		&path,
		&body)
	_, err := c.GCNIITrain(context.Background(), trainRequest())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("got %v, want a 409 APIError", err)
	}
	path = ""
	bad := []*GCNIITrainRequest{
		{Graphs: trainRequest().Graphs},
		{UserID: "u"},
		{
			UserID: "u",
			Graphs: []GCNIIGraph{{N: 2, Class: "c", Edges: [][2]int{{0, 5}}}},
		},
		{
			UserID: "u",
			Graphs: []GCNIIGraph{{N: 2, Edges: [][2]int{{0, 1}}}},
		},
	}
	for i, req := range bad {
		if _, err := c.GCNIITrain(context.Background(), req); !errors.Is(err, ErrInvalidGCNII) {
			t.Errorf("request %d: got %v, want ErrInvalidGCNII", i, err)
		}
	}
	if path != "" {
		t.Errorf("an invalid request was sent to %s", path)
	}
}

func TestGCNIITrainValidation(t *testing.T) {
	req := trainRequest()
	req.Validation = []GCNIIGraph{{N: 2, Edges: [][2]int{{0, 1}}, Class: "a"}}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	req.Validation[0].Class = ""
	if err := req.Validate(); !errors.Is(err, ErrInvalidGCNII) {
		t.Fatalf("a validation graph without class: %v", err)
	}
}

func TestGCNIICancel(t *testing.T) {
	var path string
	var body map[string]any
	c := gcniiServer(t,
		http.StatusAccepted,
		`{"user_id":"alice","trained":false,"job":{"status":"running","epoch":2,"epoch_loss":0.5}}`,
		&path,
		&body)
	st, err := c.GCNIICancel(context.Background(), "alice")
	if err != nil || path != "/v1/gcnii/train/cancel" || body["user_id"] != "alice" || st.Job.Epoch != 2 || *st.Job.EpochLoss != 0.5 {
		t.Fatalf("cancel: %v %s %v %+v", err, path, body, st)
	}
	if _, err := c.GCNIICancel(context.Background(), ""); err == nil {
		t.Fatal("a cancel without a user was sent")
	}
	c = gcniiServer(t,
		http.StatusConflict,
		`{"error":"user alice is not training"}`,
		&path,
		&body)
	var apiErr *APIError
	if _, err := c.GCNIICancel(context.Background(), "alice"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("conflict: %v", err)
	}
}
