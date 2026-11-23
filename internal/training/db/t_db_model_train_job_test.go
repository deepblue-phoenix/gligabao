package db_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

func TestModelTrainJobs(t *testing.T) { dbtest.Run(t, modelTrainJobsScenario) }

func modelTrainJobsScenario(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	stale := func(ago time.Duration) time.Time { return time.Now().Add(-ago) }
	job := func(id,
		user,
		instance string) trainingdb.ModelTrainJob {
		return trainingdb.ModelTrainJob{
			JobID:      id,
			ModelType:  trainingdb.ModelTypeGCNII,
			UserID:     user,
			InstanceID: instance,
			Job:        []byte(`{"status":"running"}`),
		}
	}
	id1, err := trainingdb.ClaimModelTrainJob(ctx, d, job("j1", "alice", "A"), stale(time.Minute))
	if err != nil || id1 == 0 {
		t.Fatalf("claim: %d %v", id1, err)
	}
	// the same user on another instance is refused, another user is not
	if _, err := trainingdb.ClaimModelTrainJob(ctx, d, job("j2", "alice", "B"), stale(time.Minute)); !errors.Is(err, trainingdb.ErrModelTrainJobRunning) {
		t.Fatalf("second claim: %v", err)
	}
	if _, err := trainingdb.ClaimModelTrainJob(ctx, d, job("j3", "bob", "B"), stale(time.Minute)); err != nil {
		t.Fatalf("bob: %v", err)
	}
	// only the owner beats
	if ok, err := trainingdb.BeatModelTrainJob(ctx, d, "j1", "B", `{"status":"running","x":1}`); err != nil || ok {
		t.Fatalf("a stranger beat: %v %v", ok, err)
	}
	if ok, err := trainingdb.BeatModelTrainJob(ctx, d, "j1", "A", `{"status":"running","x":1}`); err != nil || !ok {
		t.Fatalf("owner beat: %v %v", ok, err)
	}
	got, err := trainingdb.LatestModelTrainJob(ctx, d, trainingdb.ModelTypeGCNII, "alice")
	if err != nil || got.JobID != "j1" || got.Status != trainingdb.ModelTrainJobRunning || string(got.Job) != `{"status":"running","x":1}` || got.FinishedAt != nil {
		t.Fatalf("latest: %+v %v", got, err)
	}
	// no beat for a while: the next claim marks it lost and wins
	time.Sleep(1100 * time.Millisecond)
	id4, err := trainingdb.ClaimModelTrainJob(ctx, d, job("j4", "alice", "B"), time.Now().Add(-500*time.Millisecond))
	if err != nil || id4 == 0 {
		t.Fatalf("claim after the owner stopped: %d %v", id4, err)
	}
	old, err := trainingdb.LatestModelTrainJob(ctx, d, trainingdb.ModelTypeGCNII, "alice")
	if err != nil || old.JobID != "j4" {
		t.Fatalf("latest is the newest: %+v %v", old, err)
	}
	if ok, _ := trainingdb.BeatModelTrainJob(ctx, d, "j1", "A", "{}"); ok {
		t.Fatal("a lost job beat")
	}
	// the owner ends it with a model; ending twice does nothing more
	modelID, _, err := trainingdb.RecordTrainedModel(ctx, d, trainingdb.TrainedModel{ModelType: trainingdb.ModelTypeGCNII, UserID: "alice", JobID: "j4", Path: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if err := trainingdb.EndModelTrainJob(ctx, d, "j4", trainingdb.ModelTrainJobDone, "", `{"status":"done"}`, &modelID); err != nil {
		t.Fatal(err)
	}
	done, _ := trainingdb.LatestModelTrainJob(ctx, d, trainingdb.ModelTypeGCNII, "alice")
	if done.Status != trainingdb.ModelTrainJobDone || done.ModelID == nil || *done.ModelID != modelID || done.FinishedAt == nil {
		t.Fatalf("done: %+v", done)
	}
	// a finished job frees the user: a new claim works
	if _, err := trainingdb.ClaimModelTrainJob(ctx, d, job("j5", "alice", "A"), stale(time.Minute)); err != nil {
		t.Fatalf("claim after done: %v", err)
	}
	if _, err := trainingdb.LatestModelTrainJob(ctx, d, trainingdb.ModelTypeGCNII, "nobody"); !errors.Is(err, trainingdb.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}
