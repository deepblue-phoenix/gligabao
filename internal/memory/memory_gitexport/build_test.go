package memory_gitexport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

func tp(t time.Time) *time.Time { return &t }

func i64(v int64) *int64 { return &v }

// fixture is a dataset with three remember calls: Alice works at Acme; then a chunked text that makes her work at Beta (which
// closes the first fact); then a text that states the second fact again.
func fixture() Input {
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return t0.AddDate(0, 0, n) }
	return Input{
		Name: "work", Description: "A test <dataset> & more", Tags: []string{"a", "b"}, CreatedAt: day(-1),
		Artifacts: []graph.Artifact{
			{ID: 1, Kind: "episode", Text: "Alice works at Acme.\nSecond line", SessionID: "s1", ValidAt: day(0), CreatedAt: day(0)},
			{ID: 2, Kind: "knowledge", Text: "Alice moved to Beta. A long text.", ValidAt: day(10), CreatedAt: day(1)},
			{ID: 3, Kind: "knowledge", Text: "Alice moved to Beta.", ParentID: 2, ChunkIndex: 0, StartOffset: 0, EndOffset: 20, Tokens: 5, ValidAt: day(10), CreatedAt: day(1)},
			{ID: 4, Kind: graph.ArtifactChunkSummary, Text: "Alice moved.", ParentID: 3, ValidAt: day(10), CreatedAt: day(1)},
			{ID: 5, Kind: "episode", Text: "", ValidAt: day(20), CreatedAt: day(2)},
		},
		Entities: []graph.EvidenceEntity{
			{ID: 1, Name: "Alice", Type: "person", Description: "engineer", ModelID: "m1"},
			{ID: 2, Name: "Acme", Type: "organization"},
			{ID: 3, Name: "Beta Corp/Ltd", Type: "organization"},
		},
		Edges: []graph.EvidenceEdge{
			{ID: 1, SourceID: 1, TargetID: 2, Source: "Alice", Target: "Acme", Relation: "works_at", Description: "Alice works at Acme.", ArtifactID: i64(1),
				ValidAt: tp(day(0)), InvalidAt: tp(day(10)), CreatedAt: tp(day(0)), ExpiredAt: tp(day(1))},
			{ID: 2, SourceID: 1, TargetID: 3, Source: "Alice", Target: "Beta Corp/Ltd", Relation: "works_at", Description: "Alice works at Beta.", ArtifactID: i64(3),
				ValidAt: tp(day(10)), CreatedAt: tp(day(1))},
		},
		EntityArtifacts: []graph.EntityArtifact{{EntityID: 1, ArtifactID: 1}, {EntityID: 2, ArtifactID: 1}, {EntityID: 1, ArtifactID: 3}, {EntityID: 3, ArtifactID: 3}, {EntityID: 1, ArtifactID: 5}},
		EdgeArtifacts:   []graph.EdgeArtifact{{EdgeID: 1, ArtifactID: 1}, {EdgeID: 2, ArtifactID: 3}, {EdgeID: 2, ArtifactID: 5}},
		EdgeEvents: map[int64][]graph.EdgeEvent{
			1: {{EdgeID: 1, ArtifactID: 1, Event: graph.EdgeCreated, CreatedAt: day(0)}},
			2: {
				{EdgeID: 2, ArtifactID: 3, Event: graph.EdgeCreated, CreatedAt: day(1)},
				{EdgeID: 1, ArtifactID: 3, Event: graph.EdgeInvalidated, InvalidAtAfter: tp(day(10)), CausedByEdgeID: 2, CreatedAt: day(1)},
			},
			5: {{EdgeID: 2, ArtifactID: 5, Event: graph.EdgeConfirmed, CreatedAt: day(2)}},
		},
		Models: map[string]string{"m1": "claude-x"},
		Classes: []graph.ArtifactClass{
			{ArtifactID: 3, Class: "hr"}, {ArtifactID: 3, Class: "career_change"}, {ArtifactID: 3, Class: "hr"},
		},
		RememberRuns: []RememberRunLog{
			{Run: relational.RememberRun{ID: 3, Kind: "episode", Status: relational.RememberFailed, Error: sp("llm: overloaded\nretry later"), StartedAt: day(1).Add(2 * time.Hour),
				Stages: map[string]relational.RememberStage{}}},
			{Run: relational.RememberRun{ID: 2, Kind: "knowledge", Status: relational.RememberSucceeded, ArtifactID: i64(2), StartedAt: day(1).Add(-time.Minute), FinishedAt: tp(day(1)),
				Params: map[string]any{"session_id": "", "chunk_size": 1024},
				Stages: map[string]relational.RememberStage{
					relational.RememberStageExtractGraph: {Status: relational.RememberSucceeded, InferenceIDs: []int64{7, 8}},
					relational.RememberStageChunkText:    {Status: relational.RememberNotStarted},
				}},
				Inferences: map[int64]relational.Inference{7: {ID: 7, Stage: "extract_graph", Model: "claude-x", Provider: "anthropic", Reasoning: "none", InputTokens: 100, OutputTokens: 20, CostUSD: 0.5, CreatedAt: day(1)}},
				Outputs:    []relational.RememberStageOutput{{ID: 1, RunID: 2, Stage: "extract_graph", Name: "extract_graph", Label: "chunk 1 of 1", Output: []byte(`{"entities":["Alice"]}`), CreatedAt: day(1)}}},
			{Run: relational.RememberRun{ID: 1, Kind: "episode", Status: relational.RememberSucceeded, ArtifactID: i64(1), StartedAt: day(0).Add(-time.Minute), Stages: map[string]relational.RememberStage{}}},
		},
		QueryRuns: []QueryRunLog{{Run: relational.QueryRun{ID: 1, Question: "Where does <Alice> work?", SessionID: "s1", Status: relational.RememberSucceeded, StartedAt: day(1).Add(8 * time.Hour),
			Stages: map[string]relational.RememberStage{relational.QueryStageQuery: {Status: relational.RememberSucceeded, InferenceIDs: []int64{9}}}},
			Inferences: map[int64]relational.Inference{9: {ID: 9, Stage: "query", Model: "claude-x", Reasoning: "low", CreatedAt: day(1).Add(8 * time.Hour)}}}},
	}
}

func sp(s string) *string { return &s }

func hasGit() bool { _, err := exec.LookPath("git"); return err == nil }

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestWriteBuildsAValidRepository(t *testing.T) {
	if !hasGit() {
		t.Skip("no git binary")
	}
	dir := t.TempDir()
	res, err := Write(fixture(), DirSink{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Commits != 6 || res.Artifacts != 5 || res.Entities != 3 || res.Edges != 2 || res.Runs != 4 {
		t.Fatalf("result = %+v", res)
	}
	git(t, dir, "fsck", "--strict", "--no-dangling")
	if head := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD")); head != res.Head {
		t.Errorf("HEAD = %s, want %s", head, res.Head)
	}

	// Oldest first: the root commit, then one commit per call, dated with the call's creation time.
	log := strings.Split(strings.TrimSpace(git(t, dir, "log", "--reverse", "--format=%an|%ad|%s", "--date=format-local:%Y-%m-%d")), "\n")
	want := []string{
		"gligabao|2024-12-31|Create dataset work",
		"gligabao|2025-01-01|remember #1: Alice works at Acme.",
		"gligabao|2025-01-02|remember #2: Alice moved to Beta. A long text.",
		"gligabao|2025-01-02|remember run #3 failed: llm: overloaded",
		"gligabao|2025-01-02|query #1: Where does <Alice> work?",
		"gligabao|2025-01-03|remember #5: (no text)",
	}
	if len(log) != len(want) {
		t.Fatalf("log = %q", log)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Errorf("commit %d = %q, want %q", i, log[i], want[i])
		}
	}

	// The fact is open in the first call's commit, closed in the second.
	edge1 := "edges/works_at/000001.md"
	if f := git(t, dir, "show", "HEAD~4:"+edge1); strings.Contains(f, "invalid_at") || strings.Contains(f, "expired_at") {
		t.Errorf("fact 1 in call 1:\n%s", f)
	}
	f := git(t, dir, "show", "HEAD~3:"+edge1)
	for _, w := range []string{"invalid_at: 2025-01-11T10:00:00Z", "expired_at: 2025-01-02T10:00:00Z", "artifacts: [1]", "Alice works at Acme."} {
		if !strings.Contains(f, w) {
			t.Errorf("fact 1 in call 2 misses %q:\n%s", w, f)
		}
	}
	if d := git(t, dir, "diff", "--name-status", "HEAD~4", "HEAD~3"); !strings.Contains(d, "M\t"+edge1) {
		t.Errorf("call 2 did not modify fact 1:\n%s", d)
	}
	// The call that confirms a fact only adds its artifact to the fact's file.
	if d := git(t, dir, "diff", "--name-status", "HEAD~1", "HEAD"); !strings.Contains(d, "A\tartifacts/000005.md") || !strings.Contains(d, "M\tedges/works_at/000002.md") || strings.Contains(d, edge1) {
		t.Errorf("call 3 changed:\n%s", d)
	}
	if f := git(t, dir, "show", "HEAD:edges/works_at/000002.md"); !strings.Contains(f, "artifacts: [3, 5]") || strings.Contains(f, "invalid_at") {
		t.Errorf("fact 2:\n%s", f)
	}

	// Artifacts: the chunk and its summary are under the root's directory; entities by type.
	for _, p := range []string{"README.md", "artifacts/000002.md", "artifacts/000002/000003-chunk-0.md", "artifacts/000002/000004-summary.md",
		"entities/person/alice-1.md", "entities/organization/acme-2.md", "entities/organization/beta-corp-ltd-3.md"} {
		git(t, dir, "cat-file", "-e", "HEAD:"+p)
	}
	if f := git(t, dir, "show", "HEAD:entities/person/alice-1.md"); !strings.Contains(f, "artifacts: [1, 3, 5]") || !strings.Contains(f, `model: "claude-x"`) || !strings.Contains(f, "engineer") {
		t.Errorf("Alice:\n%s", f)
	}
	if f := git(t, dir, "show", "HEAD:artifacts/000001.md"); !strings.Contains(f, `session: "s1"`) || !strings.HasSuffix(f, "Alice works at Acme.\nSecond line\n") {
		t.Errorf("artifact 1:\n%s", f)
	}
	if f := git(t, dir, "show", "HEAD~3^{commit}"); !strings.Contains(f, "- invalidated edge 1: Alice works_at Acme") || !strings.Contains(f, "Artifacts: 3") {
		t.Errorf("message of call 2:\n%s", f)
	}

	// Graph classes are in the front matter of their artifact, sorted and once each.
	if f := git(t, dir, "show", "HEAD:artifacts/000002/000003-chunk-0.md"); !strings.Contains(f, `classes: ["career_change", "hr"]`) {
		t.Errorf("chunk:\n%s", f)
	}
	// Run logs: a remember run is in the commit of its artifact, a query run and a failed run have commits of their own.
	if d := git(t, dir, "show", "--name-status", "--format=", "HEAD~3"); !strings.Contains(d, "A\truns/remember/000002.json") {
		t.Errorf("call 2 files:\n%s", d)
	}
	if m := git(t, dir, "log", "-1", "--format=%B", "HEAD~3"); !strings.Contains(m, "Remember-Run-Id: 2") {
		t.Errorf("message of call 2:\n%s", m)
	}
	f = git(t, dir, "show", "HEAD:runs/remember/000002.json")
	for _, w := range []string{`"status": "succeeded"`, `"stage": "extract_graph"`, `"model": "claude-x"`, `"cost_usd": 0.5`, `"label": "chunk 1 of 1"`, `"entities"`, `"chunk_size": 1024`} {
		if !strings.Contains(f, w) {
			t.Errorf("remember run 2 misses %s:\n%s", w, f)
		}
	}
	if strings.Contains(f, "chunk_text") {
		t.Errorf("a stage that did not start is in the log:\n%s", f)
	}
	if d := git(t, dir, "show", "--name-status", "--format=", "HEAD~1"); d != "A\truns/query/000001.json\n" {
		t.Errorf("the query commit changed:\n%s", d)
	}
	if d := git(t, dir, "show", "--name-status", "--format=", "HEAD~2"); d != "A\truns/remember/000003.json\n" {
		t.Errorf("the failed run's commit changed:\n%s", d)
	}
	if f := git(t, dir, "show", "HEAD:runs/query/000001.json"); !strings.Contains(f, `"question": "Where does <Alice> work?"`) || !strings.Contains(f, `"reasoning": "low"`) {
		t.Errorf("query run:\n%s", f)
	}
	// A real checkout works.
	out := t.TempDir() + "/out"
	if b, err := exec.Command("git", "clone", "-q", dir, out).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, b)
	}
}

func TestWriteIsDeterministicAndTarMatchesDir(t *testing.T) {
	a, err := Write(fixture(), DirSink{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var buf1, buf2 bytes.Buffer
	for _, buf := range []*bytes.Buffer{&buf1, &buf2} {
		sink := NewTarGzSink(buf, "work.git")
		b, err := Write(fixture(), sink)
		if err != nil {
			t.Fatal(err)
		}
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
		if b.Head != a.Head {
			t.Fatalf("head %s != %s", b.Head, a.Head)
		}
	}
	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Error("the archive is not deterministic")
	}
	gz, err := gzip.NewReader(&buf1)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	for _, n := range []string{"work.git/HEAD", "work.git/config", "work.git/refs/heads/main"} {
		if !names[n] {
			t.Errorf("archive has no %s", n)
		}
	}
}

func TestEmptyDatasetAndOrphans(t *testing.T) {
	if !hasGit() {
		t.Skip("no git binary")
	}
	dir := t.TempDir()
	res, err := Write(Input{Name: "empty", CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}, DirSink{Dir: dir})
	if err != nil || res.Commits != 1 {
		t.Fatalf("empty: %+v, %v", res, err)
	}
	git(t, dir, "fsck", "--strict")

	// An entity and a fact whose artifact was deleted end up in a last commit.
	dir = t.TempDir()
	in := Input{Name: "o", CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Entities: []graph.EvidenceEntity{{ID: 1, Name: "X", Type: "t"}, {ID: 2, Name: "Y", Type: "t"}},
		Edges:    []graph.EvidenceEdge{{ID: 1, SourceID: 1, TargetID: 2, Source: "X", Target: "Y", Relation: "r", InvalidAt: tp(time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC))}}}
	res, err = Write(in, DirSink{Dir: dir})
	if err != nil || res.Commits != 2 || res.Entities != 2 || res.Edges != 1 {
		t.Fatalf("orphans: %+v, %v", res, err)
	}
	git(t, dir, "fsck", "--strict")
	if f := git(t, dir, "show", "HEAD:edges/r/000001.md"); !strings.Contains(f, "invalid_at: 2025-02-01") {
		t.Errorf("orphan fact:\n%s", f)
	}
}
