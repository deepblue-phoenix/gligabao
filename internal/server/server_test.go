package server

import (
	"bytes"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

// TestTrainingEndpointsAreNotAdmin checks that the training endpoints are served under /v1/gb/training/ without the admin UI
// (and not under the admin path any more).
func TestTrainingEndpointsAreNotAdmin(t *testing.T) {
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		dir := filepath.Join(t.TempDir(), "datasets")
		h := NewHandler(t.Context(), &db.DB{Relational: d, TrainingDatasetDir: dir}, nil, nil, nil, nil, nil, nil, false, false)
		do := func(req *http.Request) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}

		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", "a.jsonl")
		fw.Write([]byte(`{"instruction":"a","output":"b"}` + "\n"))
		mw.Close()
		req := httptest.NewRequest("POST", "/v1/gb/training/datasets", &b)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if rec := do(req); rec.Code != http.StatusCreated {
			t.Fatalf("upload: %d %s", rec.Code, rec.Body)
		}
		if rec := do(httptest.NewRequest("GET", "/v1/gb/training/datasets", nil)); rec.Code != 200 || !strings.Contains(rec.Body.String(), "a.jsonl") { // no sidecar needed
			t.Fatalf("datasets: %d %s", rec.Code, rec.Body)
		}
		if rec := do(httptest.NewRequest("GET", "/v1/gb/training/jobs", nil)); rec.Code != 200 {
			t.Fatalf("jobs: %d %s", rec.Code, rec.Body)
		}
		if rec := do(httptest.NewRequest("POST", "/v1/gb/training/jobs", strings.NewReader("{}"))); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("start: %d", rec.Code)
		}
		if rec := do(httptest.NewRequest("GET", "/v1/gb/admin/api/training/jobs", nil)); rec.Code == 200 && strings.Contains(rec.Body.String(), "jobs") {
			t.Fatalf("the admin path still serves training: %s", rec.Body)
		}
	})
}
