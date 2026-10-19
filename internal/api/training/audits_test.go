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
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/training"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

// The fake model, judge and dataset of the eval tests (evals_test.go) serve here too: an audit asks the same questions.

func startAudit(m *training.AuditManager, modelID, body string) evalCall {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req.SetPathValue("id", modelID)
	m.StartOf()(rec, req)
	return evalCall{rec.Code, rec.Body.Bytes()}
}

func auditOf(t *testing.T, c evalCall) relational.TrainedModelAuditRun {
	t.Helper()
	var a relational.TrainedModelAuditRun
	if err := json.Unmarshal(c.body, &a); err != nil {
		t.Fatalf("%d %s", c.code, c.body)
	}
	return a
}

func getAudit(t *testing.T, m *training.AuditManager, id int64) (relational.TrainedModelAuditRun, []relational.TrainedModelAuditCase) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(id))
	m.GetOf()(rec, req)
	var out struct {
		Audit relational.TrainedModelAuditRun
		Cases []relational.TrainedModelAuditCase
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("get audit: %d %s", rec.Code, rec.Body)
	}
	return out.Audit, out.Cases
}

func TestAuditATrainedModel(t *testing.T) { dbtest.Run(t, testAuditATrainedModel) }

func testAuditATrainedModel(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 5)}}
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobaudit1", ModelSize: "12b", Dataset: "mine.jsonl", NumTrainSteps: 5, Path: "trained/jobaudit1"})
	if err != nil {
		t.Fatal(err)
	}
	sc, judge := &evalSidecar{wrong: map[string]bool{"q3": true}}, &evalJudge{}
	m := training.NewAuditManager(ctx, dbs, sc, judge)

	call := startAudit(m, fmt.Sprint(mid), `{"batch_size": 2, "judge_model": "my-judge", "max_tokens": 77, "temperature": 0.2}`)
	if call.code != http.StatusAccepted {
		t.Fatalf("start: %d %s", call.code, call.body)
	}
	started := auditOf(t, call)
	if started.Status != relational.TrainedModelRunRunning || started.TrainedModelID != mid || started.Dataset != "mine" || started.NumExamples != 5 ||
		len(started.DatasetSHA256) != 64 || !strings.Contains(string(started.Params), `"batch_size":2`) {
		t.Fatalf("started: %+v", started)
	}
	m.Wait()

	au, cases := getAudit(t, m, started.ID)
	if au.Status != relational.TrainedModelRunCompleted || au.FinishedAt == nil || au.Error != "" {
		t.Fatalf("finished: %+v", au)
	}
	if au.NumAnswered != 5 || au.NumJudged != 5 || au.NumErrored != 0 || au.NumCorrect != 4 || au.NumIncorrect != 1 || au.ScoreSum != 21 || au.NumBatches != 3 {
		t.Fatalf("counters: %+v", au)
	}
	if judge.calls != 3 || fmt.Sprint(judge.sizes) != "[2 2 1]" || judge.model != "my-judge" {
		t.Fatalf("judge: %d calls, sizes %v, model %q", judge.calls, judge.sizes, judge.model)
	}
	if au.JudgeModel != "judge-model" || au.JudgeCalls != 3 || au.JudgeInputTokens != 150 || au.JudgeOutputTokens != 60 || au.JudgeCostUSD != 1.5 {
		t.Fatalf("judge usage: %+v", au)
	}
	if len(cases) != 5 || cases[3].Response != "wrong" || cases[3].Score == nil || *cases[3].Score != 1 || cases[0].Reference != "a0" ||
		cases[0].Reason != "because" || cases[0].Status != relational.TrainedModelCaseJudged || cases[0].AuditRunID != started.ID {
		t.Fatalf("cases: %+v", cases)
	}
	// The model got the dataset's questions on its own weights, with the settings of the audit.
	if len(sc.reqs) != 5 || sc.reqs[0].Prompt != "q0" || sc.reqs[0].ModelPath != "trained/jobaudit1" || sc.reqs[0].ModelSize != "12b" ||
		sc.reqs[0].MaxTokens != 77 || sc.reqs[0].Temperature == nil || *sc.reqs[0].Temperature != 0.2 {
		t.Fatalf("sidecar requests: %+v", sc.reqs)
	}
	// The judge's calls are recorded like the other LLM calls, under the audit's own type.
	var n, nEval int
	if training.CallAuditJudge != "training_audit_judge" {
		t.Fatalf("call type %q", training.CallAuditJudge)
	}
	if err := d.QueryRow(`SELECT count(*) FROM gb_inferences WHERE type = 'training_audit_judge' AND stage = 'judge'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("recorded judge calls: %d %v", n, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM gb_inferences WHERE type = 'eval_judge'`).Scan(&nEval); err != nil || nEval != 0 {
		t.Fatalf("an audit recorded eval judge calls: %d %v", nEval, err)
	}
	// An audit is not an eval run.
	if evs, err := relational.ListTrainedModelEvalRuns(ctx, d, mid, 0); err != nil || len(evs) != 0 {
		t.Fatalf("an audit shows up as an eval: %+v %v", evs, err)
	}

	// The model's audits are listed, newest first, and run again.
	if c := startAudit(m, fmt.Sprint(mid), `{"limit": 3}`); c.code != http.StatusAccepted || auditOf(t, c).NumExamples != 3 {
		t.Fatalf("second audit: %d %s", c.code, c.body)
	}
	m.Wait()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", fmt.Sprint(mid))
	m.ListOf()(rec, req)
	var list struct {
		Audits []relational.TrainedModelAuditRun
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 || len(list.Audits) != 2 || list.Audits[0].NumExamples != 3 || list.Audits[0].Status != "completed" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
}

func TestAuditRefusals(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 2)}}
	ok, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j-ok", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 1, Path: "trained/j-ok"})
	dflt, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j-default", ModelSize: "4b", NumTrainSteps: 1, Path: "trained/j-default"})
	gone, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j-gone", ModelSize: "4b", Dataset: "deleted.jsonl", NumTrainSteps: 1, Path: "trained/j-gone"})
	m := training.NewAuditManager(ctx, dbs, &evalSidecar{}, &evalJudge{})

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
		{"negative limit", fmt.Sprint(ok), `{"limit": -1}`, 400},
		{"bad temperature", fmt.Sprint(ok), `{"temperature": 5}`, 400},
		{"too many tokens", fmt.Sprint(ok), `{"max_tokens": 999999}`, 400},
	} {
		if c := startAudit(m, tc.model, tc.body); c.code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, c.code, c.body, tc.want)
		}
	}
	if c := startAudit(training.NewAuditManager(ctx, dbs, nil, &evalJudge{}), fmt.Sprint(ok), `{}`); c.code != 503 {
		t.Errorf("no sidecar: %d", c.code)
	}
	if c := startAudit(training.NewAuditManager(ctx, dbs, &evalSidecar{}, nil), fmt.Sprint(ok), `{}`); c.code != 503 {
		t.Errorf("no LLM: %d", c.code)
	}
	if audits, _ := relational.ListTrainedModelAuditRuns(ctx, d, 0, 0); len(audits) != 0 {
		t.Errorf("refused audits left rows: %+v", audits)
	}
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
			t.Errorf("unknown audit: %d", rec.Code)
		}
	}
}

func TestAuditCancelAndConflict(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 6)}}
	mid, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j1", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 1, Path: "trained/j1"})
	sc := &evalSidecar{block: make(chan struct{}), started: make(chan struct{}, 1)}
	m := training.NewAuditManager(ctx, dbs, sc, &evalJudge{})

	first := startAudit(m, fmt.Sprint(mid), `{"batch_size": 3}`)
	if first.code != 202 {
		t.Fatalf("start: %d %s", first.code, first.body)
	}
	id := auditOf(t, first).ID
	<-sc.started
	if c := startAudit(m, fmt.Sprint(mid), `{}`); c.code != http.StatusConflict {
		t.Fatalf("second audit of the same model: %d %s", c.code, c.body)
	}
	cancel := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/x", nil)
		req.SetPathValue("id", fmt.Sprint(id))
		m.CancelOf()(rec, req)
		return rec.Code
	}
	if code := cancel(); code != 202 {
		t.Fatalf("cancel: %d", code)
	}
	m.Wait()
	au, cases := getAudit(t, m, id)
	if au.Status != relational.TrainedModelRunCancelled || au.FinishedAt == nil || len(cases) != 0 {
		t.Fatalf("cancelled: %+v, %d cases", au, len(cases))
	}
	if code := cancel(); code != http.StatusConflict {
		t.Fatalf("cancel again: %d", code)
	}
	close(sc.block)
	if c := startAudit(m, fmt.Sprint(mid), `{}`); c.code != 202 {
		t.Fatalf("audit after cancel: %d %s", c.code, c.body)
	}
	m.Wait()
}

func TestAuditFailsWhenTheModelDoesNotAnswer(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 20)}}
	mid, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j1", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 1, Path: "trained/j1"})
	m := training.NewAuditManager(ctx, dbs, &failingInferer{}, &evalJudge{})
	c := startAudit(m, fmt.Sprint(mid), `{"batch_size": 2}`)
	m.Wait()
	au, cases := getAudit(t, m, auditOf(t, c).ID)
	if au.Status != relational.TrainedModelRunErrored || !strings.Contains(au.Error, "weights are gone") || au.NumErrored != 4 || au.NumJudged != 0 || len(cases) != 4 ||
		cases[0].Status != relational.TrainedModelCaseErrored || !strings.Contains(cases[0].Error, "weights are gone") {
		t.Fatalf("%+v cases %+v", au, cases)
	}
}

func TestAuditsInterruptedByARestartAreFailed(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	ctx := context.Background()
	mid, _, _ := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: "j1", ModelSize: "4b", NumTrainSteps: 1, Path: "trained/j1"})
	id, err := relational.RecordTrainedModelAuditRun(ctx, d, relational.TrainedModelAuditRun{TrainedModelID: mid})
	if err != nil {
		t.Fatal(err)
	}
	training.NewAuditManager(ctx, []training.Database{{Name: "standard", DB: d}}, nil, nil)
	au, err := relational.GetTrainedModelAuditRun(ctx, d, id)
	if err != nil || au.Status != relational.TrainedModelRunErrored || !strings.Contains(au.Error, "restart") {
		t.Fatalf("%+v %v", au, err)
	}
}

func TestAuditLiveFeed(t *testing.T) { dbtest.Run(t, testAuditLiveFeed) }

func testAuditLiveFeed(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	dbs := []training.Database{{Name: "standard", DB: d, DatasetDir: evalDatasetDir(t, 3)}}
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobaudit9", ModelSize: "12b", Dataset: "mine.jsonl", NumTrainSteps: 5, Path: "trained/jobaudit9"})
	if err != nil {
		t.Fatal(err)
	}
	sc := &evalSidecar{wrong: map[string]bool{"q1": true}, block: make(chan struct{}), started: make(chan struct{}, 10)}
	m := training.NewAuditManager(ctx, dbs, sc, &evalJudge{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /audits/{id}/events", m.EventsOf())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	started := auditOf(t, startAudit(m, fmt.Sprint(mid), `{"batch_size": 2}`))
	<-sc.started
	resp, err := http.Get(fmt.Sprintf("%s/audits/%d/events", srv.URL, started.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	events := sseAuditEvents(resp.Body)
	next := func() training.AuditEvent {
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
		return training.AuditEvent{}
	}
	if e := next(); e.Type != "snapshot" || e.Audit == nil || e.Audit.Status != "running" || len(e.Cases) != 0 {
		t.Fatalf("first event: %+v", e)
	}
	close(sc.block)
	var got []string
	answers := 0
	var last training.AuditEvent
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
			if e.Audit == nil || e.Audit.NumExamples != 3 || len(e.Cases) == 0 || e.Cases[0].Status != "judged" || e.Cases[0].Score == nil {
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
	if last.Audit == nil || last.Audit.Status != "completed" || last.Audit.NumJudged != 3 {
		t.Fatalf("end event: %+v", last)
	}
	m.Wait()

	// A watcher that comes after the end gets what was stored, then the end.
	resp2, err := http.Get(fmt.Sprintf("%s/audits/%d/events", srv.URL, started.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	late := sseAuditEvents(resp2.Body)
	if e := <-late; e.Type != "snapshot" || len(e.Cases) != 3 || e.Cases[1].Status != "judged" || e.Audit.Status != "completed" {
		t.Fatalf("late snapshot: %+v", e)
	}
	if e := <-late; e.Type != "end" {
		t.Fatalf("late end: %+v", e)
	}
	if resp3, err := http.Get(srv.URL + "/audits/999/events"); err != nil || resp3.StatusCode != 404 {
		t.Fatalf("unknown audit: %v %v", resp3, err)
	}
}

// sseAuditEvents reads a server-sent events body and sends the data of each event to the channel.
func sseAuditEvents(body io.Reader) <-chan training.AuditEvent {
	out := make(chan training.AuditEvent, 100)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var e training.AuditEvent
				if json.Unmarshal([]byte(data), &e) == nil {
					out <- e
				}
			}
		}
	}()
	return out
}
