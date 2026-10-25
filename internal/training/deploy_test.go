package training_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

func hexSum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// fakeFetcher serves files from a map (model path + "/" + file), counting the downloads.
type fakeFetcher struct {
	files     map[string]string
	downloads atomic.Int32
}

func (f *fakeFetcher) DownloadModelFile(ctx context.Context, modelPath, file string, w io.Writer) (int64, error) {
	f.downloads.Add(1)
	content, ok := f.files[modelPath+"/"+file]
	if !ok {
		return 0, errors.New("no such file")
	}
	n, err := io.WriteString(w, content)
	return int64(n), err
}

func testModel() *trainingdb.TrainedModel {
	return &trainingdb.TrainedModel{ID: 3, JobID: "job1", Path: "trained/job1", Files: []trainingdb.TrainedModelFile{
		{Path: "adapter_config.json", SizeBytes: 5, SHA256: hexSum("{cfg}")},
		{Path: "adapter_model.safetensors", SizeBytes: 7, SHA256: hexSum("weights")},
		{Path: "tok/tokenizer.model", SizeBytes: 3}, // no sha256 recorded
	}}
}

func TestResolveDeployers(t *testing.T) {
	ok := func(ctx context.Context, r core.DeployTrainedModelRequest) (core.DeployTrainedModelResult, error) {
		return core.DeployTrainedModelResult{}, nil
	}
	got, err := core.ResolveDeployers([]core.TrainedModelDeployer{{Name: "b", Deploy: ok}, {Name: "a.x-1_", Deploy: ok}})
	if err != nil || len(got) != 2 || got[0].Name != "a.x-1_" {
		t.Fatalf("valid: %+v %v", got, err)
	}
	for name, list := range map[string][]core.TrainedModelDeployer{
		"empty name": {{Name: "", Deploy: ok}},
		"bad name":   {{Name: "a b", Deploy: ok}},
		"slash":      {{Name: "a/b", Deploy: ok}},
		"no func":    {{Name: "a"}},
		"duplicate":  {{Name: "a", Deploy: ok}, {Name: "a", Deploy: ok}},
	} {
		if _, err := core.ResolveDeployers(list); !errors.Is(err, core.ErrInvalidDeployer) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got, err := core.ResolveDeployers(nil); err != nil || len(got) != 0 {
		t.Errorf("none: %v %v", got, err)
	}
}

func TestModelDir(t *testing.T) {
	root := t.TempDir()
	dir, err := core.ModelDir(root, &trainingdb.TrainedModel{JobID: "job-1_x"})
	if err != nil || dir != filepath.Join(root, "job-1_x") {
		t.Fatalf("%q %v", dir, err)
	}
	for _, bad := range []string{"", "..", "a/b", `a\b`, ".hidden"} {
		if _, err := core.ModelDir(root, &trainingdb.TrainedModel{JobID: bad}); err == nil {
			t.Errorf("job id %q accepted", bad)
		}
	}
}

func TestFetchModelFiles(t *testing.T) {
	f := &fakeFetcher{files: map[string]string{
		"trained/job1/adapter_config.json": "{cfg}", "trained/job1/adapter_model.safetensors": "weights", "trained/job1/tok/tokenizer.model": "tok",
	}}
	tm := testModel()
	dir := filepath.Join(t.TempDir(), "job1")
	if err := core.FetchModelFiles(context.Background(), f, tm, dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "tok", "tokenizer.model")); err != nil || string(b) != "tok" {
		t.Fatalf("nested file: %q %v", b, err)
	}
	if f.downloads.Load() != 3 {
		t.Fatalf("downloads: %d", f.downloads.Load())
	}
	// no temporary files are left behind
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".fetch-") {
			t.Fatalf("leftover %s", e.Name())
		}
	}

	// A second fetch finds everything in place and downloads nothing; a damaged file is fetched again.
	if err := core.FetchModelFiles(context.Background(), f, tm, dir); err != nil || f.downloads.Load() != 3 {
		t.Fatalf("refetch: %v, downloads %d", err, f.downloads.Load())
	}
	if err := os.WriteFile(filepath.Join(dir, "adapter_model.safetensors"), []byte("WEIGHTS"), 0o644); err != nil { // same size, other content
		t.Fatal(err)
	}
	if err := core.FetchModelFiles(context.Background(), f, tm, dir); err != nil || f.downloads.Load() != 4 {
		t.Fatalf("repair: %v, downloads %d", err, f.downloads.Load())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "adapter_model.safetensors")); string(b) != "weights" {
		t.Fatalf("not repaired: %q", b)
	}
}

func TestFetchModelFilesRejects(t *testing.T) {
	ctx := context.Background()
	tm := testModel()

	// wrong content (sha256 differs), then wrong size
	f := &fakeFetcher{files: map[string]string{"trained/job1/adapter_config.json": "{bad}", "trained/job1/adapter_model.safetensors": "weights", "trained/job1/tok/tokenizer.model": "tok"}}
	dir := t.TempDir()
	if err := core.FetchModelFiles(ctx, f, tm, dir); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("sha256 mismatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "adapter_config.json")); !os.IsNotExist(err) {
		t.Fatalf("a rejected file was kept: %v", err)
	}
	f.files["trained/job1/adapter_config.json"] = "{cfg}longer"
	if err := core.FetchModelFiles(ctx, f, tm, dir); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("size mismatch: %v", err)
	}

	// a model without files, and paths that leave the directory
	if err := core.FetchModelFiles(ctx, f, &trainingdb.TrainedModel{JobID: "j"}, t.TempDir()); err == nil {
		t.Fatal("model without files accepted")
	}
	for _, bad := range []string{"../x", "/etc/passwd", "a/../../x", `a\b`} {
		tm := &trainingdb.TrainedModel{JobID: "j", Files: []trainingdb.TrainedModelFile{{Path: bad, SizeBytes: 1}}}
		if err := core.FetchModelFiles(ctx, f, tm, t.TempDir()); err == nil || !strings.Contains(err.Error(), "relative path") {
			t.Errorf("path %q: %v", bad, err)
		}
	}
	// the fetcher failing
	if err := core.FetchModelFiles(ctx, &fakeFetcher{}, tm, t.TempDir()); err == nil {
		t.Fatal("fetcher error swallowed")
	}
}
