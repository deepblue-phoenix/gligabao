package remember

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/jev"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

type fakeJev struct {
	err   error
	calls int
}

func (f *fakeJev) Classify(context.Context, string) (*jev.Classification, jev.Call, error) {
	f.calls++
	call := jev.Call{Started: time.Now(), Latency: 12 * time.Millisecond}
	if f.err != nil {
		return nil, call, f.err
	}
	return &jev.Classification{Complexity: jev.Medium, Confidence: 0.7, Model: "jev-1", InputTokens: 30, OutputTokens: 5, Call: call}, call, nil
}

func TestRememberJev(t *testing.T) { dbtest.Run(t, testRememberJev) }

// testRememberJev: with Jev configured the classify_complexity step runs first and its latency is in the trajectory; a
// Jev failure never fails remember; without Jev there is no step.
func testRememberJev(t *testing.T, d *sql.DB) {
	llm := fakeLLM{`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`}
	body := `{"user_id":"u1","text":"Alice works at Acme."}`

	run := func(jv memory_core.Classifier) (steps []map[string]any) {
		t.Helper()
		var opts []memory_core.Option
		if jv != nil {
			opts = append(opts, memory_core.WithJev(jv))
		}
		rec := httptest.NewRecorder()
		testEvalHandler(t, d, llm, opts...).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/eval/remember", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var out struct {
			Trajectory struct {
				Steps []map[string]any `json:"steps"`
			} `json:"trajectory"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Trajectory.Steps
	}
	find := func(steps []map[string]any) map[string]any {
		for _, s := range steps {
			if tc, ok := s["tool_calls"].([]any); ok && len(tc) > 0 && tc[0].(map[string]any)["function_name"] == "classify_complexity" {
				return s
			}
		}
		return nil
	}

	if find(run(nil)) != nil {
		t.Error("classify_complexity ran without Jev")
	}

	fj := &fakeJev{}
	s := find(run(fj))
	if s == nil || fj.calls != 1 {
		t.Fatalf("step = %v, calls = %d", s, fj.calls)
	}
	extra := s["extra"].(map[string]any)
	if extra["latency_ms"].(float64) != 12 || extra["provider"] != "jev" || s["model_name"] != "jev-1" {
		t.Errorf("step = %v", s)
	}
	res := s["observation"].(map[string]any)["results"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(res, `"complexity":"medium"`) || !strings.Contains(res, `"confidence":0.7`) {
		t.Errorf("result = %s", res)
	}

	s = find(run(&fakeJev{err: errors.New("down")}))
	if s == nil || !strings.Contains(s["observation"].(map[string]any)["results"].([]any)[0].(map[string]any)["content"].(string), "down") {
		t.Errorf("failed step = %v", s)
	}
}
