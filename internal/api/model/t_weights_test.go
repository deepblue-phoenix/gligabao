package model

import (
	"context"
	"encoding/hex"
	"errors"
	apitraining "github.com/deepblue-phoenix/gligabao/internal/api/training"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/modeltrain"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

// sargoFake is the sargo of one instance: its training of the user ends after a few looks at its status, and the model is what is in its
// directory.
type sargoFake struct {
	fakeService
	weights *modeltrain.Weights
	looks   atomic.Int32
	started int64
	// doneAfter is the look at which the training is done (0: it stays running).
	doneAfter int32
}

func (s *sargoFake) Weights() *modeltrain.Weights { return s.weights }

func (s *sargoFake) GCNIITrainStatus(_ context.Context,
	userID string) (*sargo.GCNIIStatus, error) {
	_, statErr := os.Stat(filepath.Join(s.weights.UserDir(userID), modeltrain.WeightsFile))
	if s.started == 0 {
		return &sargo.GCNIIStatus{UserID: userID, Trained: statErr == nil}, nil // this sargo trained nothing
	}
	job := sargo.GCNIIJob{
		Status:      sargo.GCNIIJobRunning,
		StartedUnix: s.started,
		Graphs:      7,
		Epochs:      30,
		Classes:     []string{"a", "b"},
	}
	if s.doneAfter > 0 && s.looks.Add(1) > s.doneAfter {
		acc, loss := 0.9, 0.2
		job.Status = sargo.GCNIIJobDone
		job.Accuracy = &acc
		job.Loss = &loss
	}
	return &sargo.GCNIIStatus{
		UserID:  userID,
		Trained: statErr == nil,
		Job:     &job,
	}, nil
}

func (s *sargoFake) GCNIIInfer(_ context.Context,
	req *sargo.GCNIIInferRequest) (*sargo.GCNIIInferResponse, error) {
	b, err := os.ReadFile(filepath.Join(s.weights.UserDir(req.UserID), modeltrain.WeightsFile))
	if err != nil {
		return nil, &sargo.APIError{Status: http.StatusNotFound, Message: "no model"}
	}
	return &sargo.GCNIIInferResponse{Class: string(b)}, nil // the class says which weights answered
}

// newInstance is the weights of one instance of the cluster: its own directory of sargo and the store the instances share.
func newInstance(t *testing.T,
	shared string) *modeltrain.Weights {
	t.Helper()
	w, err := modeltrain.NewWeights(context.Background(),
		modeltrain.WeightsConfig{LocalDir: shared},
		filepath.Join(t.TempDir(), "gcnii"))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func writeModel(t *testing.T,
	w *modeltrain.Weights,
	user,
	weights string) {
	t.Helper()
	dir := w.UserDir(user)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, modeltrain.WeightsFile), []byte(weights), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, modeltrain.ClassesFile), []byte(`["a","b"]`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T,
	what string,
	ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

func jobOf(t *testing.T,
	rec *Recorder,
	user string) *trainingdb.ModelTrainJob {
	t.Helper()
	j, err := rec.latestJob(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestTrainingIsClaimedWatchedRecordedAndSeenByTheOtherInstances(t *testing.T) {
	old := watchEvery
	watchEvery = time.Millisecond
	t.Cleanup(func() { watchEvery = old })
	d := dbtest.OpenSQLite(t)
	shared := filepath.Join(t.TempDir(), "mw")
	a := &sargoFake{weights: newInstance(t, shared), started: 1700000000, doneAfter: 3}
	b := &sargoFake{weights: newInstance(t, shared)}
	recA, recB := NewRecorder(a, d), NewRecorder(b, d)
	ctx := context.Background()
	writeModel(t, a.weights, "alice", "WEIGHTS-1")

	claim, err := recA.Claim(ctx, "alice", trainInfo{Dataset: "main", Run: "run1"})
	if err != nil {
		t.Fatal(err)
	}
	// the user cannot train again, on any instance, while it runs
	if _, err := recB.Claim(ctx, "alice", trainInfo{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second claim: %v", err)
	}
	if _, err := recA.Claim(ctx, "alice", trainInfo{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second claim on the same instance: %v", err)
	}
	if _, err := recB.Claim(ctx, "bob", trainInfo{}); err != nil {
		t.Fatalf("another user: %v", err)
	}
	// instance B, which knows nothing of the training, shows it as running
	if job := recB.JobOf(ctx, "alice", nil); job == nil || job.Status != sargo.GCNIIJobRunning {
		t.Fatalf("B's view while it runs: %+v", job)
	}

	recA.Watch(claim, "alice", sargo.GCNIIJob{StartedUnix: a.started})
	waitFor(t, "the job is done", func() bool {
		j := jobOf(t, recA, "alice")
		return j != nil && j.Status == trainingdb.ModelTrainJobDone
	})
	row := jobOf(t, recA, "alice")
	if row.ModelID == nil || row.JobID != claim.JobID || row.InstanceID != recA.instance || row.FinishedAt == nil {
		t.Fatalf("job row: %+v", row)
	}
	m, err := trainingdb.GetTrainedModel(ctx, d, *row.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	if m.ModelType != "gcnii" || m.UserID != "alice" || m.Storage != trainingdb.StorageLocal || m.Dataset != "main" || m.JobID != claim.JobID ||
		m.NumExamples != 7 || m.NumTrainSteps != 30 || m.FinalLoss == nil || *m.FinalLoss != 0.2 {
		t.Fatalf("model row: %+v", m)
	}
	if !strings.HasPrefix(m.Path, "gcnii/alice/gcnii_") || !strings.HasSuffix(m.Path, m.JobID) {
		t.Fatalf("path %q job %q", m.Path, m.JobID)
	}
	if len(m.Files) != 2 || m.Files[0].Path != "model.weights" || m.Files[0].SizeBytes != 9 || len(m.Files[0].SHA256) != 64 || m.SizeBytes != 18 {
		t.Fatalf("files: %+v", m.Files)
	}
	if !strings.Contains(string(m.Params), `"run":"run1"`) || !strings.Contains(string(m.Params), `"accuracy":0.9`) {
		t.Fatalf("params: %s", m.Params)
	}
	if got, err := a.weights.Store.Get(ctx, "alice", m.JobID+"/model.weights"); err != nil || string(got) != "WEIGHTS-1" {
		t.Fatalf("stored: %q %v", got, err)
	}
	if a.weights.Marker("alice") != m.ID {
		t.Fatalf("A's marker %d, want %d", a.weights.Marker("alice"), m.ID)
	}

	// B shows it done, and uses the model of A: written into B's directory before the request, not served from nothing
	if job := recB.JobOf(ctx, "alice", nil); job == nil || job.Status != sargo.GCNIIJobDone || job.Accuracy == nil || *job.Accuracy != 0.9 {
		t.Fatalf("B's view when done: %+v", job)
	}
	infer := func(rec *Recorder, svc Service, user string) *httptest.ResponseRecorder {
		return call(Infer(svc, rec), http.MethodPost, user, `{"model_type":"gcnii","graph":{"n":1,"edges":[]}}`)
	}
	if rec := infer(nil, b, "alice"); rec.Code != http.StatusNotFound {
		t.Fatalf("without a recorder B has no model: %d", rec.Code)
	}
	if rec := infer(recB, b, "alice"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEIGHTS-1") {
		t.Fatalf("B after the sync: %d %s", rec.Code, rec.Body)
	}
	// another user has nothing to sync
	if rec := infer(recB, b, "carol"); rec.Code != http.StatusNotFound {
		t.Fatalf("carol: %d", rec.Code)
	}

	// A trains a newer model; B, which has the old one, is brought up to date
	claim2, err := recA.Claim(ctx, "alice", trainInfo{})
	if err != nil {
		t.Fatalf("claim after done: %v", err)
	}
	writeModel(t, a.weights, "alice", "WEIGHTS-22")
	a.looks.Store(0)
	recA.Watch(claim2, "alice", sargo.GCNIIJob{StartedUnix: a.started})
	waitFor(t, "the second job is done", func() bool {
		j := jobOf(t, recA, "alice")
		return j != nil && j.JobID == claim2.JobID && j.Status == trainingdb.ModelTrainJobDone
	})
	if rec := infer(recB, b, "alice"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEIGHTS-22") {
		t.Fatalf("B after the newer model: %d %s", rec.Code, rec.Body)
	}
	// the list of the user has both, newest first; another user has none
	list := func(user string) string {
		req := httptest.NewRequest(http.MethodGet, "/?model_type=gcnii", nil)
		req.Header.Set(httputil.UserIDHeader, user)
		out := httptest.NewRecorder()
		apitraining.ListModelsOf(d)(out, req)
		return out.Body.String()
	}
	if got := list("alice"); !strings.Contains(got, claim.JobID) || !strings.Contains(got, claim2.JobID) {
		t.Fatalf("alice's list: %s", got)
	}
	if got := list("dave"); !strings.Contains(got, `"models":[]`) {
		t.Fatalf("dave's list: %s", got)
	}
}

func TestAnInstanceThatStopsLeavesALostJob(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	shared := filepath.Join(t.TempDir(), "mw")
	a := &sargoFake{weights: newInstance(t, shared)}
	b := &sargoFake{weights: newInstance(t, shared)}
	recA, recB := NewRecorder(a, d), NewRecorder(b, d)
	ctx := context.Background()
	if _, err := recA.Claim(ctx, "alice", trainInfo{}); err != nil {
		t.Fatal(err)
	}
	// A never beats: after the time without a beat B sees the job as failed, and the user can train again
	old := beatStale
	beatStale = -time.Second // every beat is stale
	t.Cleanup(func() { beatStale = old })
	job := recB.JobOf(ctx, "alice", nil)
	if job == nil || job.Status != sargo.GCNIIJobFailed || !strings.Contains(job.Error, "stopped") {
		t.Fatalf("lost job as seen by B: %+v", job)
	}
	beatStale = old
	if _, err := recB.Claim(ctx, "alice", trainInfo{}); err != nil {
		t.Fatalf("claim after the loss: %v", err)
	}
}

func TestAClaimSargoRefusedIsFailedAndFreesTheUser(t *testing.T) {
	old := watchEvery
	watchEvery = time.Hour // the watcher of the accepted training does not look during the test
	t.Cleanup(func() { watchEvery = old })
	d := dbtest.OpenSQLite(t)
	a := &sargoFake{weights: newInstance(t, filepath.Join(t.TempDir(), "mw"))}
	rec := NewRecorder(a, d)
	a.err = &sargo.APIError{Status: http.StatusBadRequest, Message: "bad graphs"}
	if out := call(Train(a, rec), http.MethodPost, "alice", trainBody); out.Code != http.StatusBadRequest {
		t.Fatalf("refused: %d %s", out.Code, out.Body)
	}
	if j := jobOf(t, rec, "alice"); j == nil || j.Status != trainingdb.ModelTrainJobFailed || !strings.Contains(j.Error, "bad graphs") {
		t.Fatalf("job: %+v", j)
	}
	a.err = nil
	if out := call(Train(a, rec), http.MethodPost, "alice", trainBody); out.Code != http.StatusAccepted {
		t.Fatalf("second try: %d %s", out.Code, out.Body)
	}
	if out := call(Train(a, rec), http.MethodPost, "alice", trainBody); out.Code != http.StatusConflict {
		t.Fatalf("while running: %d %s", out.Code, out.Body)
	}
}

func TestSyncLeavesTheDirectoryAloneWhileThisInstanceTrains(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	a := &sargoFake{weights: newInstance(t, filepath.Join(t.TempDir(), "mw"))}
	rec := NewRecorder(a, d)
	ctx := context.Background()
	writeModel(t, a.weights, "alice", "OLD")
	files, err := a.weights.Save(ctx, "alice", "gcnii_1_aa")
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.weights.NewModel(modeltrain.ModelInfo{UserID: "alice", JobID: "gcnii_1_aa", Graphs: 1, Epochs: 1}, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := trainingdb.RecordTrainedModel(ctx, d, m); err != nil {
		t.Fatal(err)
	}
	// this instance is training: sargo wrote NEW into the directory, which is not recorded yet
	if _, err := rec.Claim(ctx, "alice", trainInfo{}); err != nil {
		t.Fatal(err)
	}
	writeModel(t, a.weights, "alice", "NEW")
	if restored, err := rec.Sync(ctx, "alice"); err != nil || restored {
		t.Fatalf("sync while training: %v %v", restored, err)
	}
	if b, _ := os.ReadFile(filepath.Join(a.weights.UserDir("alice"), modeltrain.WeightsFile)); string(b) != "NEW" {
		t.Fatalf("the new model was overwritten: %q", b)
	}
}

func TestRestoreRefusesWeightsThatDoNotMatchTheRecord(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	shared := filepath.Join(t.TempDir(), "mw")
	a := &sargoFake{weights: newInstance(t, shared)}
	rec := NewRecorder(a, d)
	writeModel(t, a.weights, "alice", "GOOD")
	files, err := a.weights.Save(context.Background(), "alice", "gcnii_1_aa")
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.weights.NewModel(modeltrain.ModelInfo{UserID: "alice", JobID: "gcnii_1_aa", Graphs: 1, Epochs: 1}, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := trainingdb.RecordTrainedModel(context.Background(), d, m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "gcnii", "alice", "gcnii_1_aa", "model.weights"), []byte("BAD!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(a.weights.UserDir("alice")); err != nil {
		t.Fatal(err)
	}
	if ok, err := rec.Sync(context.Background(), "alice"); err == nil || ok {
		t.Fatalf("restored damaged weights: %v %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(a.weights.UserDir("alice"), modeltrain.WeightsFile)); err == nil {
		t.Fatal("the damaged weights were written")
	}
}

func TestSargoDirectoryNameIsTheHexOfTheUser(t *testing.T) {
	w := newInstance(t, t.TempDir())
	if got, want := filepath.Base(w.UserDir("al/ice")), hex.EncodeToString([]byte("al/ice")); got != want {
		t.Fatalf("%q != %q", got, want)
	}
}

func TestNoStoreMeansNoRecorder(t *testing.T) {
	if NewRecorder(&fakeService{}, nil) != nil {
		t.Fatal("a service without weights has a recorder")
	}
	var rec *Recorder
	if c, err := rec.Claim(context.Background(), "alice", trainInfo{}); c != nil || err != nil {
		t.Fatal("a nil recorder claimed")
	}
	rec.Watch(nil, "alice", sargo.GCNIIJob{})
	rec.Abort(nil, errors.New("x"))
	if ok, err := rec.Sync(context.Background(), "alice"); ok || err != nil {
		t.Fatal("a nil recorder synced something")
	}
	local := &sargo.GCNIIJob{Status: "done"}
	if rec.JobOf(context.Background(), "alice", local) != local {
		t.Fatal("a nil recorder changed the job")
	}
}
