package training_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

// streamSidecar is a sidecar whose job events the test feeds: StreamTrainingJobEvents replays what was fed so far from
// `from` on, then delivers what is fed next, until the feed is closed (a stream the sidecar ended).
type streamSidecar struct {
	fakeSidecar
	mu    sync.Mutex
	sent  []sidecar.TrainingEvent
	feed  chan sidecar.TrainingEvent
	froms []int
}

func newStreamSidecar() *streamSidecar {
	return &streamSidecar{feed: make(chan sidecar.TrainingEvent, 16)}
}

func (f *streamSidecar) StreamTrainingJobEvents(ctx context.Context, _ string, from int, fn func(sidecar.TrainingEvent) error) error {
	f.mu.Lock()
	f.froms = append(f.froms, from)
	replay := append([]sidecar.TrainingEvent(nil), f.sent[min(from, len(f.sent)):]...)
	f.mu.Unlock()
	for _, ev := range replay {
		if err := fn(ev); err != nil {
			return err
		}
	}
	for {
		select {
		case ev, ok := <-f.feed:
			if !ok {
				return nil
			}
			f.mu.Lock()
			ev.Seq = len(f.sent)
			f.sent = append(f.sent, ev)
			f.mu.Unlock()
			if err := fn(ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func status(s string) sidecar.TrainingEvent {
	return sidecar.TrainingEvent{Type: sidecar.EventStatus, Status: s}
}

func progress(step int, loss float64) sidecar.TrainingEvent {
	return sidecar.TrainingEvent{Type: sidecar.EventProgress, Step: &step, Loss: &loss}
}

// sseClient reads the events of an SSE response.
type sseClient struct {
	t    *testing.T
	body *bufio.Scanner
	resp *http.Response
}

func openSSE(t *testing.T, ctx context.Context, url string, header ...string) *sseClient {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE response: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return &sseClient{t: t, body: bufio.NewScanner(resp.Body), resp: resp}
}

type sseEvent struct {
	ID, Name string
	Data     core.FeedEvent
}

// next returns the next event, skipping comments.
func (c *sseClient) next() sseEvent {
	c.t.Helper()
	var e sseEvent
	for c.body.Scan() {
		line := c.body.Text()
		switch {
		case line == "":
			if e.Name != "" {
				return e
			}
		case strings.HasPrefix(line, "id: "):
			e.ID = line[4:]
		case strings.HasPrefix(line, "event: "):
			e.Name = line[7:]
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(line[6:]), &e.Data); err != nil {
				c.t.Fatalf("bad data %q: %v", line, err)
			}
		}
	}
	c.t.Fatalf("SSE stream ended: %v", c.body.Err())
	return e
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestTrainingMonitor: a job started through the endpoint is followed on the sidecar's SSE stream; its events are
// stored, update its row, and are forwarded to the SSE feeds (live, and replayed from the database).
func TestTrainingMonitor(t *testing.T) { dbtest.Run(t, testTrainingMonitor) }

func testTrainingMonitor(t *testing.T, d *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	addDataset(t, dir, "mine")
	dsDir := dir
	sc := newStreamSidecar()
	mon := core.NewMonitor(ctx, d, sc, core.WithRetryDelay(time.Millisecond))

	events := httptest.NewServer(training.EventsOf(d, mon))
	defer func() { cancel(); events.Close() }() // the open SSE streams end with ctx; Close waits for them
	all := openSSE(t, ctx, events.URL)

	rec := httptest.NewRecorder()
	training.StartJobOf(d, dsDir, sc, mon)(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"dataset": "mine", "num_train_steps": 10}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	live := openSSE(t, ctx, events.URL+"?run_id=1") // nothing stored yet

	sc.feed <- status("running")
	sc.feed <- progress(5, 2.5)
	sc.feed <- progress(10, 1.5)
	waitFor(t, "3 events stored", func() bool { n, _ := trainingdb.NextTrainingJobEventSeq(ctx, d, 1); return n == 3 })
	for i, want := range []string{"status", "progress", "progress"} {
		for _, c := range []*sseClient{all, live} {
			ev := c.next()
			if ev.Name != want || ev.Data.RunID != 1 || ev.Data.JobID != "fc-4b" {
				t.Fatalf("event %d: %+v", i, ev)
			}
			if c == live && ev.ID != strconv.Itoa(i) {
				t.Fatalf("event %d has id %q", i, ev.ID)
			}
		}
	}
	run, _ := trainingdb.GetTrainingJobRun(ctx, d, 1)
	if run.Status != "running" || run.LastStep != 10 || run.LastLoss == nil || *run.LastLoss != 1.5 || run.FinishedAt != nil {
		t.Fatalf("run mid-training: %+v", run)
	}

	fl := 0.9
	done := status("completed")
	done.FinalLoss = &fl
	done.Model = &sidecar.TrainedModelInfo{BaseModel: "google/gemma-4-E4B-it", ModelSize: "4b", Path: "trained/fc-4b", SizeBytes: 30,
		Files:       []sidecar.TrainedModelFile{{Path: "adapter_config.json", SizeBytes: 10, SHA256: "h"}, {Path: "w.safetensors", SizeBytes: 20}},
		NumExamples: 8, DatasetSHA256: "dsh", Params: json.RawMessage(`{"lora_rank":16}`)}
	sc.feed <- done
	if ev := live.next(); ev.Name != "status" || ev.Data.Status != "completed" || ev.ID != "3" {
		t.Fatalf("completed event: %+v", ev)
	}
	waitFor(t, "run finished", func() bool { r, _ := trainingdb.GetTrainingJobRun(ctx, d, 1); return r.FinishedAt != nil })
	run, _ = trainingdb.GetTrainingJobRun(ctx, d, 1)
	if run.Status != "completed" || run.FinalLoss == nil || *run.FinalLoss != 0.9 {
		t.Fatalf("finished run: %+v", run)
	}
	// The completed job left a trained model, made from its run and the completed event.
	models, err := trainingdb.ListTrainedModels(ctx, d, 0)
	if err != nil || len(models) != 1 {
		t.Fatalf("trained models: %+v, %v", models, err)
	}
	if tm := models[0]; tm.RunID == nil || *tm.RunID != 1 || tm.JobID != "fc-4b" || tm.Path != "trained/fc-4b" || tm.ModelSize != "4b" ||
		tm.BaseModel != "google/gemma-4-E4B-it" || tm.NumTrainSteps != 10 || tm.FinalLoss == nil || *tm.FinalLoss != 0.9 ||
		tm.SizeBytes != 30 || len(tm.Files) != 2 || tm.Files[0].SHA256 != "h" || tm.NumExamples != 8 || tm.DatasetSHA256 != "dsh" ||
		string(tm.Params) != `{"lora_rank":16}` {
		t.Fatalf("trained model: %+v", tm)
	}
	stored, err := trainingdb.ListTrainingJobEvents(ctx, d, 1, 0)
	if err != nil || len(stored) != 4 || stored[3].Seq != 3 || stored[3].Type != "status" {
		t.Fatalf("stored events: %+v, %v", stored, err)
	}
	if sc.froms[0] != 0 {
		t.Fatalf("stream started from %v", sc.froms)
	}

	// A client that connects later gets the whole history; a reconnecting one (Last-Event-ID) only what it missed.
	replay := openSSE(t, ctx, events.URL+"?run_id=1")
	for i, want := range []string{"running", "", "", "completed"} {
		ev := replay.next()
		if ev.ID != strconv.Itoa(i) || ev.Data.Status != want || ev.Data.JobID != "fc-4b" {
			t.Fatalf("replayed event %d: %+v", i, ev)
		}
	}
	resumed := openSSE(t, ctx, events.URL+"?run_id=1", "Last-Event-ID", "2")
	if ev := resumed.next(); ev.ID != "3" || ev.Data.Status != "completed" {
		t.Fatalf("resumed: %+v", ev)
	}

	// Jobs list, newest first.
	rec = httptest.NewRecorder()
	training.ListJobsOf(d)(rec, httptest.NewRequest("GET", "/x", nil))
	var list struct{ Jobs []trainingdb.TrainingJobRun }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Jobs) != 1 || list.Jobs[0].Status != "completed" {
		t.Fatalf("list: %v %s", err, rec.Body)
	}

	// Bad requests.
	for url, want := range map[string]int{"?run_id=99": 404, "?run_id=x": 400, "?run_id=0x": 400} {
		rec := httptest.NewRecorder()
		training.EventsOf(d, mon)(rec, httptest.NewRequest("GET", "/x"+url, nil))
		if rec.Code != want {
			t.Fatalf("%s: %d", url, rec.Code)
		}
	}
	close(sc.feed)
}

// A job that never reports its end is marked failed once the watch timeout has passed, and a restart picks up unfinished jobs
// where the stored events end.
func TestTrainingMonitorTimeoutAndResume(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Resume: run 1 is unfinished with 2 events stored; run 2 is finished and left alone.
	started := time.Now().Add(-time.Minute)
	id1, _ := trainingdb.RecordTrainingJobRun(ctx, d, trainingdb.TrainingJobRun{JobID: "j1", Status: "started", ModelSize: "4b", NumTrainSteps: 5, StartedAt: started})
	trainingdb.RecordTrainingJobEvent(ctx, d, id1, 0, "status", `{"seq":0,"type":"status","status":"running"}`)
	trainingdb.RecordTrainingJobEvent(ctx, d, id1, 1, "progress", `{"seq":1,"type":"progress","step":1}`)
	id2, _ := trainingdb.RecordTrainingJobRun(ctx, d, trainingdb.TrainingJobRun{JobID: "j2", Status: "started", ModelSize: "4b", NumTrainSteps: 5, StartedAt: started})
	trainingdb.UpdateTrainingJobRun(ctx, d, id2, trainingdb.TrainingJobRunUpdate{Status: trainingdb.TrainingJobCompleted})

	sc := newStreamSidecar()
	sc.sent = []sidecar.TrainingEvent{status("running"), progress(1, 3)}
	core.NewMonitor(ctx, d, sc, core.WithRetryDelay(time.Millisecond), core.WithWatchTimeout(time.Hour))
	waitFor(t, "resumed stream", func() bool { sc.mu.Lock(); defer sc.mu.Unlock(); return len(sc.froms) == 1 })
	sc.mu.Lock()
	if sc.froms[0] != 2 {
		t.Fatalf("resumed from %d, want 2 (events 0 and 1 are stored)", sc.froms[0])
	}
	sc.mu.Unlock()
	sc.feed <- status("completed")
	waitFor(t, "run 1 finished", func() bool { r, _ := trainingdb.GetTrainingJobRun(ctx, d, id1); return r.Status == "completed" })
	if r, _ := trainingdb.GetTrainingJobRun(ctx, d, id2); r.Status != "completed" {
		t.Fatal(r)
	}

	// Timeout: the sidecar's stream ends again and again without the job ending.
	sc2 := newStreamSidecar()
	mon := core.NewMonitor(ctx, d, sc2, core.WithRetryDelay(time.Millisecond), core.WithWatchTimeout(50*time.Millisecond))
	id3, _ := trainingdb.RecordTrainingJobRun(ctx, d, trainingdb.TrainingJobRun{JobID: "j3", Status: "started", ModelSize: "4b", NumTrainSteps: 5, StartedAt: time.Now()})
	run3, _ := trainingdb.GetTrainingJobRun(ctx, d, id3)
	close(sc2.feed) // every stream ends at once
	mon.Watch(*run3)
	waitFor(t, "run 3 failed", func() bool { r, _ := trainingdb.GetTrainingJobRun(ctx, d, id3); return r.Status == "failed" })
	run3, _ = trainingdb.GetTrainingJobRun(ctx, d, id3)
	if run3.FinishedAt == nil || !strings.Contains(run3.Error, "did not report") {
		t.Fatalf("timed out run: %+v", run3)
	}
	if evs, _ := trainingdb.ListTrainingJobEvents(ctx, d, id3, 0); len(evs) != 1 || evs[0].Type != "status" {
		t.Fatalf("timeout event: %+v", evs)
	}
}

// Only a completed job that reports its weights leaves a trained model: not a failed or a cancelled one, nor a completed one
// from a sidecar that does not report them.
func TestTrainingMonitorTrainedModels(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sc := newStreamSidecar()
	mon := core.NewMonitor(ctx, d, sc, core.WithRetryDelay(time.Millisecond))
	model := &sidecar.TrainedModelInfo{Path: "trained/x", Files: []sidecar.TrainedModelFile{{Path: "a", SizeBytes: 1}}, SizeBytes: 1}

	for i, last := range []sidecar.TrainingEvent{status("failed"), status("cancelled"), status("completed")} {
		sc := newStreamSidecar()
		mon := core.NewMonitor(ctx, d, sc, core.WithRetryDelay(time.Millisecond))
		id, _ := trainingdb.RecordTrainingJobRun(ctx, d, trainingdb.TrainingJobRun{JobID: "j" + strconv.Itoa(i), Status: "started", ModelSize: "4b", NumTrainSteps: 5, StartedAt: time.Now()})
		run, _ := trainingdb.GetTrainingJobRun(ctx, d, id)
		sc.feed <- last // none of them has a model attached (the completed one is from a sidecar that does not report it)
		mon.Watch(*run)
		waitFor(t, "run finished", func() bool { r, _ := trainingdb.GetTrainingJobRun(ctx, d, id); return r.FinishedAt != nil })
	}
	if models, _ := trainingdb.ListTrainedModels(ctx, d, 0); len(models) != 0 {
		t.Fatalf("models without weights: %+v", models)
	}

	// The same completed event replayed (a restart) does not make a second model.
	id, _ := trainingdb.RecordTrainingJobRun(ctx, d, trainingdb.TrainingJobRun{JobID: "jm", Status: "started", ModelSize: "4b", NumTrainSteps: 5, StartedAt: time.Now()})
	run, _ := trainingdb.GetTrainingJobRun(ctx, d, id)
	done := status("completed")
	done.Model = model
	sc.feed <- done
	mon.Watch(*run)
	waitFor(t, "model recorded", func() bool { m, _ := trainingdb.ListTrainedModels(ctx, d, 0); return len(m) == 1 })
	waitFor(t, "run finished", func() bool { r, _ := trainingdb.GetTrainingJobRun(ctx, d, id); return r.FinishedAt != nil })
	if models, _ := trainingdb.ListTrainedModels(ctx, d, 0); len(models) != 1 || models[0].ModelSize != "4b" || models[0].Name == "" {
		t.Fatalf("models: %+v", models)
	}
}

// A failure to update the job's row from an event must not leave the event stored and the row unchanged: the monitor
// retries the event, and the row gets it. (The row used to be updated after the event was stored, so the retry found the
// event stored and did nothing: a completed job stayed "training" in the database for good.)
func TestTrainingMonitorRetriesAFailedRowUpdate(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	id, _ := trainingdb.RecordTrainingJobRun(ctx, d, trainingdb.TrainingJobRun{JobID: "j1", Status: "started", ModelSize: "4b", NumTrainSteps: 5, StartedAt: time.Now()})
	// The row's updates fail until the trigger is dropped.
	if _, err := d.ExecContext(ctx, `CREATE TRIGGER fail_updates BEFORE UPDATE ON gb_training_job_runs BEGIN SELECT RAISE(ABORT, 'database is busy'); END`); err != nil {
		t.Fatal(err)
	}
	sc := newStreamSidecar()
	sc.sent = []sidecar.TrainingEvent{status("completed")}
	core.NewMonitor(ctx, d, sc, core.WithRetryDelay(time.Millisecond), core.WithWatchTimeout(time.Hour))
	// The stream is opened again and again: the event is retried each time.
	waitFor(t, "the event is retried", func() bool { sc.mu.Lock(); defer sc.mu.Unlock(); return len(sc.froms) >= 3 })
	if r, _ := trainingdb.GetTrainingJobRun(ctx, d, id); r.Status == "completed" {
		t.Fatalf("the row was updated while its updates fail: %+v", r)
	}
	if _, err := d.ExecContext(ctx, `DROP TRIGGER fail_updates`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the row is updated", func() bool { r, _ := trainingdb.GetTrainingJobRun(ctx, d, id); return r.Status == "completed" })
	r, _ := trainingdb.GetTrainingJobRun(ctx, d, id)
	if r.FinishedAt == nil {
		t.Fatalf("a completed run has an end: %+v", r)
	}
	if evs, _ := trainingdb.ListTrainingJobEvents(ctx, d, id, 0); len(evs) != 1 {
		t.Fatalf("the event is stored once: %+v", evs)
	}
}
