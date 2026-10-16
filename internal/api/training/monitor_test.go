package training_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
)

// streamSidecar is a sidecar whose job events the test feeds: StreamTrainingJobEvents replays what was fed so far from
// `from` on, then delivers what is fed next, until the feed is closed (a stream the sidecar ended).
type streamSidecar struct {
	fakeSidecar
	mu       sync.Mutex
	sent     []sidecar.TrainingEvent
	feed     chan sidecar.TrainingEvent
	froms    []int
	datasets []sidecar.TrainingDataset
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

func (f *streamSidecar) ListTrainingDatasets(context.Context) ([]sidecar.TrainingDataset, error) {
	return f.datasets, f.err
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
	Data     training.FeedEvent
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
	dbs := []training.Database{{Name: "standard", DB: d}}
	sc := newStreamSidecar()
	mon := training.NewMonitor(ctx, dbs, sc, training.WithRetryDelay(time.Millisecond))

	events := httptest.NewServer(training.EventsOf(dbs, mon))
	defer func() { cancel(); events.Close() }() // the open SSE streams end with ctx; Close waits for them
	all := openSSE(t, ctx, events.URL)

	rec := httptest.NewRecorder()
	training.StartJobOf(dbs, sc, mon)(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"num_train_steps": 10}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	live := openSSE(t, ctx, events.URL+"?run_id=1") // nothing stored yet

	sc.feed <- status("running")
	sc.feed <- progress(5, 2.5)
	sc.feed <- progress(10, 1.5)
	waitFor(t, "3 events stored", func() bool { n, _ := relational.NextTrainingJobEventSeq(ctx, d, 1); return n == 3 })
	for i, want := range []string{"status", "progress", "progress"} {
		for _, c := range []*sseClient{all, live} {
			ev := c.next()
			if ev.Name != want || ev.Data.RunID != 1 || ev.Data.JobID != "fc-4b" || ev.Data.DB != "standard" {
				t.Fatalf("event %d: %+v", i, ev)
			}
			if c == live && ev.ID != strconv.Itoa(i) {
				t.Fatalf("event %d has id %q", i, ev.ID)
			}
		}
	}
	run, _ := relational.GetTrainingJobRun(ctx, d, 1)
	if run.Status != "running" || run.LastStep != 10 || run.LastLoss == nil || *run.LastLoss != 1.5 || run.FinishedAt != nil {
		t.Fatalf("run mid-training: %+v", run)
	}

	fl := 0.9
	done := status("completed")
	done.FinalLoss = &fl
	sc.feed <- done
	if ev := live.next(); ev.Name != "status" || ev.Data.Status != "completed" || ev.ID != "3" {
		t.Fatalf("completed event: %+v", ev)
	}
	waitFor(t, "run finished", func() bool { r, _ := relational.GetTrainingJobRun(ctx, d, 1); return r.FinishedAt != nil })
	run, _ = relational.GetTrainingJobRun(ctx, d, 1)
	if run.Status != "completed" || run.FinalLoss == nil || *run.FinalLoss != 0.9 {
		t.Fatalf("finished run: %+v", run)
	}
	stored, err := relational.ListTrainingJobEvents(ctx, d, 1, 0)
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
	training.ListJobsOf(dbs)(rec, httptest.NewRequest("GET", "/x", nil))
	var list struct{ Jobs []relational.TrainingJobRun }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Jobs) != 1 || list.Jobs[0].Status != "completed" {
		t.Fatalf("list: %v %s", err, rec.Body)
	}

	// Bad requests.
	for url, want := range map[string]int{"?run_id=99": 404, "?run_id=x": 400, "?run_id=0x": 400, "?db=nope": 404} {
		rec := httptest.NewRecorder()
		training.EventsOf(dbs, mon)(rec, httptest.NewRequest("GET", "/x"+url, nil))
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
	dbs := []training.Database{{Name: "standard", DB: d}}

	// Resume: run 1 is unfinished with 2 events stored; run 2 is finished and left alone.
	started := time.Now().Add(-time.Minute)
	id1, _ := relational.RecordTrainingJobRun(ctx, d, relational.TrainingJobRun{JobID: "j1", Status: "started", ModelSize: "4b", NumTrainSteps: 5, StartedAt: started})
	relational.RecordTrainingJobEvent(ctx, d, id1, 0, "status", `{"seq":0,"type":"status","status":"running"}`)
	relational.RecordTrainingJobEvent(ctx, d, id1, 1, "progress", `{"seq":1,"type":"progress","step":1}`)
	id2, _ := relational.RecordTrainingJobRun(ctx, d, relational.TrainingJobRun{JobID: "j2", Status: "started", ModelSize: "4b", NumTrainSteps: 5, StartedAt: started})
	relational.UpdateTrainingJobRun(ctx, d, id2, relational.TrainingJobRunUpdate{Status: relational.TrainingJobCompleted})

	sc := newStreamSidecar()
	sc.sent = []sidecar.TrainingEvent{status("running"), progress(1, 3)}
	training.NewMonitor(ctx, dbs, sc, training.WithRetryDelay(time.Millisecond), training.WithWatchTimeout(time.Hour))
	waitFor(t, "resumed stream", func() bool { sc.mu.Lock(); defer sc.mu.Unlock(); return len(sc.froms) == 1 })
	sc.mu.Lock()
	if sc.froms[0] != 2 {
		t.Fatalf("resumed from %d, want 2 (events 0 and 1 are stored)", sc.froms[0])
	}
	sc.mu.Unlock()
	sc.feed <- status("completed")
	waitFor(t, "run 1 finished", func() bool { r, _ := relational.GetTrainingJobRun(ctx, d, id1); return r.Status == "completed" })
	if r, _ := relational.GetTrainingJobRun(ctx, d, id2); r.Status != "completed" {
		t.Fatal(r)
	}

	// Timeout: the sidecar's stream ends again and again without the job ending.
	sc2 := newStreamSidecar()
	mon := training.NewMonitor(ctx, dbs, sc2, training.WithRetryDelay(time.Millisecond), training.WithWatchTimeout(50*time.Millisecond))
	id3, _ := relational.RecordTrainingJobRun(ctx, d, relational.TrainingJobRun{JobID: "j3", Status: "started", ModelSize: "4b", NumTrainSteps: 5, StartedAt: time.Now()})
	run3, _ := relational.GetTrainingJobRun(ctx, d, id3)
	close(sc2.feed) // every stream ends at once
	mon.Watch(dbs[0], *run3)
	waitFor(t, "run 3 failed", func() bool { r, _ := relational.GetTrainingJobRun(ctx, d, id3); return r.Status == "failed" })
	run3, _ = relational.GetTrainingJobRun(ctx, d, id3)
	if run3.FinishedAt == nil || !strings.Contains(run3.Error, "did not report") {
		t.Fatalf("timed out run: %+v", run3)
	}
	if evs, _ := relational.ListTrainingJobEvents(ctx, d, id3, 0); len(evs) != 1 || evs[0].Type != "status" {
		t.Fatalf("timeout event: %+v", evs)
	}
}

func TestTrainingDatasets(t *testing.T) {
	sc := newStreamSidecar()
	get := func(sc training.Sidecar) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		training.SidecarDatasetsOf(sc)(rec, httptest.NewRequest("GET", "/x", nil))
		return rec
	}
	if rec := get(sc); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"datasets":[]}` {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body)
	}
	size := int64(12)
	sc.datasets = []sidecar.TrainingDataset{{Filename: "a.jsonl", SizeBytes: &size}}
	if rec := get(sc); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"filename":"a.jsonl"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	sc.err = errors.New("down")
	if rec := get(sc); rec.Code != http.StatusBadGateway {
		t.Fatalf("sidecar error: %d", rec.Code)
	}
	if rec := get(nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no sidecar: %d", rec.Code)
	}
}
