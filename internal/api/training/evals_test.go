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

	trainingeval "github.com/deepblue-phoenix/gligabao/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
)

// evalSidecar is a trained model that answers "a<i>" to "q<i>" (what a dataset says; an audit's), or the expected answer of the
// eval case when it is one (answers, see evalAnswers), except "wrong" to the prompts in wrong.
// With block set, it holds each answer until block is closed or the request is cancelled.
type evalSidecar struct {
	mu      sync.Mutex
	reqs    []sidecar.InferRequest
	answers map[string]string
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
	if a, ok := s.answers[req.Prompt]; ok {
		return sidecar.InferResponse{Response: a, InputTokens: 4, OutputTokens: 2}, nil
	}
	return sidecar.InferResponse{Response: "a" + strings.TrimPrefix(req.Prompt, "q"), InputTokens: 4, OutputTokens: 2}, nil
}

// theEval is the eval the tests run: the embedded one named like the training dataset of the models of the tests.
const theEval = "world-knowledge-base"

// evalCases are the cases of theEval, in order.
func evalCases(t *testing.T) []trainingeval.Case {
	t.Helper()
	cases, err := trainingeval.LoadEvals(nil, theEval)
	if err != nil || len(cases) < 6 {
		t.Fatalf("the eval %s: %d cases, %v", theEval, len(cases), err)
	}
	return cases
}

// evalAnswers makes a sidecar that knows the expected answer of every case of theEval, and answers "wrong" to the cases listed.
func evalAnswers(t *testing.T, wrong ...int) *evalSidecar {
	t.Helper()
	cases := evalCases(t)
	sc := &evalSidecar{answers: map[string]string{}, wrong: map[string]bool{}}
	for _, c := range cases {
		sc.answers[c.Example().Prompt()] = c.Output
	}
	for _, i := range wrong {
		sc.wrong[cases[i].Example().Prompt()] = true
	}
	return sc
}

// evalModel records a trained model whose dataset is the one theEval is named after.
func evalModel(t *testing.T, d *sql.DB, job string) int64 {
	t.Helper()
	mid, _, err := relational.RecordTrainedModel(context.Background(), d, relational.TrainedModel{
		JobID: job, ModelSize: "12b", Dataset: theEval + ".jsonl", NumTrainSteps: 5, Path: "trained/" + job})
	if err != nil {
		t.Fatal(err)
	}
	return mid
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

func (c evalCall) eval(t *testing.T) relational.TrainedModelRun {
	t.Helper()
	var e relational.TrainedModelRun
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

func getEval(t *testing.T, m *training.EvalManager, id int64) (relational.TrainedModelRun, []relational.TrainedModelCase) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(id))
	m.GetOf()(rec, req)
	var out struct {
		Run   relational.TrainedModelRun
		Cases []relational.TrainedModelCase
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("get eval: %d %s", rec.Code, rec.Body)
	}
	return out.Run, out.Cases
}

func TestEvalATrainedModel(t *testing.T) { dbtest.Run(t, testEvalATrainedModel) }

func testEvalATrainedModel(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d}}
	mid := evalModel(t, d, "jobeval1")
	cases0 := evalCases(t)
	sc, judge := evalAnswers(t, 3), &evalJudge{}
	m := training.NewEvalManager(ctx, dbs, sc, judge)

	// With no "eval" the model is asked the eval named like its training dataset.
	call := startEval(m, fmt.Sprint(mid), `{"limit": 5, "batch_size": 2, "judge_model": "my-judge", "max_tokens": 77, "temperature": 0.2}`)
	if call.code != http.StatusAccepted {
		t.Fatalf("start: %d %s", call.code, call.body)
	}
	started := call.eval(t)
	if started.Status != relational.TrainedModelRunRunning || started.Kind != "eval" || started.TrainedModelID != mid || started.Eval != theEval || started.NumExamples != 5 ||
		started.Dataset != "" || !strings.Contains(string(started.Params), `"batch_size":2`) {
		t.Fatalf("started: %+v", started)
	}
	m.Wait()

	ev, cases := getEval(t, m, started.ID)
	if ev.Status != relational.TrainedModelRunCompleted || ev.FinishedAt == nil || ev.Error != "" {
		t.Fatalf("finished: %+v", ev)
	}
	if ev.NumAnswered != 5 || ev.NumJudged != 5 || ev.NumErrored != 0 || ev.NumCorrect != 4 || ev.NumIncorrect != 1 || ev.ScoreSum != 21 || ev.NumBatches != 3 {
		t.Fatalf("counters: %+v", ev)
	}
	// 5 questions in batches of 2: three judge calls (2, 2, 1), not five.
	if judge.calls != 3 || fmt.Sprint(judge.sizes) != "[2 2 1]" || judge.model != "my-judge" {
		t.Fatalf("judge: %d calls, sizes %v, model %q", judge.calls, judge.sizes, judge.model)
	}
	if ev.JudgeModel != "judge-model" || ev.JudgeCalls != 3 || ev.JudgeInputTokens != 150 || ev.JudgeOutputTokens != 60 || ev.JudgeCostUSD != 1.5 {
		t.Fatalf("judge usage: %+v", ev)
	}
	// The cases are those of the eval, with their names.
	if len(cases) != 5 || cases[3].Response != "wrong" || cases[3].Score == nil || *cases[3].Score != 1 || cases[0].Reference != cases0[0].Output || cases[0].Reason != "because" {
		t.Fatalf("cases: %+v", cases)
	}
	for i, c := range cases {
		if c.Name != cases0[i].Name || c.Instruction != cases0[i].Instruction || c.Idx != i || c.RunID != started.ID {
			t.Fatalf("case %d: %+v, want the eval's %q", i, c, cases0[i].Name)
		}
	}
	// The model got the eval's questions on its own weights, with the settings of the eval.
	if len(sc.reqs) != 5 || sc.reqs[0].Prompt != cases0[0].Example().Prompt() || sc.reqs[0].ModelPath != "trained/jobeval1" || sc.reqs[0].ModelSize != "12b" ||
		sc.reqs[0].MaxTokens != 77 || sc.reqs[0].Temperature == nil || *sc.reqs[0].Temperature != 0.2 {
		t.Fatalf("sidecar requests: %+v", sc.reqs)
	}
	// The judge's calls are recorded like the other LLM calls.
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM gb_inferences WHERE type = 'training_eval_judge' AND stage = 'judge'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("recorded judge calls: %d %v", n, err)
	}
	// The results are stored under the eval's name and the cases' names, like a knowledge eval's.
	if err := d.QueryRow(fmt.Sprintf(`SELECT count(*) FROM gb_experiment_case_results WHERE type = 'training' AND eval = '%s' AND case_name = '%s'`, theEval, cases0[3].Name)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stored case result: %d %v", n, err)
	}

	// Named cases only; the model's evals are listed, newest first, and run again.
	named := startEval(m, fmt.Sprint(mid), `{"eval": "`+theEval+`", "cases": ["`+cases0[2].Name+`", "`+cases0[1].Name+`"]}`)
	if named.code != http.StatusAccepted || named.eval(t).NumExamples != 2 {
		t.Fatalf("named cases: %d %s", named.code, named.body)
	}
	m.Wait()
	if _, cs := getEval(t, m, named.eval(t).ID); len(cs) != 2 || cs[0].Name != cases0[1].Name || cs[1].Name != cases0[2].Name {
		t.Fatalf("named cases ran: %+v", cs)
	}
	if c := startEval(m, fmt.Sprint(mid), `{"limit": 3}`); c.code != http.StatusAccepted || c.eval(t).NumExamples != 3 {
		t.Fatalf("limited eval: %d %s", c.code, c.body)
	}
	m.Wait()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(mid))
	m.ListOf()(rec, req)
	var list struct {
		Evals []relational.TrainedModelRun
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 || len(list.Evals) != 3 || list.Evals[0].NumExamples != 3 || list.Evals[0].Status != "completed" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
}

func TestEvalRefusals(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d}}
	ok := evalModel(t, d, "j-ok")
	dflt, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j-default", ModelSize: "4b", NumTrainSteps: 1, Path: "trained/j-default"})
	other, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j-other", ModelSize: "4b", Dataset: "other.jsonl", NumTrainSteps: 1, Path: "trained/j-other"})
	m := training.NewEvalManager(ctx, dbs, evalAnswers(t), &evalJudge{})

	for _, tc := range []struct {
		name, model, body string
		want              int
	}{
		{"unknown model", "99", `{}`, 404},
		{"bad id", "0", `{}`, 400},
		{"model without a dataset: no eval to default to", fmt.Sprint(dflt), `{}`, 400},
		{"no eval is named like the dataset", fmt.Sprint(other), `{}`, 400},
		{"unknown eval", fmt.Sprint(ok), `{"eval": "nope"}`, 400},
		{"unknown case", fmt.Sprint(ok), `{"cases": ["nope"]}`, 400},
		{"bad json", fmt.Sprint(ok), `{`, 400},
		{"batch too big", fmt.Sprint(ok), `{"batch_size": 1000}`, 400},
		{"negative limit", fmt.Sprint(ok), `{"limit": -1}`, 400},
		{"bad temperature", fmt.Sprint(ok), `{"temperature": 5}`, 400},
		{"too many tokens", fmt.Sprint(ok), `{"max_tokens": 999999}`, 400},
	} {
		if c := startEval(m, tc.model, tc.body); c.code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, c.code, c.body, tc.want)
		}
	}
	// A model of another dataset can be asked an eval by name.
	if c := startEval(m, fmt.Sprint(other), `{"eval": "`+theEval+`", "limit": 1}`); c.code != 202 {
		t.Errorf("an eval by name for a model of another dataset: %d %s", c.code, c.body)
	}
	m.Wait()
	// Without a sidecar or without an LLM there is nothing to run the eval with.
	if c := startEval(training.NewEvalManager(ctx, dbs, nil, &evalJudge{}), fmt.Sprint(ok), `{}`); c.code != 503 {
		t.Errorf("no sidecar: %d", c.code)
	}
	if c := startEval(training.NewEvalManager(ctx, dbs, &evalSidecar{}, nil), fmt.Sprint(ok), `{}`); c.code != 503 {
		t.Errorf("no LLM: %d", c.code)
	}
	if evals, _ := relational.ListTrainedModelEvalRuns(ctx, d, 0, 0); len(evals) != 1 {
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
	dbs := []training.Database{{Name: "standard", DB: d}}
	mid := evalModel(t, d, "j1")
	sc := evalAnswers(t)
	sc.block, sc.started = make(chan struct{}), make(chan struct{}, 1)
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
	if ev.Status != relational.TrainedModelRunCancelled || ev.FinishedAt == nil || len(cases) != 0 {
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
	dbs := []training.Database{{Name: "standard", DB: d}}
	mid := evalModel(t, d, "j1")
	m := training.NewEvalManager(ctx, dbs, &failingInferer{}, &evalJudge{})
	c := startEval(m, fmt.Sprint(mid), `{"batch_size": 2}`)
	m.Wait()
	ev, cases := getEval(t, m, c.eval(t).ID)
	if ev.Status != relational.TrainedModelRunErrored || !strings.Contains(ev.Error, "weights are gone") || ev.NumErrored != 4 || ev.NumJudged != 0 || len(cases) != 4 ||
		cases[0].Status != relational.TrainedModelCaseErrored || !strings.Contains(cases[0].Error, "weights are gone") {
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
	id, err := relational.RecordTrainedModelEvalRun(ctx, d, relational.TrainedModelRun{TrainedModelID: mid, Eval: theEval})
	if err != nil {
		t.Fatal(err)
	}
	training.NewEvalManager(ctx, []training.Database{{Name: "standard", DB: d}}, nil, nil)
	ev, err := relational.GetTrainedModelEvalRun(ctx, d, id)
	if err != nil || ev.Status != relational.TrainedModelRunErrored || !strings.Contains(ev.Error, "restart") {
		t.Fatalf("%+v %v", ev, err)
	}
}

// sseEvents reads a server-sent events body and sends the data of each event to the channel.
func sseEvents(body io.Reader) <-chan training.RunEvent {
	out := make(chan training.RunEvent, 100)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var e training.RunEvent
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
	dbs := []training.Database{{Name: "standard", DB: d}}
	mid := evalModel(t, d, "jobeval9")
	cases0 := evalCases(t)
	// The model holds its first answer until the watcher is connected.
	sc := evalAnswers(t, 1)
	sc.block, sc.started = make(chan struct{}), make(chan struct{}, 10)
	m := training.NewEvalManager(ctx, dbs, sc, &evalJudge{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /evals/{id}/events", m.EventsOf())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	started := startEval(m, fmt.Sprint(mid), `{"limit": 3, "batch_size": 2}`).eval(t)
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
	next := func() training.RunEvent {
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
		return training.RunEvent{}
	}
	if e := next(); e.Type != "snapshot" || e.Run == nil || e.Run.Status != "running" || len(e.Cases) != 0 {
		t.Fatalf("first event: %+v", e)
	}
	close(sc.block)
	var got []string
	answers := 0
	var last training.RunEvent
	for e := next(); ; e = next() {
		switch e.Type {
		case "answer":
			it := e.Cases[0]
			if it.Status != "answered" || it.Score != nil || it.Idx != answers || it.Name != cases0[answers].Name {
				t.Fatalf("answer event: %+v", it)
			}
			answers++
			got = append(got, fmt.Sprintf("%d=%s", it.Idx, it.Response))
		case "batch":
			if e.Run == nil || e.Run.NumExamples != 3 || len(e.Cases) == 0 || e.Cases[0].Status != "judged" || e.Cases[0].Score == nil {
				t.Fatalf("batch event: %+v", e)
			}
			got = append(got, fmt.Sprintf("batch%d", len(e.Cases)))
		}
		last = e
		if e.Type == "end" {
			break
		}
	}
	if want := fmt.Sprintf("[0=%s 1=wrong batch2 2=%s batch1]", cases0[0].Output, cases0[2].Output); fmt.Sprint(got) != want {
		t.Fatalf("events: %v", got)
	}
	if last.Run == nil || last.Run.Status != "completed" || last.Run.NumJudged != 3 {
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
	if e := <-late; e.Type != "snapshot" || len(e.Cases) != 3 || e.Cases[1].Status != "judged" || e.Run.Status != "completed" {
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
	dbs := []training.Database{{Name: "standard", DB: d}}
	loss := 0.8
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobexp1", ModelSize: "4b", Dataset: theEval + ".jsonl", NumTrainSteps: 3, FinalLoss: &loss, Path: "trained/jobexp1"})
	if err != nil {
		t.Fatal(err)
	}
	m := training.NewEvalManager(ctx, dbs, evalAnswers(t, 1), &evalJudge{})

	// An eval without an experiment is not in any; with a name it is a run of it, and the same name is the same experiment.
	if c := startEval(m, fmt.Sprint(mid), `{"limit": 4}`); c.code != http.StatusAccepted {
		t.Fatalf("start: %d %s", c.code, c.body)
	}
	m.Wait()
	body := `{"limit": 4, "experiment": " sweep ", "experiment_description": "first try", "experiment_config": {"lr": 0.001}}`
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
	if r.ID != first.eval(t).ID || r.Status != relational.TrainedModelRunCompleted || r.Model.ID != mid || r.Model.FinalLoss == nil ||
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

func TestEvalDefinitions(t *testing.T) {
	rec := httptest.NewRecorder()
	training.DefinitionsOf()(rec, httptest.NewRequest("GET", "/x", nil))
	var out struct {
		Evals []struct {
			Name  string
			Cases []struct{ Name, Instruction, Output string }
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 || len(out.Evals) == 0 {
		t.Fatalf("definitions: %d %s", rec.Code, rec.Body)
	}
	if e := out.Evals[0]; e.Name != theEval || len(e.Cases) != len(evalCases(t)) || e.Cases[0].Name == "" || e.Cases[0].Instruction == "" || e.Cases[0].Output == "" {
		t.Fatalf("eval: %+v", e)
	}
}
