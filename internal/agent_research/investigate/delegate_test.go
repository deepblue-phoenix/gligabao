package investigate_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
)

// fakeWorkers answers the questions with the results of its map (by question id). With all > 0, a worker waits until that many workers are
// running at once (so a loop that ran them one after the other would time out).
type fakeWorkers struct {
	mu      sync.Mutex
	results map[string]investigate.WorkerResult
	errs    map[string]error
	reqs    []investigate.WorkerRequest
	all     int
	running int
	started chan struct{}
	block   <-chan struct{}
}

func (f *fakeWorkers) Run(ctx context.Context,
	req investigate.WorkerRequest) (investigate.WorkerResult, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.running++
	if f.running == f.all && f.started != nil {
		close(f.started)
	}
	f.mu.Unlock()
	if f.started != nil {
		select {
		case <-f.started:
		case <-time.After(5 * time.Second):
			return investigate.WorkerResult{}, errors.New("the workers did not run at the same time")
		}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return investigate.WorkerResult{}, ctx.Err()
		}
	}
	res, ok := f.results[req.QuestionID]
	if !ok { // by default a worker gives up: the question is out of the way
		res.Findings = findings(investigate_core.FindingUnresolvable, "")
	}
	return res, f.errs[req.QuestionID]
}

func findings(status,
	answer string,
	claims ...investigate_core.FindingClaim) investigate_core.Findings {
	return investigate_core.Findings{
		Status: status,
		Answer: answer,
		Claims: claims,
		Queries: []investigate_core.FindingQuery{{
			Query:   "worker query for " + answer,
			Tool:    "web_search",
			Outcome: "ok",
		}},
	}
}

func claim(key,
	url string) investigate_core.FindingClaim {
	return investigate_core.FindingClaim{
		Key:  key,
		Text: "claim " + key,
		Sources: []investigate_core.FindingSource{{
			URL:         url,
			Independent: true,
		}},
	}
}

// planThree is the planning turn: three ready questions, two of them deeper than the answer-challenge rule (children of q_002 and q_003).
func planThree() reply {
	return reply{
		calls: []tc{
			{"add_question", m{"parent_id": "q_001", "question": "first", "priority": 0.9, "validation_criteria": "c1"}},
			{"add_question", m{"parent_id": "q_001", "question": "second", "priority": 0.8, "validation_criteria": "c2"}},
			{"add_question", m{"parent_id": "q_001", "question": "third", "priority": 0.7, "validation_criteria": "c3"}},
		},
	}
}

// stopAfter is a model that plays the script and then fails with errStop, so that a run ends after the turns the test is about.
var errStop = errors.New("stop here")

func stopAfter(fm *fakeModel) investigate.ModelFunc {
	return func(ctx context.Context,
		p map[string]any) (*llm.RawResponse, error) {
		if len(fm.script) == 0 {
			return nil, errStop
		}
		return fm.call(ctx, p)
	}
}

func runWith(t *testing.T,
	e *env,
	fm *fakeModel,
	dl investigate.Delegation,
	hooks investigate.Hooks) (investigate.RunResult, error) {
	t.Helper()
	return investigate.Run(context.Background(), investigate.Config{
		Session:    e.sess,
		Model:      stopAfter(fm),
		System:     "You are a researcher.",
		Task:       "Does X fit in 16 GB of VRAM?",
		External:   fakeTools(),
		Hooks:      hooks,
		Delegation: dl,
	})
}

func TestFanOutAndReduce(t *testing.T) {
	dbtest.Run(t, testFanOutAndReduce)
}

func testFanOutAndReduce(t *testing.T,
	d *sql.DB) {
	e := newEnv(t,
		d,
		loopLimits())
	fm := &fakeModel{
		t:      t,
		cost:   0.01,
		script: []reply{planThree()},
	}
	fw := &fakeWorkers{
		all:     3,
		started: make(chan struct{}),
		results: map[string]investigate.WorkerResult{
			"q_002": {
				Findings: findings("answered",
					"A (c1)",
					claim("c1", "https://a.example/x")),
				InputTokens:  10,
				OutputTokens: 5,
				CostUSD:      0.5,
			},
			"q_003": {
				Findings: findings("partial", "half"),
				CostUSD:  0.25,
			},
			"q_004": {
				Findings: findings("unresolvable", ""),
				CostUSD:  0.25,
			},
		},
	}
	var states []string
	hooks := investigate.Hooks{
		Entered: func(_ int,
			state,
			detail string) {
			states = append(states, state+":"+detail)
		},
	}
	res, err := runWith(t,
		e,
		fm,
		investigate.Delegation{
			Workers:       fw,
			MaxParallel:   3,
			ContextTokens: 500,
		},
		hooks)
	if !errors.Is(err, errStop) {
		t.Fatalf("err: %v", err)
	}
	if want := "delegating:q_002, q_003, q_004"; !slices.Contains(states, want) || !slices.Contains(states, "waiting:q_002, q_003, q_004") {
		t.Errorf("states: %v", states)
	}
	if len(fw.reqs) != 3 {
		t.Fatalf("requests: %+v", fw.reqs)
	}
	for _, r := range fw.reqs {
		if !strings.Contains(r.Brief, "Your question ("+r.QuestionID) || r.MaxCostUSD != 0 {
			t.Errorf("request: %+v", r)
		}
	}
	// The supervisor made one model call (the planning): the fan-out is code.
	if res.Turns != 1 {
		t.Errorf("turns: %d", res.Turns)
	}
	st, err := e.sess.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	q2, _ := st.Frontier.Get("q_002")
	q3, _ := st.Frontier.Get("q_003")
	q4, _ := st.Frontier.Get("q_004")
	if q2.Status != investigate_core.StatusInProgress || q3.Status != investigate_core.StatusPending || q4.Status != investigate_core.StatusUnresolvable {
		t.Fatalf("statuses: %s %s %s",
			q2.Status,
			q3.Status,
			q4.Status)
	}
	if len(st.Notes.Claims) != 1 || st.Notes.Claims[0].QuestionID != "q_002" || len(st.Notes.Sources) != 1 {
		t.Errorf("notes: %+v", st.Notes)
	}
	if !strings.Contains(q2.Attempts[0].Query, "worker query") || !strings.Contains(q3.Attempts[len(q3.Attempts)-1].Query, "delegated run") {
		t.Errorf("attempts: %+v %+v",
			q2.Attempts,
			q3.Attempts)
	}
	// The totals of the run are its own: the workers' spending is in their runs, not in this one's (but the breakers saw it).
	if res.CostUSD < 0.0099 || res.CostUSD > 0.0101 || res.InputTokens != 100 {
		t.Errorf("totals: %+v", res.Totals)
	}
	// The transitions of the fan-out are in the log with their trigger.
	if n := countTransitions(t,
		d,
		e,
		string(investigate_core.TriggerDelegated)); n != 3 {
		t.Errorf("delegated transitions: %d", n)
	}
}

func countTransitions(t *testing.T,
	d *sql.DB,
	e *env,
	trigger string) int {
	t.Helper()
	var n int
	q := `SELECT COUNT(*) FROM gb_agent_frontier_transitions WHERE run_id = ? AND trigger = ?`
	if e.pg {
		q = `SELECT COUNT(*) FROM gb_agent_frontier_transitions WHERE run_id = $1 AND trigger = $2`
	}
	if err := d.QueryRow(q,
		e.run.ID,
		trigger).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFailedWorkerSendsItsQuestionBack(t *testing.T) {
	dbtest.Run(t, testFailedWorker)
}

func testFailedWorker(t *testing.T,
	d *sql.DB) {
	e := newEnv(t,
		d,
		loopLimits())
	fm := &fakeModel{
		t:      t,
		script: []reply{planThree()},
	}
	fw := &fakeWorkers{
		results: map[string]investigate.WorkerResult{"q_003": {
			Findings: findings("partial", "x"),
		}, "q_004": {
			Findings: findings("partial", "y"),
		}},
		errs: map[string]error{"q_002": errors.New("the worker blew up")},
	}
	// A worker that fails does not fail the run: the question is pending with the failure logged.
	_, err := runWith(t,
		e,
		fm,
		investigate.Delegation{
			Workers:     fw,
			MaxParallel: 3,
			MaxWorkers:  3,
		},
		investigate.Hooks{})
	if !errors.Is(err, errStop) {
		t.Fatalf("err: %v", err)
	}
	st, _ := e.sess.Load(context.Background())
	q, _ := st.Frontier.Get("q_002")
	// (pending: the loop then selected it itself, which is why it is in progress now)
	if len(q.Attempts) != 1 || !strings.Contains(q.Attempts[0].Outcome, "worker failed: the worker blew up") {
		t.Errorf("%+v", q)
	}
}

func TestDelegationNeedsTwoQuestionsAndRespectsTheCaps(t *testing.T) {
	dbtest.Run(t, testDelegationCaps)
}

func testDelegationCaps(t *testing.T,
	d *sql.DB) {
	plan := reply{
		calls: []tc{{"add_question", m{"parent_id": "q_001", "question": "only", "priority": 0.9, "validation_criteria": "c"}}},
	}
	for name, c := range map[string]struct {
		script   []reply
		dl       investigate.Delegation
		wantRuns int
	}{
		"one question is worked on by the loop": {[]reply{plan}, investigate.Delegation{
			MaxParallel: 3,
		}, 0},
		"max_parallel 1 is no delegation": {[]reply{planThree()}, investigate.Delegation{
			MaxParallel: 1,
		}, 0},
		"the batch is cut to max_parallel": {[]reply{planThree()}, investigate.Delegation{
			MaxParallel: 2,
		}, 2},
		"max_workers cuts it too": {[]reply{planThree()}, investigate.Delegation{
			MaxParallel: 3,
			MaxWorkers:  2,
		}, 2},
		"one worker left is no fan-out": {[]reply{planThree()}, investigate.Delegation{
			MaxParallel: 3,
			MaxWorkers:  1,
		}, 0},
		"min priority": {[]reply{planThree()}, investigate.Delegation{
			MaxParallel: 3,
			MinPriority: 0.75,
		}, 2},
	} {
		e := newEnv(t,
			d,
			loopLimits())
		fw := &fakeWorkers{
			results: map[string]investigate.WorkerResult{},
		}
		c.dl.Workers = fw
		// With no fan-out the loop goes on with the question itself: the next model call (selecting q_002 first) ends the test.
		_, err := runWith(t,
			e,
			&fakeModel{
				t:      t,
				script: c.script,
			},
			c.dl,
			investigate.Hooks{})
		if !errors.Is(err, errStop) {
			t.Fatalf("%s: err: %v",
				name,
				err)
		}
		if len(fw.reqs) != c.wantRuns {
			t.Errorf("%s: %d workers ran, want %d",
				name,
				len(fw.reqs),
				c.wantRuns)
		}
	}
}

func TestFanOutIsCancelledWithTheRun(t *testing.T) {
	dbtest.Run(t, testFanOutCancel)
}

func testFanOutCancel(t *testing.T,
	d *sql.DB) {
	e := newEnv(t,
		d,
		loopLimits())
	fm := &fakeModel{
		t:      t,
		script: []reply{planThree()},
	}
	block := make(chan struct{}) // the workers never finish by themselves
	fw := &fakeWorkers{
		block: block,
	}
	ctx, cancel := context.WithCancel(context.Background())
	hooks := investigate.Hooks{
		Entered: func(_ int,
			state,
			_ string) {
			if state == investigate_core.StateDelegating {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
		},
	}
	_, err := investigate.Run(ctx, investigate.Config{
		Session:  e.sess,
		Model:    stopAfter(fm),
		System:   "s",
		Task:     "Does X fit in 16 GB of VRAM?",
		External: fakeTools(),
		Hooks:    hooks,
		Delegation: investigate.Delegation{
			Workers:     fw,
			MaxParallel: 3,
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err: %v", err)
	}
}

func TestNoCostLimitNoShare(t *testing.T) {
	dbtest.Run(t, testCostShare)
}

func testCostShare(t *testing.T,
	d *sql.DB) {
	l := loopLimits()
	l.MaxCostUSD = 10
	e := newEnv(t,
		d,
		l)
	fw := &fakeWorkers{
		results: map[string]investigate.WorkerResult{},
	}
	_, err := runWith(t,
		e,
		&fakeModel{
			t:      t,
			script: []reply{planThree()},
		},
		investigate.Delegation{
			Workers:     fw,
			MaxParallel: 2,
		},
		investigate.Hooks{})
	if !errors.Is(err, errStop) {
		t.Fatal(err)
	}
	if len(fw.reqs) != 2 || fw.reqs[0].MaxCostUSD < 4.24 || fw.reqs[0].MaxCostUSD > 4.26 {
		// 10 USD, 15% for the synthesis, shared by 2 workers: 4.25 each
		t.Errorf("requests: %+v", fw.reqs)
	}
}
