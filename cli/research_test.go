package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResearchStartBody(t *testing.T) {
	var got map[string]any
	out, err := run(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/gb/research" {
			t.Errorf("%s %s", r.Method, r.URL)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(202)
		w.Write([]byte(`{"id":4,"status":"running"}`))
	}, "", "--user", "u7", "research", "start", "solid state batteries", "--max-steps", "9", "--model", "claude-x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id": 4`) {
		t.Errorf("out = %q", out)
	}
	if got["user_id"] != "u7" || got["topic"] != "solid state batteries" || got["max_steps"] != 9.0 || got["model"] != "claude-x" {
		t.Errorf("body = %v", got)
	}
}

func TestResearchStartFromStdinAndJSON(t *testing.T) {
	var got map[string]any
	h := func(w http.ResponseWriter, r *http.Request) {
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"id":1}`))
	}
	if _, err := run(t, h, "topic from stdin", "research", "start", "-"); err != nil || got["topic"] != "topic from stdin" {
		t.Errorf("stdin: %v %v", err, got)
	}
	// Only the flags that were set are sent; the flags override --json.
	if _, err := run(t, h, "", "research", "start", "--json", `{"topic":"a","max_steps":3,"user_id":"u1"}`, "--max-steps", "5"); err != nil {
		t.Fatal(err)
	}
	if got["topic"] != "a" || got["max_steps"] != 5.0 || got["user_id"] != "u1" {
		t.Errorf("body = %v", got)
	}
	if _, ok := got["model"]; ok {
		t.Errorf("model sent: %v", got)
	}
}

func TestResearchStartUsage(t *testing.T) {
	never := func(http.ResponseWriter, *http.Request) { t.Error("the server was called") }
	for _, args := range [][]string{{"research", "start"}, {"research", "start", "  "}, {"research", "start", "t", "--raw"}} {
		if _, err := run(t, never, "", args...); codeOf(err) != exitUsage {
			t.Errorf("%v: err = %v, code %d", args, err, codeOf(err))
		}
	}
}

func TestResearchStartWaits(t *testing.T) {
	old := researchPollInterval
	researchPollInterval = time.Millisecond
	defer func() { researchPollInterval = old }()

	polls := 0
	h := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			w.WriteHeader(202)
			io.WriteString(w, `{"id":12,"user_id":"local_user","status":"running"}`)
		case r.URL.Path == "/v1/gb/research/12" && r.URL.Query().Get("user_id") == "local_user":
			if polls++; polls < 3 {
				io.WriteString(w, `{"id":12,"status":"running","report":""}`)
				return
			}
			io.WriteString(w, `{"id":12,"status":"completed","report":"# The report"}`)
		default:
			t.Errorf("%s %s", r.Method, r.URL)
		}
	}
	out, err := run(t, h, "", "research", "start", "t", "--wait", "--raw")
	if err != nil || out != "# The report\n" || polls != 3 {
		t.Fatalf("out %q err %v polls %d", out, err, polls)
	}
	// Without --raw the finished run is printed whole.
	polls = 2
	if out, err = run(t, h, "", "research", "start", "t", "--wait"); err != nil || !strings.Contains(out, `"status": "completed"`) {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestResearchStartWaitFailed(t *testing.T) {
	old := researchPollInterval
	researchPollInterval = time.Millisecond
	defer func() { researchPollInterval = old }()
	out, err := run(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			io.WriteString(w, `{"id":3,"user_id":"u"}`)
			return
		}
		io.WriteString(w, `{"id":3,"status":"failed","error":"model down"}`)
	}, "", "research", "start", "t", "--wait")
	if err == nil || !strings.Contains(err.Error(), "model down") || codeOf(err) != exitOther || !strings.Contains(out, `"failed"`) {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestResearchListAndGet(t *testing.T) {
	var seen []string
	h := func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		w.Write([]byte(`{"id":8,"status":"completed","report":"R"}`))
	}
	for _, args := range [][]string{{"research", "ls"}, {"research", "ls", "--active", "--limit", "5"}, {"research", "ls", "--status", "failed"}, {"research", "get", "8"}} {
		if _, err := run(t, h, "", args...); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, h, "", "research", "get", "8", "--raw")
	if err != nil || out != "R\n" {
		t.Fatalf("raw: %q %v", out, err)
	}
	want := "GET /v1/gb/research?user_id=local_user|GET /v1/gb/research?active=true&limit=5&user_id=local_user|" +
		"GET /v1/gb/research?status=failed&user_id=local_user|GET /v1/gb/research/8?user_id=local_user|GET /v1/gb/research/8?user_id=local_user"
	if strings.Join(seen, "|") != want {
		t.Errorf("requests = %v", seen)
	}
	if _, err := run(t, h, "", "research", "get", "x"); codeOf(err) != exitUsage {
		t.Errorf("bad id: %v", err)
	}
}

func TestResearchGetRawWithoutReport(t *testing.T) {
	_, err := run(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":8,"status":"running","report":""}`))
	}, "", "research", "get", "8", "--raw")
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("err = %v", err)
	}
}
