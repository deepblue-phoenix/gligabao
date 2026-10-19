package training_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
)

// evalSidecar is a trained model that answers "a<i>" to "q<i>" (what the dataset says), except "wrong" to the prompts in wrong.
// With block set, it holds each answer until block is closed or the request is cancelled.
type evalSidecar struct {
	mu      sync.Mutex
	reqs    []sidecar.InferRequest
	wrong   map[string]bool
	block   chan struct{}
	started chan struct{} // gets a value when an answer begins (if not nil)
}

func (s *evalSidecar) Infer(ctx context.Context, req sidecar.InferRequest) (sidecar.InferResponse, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return sidecar.InferResponse{}, ctx.Err()
		}
	}
	if s.wrong[req.Prompt] {
		return sidecar.InferResponse{Response: "wrong", InputTokens: 4, OutputTokens: 1}, nil
	}
	return sidecar.InferResponse{Response: "a" + strings.TrimPrefix(req.Prompt, "q"), InputTokens: 4, OutputTokens: 2}, nil
}

// evalJudge scores 5 when the answer equals the reference and 1 otherwise, and counts its calls.
type evalJudge struct {
	mu    sync.Mutex
	calls int
	sizes []int
	model string
}

func (j *evalJudge) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	content := req.Messages[0].Content
	var cases []struct {
		ID        int    `json:"id"`
		Reference string `json:"reference"`
		Answer    string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(content[strings.Index(content, "["):]), &cases); err != nil {
		return nil, err
	}
	j.mu.Lock()
	j.calls++
	j.sizes = append(j.sizes, len(cases))
	j.model = req.Model
	j.mu.Unlock()
	var out []map[string]any
	for _, it := range cases {
		score := 1
		if it.Answer == it.Reference {
			score = 5
		}
		out = append(out, map[string]any{"id": it.ID, "score": score, "reason": "because"})
	}
	b, _ := json.Marshal(out)
	return &ai.Response{Text: string(b), Model: "judge-model", Provider: "test", InputTokens: 50, OutputTokens: 20, CostUSD: 0.5}, nil
}

func evalDatasetDir(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	sub := filepath.Join(dir, "mine")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	var data strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&data, "{\"instruction\":\"q%d\",\"output\":\"a%d\"}\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(sub, "data.jsonl"), []byte(data.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "dataset.toml"), []byte("name = \"mine\"\nfile = \"data.jsonl\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

type evalCall struct {
	code int
	body []byte
}

func (c evalCall) eval(t *testing.T) relational.TrainedModelEvalRun {
	t.Helper()
	var e relational.TrainedModelEvalRun
	if err := json.Unmarshal(c.body, &e); err != nil {
		t.Fatalf("%d %s", c.code, c.body)
	}
	return e
}

func startEval(m *training.EvalManager, modelID, body string) evalCall {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req.SetPathValue("id", modelID)
	m.StartOf()(rec, req)
	return evalCall{rec.Code, rec.Body.Bytes()}
}

func getEval(t *testing.T, m *training.EvalManager, id int64) (relational.TrainedModelEvalRun, []relational.TrainedModelCaseResult) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(id))
	m.GetOf()(rec, req)
	var out struct {
		Eval  relational.TrainedModelEvalRun
		Cases []relational.TrainedModelCaseResult
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("get eval: %d %s", rec.Code, rec.Body)
	}
	return out.Eval, out.Cases
}

func TestEvalATrainedModel(t *testing.T) { dbtest.Run(t, testEvalATrainedModel) }

func testEvalATrainedModel(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 5)}}
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobeval1", ModelSize: "12b", Dataset: "mine.jsonl", NumTrainSteps: 5, Path: "trained/jobeval1"})
	if err != nil {
		t.Fatal(err)
	}
	sc, judge := &evalSidecar{wrong: map[string]bool{"q3": true}}, &evalJudge{}
	m := training.NewEvalManager(ctx, dbs, sc, judge)

	call := startEval(m, fmt.Sprint(mid), `{"batch_size": 2, "judge_model": "my-judge", "max_tokens": 77, "temperature": 0.2}`)
	if call.code != http.StatusAccepted {
		t.Fatalf("start: %d %s", call.code, call.body)
	}
	started := call.eval(t)
	if started.Status != relational.TrainedModelEvalRunRunning || started.TrainedModelID != mid || started.Dataset != "mine" || started.NumExamples != 5 ||
		len(started.DatasetSHA256) != 64 || !strings.Contains(string(started.Params), `"batch_size":2`) {
		t.Fatalf("started: %+v", started)
	}
	m.Wait()

	ev, cases := getEval(t, m, started.ID)
	if ev.Status != relational.TrainedModelEvalRunCompleted || ev.FinishedAt == nil || ev.Error != "" {
		t.Fatalf("finished: %+v", ev)
	}
	if ev.Scheme != relational.TrainingSchemeFineTuneDataset {
		t.Fatalf("scheme: %q", ev.Scheme)
	}
	if ev.NumAnswered != 5 || ev.NumJudged != 5 || ev.NumFailed != 0 || ev.NumCorrect != 4 || ev.NumIncorrect != 1 || ev.ScoreSum != 21 || ev.NumBatches != 3 {
		t.Fatalf("counters: %+v", ev)
	}
	// 5 questions in batches of 2: three judge calls (2, 2, 1), not five.
	if judge.calls != 3 || fmt.Sprint(judge.sizes) != "[2 2 1]" || judge.model != "my-judge" {
		t.Fatalf("judge: %d calls, sizes %v, model %q", judge.calls, judge.sizes, judge.model)
	}
	if ev.JudgeModel != "judge-model" || ev.JudgeCalls != 3 || ev.JudgeInputTokens != 150 || ev.JudgeOutputTokens != 60 || ev.JudgeCostUSD != 1.5 {
		t.Fatalf("judge usage: %+v", ev)
	}
	if len(cases) != 5 || cases[3].Response != "wrong" || cases[3].Score == nil || *cases[3].Score != 1 || cases[0].Reference != "a0" || cases[0].Reason != "because" {
		t.Fatalf("cases: %+v", cases)
	}
	// The model got the dataset's questions on its own weights, with the settings of the eval.
	if len(sc.reqs) != 5 || sc.reqs[0].Prompt != "q0" || sc.reqs[0].ModelPath != "trained/jobeval1" || sc.reqs[0].ModelSize != "12b" ||
		sc.reqs[0].MaxTokens != 77 || sc.reqs[0].Temperature == nil || *sc.reqs[0].Temperature != 0.2 {
		t.Fatalf("sidecar requests: %+v", sc.reqs)
	}
	// The judge's calls are recorded like the other LLM calls.
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM gb_inferences WHERE type = 'eval_judge' AND stage = 'judge'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("recorded judge calls: %d %v", n, err)
	}

	// The model's evals are listed, newest first, and run again.
	if c := startEval(m, fmt.Sprint(mid), `{"limit": 3}`); c.code != http.StatusAccepted || c.eval(t).NumExamples != 3 {
		t.Fatalf("second eval: %d %s", c.code, c.body)
	}
	m.Wait()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(mid))
	m.ListOf()(rec, req)
	var list struct {
		Evals []relational.TrainedModelEvalRun
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 || len(list.Evals) != 2 || list.Evals[0].NumExamples != 3 || list.Evals[0].Status != "completed" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
}

func TestEvalRefusals(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 2)}}
	ok, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j-ok", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 1, Path: "trained/j-ok"})
	dflt, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j-default", ModelSize: "4b", NumTrainSteps: 1, Path: "trained/j-default"})
	gone, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j-gone", ModelSize: "4b", Dataset: "deleted.jsonl", NumTrainSteps: 1, Path: "trained/j-gone"})
	m := training.NewEvalManager(ctx, dbs, &evalSidecar{}, &evalJudge{})

	for _, tc := range []struct {
		name, model, body string
		want              int
	}{
		{"unknown model", "99", `{}`, 404},
		{"bad id", "0", `{}`, 400},
		{"model without a dataset", fmt.Sprint(dflt), `{}`, 400},
		{"dataset gone", fmt.Sprint(gone), `{}`, 400},
		{"bad json", fmt.Sprint(ok), `{`, 400},
		{"batch too big", fmt.Sprint(ok), `{"batch_size": 1000}`, 400},
		{"unknown scheme", fmt.Sprint(ok), `{"scheme": "novel_cases"}`, 400},
		{"negative limit", fmt.Sprint(ok), `{"limit": -1}`, 400},
		{"bad temperature", fmt.Sprint(ok), `{"temperature": 5}`, 400},
		{"too many tokens", fmt.Sprint(ok), `{"max_tokens": 999999}`, 400},
	} {
		if c := startEval(m, tc.model, tc.body); c.code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, c.code, c.body, tc.want)
		}
	}
	// Without a sidecar or without an LLM there is nothing to run the eval with.
	if c := startEval(training.NewEvalManager(ctx, dbs, nil, &evalJudge{}), fmt.Sprint(ok), `{}`); c.code != 503 {
		t.Errorf("no sidecar: %d", c.code)
	}
	if c := startEval(training.NewEvalManager(ctx, dbs, &evalSidecar{}, nil), fmt.Sprint(ok), `{}`); c.code != 503 {
		t.Errorf("no LLM: %d", c.code)
	}
	if evals, _ := relational.ListTrainedModelEvalRuns(ctx, d, 0, 0); len(evals) != 0 {
		t.Errorf("refused evals left rows: %+v", evals)
	}
	// An unknown database or eval.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/x?db=nope", nil)
	req.SetPathValue("id", "1")
	m.StartOf()(rec, req)
	if rec.Code != 404 {
		t.Errorf("unknown db: %d", rec.Code)
	}
	for _, h := range []http.HandlerFunc{m.GetOf(), m.CancelOf()} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("id", "99")
		h(rec, req)
		if rec.Code != 404 {
			t.Errorf("unknown eval: %d", rec.Code)
		}
	}
}

func TestEvalCancelAndConflict(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 6)}}
	mid, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j1", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 1, Path: "trained/j1"})
	sc := &evalSidecar{block: make(chan struct{}), started: make(chan struct{}, 1)}
	m := training.NewEvalManager(ctx, dbs, sc, &evalJudge{})

	first := startEval(m, fmt.Sprint(mid), `{"batch_size": 3}`)
	if first.code != 202 {
		t.Fatalf("start: %d %s", first.code, first.body)
	}
	id := first.eval(t).ID
	<-sc.started // the model is working on its first question
	if c := startEval(m, fmt.Sprint(mid), `{}`); c.code != http.StatusConflict {
		t.Fatalf("second eval of the same model: %d %s", c.code, c.body)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(id))
	m.CancelOf()(rec, req)
	if rec.Code != 202 {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	m.Wait()
	ev, cases := getEval(t, m, id)
	if ev.Status != relational.TrainedModelEvalRunCancelled || ev.FinishedAt == nil || len(cases) != 0 {
		t.Fatalf("cancelled: %+v, %d cases", ev, len(cases))
	}
	// A finished eval cannot be cancelled again, and the model can be evaluated again.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(id))
	m.CancelOf()(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("cancel again: %d", rec.Code)
	}
	close(sc.block)
	if c := startEval(m, fmt.Sprint(mid), `{}`); c.code != 202 {
		t.Fatalf("eval after cancel: %d %s", c.code, c.body)
	}
	m.Wait()
}

func TestEvalFailsWhenTheModelDoesNotAnswer(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 20)}}
	mid, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j1", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 1, Path: "trained/j1"})
	m := training.NewEvalManager(ctx, dbs, &failingInferer{}, &evalJudge{})
	c := startEval(m, fmt.Sprint(mid), `{"batch_size": 2}`)
	m.Wait()
	ev, cases := getEval(t, m, c.eval(t).ID)
	if ev.Status != relational.TrainedModelEvalRunFailed || !strings.Contains(ev.Error, "weights are gone") || ev.NumFailed != 4 || ev.NumJudged != 0 || len(cases) != 4 ||
		cases[0].Status != relational.TrainedModelCaseFailed || !strings.Contains(cases[0].Error, "weights are gone") {
		t.Fatalf("%+v cases %+v", ev, cases)
	}
}

type failingInferer struct{}

func (failingInferer) Infer(context.Context, sidecar.InferRequest) (sidecar.InferResponse, error) {
	return sidecar.InferResponse{}, &sidecar.InferError{Message: "weights are gone"}
}

func TestEvalsInterruptedByARestartAreFailed(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx := context.Background()
	mid, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j1", ModelSize: "4b", NumTrainSteps: 1, Path: "trained/j1"})
	id, err := relational.RecordTrainedModelEvalRun(ctx, d, relational.TrainedModelEvalRun{TrainedModelID: mid})
	if err != nil {
		t.Fatal(err)
	}
	training.NewEvalManager(ctx, []training.Database{{Name: "standard", DB: d}}, nil, nil)
	ev, err := relational.GetTrainedModelEvalRun(ctx, d, id)
	if err != nil || ev.Status != relational.TrainedModelEvalRunFailed || !strings.Contains(ev.Error, "restart") {
		t.Fatalf("%+v %v", ev, err)
	}
}

// sseEvents reads a server-sent events body and sends the data of each event to the channel.
func sseEvents(body io.Reader) <-chan training.EvalEvent {
	out := make(chan training.EvalEvent, 100)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var e training.EvalEvent
				if json.Unmarshal([]byte(data), &e) == nil {
					out <- e
				}
			}
		}
	}()
	return out
}

func TestEvalLiveFeed(t *testing.T) { dbtest.Run(t, testEvalLiveFeed) }

func testEvalLiveFeed(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 3)}}
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobeval9", ModelSize: "12b", Dataset: "mine.jsonl", NumTrainSteps: 5, Path: "trained/jobeval9"})
	if err != nil {
		t.Fatal(err)
	}
	// The model holds its first answer until the watcher is connected.
	sc := &evalSidecar{wrong: map[string]bool{"q1": true}, block: make(chan struct{}), started: make(chan struct{}, 10)}
	m := training.NewEvalManager(ctx, dbs, sc, &evalJudge{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /evals/{id}/events", m.EventsOf())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	started := startEval(m, fmt.Sprint(mid), `{"batch_size": 2}`).eval(t)
	<-sc.started
	resp, err := http.Get(fmt.Sprintf("%s/evals/%d/events", srv.URL, started.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	events := sseEvents(resp.Body)
	next := func() training.EvalEvent {
		t.Helper()
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("stream closed early")
			}
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for an event")
		}
		return training.EvalEvent{}
	}
	if e := next(); e.Type != "snapshot" || e.Eval == nil || e.Eval.Status != "running" || len(e.Cases) != 0 {
		t.Fatalf("first event: %+v", e)
	}
	close(sc.block)
	var got []string
	answers := 0
	var last training.EvalEvent
	for e := next(); ; e = next() {
		switch e.Type {
		case "answer":
			it := e.Cases[0]
			if it.Status != "answered" || it.Score != nil || it.Idx != answers {
				t.Fatalf("answer event: %+v", it)
			}
			answers++
			got = append(got, fmt.Sprintf("%d=%s", it.Idx, it.Response))
		case "batch":
			if e.Eval == nil || e.Eval.NumExamples != 3 || len(e.Cases) == 0 || e.Cases[0].Status != "judged" || e.Cases[0].Score == nil {
				t.Fatalf("batch event: %+v", e)
			}
			got = append(got, fmt.Sprintf("batch%d", len(e.Cases)))
		}
		last = e
		if e.Type == "end" {
			break
		}
	}
	if fmt.Sprint(got) != "[0=a0 1=wrong batch2 2=a2 batch1]" {
		t.Fatalf("events: %v", got)
	}
	if last.Eval == nil || last.Eval.Status != "completed" || last.Eval.NumJudged != 3 {
		t.Fatalf("end event: %+v", last)
	}
	m.Wait()

	// A watcher that comes after the end gets what was stored, then the end.
	resp2, err := http.Get(fmt.Sprintf("%s/evals/%d/events", srv.URL, started.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	late := sseEvents(resp2.Body)
	if e := <-late; e.Type != "snapshot" || len(e.Cases) != 3 || e.Cases[1].Status != "judged" || e.Eval.Status != "completed" {
		t.Fatalf("late snapshot: %+v", e)
	}
	if e := <-late; e.Type != "end" {
		t.Fatalf("late end: %+v", e)
	}

	if resp3, err := http.Get(srv.URL + "/evals/999/events"); err != nil || resp3.StatusCode != 404 {
		t.Fatalf("unknown eval: %v %v", resp3, err)
	}
}

func TestEvalExperiments(t *testing.T) { dbtest.Run(t, testEvalExperiments) }

func testEvalExperiments(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 4)}}
	loss := 0.8
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobexp1", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 3, FinalLoss: &loss, Path: "trained/jobexp1"})
	if err != nil {
		t.Fatal(err)
	}
	m := training.NewEvalManager(ctx, dbs, &evalSidecar{wrong: map[string]bool{"q1": true}}, &evalJudge{})

	// An eval without an experiment is not in any; with a name it is a run of it, and the same name is the same experiment.
	if c := startEval(m, fmt.Sprint(mid), `{}`); c.code != http.StatusAccepted {
		t.Fatalf("start: %d %s", c.code, c.body)
	}
	m.Wait()
	body := `{"experiment": " sweep ", "experiment_description": "first try", "experiment_config": {"lr": 0.001}}`
	first := startEval(m, fmt.Sprint(mid), body)
	if first.code != http.StatusAccepted {
		t.Fatalf("start with an experiment: %d %s", first.code, first.body)
	}
	m.Wait()
	if c := startEval(m, fmt.Sprint(mid), `{"limit": 2, "experiment": "sweep", "experiment_description": "ignored"}`); c.code != http.StatusAccepted {
		t.Fatalf("second: %d %s", c.code, c.body)
	}
	m.Wait()
	if c := startEval(m, fmt.Sprint(mid), `{"experiment": "`+strings.Repeat("x", 121)+`"}`); c.code != http.StatusBadRequest {
		t.Fatalf("long name: %d", c.code)
	}

	rec := httptest.NewRecorder()
	m.ExperimentsOf()(rec, httptest.NewRequest("GET", "/x", nil))
	var list struct {
		Experiments []relational.TrainingExperiment
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 || len(list.Experiments) != 1 ||
		list.Experiments[0].Name != "sweep" || list.Experiments[0].NumRuns != 2 || list.Experiments[0].Description != "first try" {
		t.Fatalf("experiments: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(list.Experiments[0].ID))
	m.ExperimentOf()(rec, req)
	var got struct {
		Experiment relational.TrainingExperiment
		Runs       []relational.TrainingExperimentRun
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 || len(got.Runs) != 2 {
		t.Fatalf("experiment: %d %s", rec.Code, rec.Body)
	}
	r := got.Runs[0]
	if r.ID != first.eval(t).ID || r.Status != relational.TrainedModelEvalRunCompleted || r.Model.ID != mid || r.Model.FinalLoss == nil ||
		r.MeanScore == nil || *r.MeanScore != 4 || r.CorrectRate == nil || *r.CorrectRate != 0.75 || got.Runs[1].EvalRun.NumExamples != 2 {
		t.Fatalf("run: %+v", r)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", "99")
	m.ExperimentOf()(rec, req)
	if rec.Code != 404 {
		t.Fatalf("unknown experiment: %d", rec.Code)
	}
}
