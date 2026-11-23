package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/modeltrain"
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
	rec := call(Train(svc, nil),
		"POST",
		"alice",
		trainBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("train: %d %s", rec.Code, rec.Body)
	}
	statusReq := httptest.NewRequest("GET", "/?model_type=gcnii", nil)
	statusReq.Header.Set(httputil.UserIDHeader, "alice")
	TrainStatus(svc, nil)(httptest.NewRecorder(), statusReq)
	call(Infer(svc, nil),
		"POST",
		"alice",
		`{"model_type":"gcnii","user_id":"mallory","graph":{"n":1,"edges":[]}}`)
	call(Embed(svc, nil),
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
	if rec := call(Train(svc, nil), "POST", "", trainBody); rec.Code != http.StatusBadRequest {
		t.Errorf("no user: %d", rec.Code)
	}
	if rec := call(Train(svc, nil), "POST", "alice", "{"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: %d", rec.Code)
	}
	if rec := call(Infer(svc, nil), "POST", "alice", `{"model_type":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown model_type: %d", rec.Code)
	}
	statusReq := httptest.NewRequest("GET", "/?model_type=nope", nil)
	statusReq.Header.Set(httputil.UserIDHeader, "alice")
	if rec := httptest.NewRecorder(); func() int { TrainStatus(svc, nil)(rec, statusReq); return rec.Code }() != http.StatusBadRequest {
		t.Errorf("status with an unknown model_type")
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
		rec := call(Train(&fakeService{err: tc.err}, nil),
			"POST",
			"alice",
			trainBody)
		if rec.Code != tc.want {
			t.Errorf("%v: got %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
}

func TestTrainDatasetRejectsBadOptions(t *testing.T) {
	svc := &fakeService{}
	h := TrainDataset(svc,
		nil,
		nil,
		nil)
	if rec := call(h, http.MethodPost, "alice", `{"model_type":"gcnii","validation_fraction":1.5}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("validation_fraction: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, http.MethodPost, "alice", `{"model_type":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("model_type: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, http.MethodPost, "", `{"model_type":"gcnii"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("user: %d %s", rec.Code, rec.Body)
	}
	if len(svc.users) != 0 {
		t.Fatal("sargo was called")
	}
}

type historyService struct {
	fakeService
	dir string
}

func (h *historyService) HistoryDir() string { return h.dir }

func TestTrainDatasetRunsListsTheHistory(t *testing.T) {
	dir := t.TempDir()
	svc := &historyService{dir: dir}
	if err := modeltrain.SaveRun(dir,
		modeltrain.Run{
			ID:      "20260101T000000.000000000Z",
			UserID:  "alice",
			Dataset: memory_core.DefaultDataset,
		}); err != nil {
		t.Fatal(err)
	}
	rec := call(TrainDatasetRuns(svc), http.MethodGet, "alice", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "20260101T000000.000000000Z") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(TrainDatasetRuns(svc), http.MethodGet, "bob", ""); !strings.Contains(rec.Body.String(), `"runs":[]`) {
		t.Fatalf("another user has none: %s", rec.Body)
	}
	if rec := call(TrainDatasetRuns(&fakeService{}), http.MethodGet, "alice", ""); !strings.Contains(rec.Body.String(), `"runs":[]`) {
		t.Fatalf("no history dir: %s", rec.Body)
	}
}

// Without sargo (a nil service) Register mounts nothing, with it the routes are under /training/gcnii/.
func TestRegisterNeedsSargo(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, "/v1/gb/training", nil, nil, nil)
	req := httptest.NewRequest("POST", "/v1/gb/training/gcnii/train", strings.NewReader(trainBody))
	if _, pattern := mux.Handler(req); pattern != "" {
		t.Errorf("mounted without sargo: %q", pattern)
	}
	mux = http.NewServeMux()
	Register(mux, "/v1/gb/training", &fakeService{}, nil, nil)
	for _, route := range []string{"POST /v1/gb/training/gcnii/train", "GET /v1/gb/training/gcnii/train", "POST /v1/gb/training/gcnii/train/dataset",
		"GET /v1/gb/training/gcnii/train/dataset/runs", "POST /v1/gb/training/gcnii/infer", "POST /v1/gb/training/gcnii/embed"} {
		method, path, _ := strings.Cut(route, " ")
		if _, pattern := mux.Handler(httptest.NewRequest(method, path, nil)); pattern != route {
			t.Errorf("%s: pattern %q", route, pattern)
		}
	}
}
