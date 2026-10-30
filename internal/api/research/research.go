// Package research contains the HTTP handlers of the research agents (package agent_research): start a deep-research agent on a topic,
// list the agents (the active ones only, if asked) and read one with the states it went through (see docs/research.md).
package research

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

const maxBodyBytes = 64 << 10 // 64 KiB

type startRequest struct {
	UserID string `json:"user_id"`
	agent_research.Options
}

// writeErr answers with the status of the Kind of err (500 for any other error).
func writeErr(w http.ResponseWriter, err error) {
	var e *agent_research.Error
	if !errors.As(err, &e) {
		httputil.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	status := http.StatusInternalServerError
	switch e.Kind {
	case agent_research.KindInvalid:
		status = http.StatusBadRequest
	case agent_research.KindNotFound:
		status = http.StatusNotFound
	case agent_research.KindConflict:
		status = http.StatusConflict
	case agent_research.KindUnavailable:
		status = http.StatusServiceUnavailable
	}
	httputil.Error(w, status, e.Msg)
}

// user resolves the user_id of a request, answering 400 itself if it is not valid.
func user(w http.ResponseWriter, userID string) (string, bool) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		httputil.Error(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return userID, true
}

// Start serves POST /v1/gb/research with the JSON body {"user_id": "", "topic": "", "max_steps": 20, "model": ""}
// (agent_research.Options and the user): it starts a deep-research agent on the topic and answers 202 with the run
// (core.Run, status "running", state "starting"); the agent works in the background (see Get and List), searching the web and
// reading pages, and ends with a report.
//   - topic: what to research (required, at most 2000 characters)
//   - max_steps: the most model calls the agent makes, the last of which writes the report (default 20, at most 40)
//   - model: the model it runs on, one that supports Anthropic's web search tool (default: the server's default model)
//
// 400 for a bad body, 409 when the user has 3 agents working already, 503 without an Anthropic API key.
func Start(m *agent_research.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req startRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		userID, ok := user(w, req.UserID)
		if !ok {
			return
		}
		run, err := m.Start(r.Context(), userID, req.Options)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusAccepted, run)
	}
}

// List serves GET /v1/gb/research?user_id=...[&active=true][&status=running|completed|failed][&limit=<n>]: {"runs": [...]}, the
// agents of the user (core.Run, without their states), newest first, at most limit (default 50, at most 500). With active=true only
// the agents that are working now; with status those with that status (not together with active).
func List(m *agent_research.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		userID, ok := user(w, q.Get("user_id"))
		if !ok {
			return
		}
		opts := agent_research.ListOptions{Status: q.Get("status")}
		if s := q.Get("active"); s != "" {
			active, err := strconv.ParseBool(s)
			if err != nil {
				httputil.Error(w, http.StatusBadRequest, "active must be true or false")
				return
			}
			opts.Active = active
		}
		if s := q.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				httputil.Error(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			opts.Limit = n
		}
		runs, err := m.List(r.Context(), userID, opts)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"runs": runs})
	}
}

// Get serves GET /v1/gb/research/{id}?user_id=...: the agent (core.Run) with its states, in the order it entered them, to follow what
// it does. The report is in it once the status is "completed". 404 for an unknown id, or the one of another user's agent.
func Get(m *agent_research.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := user(w, r.URL.Query().Get("user_id"))
		if !ok {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			httputil.Error(w, http.StatusBadRequest, "id must be a positive integer")
			return
		}
		run, err := m.Get(r.Context(), userID, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, run)
	}
}
