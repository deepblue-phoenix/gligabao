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

func (f *fakeService) GCNIITrain(context.Context,
	*sargo.GCNIITrainRequest) (*sargo.GCNIITrainResponse, error) {
	return nil, f.err
}

func (f *fakeService) GCNIITrainStatus(context.Context,
	string) (*sargo.GCNIIStatus, error) {
	return nil, f.err
}

func (f *fakeService) GCNIICancel(context.Context,
	string) (*sargo.GCNIIStatus, error) {
	return nil, f.err
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

func (f *fakeService) GCNIILink(_ context.Context,
	req *sargo.GCNIILinkRequest) (*sargo.GCNIILinkResponse, error) {
	f.users = append(f.users, req.UserID)
	return &sargo.GCNIILinkResponse{Nodes: 1}, f.err
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

const graphBody = `{"model_type":"gcnii","user_id":"mallory","graph":{"n":1,"edges":[]}}`

// syncSpy records the users it is asked to sync.
type syncSpy struct{ users []string }

func (s *syncSpy) Sync(_ context.Context,
	userID string) (bool, error) {
	s.users = append(s.users, userID)
	return false, nil
}

func TestTheUserIsTheOneOfTheRequestNotTheBody(t *testing.T) {
	svc := &fakeService{}
	spy := &syncSpy{}
	call(Infer(svc, spy), "POST", "alice", graphBody)
	call(Embed(svc, spy), "POST", "alice", graphBody)
	if len(svc.users) != 2 || len(spy.users) != 2 {
		t.Fatalf("got %d calls and %d syncs, want 2 and 2", len(svc.users), len(spy.users))
	}
	for _, u := range append(svc.users, spy.users...) {
		if u != "alice" {
			t.Fatalf("a call acted as %q, want alice", u)
		}
	}
}

func TestBadRequests(t *testing.T) {
	svc := &fakeService{}
	if rec := call(Infer(svc, nil), "POST", "", graphBody); rec.Code != http.StatusBadRequest {
		t.Errorf("no user: %d", rec.Code)
	}
	if rec := call(Infer(svc, nil), "POST", "alice", "{"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: %d", rec.Code)
	}
	if rec := call(Infer(svc, nil), "POST", "alice", `{"model_type":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown model_type: %d", rec.Code)
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
		rec := call(Infer(&fakeService{err: tc.err}, nil),
			"POST",
			"alice",
			graphBody)
		if rec.Code != tc.want {
			t.Errorf("%v: got %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
}

// Without sargo (a nil service) Register mounts nothing, with it only the routes that are not training are under /training/gcnii/: the
// training is a job of the one training API.
func TestRegisterNeedsSargo(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, "/v1/gb/training", nil, nil)
	if _, pattern := mux.Handler(httptest.NewRequest("POST", "/v1/gb/training/gcnii/infer", nil)); pattern != "" {
		t.Errorf("mounted without sargo: %q", pattern)
	}
	mux = http.NewServeMux()
	Register(mux, "/v1/gb/training", &fakeService{}, nil)
	for _, route := range []string{"POST /v1/gb/training/gcnii/infer", "POST /v1/gb/training/gcnii/embed", "POST /v1/gb/training/gcnii/link"} {
		method, path, _ := strings.Cut(route, " ")
		if _, pattern := mux.Handler(httptest.NewRequest(method, path, nil)); pattern != route {
			t.Errorf("%s: pattern %q", route, pattern)
		}
	}
	for _, route := range []string{"POST /v1/gb/training/gcnii/train", "GET /v1/gb/training/gcnii/train", "POST /v1/gb/training/gcnii/train/dataset"} {
		method, path, _ := strings.Cut(route, " ")
		if _, pattern := mux.Handler(httptest.NewRequest(method, path, nil)); pattern == route {
			t.Errorf("%s is still mounted", route)
		}
	}
}
