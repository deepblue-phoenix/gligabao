package training

// The live feed of a run ("watch it run" in the admin UI): GET /v1/gb/training/evals|audits/{id}/events is a Server-Sent Events
// stream of what the run does as it does it, the same for evals and audits (core.RunStream has the feed itself).

import (
	"encoding/json"
	"net/http"
	"time"

	core "github.com/deepblue-phoenix/gligabao/internal/training"
)

const runStreamKeepalive = 15 * time.Second

// EventsOf serves GET /v1/gb/training/evals|audits/{id}/events: the live feed of a run as Server-Sent Events. Every
// event's data is a core.RunEvent JSON. The first is a "snapshot" (the run and all of its cases so far: the judged and errored
// ones from the database, and the ones the model answered that the judge has not scored yet, with status "answered"); then, while
// the run goes on, an "answer" for every question the model answers, a "batch" when a batch is judged, and finally an "end" after
// which the stream closes. For a run that is over, the stream is a snapshot and the "end". A client that reconnects gets a new
// snapshot, so it applies cases by idx. 404 for an unknown run.
func (m *runManager) EventsOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		watch, err := m.Watch(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer watch.Close()

		rc := http.NewResponseController(w)
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		send := func(e core.RunEvent) bool {
			b, err := json.Marshal(e)
			if err != nil {
				return true
			}
			_, err = w.Write([]byte("event: " + e.Type + "\ndata: " + string(b) + "\n\n"))
			return err == nil && rc.Flush() == nil
		}
		if _, err := w.Write([]byte(": connected\n\n")); err != nil || rc.Flush() != nil {
			return
		}
		if !send(core.RunEvent{Type: core.RunEventSnapshot, Run: watch.Run, Cases: watch.Cases}) {
			return
		}
		ch := watch.Events
		if ch == nil { // nothing is running this run (any more): it is as stored
			send(core.RunEvent{Type: core.RunEventEnd, Run: watch.Run})
			return
		}
		keepalive := time.NewTicker(runStreamKeepalive)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-m.Done():
				return
			case e, ok := <-ch:
				if !ok { // the run ended (the "end" event was sent before) or this watcher fell behind: it reconnects
					return
				}
				if !send(e) || e.Type == core.RunEventEnd {
					return
				}
			case <-keepalive.C:
				if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
					return
				}
			}
		}
	}
}
