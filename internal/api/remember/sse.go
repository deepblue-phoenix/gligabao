package remember

import (
	"encoding/json"
	"net/http"
	"sync"
)

// sseWriter writes Server-Sent Events to a response. send is safe to call from several goroutines (the pipeline reports
// from its own), and ignores a client that went away.
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
	return &sseWriter{w: w, rc: rc}
}

// send writes one event whose data is v as JSON.
func (s *sseWriter) send(event string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write([]byte("event: " + event + "\ndata: " + string(b) + "\n\n")); err == nil {
		_ = s.rc.Flush()
	}
}
