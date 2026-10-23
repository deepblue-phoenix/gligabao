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
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

const goodDataset = "{\"instruction\":\"a\",\"input\":\"\",\"output\":\"b\"}\n\n{\"instruction\":\"c\",\"output\":\"d\"}\r\n"

// write creates dir/rel with content (and the directories above it).
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addDataset adds a dataset to dir by hand: a subdirectory called name with a manifest and a valid data file.
func addDataset(t *testing.T, dir, name string) {
	t.Helper()
	write(t, dir, name+"/dataset.toml", "file = \"data.jsonl\"\n")
	write(t, dir, name+"/data.jsonl", goodDataset)
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
	dir := filepath.Join(t.TempDir(), "datasets_training") // created by the first upload
	dsDir := []string{dir}
	h := training.UploadDatasetOf(d, dsDir)

	rec := upload(h, "/x", "file", "mine.jsonl", goodDataset, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{`"filename":"mine.jsonl"`, `"name":"mine"`, `"file":"mine.jsonl"`, `"dir":"mine"`, `"num_records":2`, `"status":"stored"`, `"synced_at":null`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("response lacks %s: %s", want, rec.Body)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "mine", "mine.jsonl")); err != nil || string(b) != goodDataset {
		t.Fatalf("file: %q, %v", b, err)
	}
	// ... in a dataset directory of its own, with a manifest, so that it is found as a dataset.
	found, problems := core.DiscoverDatasets(dir)
	if len(found) != 1 || len(problems) != 0 || found[0].Name != "mine" || found[0].Manifest.File != "mine.jsonl" ||
		found[0].Path != filepath.Join(dir, "mine", "mine.jsonl") || found[0].Description == "" {
		t.Fatalf("discovered: %+v, %v", found, problems)
	}
	ds, err := trainingdb.GetTrainingDatasetByFilename(ctx, d, "mine.jsonl")
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
	for _, f := range []string{"renamed", "x"} {
		for _, name := range []string{f + ".jsonl", core.ManifestFilename} {
			if _, err := os.Stat(filepath.Join(dir, f, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Uploading a name again replaces the file and the record.
	if rec := upload(h, "/x", "file", "mine.jsonl", "{\"instruction\":\"z\",\"output\":\"z\"}\n", nil); rec.Code != 201 || !strings.Contains(rec.Body.String(), `"id":1`) || !strings.Contains(rec.Body.String(), `"num_records":1`) {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "mine", "mine.jsonl")); strings.Contains(string(b), "\"a\"") {
		t.Fatalf("file not replaced: %q", b)
	}

	// A dataset directory with a manifest of its own is not taken over by an upload of the same name.
	own := filepath.Join(dir, "own")
	os.MkdirAll(own, 0o755)
	os.WriteFile(filepath.Join(own, core.ManifestFilename), []byte("file = \"data.jsonl\"\n"), 0o644)
	os.WriteFile(filepath.Join(own, "data.jsonl"), []byte(goodDataset), 0o644)
	if rec := upload(h, "/x", "file", "own.jsonl", goodDataset, nil); rec.Code != http.StatusConflict {
		t.Fatalf("own manifest: %d %s", rec.Code, rec.Body)
	}
	// ... nor is a name another dataset (in another directory) has.
	named := filepath.Join(dir, "elsewhere")
	os.MkdirAll(named, 0o755)
	os.WriteFile(filepath.Join(named, core.ManifestFilename), []byte("name = \"taken\"\nfile = \"d.jsonl\"\n"), 0o644)
	os.WriteFile(filepath.Join(named, "d.jsonl"), []byte(goodDataset), 0o644)
	if rec := upload(h, "/x", "file", "taken.jsonl", goodDataset, nil); rec.Code != http.StatusConflict {
		t.Fatalf("taken name: %d %s", rec.Code, rec.Body)
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
	list, _ := trainingdb.ListTrainingDatasets(ctx, d)
	entries, _ := os.ReadDir(dir)
	files, _ := os.ReadDir(filepath.Join(dir, "mine"))
	if len(list) != 3 || len(entries) != 5 || len(files) != 2 { // mine, renamed, x, own, elsewhere; no temp files left either
		t.Fatalf("records %d, directories %d, files %d", len(list), len(entries), len(files))
	}

	// No directory configured: refused.
	if rec := upload(training.UploadDatasetOf(d, nil), "/x", "file", "n.jsonl", goodDataset, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("no dir: %d", rec.Code)
	}

	// The list endpoint shows what the manifests found, with the records (made for a dataset added by hand too).
	lrec := httptest.NewRecorder()
	training.ListDatasetsOf(d, dsDir)(lrec, httptest.NewRequest("GET", "/x", nil))
	for _, want := range []string{`"datasets":[`, `"name":"x"`, `"filename":"x.jsonl"`, `"name":"own"`, `"filename":"own.jsonl"`, `"file":"data.jsonl"`, `"problems":[]`} {
		if lrec.Code != 200 || !strings.Contains(lrec.Body.String(), want) {
			t.Fatalf("list lacks %s: %d %s", want, lrec.Code, lrec.Body)
		}
	}
	if rec, err := trainingdb.GetTrainingDatasetByFilename(ctx, d, "own.jsonl"); err != nil || rec.NumRecords != 2 {
		t.Fatalf("record of the dataset added by hand: %+v, %v", rec, err)
	}
}

// TestStartJobSendsDataset: the upload handler takes no sidecar at all, and the dataset reaches the sidecar only inside the
// request that starts a job that uses it. A job names a dataset by its name.
func TestStartJobSendsDataset(t *testing.T) { dbtest.Run(t, testStartJobSendsDataset) }

func testStartJobSendsDataset(t *testing.T, d *sql.DB) {
	ctx := t.Context()
	dir := t.TempDir()
	dsDir := []string{dir}
	sc := &fakeSidecar{}
	up := training.UploadDatasetOf(d, dsDir)
	start := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		training.StartJobOf(d, dsDir, sc, nil)(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)))
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

	if rec := startDS("mine"); rec.Code != 201 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if len(sc.reqs) != 1 || sc.reqs[0].Dataset != "mine.jsonl" || sent(0) != goodDataset {
		t.Fatalf("sidecar request: %+v", sc.reqs)
	}
	if ds, _ := trainingdb.GetTrainingDatasetByFilename(ctx, d, "mine.jsonl"); ds.Status != "synced" || ds.SyncedAt == nil {
		t.Fatalf("after start: %+v", ds)
	}

	// Uploaded again: the new version is sent, and the dataset is "stored" until a job takes it.
	upload(up, "/x", "file", "mine.jsonl", goodDataset+goodDataset, nil)
	if ds, _ := trainingdb.GetTrainingDatasetByFilename(ctx, d, "mine.jsonl"); ds.Status != "stored" {
		t.Fatalf("after re-upload: %+v", ds)
	}
	sc.err = errors.New("down")
	if rec := startDS("mine"); rec.Code != http.StatusBadGateway {
		t.Fatalf("sidecar down: %d %s", rec.Code, rec.Body)
	}
	if ds, _ := trainingdb.GetTrainingDatasetByFilename(ctx, d, "mine.jsonl"); ds.Status != "stored" {
		t.Fatalf("after failed start: %+v", ds)
	}
	sc.err = nil
	if rec := startDS("mine"); rec.Code != 201 || sent(len(sc.reqs)-1) != goodDataset+goodDataset {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}

	// A dataset added by hand to the directory (a manifest and a data file) works at once, and is recorded.
	hand := filepath.Join(dir, "by-hand")
	os.MkdirAll(hand, 0o755)
	os.WriteFile(filepath.Join(hand, core.ManifestFilename), []byte("file = \"sub/rows.jsonl\"\n"), 0o644)
	os.MkdirAll(filepath.Join(hand, "sub"), 0o755)
	os.WriteFile(filepath.Join(hand, "sub", "rows.jsonl"), []byte(goodDataset), 0o644)
	n := len(sc.reqs)
	if rec := startDS("by-hand"); rec.Code != 201 || len(sc.reqs) != n+1 || sc.reqs[n].Dataset != "by-hand.jsonl" || sent(n) != goodDataset {
		t.Fatalf("dataset added by hand: %d %s, %+v", rec.Code, rec.Body, sc.reqs)
	}

	// A job needs a dataset, one of the directory: nothing is sent otherwise, and a client cannot inject content.
	n = len(sc.reqs)
	for name, body := range map[string]string{
		"none":        `{}`,
		"no dataset":  `{"dataset_content":"eAo="}`,
		"unknown":     `{"dataset":"elsewhere"}`,
		"a file name": `{"dataset":"mine.jsonl"}`,
		"a path":      `{"dataset":"../mine"}`,
	} {
		if rec := start(body); rec.Code != http.StatusBadRequest || len(sc.reqs) != n {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// A dataset whose data file is gone is not usable: unknown to a job, a problem in the listing.
	upload(up, "/x", "file", "lost.jsonl", goodDataset, nil)
	os.Remove(filepath.Join(dir, "lost", "lost.jsonl"))
	if rec := startDS("lost"); rec.Code != http.StatusBadRequest {
		t.Fatalf("lost file: %d %s", rec.Code, rec.Body)
	}
	if _, problems := core.DiscoverDatasets(dir); len(problems) != 1 || problems[0].Dir != "lost" || !strings.Contains(problems[0].Error, "missing") {
		t.Fatalf("problems: %+v", problems)
	}
}

func TestUploadTrainingDatasetTooLarge(t *testing.T) {
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		h := training.UploadDatasetOf(d, []string{t.TempDir()})
		big := strings.Repeat("x", core.MaxDatasetBytes+1)
		if rec := upload(h, "/x", "file", "a.jsonl", big, nil); rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("too large: %d %s", rec.Code, rec.Body)
		}
	})
}
