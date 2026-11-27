package evals_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/evals"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/synth"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
)

// synthLLM writes cases: one object when the system prompt asks for one, else an array. It records the user messages.
type synthLLM struct {
	mu    sync.Mutex
	n     int
	users []string
}

var perCallRe = regexp.MustCompile(`array of exactly (\d+) case`)

func (l *synthLLM) Complete(_ context.Context,
	req llm.Request) (*llm.Response, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.users = append(l.users, req.Messages[0].Content)
	count := 1
	array := false
	if m := perCallRe.FindStringSubmatch(req.System); m != nil {
		fmt.Sscan(m[1], &count)
		array = true
	}
	items := make([]string, count)
	for i := range items {
		l.n++
		items[i] = fmt.Sprintf(`{"remember":[{"text":"Fact %d."}],"query":"Question %d?","expect_contains":["Fact %d"]}`, l.n, l.n, l.n)
	}
	text := items[0]
	if array {
		text = "```json\n[" + strings.Join(items, ",") + "]\n```"
	}
	return &llm.Response{
		Text:         text,
		InputTokens:  10,
		OutputTokens: 5,
		CostUSD:      0.01,
	}, nil
}

type synthDetail struct {
	Name        string                 `json:"name"`
	Origin      core.Origin            `json:"origin"`
	Version     int                    `json:"version"`
	Versions    []core.SynthVersion    `json:"versions"`
	Generations []core.SynthGeneration `json:"generations"`
	Cases       []struct {
		Name       string `json:"name"`
		Origin     string `json:"origin"`
		Generation int64  `json:"generation"`
		Query      string `json:"query"`
	} `json:"cases"`
}

// waitGenerated waits until the newest generation of the eval is finished and returns the detail.
func (a *api) waitGenerated(user,
	name string) synthDetail {
	a.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var d synthDetail
		a.expect(http.StatusOK, "GET", "/synth/"+name, user, nil, &d)
		if len(d.Generations) > 0 && d.Generations[0].Status.Finished() {
			return d
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("the generation did not finish: %+v", d.Generations)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSynthAPI(t *testing.T) { dbtest.Run(t, testSynthAPI) }

func testSynthAPI(t *testing.T,
	d *sql.DB) {
	a := newAPI(t, d, true)
	lm := &synthLLM{}
	a.svc.Synth = synth.New(synth.Config{
		DB:    d,
		Store: a.m.Store(),
		LLM:   lm,
		Env:   a.m.Env,
		Models: func() []string {
			names := []string{}
			for _, mi := range a.m.Models() {
				names = append(names, mi.Name)
			}
			return names
		},
	})
	t.Cleanup(a.svc.Synth.Wait)
	model := a.m.Models()[0].Name

	var form struct {
		Models      []string `json:"models"`
		CanGenerate bool     `json:"can_generate"`
		MaxExamples int      `json:"max_examples"`
	}
	a.expect(http.StatusOK, "GET", "/synth", "alice", nil, &form)
	if len(form.Models) == 0 || !form.CanGenerate || form.MaxExamples == 0 {
		t.Fatalf("form: %+v", form)
	}

	// Not valid: no prompt, unknown model, too many examples, unknown key, reserved name.
	a.expect(http.StatusBadRequest, "POST", "/synth", "alice", `{"name":"s1","model":"`+model+`","num_examples":3}`, nil)
	a.expect(http.StatusBadRequest, "POST", "/synth", "alice", `{"name":"s1","prompt":"p","model":"nope","num_examples":3}`, nil)
	a.expect(http.StatusBadRequest, "POST", "/synth", "alice", `{"name":"s1","prompt":"p","model":"`+model+`","num_examples":100000}`, nil)
	a.expect(http.StatusBadRequest, "POST", "/synth", "alice", `{"name":"s1","prompt":"p","model":"`+model+`","num_examples":3,"bogus":1}`, nil)
	a.expect(http.StatusBadRequest, "POST", "/synth", "alice", `{"name":"synth","prompt":"p","model":"`+model+`","num_examples":3}`, nil)
	a.expect(http.StatusBadRequest, "POST", "/synth", "", `{}`, nil)

	// Create: one case per call, anchored on the first.
	var started struct {
		Name       string                `json:"name"`
		Version    int                   `json:"version"`
		Generation *core.SynthGeneration `json:"generation"`
	}
	a.expect(http.StatusAccepted, "POST", "/synth", "alice",
		`{"name":"s1","description":"d","prompt":"Questions about pets","corpus_prompt":"Vet notes","model":"`+model+`","num_examples":3}`,
		&started)
	if started.Version != 1 || started.Generation == nil || started.Generation.Requested != 3 {
		t.Fatalf("started: %+v", started)
	}
	a.expect(http.StatusConflict, "POST", "/synth", "alice", `{"name":"s1","prompt":"p","model":"`+model+`","num_examples":3}`, nil)
	det := a.waitGenerated("alice", "s1")
	g := det.Generations[0]
	if g.Status != core.SynthCompleted || g.Generated != 3 || g.Calls != 3 || g.CostUSD < 0.029 || det.Origin != core.OriginSynthetic || len(det.Cases) != 3 {
		t.Fatalf("detail: %+v", det)
	}
	for i, c := range det.Cases {
		if c.Name != fmt.Sprintf("case-%03d", i+1) || c.Origin != "generated" {
			t.Errorf("case %d: %+v", i, c)
		}
	}
	if strings.Contains(lm.users[0], "Question 1?") || !strings.Contains(lm.users[2], "Question 1?") || strings.Contains(lm.users[2], "Anchor") {
		t.Errorf("the calls were not told the earlier queries: %q", lm.users)
	}

	// The stored eval is an ordinary one: it is listed as synthetic, next to a human one, and the file is the user's.
	a.expect(http.StatusCreated, "PUT", "/h1", "alice", eval("h1", "alice"), nil)
	var list struct {
		Evals []evals.EvalView `json:"evals"`
	}
	a.expect(http.StatusOK, "GET", "", "alice", nil, &list)
	origins := map[string]core.Origin{}
	for _, e := range list.Evals {
		origins[e.Name] = e.Origin
		if e.Name == "s1" && (e.Synth == nil || e.Synth.Version != 1 || e.Synth.Generation != core.SynthCompleted || e.Cases != 3) {
			t.Errorf("synth view: %+v", e)
		}
	}
	if origins["s1"] != core.OriginSynthetic || origins["h1"] != core.OriginHuman {
		t.Errorf("origins: %v", origins)
	}
	var onlySynth struct {
		Evals []evals.EvalView `json:"evals"`
	}
	a.expect(http.StatusOK, "GET", "/synth", "alice", nil, &onlySynth)
	if len(onlySynth.Evals) != 1 || onlySynth.Evals[0].Name != "s1" {
		t.Errorf("synth list: %+v", onlySynth.Evals)
	}
	a.expect(http.StatusNotFound, "GET", "/synth/h1", "alice", nil, nil) // a human eval has no definition
	a.expect(http.StatusNotFound, "GET", "/synth/s1", "bob", nil, nil)   // another user's
	a.expect(http.StatusOK, "GET", "/s1", "alice", nil, nil)             // the file
	a.expect(http.StatusNotFound, "PUT", "/synth/cases/s1/nope", "alice", `{"query":"q"}`, nil)

	// The user edits one case.
	var edited struct {
		Origin string `json:"origin"`
	}
	a.expect(http.StatusBadRequest, "PUT", "/synth/cases/s1/case-002", "alice", `{"remember":[{"text":""}]}`, nil)
	a.expect(http.StatusBadRequest, "PUT", "/synth/cases/s1/case-002", "alice", `{"typo":1}`, nil)
	a.expect(http.StatusOK, "PUT", "/synth/cases/s1/case-002", "alice",
		`{"remember":[{"text":"Mine."}],"query":"My question?","expect_contains":["Mine"]}`, &edited)
	if edited.Origin != "edited" {
		t.Errorf("edited: %+v", edited)
	}

	// Changing the definition makes version 2 (the same settings do not); the cases stay until it is generated again.
	var upd struct {
		Version    int                   `json:"version"`
		Generation *core.SynthGeneration `json:"generation"`
	}
	a.expect(http.StatusOK, "PUT", "/synth/s1", "alice", `{"prompt":"Questions about pets"}`, &upd)
	if upd.Version != 1 || upd.Generation != nil {
		t.Errorf("same settings: %+v", upd)
	}
	a.expect(http.StatusOK, "PUT", "/synth/s1", "alice", `{"prompt":"Questions about birds","corpus_prompt":""}`, &upd)
	if upd.Version != 2 {
		t.Errorf("update: %+v", upd)
	}
	a.expect(http.StatusBadRequest, "PUT", "/synth/s1", "alice", `{"num_examples":0}`, nil)
	det = a.waitGenerated("alice", "s1")
	if det.Version != 2 || len(det.Versions) != 2 || det.Versions[1].Prompt != "Questions about pets" || det.Versions[0].Prompt != "Questions about birds" {
		t.Fatalf("versions: %+v", det.Versions)
	}

	// Regenerate: the edited case stays, the others are written again with the new version.
	a.expect(http.StatusAccepted, "POST", "/synth/generate/s1", "alice", "", &started)
	det = a.waitGenerated("alice", "s1")
	g = det.Generations[0]
	if started.Generation == nil || g.Version != 2 || g.Status != core.SynthCompleted || g.Kept != 1 || g.Requested != 2 || g.Generated != 2 || len(det.Cases) != 3 {
		t.Fatalf("regenerated: %+v / %+v", g, det.Cases)
	}
	for _, c := range det.Cases {
		want := "generated"
		if c.Name == "case-002" {
			want = "edited"
			if c.Query != "My question?" {
				t.Errorf("the edited case changed: %+v", c)
			}
		} else if core.SynthGenerationID(c.Generation) != g.ID {
			t.Errorf("case %s is from generation %d, want %d", c.Name, c.Generation, g.ID)
		}
		if c.Origin != want {
			t.Errorf("case %s: origin %s, want %s", c.Name, c.Origin, want)
		}
	}

	// Without keeping, every case is replaced; in bulk mode (4 examples, 3 per call: two calls).
	a.expect(http.StatusOK, "PUT", "/synth/s1", "alice", `{"mode":"bulk","bulk_size":3,"num_examples":4}`, nil)
	a.expect(http.StatusAccepted, "POST", "/synth/generate/s1", "alice", `{"keep_edited":false}`, nil)
	det = a.waitGenerated("alice", "s1")
	g = det.Generations[0]
	if g.Status != core.SynthCompleted || g.Kept != 0 || g.Calls != 2 || len(det.Cases) != 4 || det.Version != 3 {
		t.Fatalf("bulk: %+v / %d cases", g, len(det.Cases))
	}
	for _, c := range det.Cases {
		if c.Origin != "generated" {
			t.Errorf("case %s: %s", c.Name, c.Origin)
		}
	}

	// Update with regenerate in one request.
	a.expect(http.StatusAccepted, "PUT", "/synth/s1", "alice", `{"num_examples":2,"regenerate":true,"keep_edited":false}`, &upd)
	if upd.Version != 4 || upd.Generation == nil {
		t.Errorf("update+regenerate: %+v", upd)
	}
	det = a.waitGenerated("alice", "s1")
	if len(det.Cases) != 2 || det.Generations[0].Version != 4 || len(det.Generations) != 4 {
		t.Fatalf("after update+regenerate: %d cases, %+v", len(det.Cases), det.Generations)
	}

	// The synthetic eval is deleted like any other (with its definition), and another user cannot see it.
	a.expect(http.StatusNotFound, "DELETE", "/s1", "bob", nil, nil)
	a.expect(http.StatusNoContent, "DELETE", "/s1", "alice", nil, nil)
	a.expect(http.StatusNotFound, "GET", "/synth/s1", "alice", nil, nil)
	var n int
	if err := d.QueryRow(`SELECT (SELECT COUNT(*) FROM gb_synth_defs) + (SELECT COUNT(*) FROM gb_synth_generations) + (SELECT COUNT(*) FROM gb_synth_cases) + (SELECT COUNT(*) FROM gb_synth_def_versions)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("synth rows left: %d, %v", n, err)
	}
}

func TestSynthAPINoLLM(t *testing.T) { dbtest.Run(t, testSynthAPINoLLM) }

func testSynthAPINoLLM(t *testing.T,
	d *sql.DB) {
	a := newAPI(t, d, false)
	// no manager: 503
	a.expect(http.StatusServiceUnavailable, "GET", "/synth", "alice", nil, nil)
	a.svc.Synth = synth.New(synth.Config{
		DB:     d,
		Store:  a.m.Store(),
		Env:    a.m.Env,
		Models: func() []string { return []string{"m"} },
	})
	model := "m"
	a.expect(http.StatusServiceUnavailable, "POST", "/synth", "alice", `{"name":"s","prompt":"p","model":"`+model+`","num_examples":1}`, nil)
	var form struct {
		CanGenerate bool `json:"can_generate"`
	}
	a.expect(http.StatusOK, "GET", "/synth", "alice", nil, &form)
	if form.CanGenerate {
		t.Errorf("can_generate without an LLM")
	}
}
