package gcnii_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	"github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/internal/training/gcnii"
	"github.com/deepblue-phoenix/gligabao/monitoring"
)

// fakeSargo is a sargo whose training the test moves along: the status it reports is the job it holds.
type fakeSargo struct {
	mu       sync.Mutex
	job      *sargo.GCNIIJob
	trainErr error
	trained  []*sargo.GCNIITrainRequest
	canceled int
}

func (f *fakeSargo) GCNIITrain(_ context.Context,
	req *sargo.GCNIITrainRequest) (*sargo.GCNIITrainResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.trainErr != nil {
		return nil, f.trainErr
	}
	f.trained = append(f.trained, req)
	f.job = &sargo.GCNIIJob{
		Status:      sargo.GCNIIJobRunning,
		Graphs:      len(req.Graphs),
		Epochs:      3,
		StartedUnix: time.Now().Unix(),
	}
	return &sargo.GCNIITrainResponse{UserID: req.UserID, Job: *f.job}, nil
}

func (f *fakeSargo) GCNIITrainStatus(_ context.Context,
	userID string) (*sargo.GCNIIStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var job *sargo.GCNIIJob
	if f.job != nil {
		cp := *f.job
		job = &cp
	}
	return &sargo.GCNIIStatus{UserID: userID, Job: job}, nil
}

func (f *fakeSargo) GCNIICancel(_ context.Context,
	userID string) (*sargo.GCNIIStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job == nil || f.job.Status != sargo.GCNIIJobRunning {
		return nil, &sargo.APIError{Status: 409, Message: "not training"}
	}
	f.canceled++
	f.job.Status = sargo.GCNIIJobCancelled
	return &sargo.GCNIIStatus{UserID: userID, Job: f.job}, nil
}

// update changes the job the fake reports.
func (f *fakeSargo) update(fn func(j *sargo.GCNIIJob)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.job)
}

const params = `{"model_type":"gcnii","epochs":3,"graphs":[{"n":2,"edges":[[0,1]],"class":"a"},{"n":2,"edges":[],"class":"b"}]}`

func setup(t *testing.T,
	svc *fakeSargo) (db dbHandle, backends training.Backends, mon *training.Monitor) {
	t.Helper()
	d := dbtest.OpenSQLite(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b := gcnii.New(gcnii.Config{
		Svc:       svc,
		DB:        d,
		PollEvery: 5 * time.Millisecond,
	})
	backends = training.NewBackends(b)
	mon = training.NewMonitor(ctx,
		d,
		backends,
		training.WithRetryDelay(time.Millisecond))
	return dbHandle{d}, backends, mon
}

type dbHandle struct{ DB *sql.DB }

func waitFor(t *testing.T,
	what string,
	ok func() bool) {
	t.Helper()
	for i := 0; i < 600; i++ {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func run(t *testing.T,
	db dbHandle,
	id int64) *trainingdb.TrainingJobRun {
	t.Helper()
	r, err := trainingdb.GetTrainingJobRun(context.Background(),
		db.DB,
		id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A GCNII* job goes through the same pipeline as a LoRA one: StartJob records the run, the monitor follows it and its events (status,
// progress per epoch, the end) reach the subscribers and the run row.
func TestGCNIIJobIsFollowedByTheMonitor(t *testing.T) {
	svc := &fakeSargo{}
	db, backends, mon := setup(t, svc)
	sub, cancel := mon.Subscribe(0, "alice")
	defer cancel()

	started, err := training.StartJob(context.Background(),
		db.DB,
		backends,
		mon,
		training.StartJobRequest{
			UserID:    "alice",
			ModelType: "gcnii",
			Params:    json.RawMessage(params),
		})
	if err != nil {
		t.Fatal(err)
	}
	if started.Type() != trainingdb.ModelTypeGCNII || started.NumTrainSteps != 3 || started.Status != "running" {
		t.Fatalf("run %+v", started)
	}
	svc.update(func(j *sargo.GCNIIJob) { j.Epoch, j.EpochLoss = 1, ptr(0.9) })
	waitFor(t, "the first epoch", func() bool { return run(t, db, started.ID).LastStep == 1 })
	acc := 0.75
	svc.update(func(j *sargo.GCNIIJob) {
		j.Status, j.Epoch, j.EpochLoss, j.Loss, j.Accuracy = sargo.GCNIIJobDone, 3, ptr(0.2), ptr(0.25), &acc
	})
	waitFor(t, "the end", func() bool { return run(t, db, started.ID).Status == trainingdb.TrainingJobCompleted })
	r := run(t, db, started.ID)
	if r.FinalLoss == nil || *r.FinalLoss != 0.25 || r.FinishedAt == nil {
		t.Fatalf("completed run %+v", r)
	}

	var types []string
	for len(types) < 4 {
		select {
		case ev := <-sub.C:
			if ev.ProviderType != monitoring.ProviderSargo || ev.RunID != started.ID {
				t.Fatalf("event %+v", ev)
			}
			types = append(types, ev.Type+"/"+ev.Status)
		case <-time.After(5 * time.Second):
			t.Fatalf("events so far: %v", types)
		}
	}
	if types[0] != "started/" || types[1] != "status/running" || types[2] != "progress/" || types[3] != "status/completed" {
		t.Fatalf("events %v", types)
	}
	history, err := training.JobHistory(context.Background(),
		db.DB,
		"alice",
		started.ID,
		0)
	if err != nil || len(history) < 3 || history[len(history)-1].Status != trainingdb.TrainingJobCompleted {
		t.Fatalf("history %v %v", history, err)
	}
	// the run is the user's only
	if _, err := training.GetJobRun(context.Background(), db.DB, "bob", started.ID); err == nil {
		t.Fatal("bob read the run of alice")
	}
}

func TestStopGCNIIJob(t *testing.T) {
	svc := &fakeSargo{}
	db, backends, mon := setup(t, svc)
	started, err := training.StartJob(context.Background(),
		db.DB,
		backends,
		mon,
		training.StartJobRequest{UserID: "alice", ModelType: "gcnii", Params: json.RawMessage(params)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := training.StopJob(context.Background(), db.DB, backends, mon, "alice", started.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancelled", func() bool { return run(t, db, started.ID).Status == trainingdb.TrainingJobCancelled })
	if svc.canceled != 1 {
		t.Fatalf("sargo asked %d times", svc.canceled)
	}
	var te *training.Error
	if _, err := training.StopJob(context.Background(), db.DB, backends, mon, "alice", started.ID); !errors.As(err, &te) || te.Kind != training.KindConflict {
		t.Fatalf("stopping an ended job: %v", err)
	}
}

func TestSargoRefusalFailsTheRun(t *testing.T) {
	svc := &fakeSargo{trainErr: &sargo.APIError{Status: 409, Message: "a training for user alice is already running"}}
	db, backends, mon := setup(t, svc)
	_, err := training.StartJob(context.Background(),
		db.DB,
		backends,
		mon,
		training.StartJobRequest{UserID: "alice", ModelType: "gcnii", Params: json.RawMessage(params)})
	var te *training.Error
	if !errors.As(err, &te) || te.Kind != training.KindConflict {
		t.Fatalf("got %v", err)
	}
	runs, _ := trainingdb.ListUserTrainingJobRuns(context.Background(), db.DB, "alice", 0)
	if len(runs) != 1 || runs[0].Status != trainingdb.TrainingJobFailed {
		t.Fatalf("runs %+v", runs)
	}
	// bad requests never reach sargo
	for _, p := range []string{`{"model_type":"gcnii"}`, `{"model_type":"gcnii","graphs":[{"n":0,"class":"a"}]}`, `{"model_type":"gcnii","dataset":"../x"}`} {
		_, err := training.StartJob(context.Background(), db.DB, backends, mon, training.StartJobRequest{UserID: "alice", ModelType: "gcnii", Params: json.RawMessage(p)})
		if !errors.As(err, &te) || (te.Kind != training.KindInvalid && te.Kind != training.KindNotFound) {
			t.Errorf("%s: %v", p, err)
		}
	}
	if len(svc.trained) != 0 {
		t.Fatal("sargo was asked to train")
	}
}

// Without the store of the weights there are no leases: a run of another process (or of a process that was restarted) is nobody's and Reap fails it.
func TestReapFailsTheRunsNobodyFollows(t *testing.T) {
	svc := &fakeSargo{}
	db, backends, _ := setup(t, svc)
	id, err := trainingdb.RecordTrainingJobRun(context.Background(),
		db.DB,
		trainingdb.TrainingJobRun{
			UserID:        "alice",
			ModelType:     trainingdb.ModelTypeGCNII,
			Info:          json.RawMessage(`{"instance":"gone"}`),
			JobID:         "j",
			Status:        "running",
			NumTrainSteps: 3,
			StartedAt:     time.Now(),
		})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := backends.Of(trainingdb.ModelTypeGCNII)
	if err := b.Reap(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	if r := run(t, db, id); r.Status != trainingdb.TrainingJobFailed || r.FinishedAt == nil {
		t.Fatalf("run %+v", r)
	}
}

func TestBackendsOf(t *testing.T) {
	var te *training.Error
	if _, err := training.NewBackends().Of("gcnii"); !errors.As(err, &te) || te.Kind != training.KindUnavailable {
		t.Fatalf("no sargo: %v", err)
	}
	if _, err := training.NewBackends().Of("nope"); !errors.As(err, &te) || te.Kind != training.KindInvalid {
		t.Fatalf("unknown type: %v", err)
	}
}

func ptr(v float64) *float64 { return &v }
