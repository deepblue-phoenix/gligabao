package sidecar

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Types of a TrainingEvent.
const (
	EventStatus   = "status"
	EventProgress = "progress"
)

// TrainingEvent is one event of a training job's progress (sidecar/ai_train/ai_train_core/progress_events.py): a status
// change or a progress report. Fields that an event does not have are nil / empty.
type TrainingEvent struct {
	// Seq is the event's index in the job's progress file (the SSE id); StreamTrainingJobEvents sets it.
	Seq  int    `json:"seq"`
	Type string `json:"type"` // EventStatus or EventProgress
	// Status ("running", "training", "completed", "failed") is set on status events. A completed event has the FinalLoss,
	// a failed one the Error; a "training" one the MaxSteps.
	Status    string   `json:"status,omitempty"`
	MaxSteps  *int     `json:"max_steps,omitempty"`
	FinalLoss *float64 `json:"final_loss,omitempty"`
	Error     string   `json:"error,omitempty"`
	// Traceback is the Python traceback of a failed job (its last part), for diagnosis.
	Traceback string `json:"traceback,omitempty"`
	// Progress events: the Trainer's step, loss (nil when the log has none, or it is not finite), learning rate and epoch,
	// and the seconds since training began.
	Step           *int     `json:"step,omitempty"`
	Loss           *float64 `json:"loss,omitempty"`
	LearningRate   *float64 `json:"learning_rate,omitempty"`
	Epoch          *float64 `json:"epoch,omitempty"`
	ElapsedSeconds *float64 `json:"elapsed_seconds,omitempty"`
	// Timestamp is when the event was written, in seconds since the Unix epoch.
	Timestamp float64 `json:"timestamp,omitempty"`
}

// Finished reports whether the event ends the job.
func (e TrainingEvent) Finished() bool {
	return e.Type == EventStatus && (e.Status == "completed" || e.Status == "failed")
}

// StreamTrainingJobEvents follows the progress of a training job: it calls GET /training/job_events (Server-Sent Events)
// and calls fn with each event, from the one with index `from` (0 = the first) on. It returns nil when the sidecar ends
// the stream (after the job's last event, or when it has been open long enough: reconnect with from = last Seq + 1),
// fn's error if fn fails, ctx's error when ctx is done, and an error for a failed connection or a broken stream.
func (c *Client) StreamTrainingJobEvents(ctx context.Context, jobID string, from int, fn func(TrainingEvent) error) error {
	const path = "/training/job_events"
	q := url.Values{"job_id": {jobID}, "from": {strconv.Itoa(max(from, 0))}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return fmt.Errorf("sidecar GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Method: http.MethodGet, Path: path, Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}

	next := from // seq of the event when the stream gives no id
	var id string
	var data strings.Builder
	dispatch := func() error {
		defer func() { id = ""; data.Reset() }()
		if data.Len() == 0 {
			return nil
		}
		var ev TrainingEvent
		if err := json.Unmarshal([]byte(data.String()), &ev); err != nil {
			return fmt.Errorf("sidecar GET %s: decode event: %w", path, err)
		}
		ev.Seq = next
		if n, err := strconv.Atoi(id); err == nil {
			ev.Seq = n
		}
		next = ev.Seq + 1
		return fn(ev)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"): // comment (keepalive)
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "id":
				id = value
			case "data":
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(value)
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("sidecar GET %s: %w", path, err)
	}
	return ctx.Err()
}
