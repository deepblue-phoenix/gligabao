package research

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// streamInterval is how often Stream reads the run to see if it changed; pingEvery how many unchanged reads pass before a comment line is
// written to keep proxies from closing an idle stream.
const (
	streamInterval = time.Second
	pingEvery      = 10
)

// Snapshot is the data of the "snapshot" event of Stream: everything a UI shows about a research, as of one moment.
type Snapshot struct {
	// Run is the research with its states (in the order they were entered) and, once it completed, its report.
	Run *core.Run `json:"run"`
	// Subagents are the workers the investigative loop started, in the order they were started.
	Subagents []core.Run `json:"subagents"`
	// Investigation is what the investigative loop built (frontier of questions, challenges, notes); nil for a run of the simple loop.
	Investigation *admin.ResearchInvestigation `json:"investigation"`
}

// sseWriter writes Server-Sent Events to a response; send is safe for concurrent use and ignores a client that went away.
type sseWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	rc *http.ResponseController
}

// newSSEWriter sends the headers of an event stream and returns the writer; nil if the response cannot be flushed.
func newSSEWriter(w http.ResponseWriter) *sseWriter {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(": connected\n\n")); err != nil || rc.Flush() != nil {
		return nil
	}
	return &sseWriter{
		w:  w,
		rc: rc,
	}
}

// raw writes one event whose data is already JSON.
func (s *sseWriter) raw(event string,
	data []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write([]byte("event: " + event + "\ndata: " + string(data) + "\n\n")); err != nil {
		return false
	}
	return s.rc.Flush() == nil
}

// ping writes a comment line (no event).
func (s *sseWriter) ping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write([]byte(": ping\n\n")); err != nil {
		return false
	}
	return s.rc.Flush() == nil
}

// takeSnapshot reads the research of userID with its sub-agents and, for an investigative run, its investigation.
func takeSnapshot(ctx context.Context,
	m *agent_research.Manager,
	userID string,
	id int64) (Snapshot, error) {
	run, err := m.Get(ctx,
		userID,
		id)
	if err != nil {
		return Snapshot{}, err
	}
	kids, err := m.List(ctx,
		userID,
		agent_research.ListOptions{
			ParentID: id,
			Limit:    agent_research.MaxListLimit,
		})
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		Run:       run,
		Subagents: reversed(kids), // listed newest first; shown in the order they were started
	}
	if run.Phase != "" {
		inv, err := admin.LoadInvestigation(ctx,
			m,
			run)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Investigation = &inv
	}
	return snap, nil
}

// reversed returns a reversed copy of runs (never nil, so that it encodes as []).
func reversed(runs []core.Run) []core.Run {
	out := make([]core.Run, len(runs))
	for i, r := range runs {
		out[len(runs)-1-i] = r
	}
	return out
}

// Stream serves GET /v1/gb/research/{id}/stream: Server-Sent Events that follow a research until it ends (or replay a finished one).
//   - event "snapshot": a Snapshot (the run with its states and report, its sub-agents and its investigation). The first one is sent at
//     once, then one each time something changed (a new state, tokens, a question answered, the report, ...).
//   - event "done": {"status": "completed"|"failed"|"stopped"}, after the last snapshot, when the run is not active any more. The stream ends.
//   - event "error": {"error": "..."} if the run cannot be read any more. The stream ends.
//
// 404 (a plain JSON error, before the stream starts) for an unknown id or the one of another user's research.
func Stream(m *agent_research.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := user(w, httputil.UserID(r))
		if !ok {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"),
			10,
			64)
		if err != nil || id < 1 {
			httputil.Error(w,
				http.StatusBadRequest,
				"id must be a positive integer")
			return
		}
		first, err := takeSnapshot(r.Context(),
			m,
			userID,
			id)
		if err != nil {
			writeErr(w, err)
			return
		}
		sse := newSSEWriter(w)
		if sse == nil {
			httputil.Error(w,
				http.StatusInternalServerError,
				"streaming not supported")
			return
		}
		follow(r.Context(),
			m,
			userID,
			id,
			sse,
			first)
	}
}

// follow sends first, then polls the research and sends a snapshot whenever it differs from the last one sent, until the run is not
// active (then "done") or ctx ends (the client left).
func follow(ctx context.Context,
	m *agent_research.Manager,
	userID string,
	id int64,
	sse *sseWriter,
	first Snapshot) {
	ticker := time.NewTicker(streamInterval)
	defer ticker.Stop()

	snap, last, quiet := first, []byte(nil), 0
	for {
		data, err := json.Marshal(snap)
		switch {
		case err != nil:
			sse.raw("error",
				[]byte(`{"error":"failed to encode the research"}`))
			return
		case !bytes.Equal(data, last):
			if !sse.raw("snapshot",
				data) {
				return
			}
			last, quiet = data, 0
		case quiet+1 >= pingEvery:
			if !sse.ping() {
				return
			}
			quiet = 0
		default:
			quiet++
		}
		if !snap.Run.Status.Active() {
			done, _ := json.Marshal(map[string]string{"status": string(snap.Run.Status)})
			sse.raw("done",
				done)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if snap, err = takeSnapshot(ctx,
			m,
			userID,
			id); err != nil {
			if ctx.Err() == nil {
				log.WithError(err).WithField("run_id", id).Error("research stream: failed to read the research")
				sse.raw("error",
					[]byte(`{"error":"failed to read the research"}`))
			}
			return
		}
	}
}
