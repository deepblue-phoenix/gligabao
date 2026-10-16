package training_test

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

const goodDataset = "{\"instruction\":\"a\",\"input\":\"\",\"output\":\"b\"}\n\n{\"instruction\":\"c\",\"output\":\"d\"}\r\n"

func TestValidateTrainingDataset(t *testing.T) {
	if n, err := training.ValidateDataset([]byte(goodDataset)); err != nil || n != 2 {
		t.Fatalf("good: %d, %v", n, err)
	}
	for in, want := range map[string]string{
		"":           "no records",
		"\n \n":      "no records",
		"not json\n": "line 1 is not a JSON object",
		"[1]\n":      "line 1 is not a JSON object",
		"{\"instruction\":\"a\",\"output\":\"b\"}\n{\"instruction\":\"a\"}\n": "line 2 has no \"output\"",
		"{\"instruction\":1,\"output\":\"b\"}\n":                              "\"instruction\" must be a string",
		"{\"instruction\":\"a\",\"output\":\"b\",\"input\":null}\n":           "\"input\" must be a string",
		"\xff\xfe\n": "UTF-8",
	} {
		if _, err := training.ValidateDataset([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", in, err, want)
		}
	}
}

func upload(h http.HandlerFunc, url, field, filename, content string, extra map[string]string) *httptest.ResponseRecorder {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	if field != "" {
		fw, _ := mw.CreateFormFile(field, filename)
		fw.Write([]byte(content))
	}
	for k, v := range extra {
		mw.WriteField(k, v)
	}
	mw.Close()
	req := httptest.NewRequest("POST", url, &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestUploadTrainingDataset(t *testing.T) { dbtest.Run(t, testUploadTrainingDataset) }

func testUploadTrainingDataset(t *testing.T, d *sql.DB) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "datasets") // created by the first upload
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: dir}}
	h := training.UploadDatasetOf(dbs)

	rec := upload(h, "/x", "file", "mine.jsonl", goodDataset, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{`"filename":"mine.jsonl"`, `"num_records":2`, `"status":"stored"`, `"synced_at":null`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("response lacks %s: %s", want, rec.Body)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "mine.jsonl")); err != nil || string(b) != goodDataset {
		t.Fatalf("file: %q, %v", b, err)
	}
	ds, err := relational.GetTrainingDatasetByFilename(ctx, d, "mine.jsonl")
	if err != nil || ds.Status != "stored" || ds.SizeBytes != int64(len(goodDataset)) || len(ds.SHA256) != 64 {
		t.Fatalf("stored: %+v, %v", ds, err)
	}

	// "filename" overrides the file's name and a path is cut down to its base name.
	if rec := upload(h, "/x", "file", `C:\tmp\x.jsonl`, goodDataset, map[string]string{"filename": "renamed.jsonl"}); rec.Code != 201 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if rec := upload(h, "/x", "file", `C:\tmp\x.jsonl`, goodDataset, nil); rec.Code != 201 {
		t.Fatalf("path: %d %s", rec.Code, rec.Body)
	}
	for _, f := range []string{"renamed.jsonl", "x.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	// Uploading a name again replaces the file and the record.
	if rec := upload(h, "/x", "file", "mine.jsonl", "{\"instruction\":\"z\",\"output\":\"z\"}\n", nil); rec.Code != 201 || !strings.Contains(rec.Body.String(), `"id":1`) || !strings.Contains(rec.Body.String(), `"num_records":1`) {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "mine.jsonl")); strings.Contains(string(b), "\"a\"") {
		t.Fatalf("file not replaced: %q", b)
	}

	// Bad input stores nothing.
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no file":    upload(h, "/x", "", "", "", nil),
		"wrong ext":  upload(h, "/x", "file", "a.txt", goodDataset, nil),
		"bad name":   upload(h, "/x", "file", "a b.jsonl", goodDataset, nil),
		"bad json":   upload(h, "/x", "file", "a.jsonl", "nope\n", nil),
		"empty file": upload(h, "/x", "file", "a.jsonl", "", nil),
	} {
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := upload(h, "/x?db=nope", "file", "a.jsonl", goodDataset, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown db: %d", rec.Code)
	}
	list, _ := relational.ListTrainingDatasets(ctx, d)
	entries, _ := os.ReadDir(dir)
	if len(list) != 3 || len(entries) != 3 { // no temp files left either
		t.Fatalf("records %d, files %d", len(list), len(entries))
	}

	// No directory configured: refused.
	if rec := upload(training.UploadDatasetOf([]training.Database{{Name: "standard", DB: d}}), "/x", "file", "n.jsonl", goodDataset, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("no dir: %d", rec.Code)
	}

	// The list endpoint shows the records.
	lrec := httptest.NewRecorder()
	training.ListDatasetsOf(dbs)(lrec, httptest.NewRequest("GET", "/x", nil))
	if lrec.Code != 200 || !strings.Contains(lrec.Body.String(), `"filename":"x.jsonl"`) || !strings.Contains(lrec.Body.String(), `"datasets":[`) {
		t.Fatalf("list: %d %s", lrec.Code, lrec.Body)
	}
}

// TestStartJobSendsDataset: the upload handler takes no sidecar at all, and the dataset reaches the sidecar only inside the
// request that starts a job that uses it.
func TestStartJobSendsDataset(t *testing.T) { dbtest.Run(t, testStartJobSendsDataset) }

func testStartJobSendsDataset(t *testing.T, d *sql.DB) {
	ctx := t.Context()
	dir := t.TempDir()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: dir}}
	sc := &fakeSidecar{}
	up := training.UploadDatasetOf(dbs)
	start := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		training.StartJobOf(dbs, sc, nil)(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)))
		return rec
	}
	startDS := func(dataset string) *httptest.ResponseRecorder { return start(`{"dataset":"` + dataset + `"}`) }
	sent := func(i int) string {
		raw, _ := base64.StdEncoding.DecodeString(sc.reqs[i].DatasetContent)
		return string(raw)
	}

	if rec := upload(up, "/x", "file", "mine.jsonl", goodDataset, nil); rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if len(sc.reqs) != 0 {
		t.Fatalf("the upload reached the sidecar: %v", sc.reqs)
	}

	if rec := startDS("mine.jsonl"); rec.Code != 201 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if len(sc.reqs) != 1 || sc.reqs[0].Dataset != "mine.jsonl" || sent(0) != goodDataset {
		t.Fatalf("sidecar request: %+v", sc.reqs)
	}
	if ds, _ := relational.GetTrainingDatasetByFilename(ctx, d, "mine.jsonl"); ds.Status != "synced" || ds.SyncedAt == nil {
		t.Fatalf("after start: %+v", ds)
	}

	// Uploaded again: the new version is sent, and the dataset is "stored" until a job takes it.
	upload(up, "/x", "file", "mine.jsonl", goodDataset+goodDataset, nil)
	if ds, _ := relational.GetTrainingDatasetByFilename(ctx, d, "mine.jsonl"); ds.Status != "stored" {
		t.Fatalf("after re-upload: %+v", ds)
	}
	sc.err = errors.New("down")
	if rec := startDS("mine.jsonl"); rec.Code != http.StatusBadGateway {
		t.Fatalf("sidecar down: %d %s", rec.Code, rec.Body)
	}
	if ds, _ := relational.GetTrainingDatasetByFilename(ctx, d, "mine.jsonl"); ds.Status != "stored" {
		t.Fatalf("after failed start: %+v", ds)
	}
	sc.err = nil
	if rec := startDS("mine.jsonl"); rec.Code != 201 || sent(len(sc.reqs)-1) != goodDataset+goodDataset {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}

	// No dataset: nothing is sent. A client cannot inject content. A name the server has no record of is refused.
	n := len(sc.reqs)
	if rec := start(`{"dataset_content":"eAo="}`); rec.Code != 201 || sc.reqs[n].DatasetContent != "" {
		t.Fatalf("no dataset: %d %s, %+v", rec.Code, rec.Body, sc.reqs[n])
	}
	if rec := startDS("elsewhere.jsonl"); rec.Code != http.StatusBadRequest || len(sc.reqs) != n+1 {
		t.Fatalf("unknown dataset: %d %s", rec.Code, rec.Body)
	}
	// A record whose file is gone is an error.
	upload(up, "/x", "file", "lost.jsonl", goodDataset, nil)
	os.Remove(filepath.Join(dir, "lost.jsonl"))
	if rec := startDS("lost.jsonl"); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "upload it again") {
		t.Fatalf("lost file: %d %s", rec.Code, rec.Body)
	}
}

func TestUploadTrainingDatasetTooLarge(t *testing.T) {
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		h := training.UploadDatasetOf([]training.Database{{Name: "standard", DB: d, DatasetDir: t.TempDir()}})
		big := strings.Repeat("x", training.MaxDatasetBytes+1)
		if rec := upload(h, "/x", "file", "a.jsonl", big, nil); rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("too large: %d %s", rec.Code, rec.Body)
		}
	})
}
