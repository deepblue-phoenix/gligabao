package investigate_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/deepblue-phoenix/gligabao/internal/agent_defs"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db/sqlite"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

type m = map[string]any

func storeFor(t *testing.T, d *sql.DB) investigate.Store {
	if strings.HasSuffix(t.Name(), "postgres") {
		return postgres.NewInvestigateStore(d)
	}
	return sqlite.NewInvestigateStore(d)
}

func createRun(t *testing.T, d *sql.DB, user string) *core.Run {
	t.Helper()
	store := sqlite.Store
	if strings.HasSuffix(t.Name(), "postgres") {
		store = postgres.Store
	}
	run := &core.Run{UserID: user, Topic: "does X fit", State: core.StateStarting}
	if err := store.CreateRun(context.Background(), d, run); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnterState(context.Background(), d, run.ID, core.StateStarting, 0, ""); err != nil {
		t.Fatal(err)
	}
	return run
}

func session(t *testing.T, d *sql.DB, run *core.Run) investigate.Session {
	files := investigate.NotesFile{Dir: filepath.Join(t.TempDir(), "notes")}
	return investigate.Session{
		Env:      investigate.Env{Store: storeFor(t, d), Files: files, RunID: run.ID, NotesPath: files.Path(run.UserID, run.ID, run.NotesID)},
		Registry: investigate.InvestigativeRegistry(), Limits: investigate.DefaultLimits(),
	}
}

func call(t *testing.T, s investigate.Session, name string, args m) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	text, err := s.Call(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return text
}

// A whole history, driven through the tools with a store: the state read back from the database is the state the design describes.
func TestSessionEndToEnd(t *testing.T) { dbtest.Run(t, testSessionEndToEnd) }

func testSessionEndToEnd(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	run := createRun(t, d, "alice/../bob")
	if len(run.NotesID) != 32 {
		t.Fatalf("CreateRun makes the notes id: %q", run.NotesID)
	}
	s := session(t, d, run)
	if err := s.Start(ctx, "Does X fit in 16 GB of VRAM?"); err != nil {
		t.Fatal(err)
	}
	call(t, s, "add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes", "target_sources": []string{"official_docs"}})
	call(t, s, "add_question", m{"parent_id": "q_001", "question": "What is the activation memory?", "priority": 0.6, "validation_criteria": "profiling", "depends_on": []string{"q_002"}})
	if got := call(t, s, "frontier_next", m{}); !strings.Contains(got, "[q_002] in_progress") {
		t.Fatal(got)
	}
	call(t, s, "retry_question", m{"id": "q_002", "new_query": "model weights size fp16", "why": "no figure"})
	call(t, s, "frontier_next", m{})
	call(t, s, "notes_append", m{"section": "Sources", "entries": []m{{"url": "https://arxiv.org/abs/1", "title": "Paper"}, {"url": "https://docs.example.com/x", "title": "Docs"}}})
	call(t, s, "notes_append", m{"section": "Unverified", "entries": []m{{"text": "Weights are 7 GB at fp16", "question_id": "q_002",
		"sources": []m{{"source_id": "s_001", "independent": true}, {"source_id": "s_002", "independent": true}}}}})
	call(t, s, "notes_append", m{"section": "Datapoints", "entries": []m{{"value": "7", "unit": "GB", "text": "weights at fp16", "question_id": "q_002", "sources": []m{{"source_id": "s_001"}}}}})
	call(t, s, "challenge", m{"target_type": "claim", "target_id": "c_001", "negation": "weights are much larger than 7 GB", "strategy": "discrepancy",
		"expected_if_false": "a model card with a bigger size", "query": "model weights conflicting size card"})
	// The effect layer executes the search of the challenge and links its attempt (the loop of stage 2 does this).
	st, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f, chs, err := investigate.LinkAttempt(st.Frontier, st.Challenges, "ch_001", "web_search", "nothing against")
	if err != nil {
		t.Fatal(err)
	}
	after := st
	after.Frontier, after.Challenges = f, chs
	if err := investigate.Execute(ctx, s.Env, 1, investigate.Diff(st, after, nil, "")); err != nil {
		t.Fatal(err)
	}
	call(t, s, "record_challenge_result", m{"challenge_id": "ch_001", "outcome": "survived"})
	call(t, s, "notes_update", m{"id": "c_001", "change": m{"status": "VERIFIED"}})
	call(t, s, "notes_scratch", m{"text": "Conclusion: it fits."})
	call(t, s, "challenge", m{"target_type": "question_answer", "target_id": "q_002", "negation": "the weights are not 7 GB", "strategy": "criticism",
		"expected_if_false": "a correction", "query": "weights size correction errata"})
	call(t, s, "record_challenge_result", m{"challenge_id": "ch_002", "outcome": "inconclusive", "evidence": "paywalled"})
	call(t, s, "resolve_question", m{"id": "q_002", "answer": "7 GB", "claim_ids": []string{"c_001", "d_001"}})
	call(t, s, "frontier_next", m{})
	call(t, s, "prune_branch", m{"id": "q_003", "reason": "answer would not change the conclusion"})

	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The state from the database equals the state of the same calls on pure values.
	pure := replayPure(t)
	if !reflect.DeepEqual(loaded.Frontier, pure.Frontier) {
		t.Errorf("frontier from the store differs:\n got %+v\nwant %+v", loaded.Frontier, pure.Frontier)
	}
	if !reflect.DeepEqual(loaded.Challenges, pure.Challenges) {
		t.Errorf("challenges from the store differ:\n got %+v\nwant %+v", loaded.Challenges, pure.Challenges)
	}
	if investigate.Render(loaded.Notes) != investigate.Render(pure.Notes) {
		t.Errorf("notes from the store differ:\n%s\n---\n%s", investigate.Render(loaded.Notes), investigate.Render(pure.Notes))
	}
	// Transitions: one row per change, seq per question 1, 2, 3, ..., the last one equals the status.
	rows, err := d.QueryContext(ctx, queryFor(t, `SELECT question_id, seq, to_status, state_seq FROM gb_agent_frontier_transitions WHERE run_id = ? ORDER BY id`), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	last := map[string]string{}
	seqs := map[string]int{}
	for rows.Next() {
		var q, to string
		var seq, stateSeq int
		if err := rows.Scan(&q, &seq, &to, &stateSeq); err != nil {
			t.Fatal(err)
		}
		if seq != seqs[q]+1 || stateSeq != 1 {
			t.Fatalf("%s: seq %d after %d, state_seq %d", q, seq, seqs[q], stateSeq)
		}
		seqs[q], last[q] = seq, to
	}
	for _, q := range loaded.Frontier.Questions {
		if last[q.ID] != string(q.Status) || seqs[q.ID] != q.TransSeq {
			t.Errorf("%s: status %s, last transition %s (seq %d, TransSeq %d)", q.ID, q.Status, last[q.ID], seqs[q.ID], q.TransSeq)
		}
	}
	// Every notes effect left a version, and the latest one is the file.
	v, ok, err := s.Env.Store.LatestNotesVersion(ctx, run.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	onDisk, err := s.Env.Files.Read(s.Env.NotesPath)
	if err != nil || onDisk != v.Text || v.Reason != "notes_scratch" && v.Reason != "notes_update" {
		t.Fatalf("file != latest version (%q): %v", v.Reason, err)
	}
	if v.Seq != 5 {
		t.Errorf("notes versions: %d (append x3, update, scratch)", v.Seq)
	}
	back, errs := investigate.Parse(onDisk)
	if len(errs) > 0 || !reflect.DeepEqual(back, loaded.Notes) {
		t.Errorf("write, reload, parse: %v", errs)
	}
	// The structured tables hold what the notes say.
	var claims, links, sources, challengeSources int
	for q, dst := range map[string]*int{
		`SELECT COUNT(*) FROM gb_agent_claims WHERE run_id = ?`:                                &claims,
		`SELECT COUNT(*) FROM gb_agent_claim_sources WHERE run_id = ?`:                         &links,
		`SELECT COUNT(*) FROM gb_agent_sources WHERE run_id = ?`:                               &sources,
		`SELECT COUNT(*) FROM gb_agent_challenges WHERE run_id = ? AND attempt_id IS NOT NULL`: &challengeSources,
	} {
		if err := d.QueryRowContext(ctx, queryFor(t, q), run.ID).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if claims != 2 || links != 3 || sources != 2 || challengeSources != 1 {
		t.Errorf("claims %d links %d sources %d challenges with attempt %d", claims, links, sources, challengeSources)
	}
	var status string
	if err := d.QueryRowContext(ctx, queryFor(t, `SELECT status FROM gb_agent_claims WHERE run_id = ? AND id = 'c_001'`), run.ID).Scan(&status); err != nil || status != "VERIFIED" {
		t.Errorf("c_001: %q %v", status, err)
	}
}

func queryFor(t *testing.T, q string) string {
	if !strings.HasSuffix(t.Name(), "postgres") {
		return q
	}
	n := 0
	var b strings.Builder
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteString("$" + string(rune('0'+n)))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// replayPure runs the same calls on pure values (no store), linking the challenge attempt the same way.
func replayPure(t *testing.T) investigate.State {
	t.Helper()
	s, _, err := investigate.NewState("Does X fit in 16 GB of VRAM?", investigate.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	reg := investigate.InvestigativeRegistry()
	run := func(name string, args m) {
		tool, _ := reg.Get(name)
		raw, _ := json.Marshal(args)
		res, err := tool.Handler(s, raw)
		if err != nil {
			t.Fatalf("pure %s: %v", name, err)
		}
		s = res.State
	}
	run("add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes", "target_sources": []string{"official_docs"}})
	run("add_question", m{"parent_id": "q_001", "question": "What is the activation memory?", "priority": 0.6, "validation_criteria": "profiling", "depends_on": []string{"q_002"}})
	run("frontier_next", m{})
	run("retry_question", m{"id": "q_002", "new_query": "model weights size fp16", "why": "no figure"})
	run("frontier_next", m{})
	run("notes_append", m{"section": "Sources", "entries": []m{{"url": "https://arxiv.org/abs/1", "title": "Paper"}, {"url": "https://docs.example.com/x", "title": "Docs"}}})
	run("notes_append", m{"section": "Unverified", "entries": []m{{"text": "Weights are 7 GB at fp16", "question_id": "q_002",
		"sources": []m{{"source_id": "s_001", "independent": true}, {"source_id": "s_002", "independent": true}}}}})
	run("notes_append", m{"section": "Datapoints", "entries": []m{{"value": "7", "unit": "GB", "text": "weights at fp16", "question_id": "q_002", "sources": []m{{"source_id": "s_001"}}}}})
	run("challenge", m{"target_type": "claim", "target_id": "c_001", "negation": "weights are much larger than 7 GB", "strategy": "discrepancy",
		"expected_if_false": "a model card with a bigger size", "query": "model weights conflicting size card"})
	f, chs, err := investigate.LinkAttempt(s.Frontier, s.Challenges, "ch_001", "web_search", "nothing against")
	if err != nil {
		t.Fatal(err)
	}
	s.Frontier, s.Challenges = f, chs
	run("record_challenge_result", m{"challenge_id": "ch_001", "outcome": "survived"})
	run("notes_update", m{"id": "c_001", "change": m{"status": "VERIFIED"}})
	run("notes_scratch", m{"text": "Conclusion: it fits."})
	run("challenge", m{"target_type": "question_answer", "target_id": "q_002", "negation": "the weights are not 7 GB", "strategy": "criticism",
		"expected_if_false": "a correction", "query": "weights size correction errata"})
	run("record_challenge_result", m{"challenge_id": "ch_002", "outcome": "inconclusive", "evidence": "paywalled"})
	run("resolve_question", m{"id": "q_002", "answer": "7 GB", "claim_ids": []string{"c_001", "d_001"}})
	run("frontier_next", m{})
	run("prune_branch", m{"id": "q_003", "reason": "answer would not change the conclusion"})
	return s
}

// A failing effect aborts the whole list: nothing of it is stored, and no notes file is written.
func TestExecuteIsAtomic(t *testing.T) { dbtest.Run(t, testExecuteIsAtomic) }

func testExecuteIsAtomic(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	run := createRun(t, d, "u")
	s := session(t, d, run)
	if err := s.Start(ctx, "objective"); err != nil {
		t.Fatal(err)
	}
	q := investigate.Question{ID: "q_002", ParentID: "q_001", Depth: 1, Kind: investigate.KindQuestion, Question: "child", Priority: 0.5, Status: investigate.StatusPending}
	bad := investigate.Question{ID: "q_003", ParentID: "q_001", Depth: 1, Kind: investigate.KindQuestion, Question: "bad priority", Priority: 7, Status: investigate.StatusPending}
	err := investigate.Execute(ctx, s.Env, 0, []investigate.Effect{
		investigate.UpsertQuestion{Question: q},
		investigate.WriteNotes{Text: "# Notes: x\n", Reason: "notes_append"},
		investigate.UpsertQuestion{Question: bad}, // violates the CHECK on priority
	})
	if err == nil {
		t.Fatal("the bad row must fail")
	}
	l, err := s.Env.Store.Load(ctx, run.ID)
	if err != nil || len(l.Frontier.Questions) != 1 || l.NotesText != "" {
		t.Fatalf("nothing of the failed list is stored: %v %+v", err, l)
	}
	if got, _ := s.Env.Files.Read(s.Env.NotesPath); got != investigate.Render(investigate.Notes{}) {
		t.Fatalf("the notes file is not written for a failed transaction: %q", got)
	}
	// The database refuses a bad transition and a bad notes version too.
	for name, e := range map[string]investigate.Effect{
		"unknown status": investigate.RecordTransitions{Transitions: []investigate.Transition{{QuestionID: "q_001", To: "nowhere", Trigger: investigate.TriggerSelected, Actor: investigate.ActorCode}}},
		"unknown actor":  investigate.RecordTransitions{Transitions: []investigate.Transition{{QuestionID: "q_001", To: investigate.StatusPending, Trigger: investigate.TriggerSelected, Actor: "god"}}},
		"orphan dep":     investigate.AddDependency{QuestionID: "q_001", DependsOnID: "q_077"},
		"self dep":       investigate.AddDependency{QuestionID: "q_001", DependsOnID: "q_001"},
	} {
		if err := investigate.Execute(ctx, s.Env, 0, []investigate.Effect{e}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Nothing connects the registry to an agent in stage 1: the tools an agent definition may list are still the two web tools.
func TestRegistryIsNotWiredToAgents(t *testing.T) {
	if got := strings.Join(agent_defs.KnownTools, ","); got != "web_search,fetch_url" {
		t.Fatalf("agent tools: %s", got)
	}
	for _, name := range investigate.InvestigativeRegistry().Names() {
		if slices.Contains(agent_defs.KnownTools, name) {
			t.Errorf("%s is a tool of the agent definitions", name)
		}
	}
}
