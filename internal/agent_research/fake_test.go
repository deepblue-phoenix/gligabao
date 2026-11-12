package agent_research_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

// teamFake is a fake Messages API for agents that run at the same time: each agent has its own script of answers. The agent is told by
// its prompt (a worker has the question it works on in its brief: "Your question (q_002, ..."; the rest is "root"), and each call gets the
// next answer of its script. A web_search call (the request of the search tool) is answered with a canned text and is not counted.
type teamFake struct {
	mu      sync.Mutex
	scripts map[string][]string
	calls   map[string]int
	bodies  map[string][]string
	// hang are the agents whose first call never answers, until their request is cancelled.
	hang map[string]bool
}

var workerKey = regexp.MustCompile(`Your question \((q_\d+)`)

func (f *teamFake) start(t *testing.T) *ai.Client { return f.startWith(t) }

func (f *teamFake) startWith(t *testing.T,
	opts ...ai.Option) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		if strings.Contains(body, "web_search_20250305") {
			io.WriteString(w, searchAnswer)
			return
		}
		key := "root"
		if m := workerKey.FindStringSubmatch(body); m != nil {
			key = m[1]
		}
		f.mu.Lock()
		f.calls[key]++
		n := f.calls[key]
		f.bodies[key] = append(f.bodies[key], body)
		script := f.scripts[key]
		hang := f.hang[key]
		f.mu.Unlock()
		if hang {
			<-r.Context().Done()
			return
		}
		if n > len(script) {
			http.Error(w,
				`{"type":"error","error":{"type":"api_error","message":"script ended"}}`,
				http.StatusInternalServerError)
			return
		}
		io.WriteString(w, script[n-1])
	}))
	t.Cleanup(srv.Close)
	c, err := ai.New("k", append([]ai.Option{ai.WithBaseURL(srv.URL)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newTeamFake(scripts map[string][]string) *teamFake {
	return &teamFake{
		scripts: scripts,
		calls:   map[string]int{},
		bodies:  map[string][]string{},
		hang:    map[string]bool{},
	}
}

// call is an answer of the model that uses tools: pairs of name and input.
func call(tools ...[2]string) string {
	var blocks []string
	for i, t := range tools {
		blocks = append(blocks, `{"type":"tool_use","id":"t`+string(rune('a'+i))+`","name":"`+t[0]+`","input":`+t[1]+`}`)
	}
	return `{"model":"m","stop_reason":"tool_use","content":[` + strings.Join(blocks, ",") + `],"usage":{"input_tokens":1,"output_tokens":1}}`
}

func final(text string) string {
	return `{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"` + text + `"}],"usage":{"input_tokens":1,"output_tokens":1}}`
}

func (f *teamFake) body(key string,
	i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.bodies[key]) {
		return ""
	}
	return f.bodies[key][i]
}
