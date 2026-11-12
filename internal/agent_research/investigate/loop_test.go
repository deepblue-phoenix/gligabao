package investigate_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

type env struct {
	d      *sql.DB
	pg     bool
	run    *core.Run
	sess   investigate_core.Session
	phases []string
	states []string
	outs   map[string]string // the output of the tool calls, by "name#k"
}

// tc is one tool call of a scripted reply.
type tc struct {
	name string
	in   m
}

// reply is what the fake model says in one turn: tool calls, or (with none) only text.
type reply struct {
	text  string
	calls []tc
}

// fakeModel plays a script, one reply per model call; it records every request. With autoNotes a turn that only offers the notes tools
// (forced extraction, consolidation) is answered with a notes_scratch call and does not use up the script.
type fakeModel struct {
	t         *testing.T
	script    []reply
	autoNotes bool
	cost      float64
	payloads  []map[string]any
	texts     []string
	n         int
}

func (f *fakeModel) call(_ context.Context,
	payload map[string]any) (*ai.RawResponse, error) {
	raw, _ := json.Marshal(payload)
	f.texts = append(f.texts, string(raw))
	f.payloads = append(f.payloads, payload)
	var r reply
	if f.autoNotes && slices.Equal(f.toolNames(len(f.payloads)-1), []string{"notes_read", "notes_append", "notes_update", "notes_scratch"}) {
		r = reply{
			calls: []tc{{"notes_scratch", m{"text": "note"}}},
		}
	} else {
		if len(f.script) == 0 {
			f.t.Fatalf("the script ran out at model call %d", len(f.payloads))
		}
		r, f.script = f.script[0], f.script[1:]
	}
	f.n++
	var content []json.RawMessage
	if r.text != "" {
		b, _ := json.Marshal(m{"type": "text", "text": r.text})
		content = append(content, b)
	}
	for i, c := range r.calls {
		b, _ := json.Marshal(m{"type": "tool_use", "id": fmt.Sprintf("tu_%d_%d",
			f.n,
			i), "name": c.name, "input": c.in})
		content = append(content, b)
	}
	return &ai.RawResponse{
		Model:        "fake",
		StopReason:   "end_turn",
		Content:      content,
		InputTokens:  100,
		OutputTokens: 50,
		CostUSD:      f.cost,
	}, nil
}

func (f *fakeModel) toolNames(i int) []string {
	var out []string
	for _, tl := range f.payloads[i]["tools"].([]any) {
		out = append(out, tl.(map[string]any)["name"].(string))
	}
	return out
}

func fakeTools() []agent.Tool {
	schema := m{"type": "object", "properties": m{}}
	return []agent.Tool{
		{
			Name:        "web_search",
			Description: "search",
			Schema:      schema,
			Run: func(context.Context,
				json.RawMessage) (string, error) {
				return "a short briefing", nil
			},
		},
		{
			Name:        "fetch_page",
			Description: "read",
			Schema:      schema,
			Run: func(context.Context,
				json.RawMessage) (string, error) {
				return strings.Repeat("page ", 40) + "MARKER" + strings.Repeat(" text", 40), nil
			},
		},
	}
}

func newEnv(t *testing.T,
	d *sql.DB,
	limits investigate_core.Limits) *env {
	run := createRun(t,
		d,
		"user")
	files := investigate.NotesFile{
		Dir: filepath.Join(t.TempDir(), "notes"),
	}
	return &env{
		d:    d,
		pg:   strings.HasSuffix(t.Name(), "postgres"),
		run:  run,
		outs: map[string]string{},
		sess: investigate_core.Session{
			Env: investigate_core.Env{
				Store: storeFor(t, d),
				Files: files,
				RunID: run.ID,
				NotesPath: files.Path(run.UserID,
					run.ID,
					run.NotesID),
			},
			Registry: investigate_core.LoopRegistry(),
			Limits:   limits,
		},
	}
}

func loopLimits() investigate_core.Limits {
	l := investigate_core.DefaultLimits()
	l.MinSources, l.MinDomains, l.MinIndependentSources = 1, 1, 1
	l.ExtractionThreshold, l.MaxIterations = 200, 40
	return l
}

func (e *env) run1(t *testing.T,
	fm *fakeModel,
	hooks investigate.Hooks) (investigate.RunResult, error) {
	t.Helper()
	count := map[string]int{}
	hooks.PhaseChanged = func(p investigate_core.Phase,
		stop string) {
		e.phases = append(e.phases, string(p)+"/"+stop)
	}
	prevBefore := hooks.BeforeModel
	hooks.BeforeModel = func(turn int,
		state,
		detail string) {
		e.states = append(e.states, state)
		if prevBefore != nil {
			prevBefore(turn,
				state,
				detail)
		}
	}
	hooks.AfterTool = func(turn int,
		name,
		out string,
		isErr bool,
		_ investigate.Totals) {
		count[name]++
		e.outs[fmt.Sprintf("%s#%d",
			name,
			count[name])] = out
	}
	return investigate.Run(context.Background(), investigate.Config{
		Session:  e.sess,
		Model:    fm.call,
		System:   "You are a researcher.",
		Task:     "Does X fit in 16 GB of VRAM?",
		External: fakeTools(),
		Hooks:    hooks,
	})
}

func toolNames(payload map[string]any) []string {
	var out []string
	for _, tl := range payload["tools"].([]any) {
		out = append(out, tl.(map[string]any)["name"].(string))
	}
	return out
}

var notesOnly = []string{"notes_read", "notes_append", "notes_update", "notes_scratch"}

// A whole run on a fake model: planning, the selection, a page that forces an extraction, a text-only turn that is reminded, a challenge and its
// search, a failed finish_exploration with the list of what is missing, the conclusion, a passing finish_exploration, a synthesis with a
// text-only turn and a report that fails the gate, and the report.
func TestRunExplorationAndSynthesis(t *testing.T) {
	dbtest.Run(t, testRunExplorationAndSynthesis)
}

func testRunExplorationAndSynthesis(t *testing.T,
	d *sql.DB) {
	e := newEnv(t,
		d,
		loopLimits())
	fm := &fakeModel{
		t:    t,
		cost: 0.01,
		script: []reply{
			/* 1 */ {
				calls: []tc{{"add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes"}},
					{"add_question", m{"parent_id": "q_001", "question": "A minor point", "priority": 0.3, "validation_criteria": "anything"}}},
			},
			/* 2 */ {
				calls: []tc{{"fetch_page", m{"url": "https://a.com/p"}}},
			},
			/* 3 extract */ {
				calls: []tc{
					{"notes_append", m{"section": "Sources", "entries": []m{{"url": "https://a.com/p", "title": "A"}}}},
					{"notes_append", m{"section": "Unverified", "entries": []m{{"text": "weights are 8 GB", "question_id": "q_002", "sources": []m{{"source_id": "s_001", "independent": true}}}}}}},
			},
			/* 4 */ {
				text: "Let me think about this.",
			},
			/* 5 */ {
				calls: []tc{
					{"challenge", m{"target_type": "claim", "target_id": "c_001", "negation": "weights are not 8 GB", "strategy": "negation", "expected_if_false": "a model card with another size", "query": "weights not 8 GB errata"}},
					{"web_search", m{"query": "weights not 8 GB errata"}},
					{"record_challenge_result", m{"challenge_id": "ch_001", "outcome": "survived", "evidence": "nothing"}}},
			},
			/* 6 */ {
				calls: []tc{
					{"notes_update", m{"id": "c_001", "change": m{"status": "VERIFIED"}}},
					{"resolve_question", m{"id": "q_002", "answer": "8 GB", "claim_ids": []string{"c_001"}, "reason": "shallow"}}},
			},
			/* 7 */ {
				calls: []tc{
					{"finish_exploration", m{}},
					{"resolve_question", m{"id": "q_001", "answer": "it fits", "claim_ids": []string{"c_001"}, "reason": "test"}},
					{"notes_scratch", m{"text": "Conclusion: it fits."}}},
			},
			/* 8 */ {
				calls: []tc{
					{"challenge", m{"target_type": "conclusion", "negation": "it does not fit", "strategy": "negation", "expected_if_false": "a report of an out of memory error", "query": "does not fit 16 GB official"}},
					{"web_search", m{"query": "does not fit 16 GB official"}},
					{"record_challenge_result", m{"challenge_id": "ch_002", "outcome": "survived"}}},
			},
			/* 9 */ {
				calls: []tc{{"finish_exploration", m{}}},
			},
			/* 10 */ {
				text: "Writing the report.",
			},
			/* 11 */ {
				calls: []tc{{"finalize_report", m{"report": "Summary: 8 GB (c_001, s_099)."}}},
			},
			/* 12 */ {
				calls: []tc{{"finalize_report", m{"report": "Summary: the weights are 8 GB (c_001, s_001).\n## Gaps and assumptions\nnone"}}},
			},
		},
	}
	res, err := e.run1(t,
		fm,
		investigate.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != investigate_core.StopFinalized || res.Phase != investigate_core.PhaseSynthesis || res.Turns != 12 {
		t.Fatalf("result: %+v", res)
	}
	if !strings.Contains(res.Report, "[s_001](https://a.com/p)") || strings.Contains(res.Report, "s_099") {
		t.Errorf("report: %s", res.Report)
	}
	if want := []string{"exploration/", "synthesis/", "synthesis/finalized"}; !slices.Equal(e.phases, want) {
		t.Errorf("phases: %v", e.phases)
	}
	// Planning, the forced extraction (notes tools only) and the states.
	if n := toolNames(fm.payloads[0]); !slices.Contains(n, "add_question") || !slices.Contains(n, "frontier_next") {
		t.Errorf("planning tools: %v", n)
	}
	if n := toolNames(fm.payloads[2]); !slices.Equal(n, notesOnly) {
		t.Errorf("the extraction turn offers only the notes tools: %v", n)
	}
	if !strings.Contains(fm.texts[2], "Extract what matters for question q_002") {
		t.Error("the extraction instruction")
	}
	if !strings.Contains(fm.texts[2], "MARKER") {
		t.Error("the extraction turn sees the page")
	}
	if strings.Contains(fm.texts[4], "MARKER") {
		t.Error("the page is evicted once it is in the notes")
	}
	if want := []string{"planning", "thinking", "extracting", "verifying", "verifying"}; !slices.Equal(e.states[:5], want) || !slices.Equal(e.states[len(e.states)-3:], []string{"synthesizing", "synthesizing", "synthesizing"}) {
		t.Errorf("states: %v", e.states)
	}
	// The text-only turn: the next prompt says it does not end anything and repeats the open frontier.
	if !strings.Contains(fm.texts[4], "does not end the investigation") || !strings.Contains(fm.texts[4], "q_002") {
		t.Error("the reminder")
	}
	// The gate failure message is the tool's error; the passing one is not.
	if out := e.outs["finish_exploration#1"]; !strings.Contains(out, "the exploration is not finished") || !strings.Contains(out, "root question q_001") {
		t.Errorf("gate failure: %s", out)
	}
	if out := e.outs["finish_exploration#2"]; !strings.Contains(out, "exploration finished") {
		t.Errorf("gate pass: %s", out)
	}
	if out := e.outs["finalize_report#1"]; !strings.Contains(out, "s_099") {
		t.Errorf("finalization failure: %s", out)
	}
	// Synthesis: no search tool; the model's answer without a tool is reminded.
	syn := toolNames(fm.payloads[9])
	if slices.Contains(syn, "web_search") || slices.Contains(syn, "fetch_page") || !slices.Contains(syn, "finalize_report") {
		t.Errorf("synthesis tools: %v", syn)
	}
	if !strings.Contains(fm.texts[10], "call it now with the report") {
		t.Error("the synthesis reminder")
	}
	// Every attempt is logged; the challenge's search is linked to it.
	st, err := e.sess.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	q, _ := st.Frontier.Get("q_002")
	if len(q.Attempts) < 2 || q.Attempts[0].Tool != "fetch_page" {
		t.Errorf("attempts: %+v", q.Attempts)
	}
	if st.Challenges[0].AttemptSeq == 0 || st.Challenges[0].Outcome != investigate_core.OutcomeSurvived {
		t.Errorf("challenge: %+v", st.Challenges[0])
	}
	// The notes file is the latest version.
	v, ok, err := e.sess.Env.Store.LatestNotesVersion(context.Background(), e.run.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if file, _ := e.sess.Env.Files.Read(e.sess.Env.NotesPath); file != v.Text {
		t.Error("the notes file is not the latest version")
	}
	if !strings.Contains(filepath.Base(e.sess.Env.NotesPath), e.run.NotesID) {
		t.Error("the notes file is named with the notes id")
	}
}

// A deleted notes file is rebuilt from the latest version, with a version of reason restore, before the next turn.
func TestRunRestoresTheNotesFile(t *testing.T) {
	dbtest.Run(t, testRunRestoresTheNotesFile)
}

func testRunRestoresTheNotesFile(t *testing.T,
	d *sql.DB) {
	e := newEnv(t,
		d,
		loopLimits())
	fm := &fakeModel{
		t: t,
		script: []reply{
			{
				calls: []tc{{"notes_scratch", m{"text": "first thoughts"}}},
			},
			{
				text: "hm",
			},
			{
				text: "hm",
			},
			{
				calls: []tc{{"notes_scratch", m{"text": "second thoughts"}}},
			},
			{
				calls: []tc{{"finalize_report", m{"report": "Done."}}},
			},
		},
	}
	// The file is removed before the third turn.
	hooks := investigate.Hooks{
		BeforeModel: func(turn int,
			_,
			_ string) {
			if turn == 3 { // the file is gone during the third turn, which writes nothing
				if err := os.Remove(e.sess.Env.NotesPath); err != nil {
					t.Error(err)
				}
			}
		},
	}
	e.sess.Limits.MaxIterations = 5 // exploration may take 4 turns, the fifth is the report
	if _, err := e.run1(t,
		fm,
		hooks); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.sess.Env.NotesPath); err != nil {
		t.Fatalf("the file was not restored: %v", err)
	}
	if got := e.versionReasons(t); !slices.Contains(got, "restore") || got[0] != "notes_scratch" {
		t.Errorf("versions: %v", got)
	}
	v, _, _ := e.sess.Env.Store.LatestNotesVersion(context.Background(), e.run.ID)
	if file, _ := e.sess.Env.Files.Read(e.sess.Env.NotesPath); file != v.Text || !strings.Contains(v.Text, "second thoughts") {
		t.Errorf("file and latest version differ: %q", file)
	}
}

func (e *env) versionReasons(t *testing.T) []string {
	t.Helper()
	rows, err := e.d.Query(`SELECT reason FROM gb_agent_notes_versions WHERE run_id = `+e.ph()+` ORDER BY seq`, e.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *env) ph() string {
	if e.pg {
		return "$1"
	}
	return "?"
}

// A breaker in exploration moves the run to synthesis with a note, and the run completes; the last turn is forced to finalize_report.
func TestRunBreakerInExploration(t *testing.T) {
	dbtest.Run(t, testRunBreakerInExploration)
}

func testRunBreakerInExploration(t *testing.T,
	d *sql.DB) {
	l := loopLimits()
	l.MaxIterations = 10 // exploration may take 8 turns, synthesis the rest
	e := newEnv(t,
		d,
		l)
	search := reply{
		calls: []tc{{"web_search", m{"query": "something"}}},
	}
	fm := &fakeModel{
		t:         t,
		autoNotes: true,
		script: []reply{
			{
				calls: []tc{{"add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes"}}},
			},
			search, search, search, search, search,
			/* synthesis */ {
				text: "hm",
			},
			{
				calls: []tc{{"finalize_report", m{"report": "Cut short: the weights were not found."}}},
			},
		},
	}
	res, err := e.run1(t,
		fm,
		investigate.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != investigate_core.StopIterations || res.Report == "" || res.Turns != 10 {
		t.Fatalf("result: %+v", res)
	}
	if want := []string{"exploration/", "synthesis/breaker:iterations", "synthesis/breaker:iterations"}; !slices.Equal(e.phases, want) {
		t.Errorf("phases: %v", e.phases)
	}
	if !strings.Contains(fm.texts[8], "cut short by `breaker:iterations`") || !strings.Contains(fm.texts[8], "open gap (q_002)") {
		t.Error("the synthesis input says the investigation was cut and lists the open question")
	}
	if _, forced := fm.payloads[8]["tool_choice"]; forced {
		t.Error("the turn before the last is not forced")
	}
	tcho, _ := fm.payloads[9]["tool_choice"].(map[string]any)
	if tcho["name"] != "finalize_report" || tcho["type"] != "tool" {
		t.Errorf("the last turn is forced: %v", fm.payloads[9]["tool_choice"])
	}
	st, _ := e.sess.Load(context.Background())
	if !strings.Contains(st.Notes.Scratch, "cut short by breaker: iterations") {
		t.Errorf("scratch: %q", st.Notes.Scratch)
	}
}

// A breaker in synthesis (the reserve is used up) fails the run with the breaker named.
func TestRunBreakerInSynthesis(t *testing.T) {
	dbtest.Run(t, testRunBreakerInSynthesis)
}

func testRunBreakerInSynthesis(t *testing.T,
	d *sql.DB) {
	l := loopLimits()
	l.MaxIterations = 10
	e := newEnv(t,
		d,
		l)
	search := reply{
		calls: []tc{{"web_search", m{"query": "something"}}},
	}
	bad := reply{
		calls: []tc{{"finalize_report", m{"report": "uses s_404"}}},
	}
	fm := &fakeModel{
		t:         t,
		autoNotes: true,
		script: []reply{
			{
				calls: []tc{{"add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes"}}},
			},
			search, search, search, search, search, bad, bad,
		},
	}
	res, err := e.run1(t,
		fm,
		investigate.Hooks{})
	var be *investigate.BreakerError
	if !errors.As(err, &be) || be.Breaker.Name != "iterations" || !strings.Contains(err.Error(), "breaker: iterations") {
		t.Fatalf("err: %v", err)
	}
	if res.Report != "" || res.StopReason != "breaker:iterations" || res.Turns != 10 {
		t.Errorf("result: %+v", res)
	}
	if last := e.phases[len(e.phases)-1]; last != "synthesis/breaker:iterations" {
		t.Errorf("phases: %v", e.phases)
	}
}

// The cost breaker and the reserve: exploration stops at 85% of the cap, and synthesis may spend the rest.
func TestRunCostBreakerAndReserve(t *testing.T) {
	dbtest.Run(t, testRunCostBreakerAndReserve)
}

func testRunCostBreakerAndReserve(t *testing.T,
	d *sql.DB) {
	l := loopLimits()
	l.MaxCostUSD = 0.10
	e := newEnv(t,
		d,
		l)
	search := reply{
		calls: []tc{{"web_search", m{"query": "something"}}},
	}
	fm := &fakeModel{
		t:         t,
		cost:      0.01,
		autoNotes: true,
		script: []reply{
			{
				calls: []tc{{"add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes"}}},
			},
			search, search, search, search, search, search,
			{
				calls: []tc{{"finalize_report", m{"report": "Cut short by the cost cap."}}},
			},
		},
	}
	res, err := e.run1(t,
		fm,
		investigate.Hooks{})
	if err != nil || res.StopReason != investigate_core.StopCost || res.Report == "" {
		t.Fatalf("%+v %v",
			res,
			err)
	}
	if res.CostUSD < 0.085 || res.CostUSD > 0.10 {
		t.Errorf("cost %.4f: exploration stops at 85%% of the cap, synthesis stays under the cap", res.CostUSD)
	}
}

// The token breaker.
func TestRunTokenBreaker(t *testing.T) {
	dbtest.Run(t, testRunTokenBreaker)
}

func testRunTokenBreaker(t *testing.T,
	d *sql.DB) {
	l := loopLimits()
	l.MaxTotalTokens = 1500 // 150 per call: exploration may take 8 (1275), synthesis up to 10
	e := newEnv(t,
		d,
		l)
	search := reply{
		calls: []tc{{"web_search", m{"query": "something"}}},
	}
	fm := &fakeModel{
		t:         t,
		autoNotes: true,
		script: []reply{
			{
				calls: []tc{{"add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes"}}},
			},
			search, search, search, search, search, search,
			{
				calls: []tc{{"finalize_report", m{"report": "Cut short by the token ceiling."}}},
			},
		},
	}
	res, err := e.run1(t,
		fm,
		investigate.Hooks{})
	if err != nil || res.StopReason != investigate_core.StopTokens || res.Report == "" {
		t.Fatalf("%+v %v",
			res,
			err)
	}
}

// Synthesis may send the run back to exploration once (max_reopens), and no more.
func TestRunReopen(t *testing.T) {
	dbtest.Run(t, testRunReopen)
}

func testRunReopen(t *testing.T,
	d *sql.DB) {
	l := loopLimits()
	l.MinSources, l.MinDomains = 0, 0
	e := newEnv(t,
		d,
		l)
	fm := &fakeModel{
		t: t,
		script: []reply{
			/* 1 */ {
				calls: []tc{{"add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes"}}},
			},
			/* 2 */ {
				calls: []tc{
					{"resolve_question", m{"id": "q_002", "answer": "8 GB", "reason": "r"}},
					{"resolve_question", m{"id": "q_001", "answer": "it fits", "reason": "r"}},
					{"notes_scratch", m{"text": "Conclusion: it fits."}},
					{"challenge", m{"target_type": "conclusion", "negation": "it does not fit", "strategy": "negation", "expected_if_false": "e", "query": "does not fit official"}},
					{"web_search", m{"query": "does not fit official"}},
					{"record_challenge_result", m{"challenge_id": "ch_001", "outcome": "survived"}}},
			},
			/* 3 synthesis */ {
				calls: []tc{{"reopen_question", m{"id": "q_002", "why": "a hole"}}},
			},
			/* 4 exploration again */ {
				calls: []tc{{"resolve_question", m{"id": "q_002", "answer": "8 GB, confirmed", "reason": "r"}}},
			},
			/* 5 synthesis */ {
				calls: []tc{{"reopen_question", m{"id": "q_002", "why": "again"}}},
			},
			/* 6 */ {
				calls: []tc{{"finalize_report", m{"report": "Summary: it fits."}}},
			},
		},
	}
	res, err := e.run1(t,
		fm,
		investigate.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exploration/", "synthesis/frontier_exhausted", "exploration/", "synthesis/frontier_exhausted", "synthesis/frontier_exhausted"}
	if !slices.Equal(e.phases, want) || res.StopReason != investigate_core.StopFrontierExhausted || res.Report == "" {
		t.Fatalf("%+v phases %v",
			res,
			e.phases)
	}
	if out := e.outs["reopen_question#2"]; !strings.Contains(out, "cannot reopen exploration again") {
		t.Errorf("the second reopen: %q", out)
	}
	if toolNames(fm.payloads[3]) == nil || slices.Contains(toolNames(fm.payloads[3]), "finalize_report") {
		t.Errorf("back in exploration: %v", toolNames(fm.payloads[3]))
	}
}

// Notes over their budget make a consolidation turn (notes tools only); a second one only if the first did not reduce them.
func TestRunConsolidation(t *testing.T) {
	dbtest.Run(t, testRunConsolidation)
}

func testRunConsolidation(t *testing.T,
	d *sql.DB) {
	l := loopLimits()
	l.NotesMaxTokens = 20
	l.MaxIterations = 6
	e := newEnv(t,
		d,
		l)
	fm := &fakeModel{
		t:         t,
		autoNotes: true,
		script: []reply{
			{
				calls: []tc{{"add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes"}}},
			},
			{
				text: "x",
			}, {
				text: "x",
			}, {
				text: "x",
			},
		},
	}
	_, _ = e.run1(t,
		fm,
		investigate.Hooks{})
	if len(e.states) < 3 || !slices.Equal(e.states[:3], []string{"consolidating", "consolidating", "planning"}) {
		t.Fatalf("states: %v", e.states)
	}
	if n := toolNames(fm.payloads[0]); !slices.Equal(n, notesOnly) {
		t.Errorf("consolidation tools: %v", n)
	}
	if !strings.Contains(fm.texts[0], "over their size budget") {
		t.Error("consolidation instruction")
	}
}

// Run options and the ends: a model failure ends the run with its error.
func TestRunModelFailure(t *testing.T) {
	d := dbtest.OpenSQLite(t)
	e := newEnv(t,
		d,
		loopLimits())
	boom := errors.New("model down")
	_, err := investigate.Run(context.Background(), investigate.Config{
		Session:  e.sess,
		Task:     "t",
		External: fakeTools(),
		Model: func(context.Context,
			map[string]any) (*ai.RawResponse, error) {
			return nil, boom
		},
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
}
