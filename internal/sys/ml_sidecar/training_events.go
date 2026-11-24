package ml_sidecar

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

	"github.com/deepblue-phoenix/gligabao/monitoring"
)

// Types of a TrainingEvent (the ones the ML sidecar reports).
const (
	EventStatus   = monitoring.EventStatus
	EventProgress = monitoring.EventProgress
	EventLog      = monitoring.EventLog
	EventSystem   = monitoring.EventSystem
)

// TrainingEvent is one event of a training job's progress (ml_sidecar/ai_train/ai_train_core/progress_events.py): a status
// change, a progress report, a log line or a system event. It is the public monitoring.TrainingEvent, which the server completes
// with the job it is of and passes on; StreamTrainingJobEvents sets its Seq.
type TrainingEvent = monitoring.TrainingEvent

// TrainedModelInfo describes the weights a completed training job saved.
type TrainedModelInfo = monitoring.TrainedModelInfo

// TrainedModelFile is one file of a trained model's weights.
type TrainedModelFile = monitoring.TrainedModelFile

// StreamTrainingJobEvents follows the progress of a training job: it calls GET /training/job_events (Server-Sent Events)
// and calls fn with each event, from the one with index `from` (0 = the first) on. It returns nil when the ML sidecar ends
// the stream (after the job's last event, or when it has been open long enough: reconnect with from = last Seq + 1),
// fn's error if fn fails, ctx's error when ctx is done, and an error for a failed connection or a broken stream.
func (c *Client) StreamTrainingJobEvents(ctx context.Context,
	jobID string,
	from int,
	fn func(TrainingEvent) error) error {
	return c.streamEvents(ctx,
		"/training/job_events",
		jobID,
		from,
		func(seq int,
			data []byte) error {
			var ev TrainingEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				return fmt.Errorf("ML sidecar GET /training/job_events: decode event: %w", err)
			}
			ev.Seq = seq
			return fn(ev)
		})
}

// streamEvents follows the SSE stream of the events of a job at path (GET path?job_id=&from=): it calls fn with the index and the JSON of
// each event, from the one with index `from` on. The results are those of StreamTrainingJobEvents.
func (c *Client) streamEvents(ctx context.Context,
	path,
	jobID string,
	from int,
	fn func(seq int,
		data []byte) error) error {
	q := url.Values{"job_id": {jobID}, "from": {strconv.Itoa(max(from, 0))}}
	req, err := http.NewRequestWithContext(ctx,
		http.MethodGet,
		c.baseURL+path+"?"+q.Encode(),
		nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return fmt.Errorf("ML sidecar GET %s: %w",
			path,
			err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{
			Method: http.MethodGet,
			Path:   path,
			Code:   resp.StatusCode,
			Body:   strings.TrimSpace(string(body)),
		}
	}

	next := from // index of the event when the stream gives no id
	var id string
	var data strings.Builder
	dispatch := func() error {
		defer func() { id = ""; data.Reset() }()
		if data.Len() == 0 {
			return nil
		}
		seq := next
		if n, err := strconv.Atoi(id); err == nil {
			seq = n
		}
		next = seq + 1
		return fn(seq, []byte(data.String()))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte,
		0,
		64<<10), 4<<20)
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
		return fmt.Errorf("ML sidecar GET %s: %w",
			path,
			err)
	}
	return ctx.Err()
}
