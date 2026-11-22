package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

// fakeService records the user of every call and answers with err (or success).
type fakeService struct {
	users []string
	err   error
}

func (f *fakeService) GCNIITrain(_ context.Context,
	req *sargo.GCNIITrainRequest) (*sargo.GCNIITrainResponse, error) {
	f.users = append(f.users, req.UserID)
	if f.err != nil {
		return nil, f.err
	}
	return &sargo.GCNIITrainResponse{
		UserID: req.UserID,
		Job: sargo.GCNIIJob{
			Status: sargo.GCNIIJobRunning,
		},
	}, nil
}

func (f *fakeService) GCNIITrainStatus(_ context.Context,
	userID string) (*sargo.GCNIIStatus, error) {
	f.users = append(f.users, userID)
	return &sargo.GCNIIStatus{
		UserID:  userID,
		Trained: true,
	}, f.err
}

func (f *fakeService) GCNIIInfer(_ context.Context,
	req *sargo.GCNIIInferRequest) (*sargo.GCNIIInferResponse, error) {
	f.users = append(f.users, req.UserID)
	return &sargo.GCNIIInferResponse{Class: "x"}, f.err
}

func (f *fakeService) GCNIIEmbed(_ context.Context,
	req *sargo.GCNIIEmbedRequest) (*sargo.GCNIIEmbedResponse, error) {
	f.users = append(f.users, req.UserID)
	return &sargo.GCNIIEmbedResponse{Dim: 1}, f.err
}

func call(h http.HandlerFunc,
	method,
	user,
	body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method,
		"/",
		strings.NewReader(body))
	if user != "" {
		req.Header.Set(httputil.UserIDHeader, user)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

const trainBody = `{"model_type":"gcnii","user_id":"mallory","graphs":[{"n":2,"edges":[[0,1]],"class":"a"}]}`

func TestTheUserIsTheOneOfTheRequestNotTheBody(t *testing.T) {
	svc := &fakeService{}
	rec := call(Train(svc),
		"POST",
		"alice",
		trainBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("train: %d %s", rec.Code, rec.Body)
	}
	statusReq := httptest.NewRequest("GET", "/?model_type=gcnii", nil)
	statusReq.Header.Set(httputil.UserIDHeader, "alice")
	TrainStatus(svc)(httptest.NewRecorder(), statusReq)
	call(Infer(svc),
		"POST",
		"alice",
		`{"model_type":"gcnii","user_id":"mallory","graph":{"n":1,"edges":[]}}`)
	call(Embed(svc),
		"POST",
		"alice",
		`{"model_type":"gcnii","user_id":"mallory","graph":{"n":1,"edges":[]}}`)
	if len(svc.users) != 4 {
		t.Fatalf("got %d calls, want 4", len(svc.users))
	}
	for _, u := range svc.users {
		if u != "alice" {
			t.Fatalf("a call acted as %q, want alice", u)
		}
	}
}

func TestBadRequests(t *testing.T) {
	svc := &fakeService{}
	if rec := call(Train(svc), "POST", "", trainBody); rec.Code != http.StatusBadRequest {
		t.Errorf("no user: %d", rec.Code)
	}
	if rec := call(Train(svc), "POST", "alice", "{"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: %d", rec.Code)
	}
	if rec := call(Train(svc), "POST", "alice", `{"graphs":[]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no model_type: %d", rec.Code)
	}
	if rec := call(Infer(svc), "POST", "alice", `{"model_type":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown model_type: %d", rec.Code)
	}
	statusReq := httptest.NewRequest("GET", "/", nil)
	statusReq.Header.Set(httputil.UserIDHeader, "alice")
	if rec := httptest.NewRecorder(); func() int { TrainStatus(svc)(rec, statusReq); return rec.Code }() != http.StatusBadRequest {
		t.Errorf("status without model_type")
	}
	if len(svc.users) != 0 {
		t.Errorf("sargo was called for a bad request: %v", svc.users)
	}
}

func TestSargoErrorsAreMapped(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{&sargo.APIError{Status: 409, Message: "busy"}, http.StatusConflict},
		{&sargo.APIError{Status: 404, Message: "no model"}, http.StatusNotFound},
		{&sargo.APIError{Status: 503, Message: "off"}, http.StatusServiceUnavailable},
		{&sargo.APIError{Status: 500, Message: "boom"}, http.StatusBadGateway},
		{context.DeadlineExceeded, http.StatusBadGateway},
		{sargo.ErrInvalidGCNII, http.StatusBadRequest},
	} {
		rec := call(Train(&fakeService{err: tc.err}),
			"POST",
			"alice",
			trainBody)
		if rec.Code != tc.want {
			t.Errorf("%v: got %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
}
