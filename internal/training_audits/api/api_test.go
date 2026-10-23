package api_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/training_audits"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/api"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/audittest"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
)

func server(t *testing.T, m *training_audits.Manager) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	api.New(m).Register(mux, "/v1/gb/training")
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func call(t *testing.T, method, url, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		buf.WriteString(sc.Text() + "\n")
	}
	return resp.StatusCode, []byte(buf.String())
}

func decode[T any](t *testing.T, code int, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%d %s", code, body)
	}
	return v
}

func TestAuditEndpoints(t *testing.T) { audittest.Run(t, testAuditEndpoints) }

func testAuditEndpoints(t *testing.T, d *sql.DB, ops training_audits.Ops) {
	ctx := context.Background()
	mid := audittest.RecordModel(t, d, "jobapi", "mine.jsonl")
	m := training_audits.NewManager(ctx, d, ops, []string{audittest.DatasetDir(t, 5)}, &audittest.Sidecar{Wrong: map[string]bool{"q3": true}}, &audittest.Judge{})
	s := server(t, m)
	base := s.URL + "/v1/gb/training"

	code, body := call(t, "POST", fmt.Sprintf("%s/models/%d/audits", base, mid), `{"batch_size": 2, "limit": 4}`)
	if code != http.StatusAccepted {
		t.Fatalf("start: %d %s", code, body)
	}
	started := decode[core.AuditRun](t, code, body)
	if started.Status != core.StatusRunning || started.Counts.Total != 4 || started.Dataset != "mine" {
		t.Fatalf("started: %+v", started)
	}
	m.Wait()

	code, body = call(t, "GET", fmt.Sprintf("%s/audits/%d", base, started.ID), "")
	got := decode[core.AuditRun](t, code, body)
	if code != 200 || got.Status != core.StatusCompleted || len(got.CaseRuns) != 4 || got.NumCorrect != 3 || got.CaseRuns[3].Response != "wrong" ||
		got.CaseRuns[0].Reference != "a0" || got.CaseRuns[0].JudgeScore == nil || !strings.Contains(string(body), `"case_runs"`) {
		t.Fatalf("get: %d %s", code, body)
	}

	code, body = call(t, "GET", fmt.Sprintf("%s/models/%d/audits?limit=5", base, mid), "")
	list := decode[struct{ Audits []core.AuditRun }](t, code, body)
	if code != 200 || len(list.Audits) != 1 || list.Audits[0].ID != started.ID || list.Audits[0].CaseRuns != nil {
		t.Fatalf("list: %d %s", code, body)
	}

	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/models/999/audits", "", 404},
		{"POST", "/models/x/audits", "", 400},
		{"POST", fmt.Sprintf("/models/%d/audits", mid), "{not json", 400},
		{"POST", fmt.Sprintf("/models/%d/audits", mid), `{"batch_size": 100}`, 400},
		{"GET", "/models/999/audits", "", 404},
		{"GET", fmt.Sprintf("/models/%d/audits?limit=0", mid), "", 400},
		{"GET", "/audits/999", "", 404},
		{"GET", "/audits/0", "", 400},
		{"GET", "/audits/999/events", "", 404},
		{"POST", "/audits/999/cancel", "", 404},
		{"POST", fmt.Sprintf("/audits/%d/cancel", started.ID), "", 409},
	} {
		if code, body := call(t, tc.method, base+tc.path, tc.body); code != tc.want || !strings.Contains(string(body), `"error"`) {
			t.Errorf("%s %s: %d %s, want %d", tc.method, tc.path, code, body, tc.want)
		}
	}

	// The feed of a finished audit is the snapshot and the end.
	code, body = call(t, "GET", fmt.Sprintf("%s/audits/%d/events", base, started.ID), "")
	events := parseSSE(string(body))
	if code != 200 || len(events) != 2 || events[0].name != "snapshot" || events[1].name != "end" {
		t.Fatalf("events of a finished audit: %d %s", code, body)
	}
	snap := decode[training_audits.Event](t, code, []byte(events[0].data))
	if snap.Type != "snapshot" || snap.Run.ID != started.ID || len(snap.CaseRuns) != 4 {
		t.Errorf("snapshot: %+v", snap)
	}
	if end := decode[training_audits.Event](t, code, []byte(events[1].data)); end.Run.Status != core.StatusCompleted {
		t.Errorf("end: %+v", end.Run)
	}
}

func TestAuditEndpointsRefuseWithoutSidecar(t *testing.T) {
	d := audittest.OpenSQLite(t)
	mid := audittest.RecordModel(t, d, "jobnone", "mine.jsonl")
	m := training_audits.NewManager(context.Background(), d, training_audits.SQLiteOps, []string{audittest.DatasetDir(t, 1)}, nil, nil)
	code, body := call(t, "POST", fmt.Sprintf("%s/v1/gb/training/models/%d/audits", server(t, m).URL, mid), "")
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "sidecar") {
		t.Errorf("%d %s", code, body)
	}
}

func TestAuditLiveFeedAndCancel(t *testing.T) {
	d := audittest.OpenSQLite(t)
	mid := audittest.RecordModel(t, d, "jobfeed", "mine.jsonl")
	sc := &audittest.Sidecar{Block: make(chan struct{}), Started: make(chan struct{}, 1)}
	m := training_audits.NewManager(context.Background(), d, training_audits.SQLiteOps, []string{audittest.DatasetDir(t, 4)}, sc, &audittest.Judge{})
	base := server(t, m).URL + "/v1/gb/training"

	code, body := call(t, "POST", fmt.Sprintf("%s/models/%d/audits", base, mid), `{"batch_size": 2}`)
	if code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	run := decode[core.AuditRun](t, code, body)
	if code, body = call(t, "POST", fmt.Sprintf("%s/models/%d/audits", base, mid), ""); code != http.StatusConflict {
		t.Fatalf("second start: %d %s", code, body)
	}
	<-sc.Started

	resp, err := http.Get(fmt.Sprintf("%s/audits/%d/events", base, run.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	names := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if n, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				names <- n
			}
		}
		close(names)
	}()
	next := func() string {
		select {
		case n, ok := <-names:
			if !ok {
				return "<closed>"
			}
			return n
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for an event")
		}
		return ""
	}
	if n := next(); n != "snapshot" {
		t.Fatalf("first event %q", n)
	}

	// Cancel the audit while the model is blocked on its first question: the feed ends.
	if code, body = call(t, "POST", fmt.Sprintf("%s/audits/%d/cancel", base, run.ID), ""); code != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", code, body)
	}
	var seen []string
	for n := next(); n != "<closed>"; n = next() {
		seen = append(seen, n)
	}
	if len(seen) == 0 || seen[len(seen)-1] != "end" {
		t.Errorf("events after the cancel: %v", seen)
	}
	m.Wait()
	code, body = call(t, "GET", fmt.Sprintf("%s/audits/%d", base, run.ID), "")
	if got := decode[core.AuditRun](t, code, body); got.Status != core.StatusCancelled {
		t.Errorf("after the cancel: %+v", got)
	}
}

type sseEvent struct{ name, data string }

func parseSSE(s string) []sseEvent {
	var out []sseEvent
	for _, block := range strings.Split(s, "\n\n") {
		var e sseEvent
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				e.name = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				e.data = v
			}
		}
		if e.name != "" {
			out = append(out, e)
		}
	}
	return out
}
