package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ResearchJob is a research started on the sidecar (POST /research/job_start): the gligabao_agent binary running in a Modal container
// (sidecar/ai_train/ai_train_core/agent_run.py).
type ResearchJob struct {
	// JobID names the job's event log there (see StreamResearchJobEvents); ProviderJobID is the compute provider's ID of the call.
	JobID         string `json:"job_id"`
	ProviderJobID string `json:"provider_job_id,omitempty"`
	Status        string `json:"status"` // "started"
}

// StartResearchJobRequest is the body of POST /research/job_start. Topic is required; zero fields take the agent's defaults.
type StartResearchJobRequest struct {
	Topic    string `json:"topic"`
	MaxSteps int    `json:"max_steps,omitempty"`
	Model    string `json:"model,omitempty"`
	Dataset  string `json:"dataset,omitempty"`
	// Agent is the name of the agent definition to run, and AgentBundle the definitions (agent_defs.Bundle as JSON: the agent, those its
	// roles start, with the prompts and skills as text): the container has no agent_defs directory. MaxCostUSD caps the cost of the
	// whole research (0: no cap).
	Agent       string          `json:"agent,omitempty"`
	AgentBundle json.RawMessage `json:"agent_bundle,omitempty"`
	MaxCostUSD  float64         `json:"max_cost_usd,omitempty"`
}

// StartResearchJob starts the agent in Modal through POST /research/job_start. The sidecar answers 503 (a *StatusError) when the app
// it fronts has no agent function (the binary was not there when it was deployed).
func (c *Client) StartResearchJob(ctx context.Context, req StartResearchJobRequest) (ResearchJob, error) {
	var out ResearchJob
	if strings.TrimSpace(req.Topic) == "" {
		return out, errors.New("sidecar: topic is required")
	}
	if err := c.post(ctx, "/research/job_start", req, &out); err != nil {
		return out, err
	}
	if strings.TrimSpace(out.JobID) == "" {
		return out, errors.New("sidecar: job_start returned no job_id")
	}
	return out, nil
}

// CancelResearchJob asks the sidecar to stop a research job (POST /research/job_cancel). The container ends the agent and the job ends
// with a "cancelled" event. 404 for a job the sidecar does not know and 409 for one that is over (a *StatusError).
func (c *Client) CancelResearchJob(ctx context.Context, jobID string) error {
	if strings.TrimSpace(jobID) == "" {
		return errors.New("sidecar: job_id is required")
	}
	var out struct {
		Status string `json:"status"`
	}
	return c.post(ctx, "/research/job_cancel", map[string]string{"job_id": jobID}, &out)
}

// Statuses of a ResearchEvent.
const (
	ResearchRunning   = "running"
	ResearchCompleted = "completed"
	ResearchFailed    = "failed"
	ResearchCancelled = "cancelled"
)

// Types of a ResearchEvent.
const (
	ResearchEventStatus = "status"
	ResearchEventState  = "state"
	ResearchEventUsage  = "usage"
	ResearchEventLog    = "log"
)

// ResearchEvent is one event of a research job (sidecar/ai_train/ai_train_core/agent_run.py). Fields that an event does not have are zero.
type ResearchEvent struct {
	// Seq is the event's index in the job's event log (the SSE id); StreamResearchJobEvents sets it.
	Seq  int    `json:"seq"`
	Type string `json:"type"` // ResearchEventStatus, ...State, ...Usage or ...Log
	// Status events: "running", "completed" (with Report and the Usage of the whole tree of agents), "failed" or "cancelled" (with Error).
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	Report string `json:"report,omitempty"`
	// State events: the agent ("main" or a sub-agent's name) entered State; StateSeq numbers its states from 1. An event that repeats a
	// (Agent, StateSeq) adds the Error the state ended with.
	Agent    string `json:"agent,omitempty"`
	StateSeq int    `json:"state_seq,omitempty"`
	State    string `json:"state,omitempty"`
	Step     int    `json:"step,omitempty"`
	Detail   string `json:"detail,omitempty"`
	// Message is the text of a log event.
	Message string `json:"message,omitempty"`
	// Usage is on usage events and on the completed status.
	Usage *ResearchUsage `json:"usage,omitempty"`
	// Export is on the final status event (completed, failed or cancelled) of a job whose agent handed back an export file: what the
	// agent learned and recorded in the container (see DownloadResearchExport). nil if there is none.
	Export *ResearchExport `json:"export,omitempty"`
	// Timestamp is when the event was written, in seconds since the Unix epoch.
	Timestamp float64 `json:"timestamp,omitempty"`
}

// ResearchExport describes the export file of a research job: its size in bytes and the hex SHA-256 of its content, to check a download.
type ResearchExport struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// ResearchUsage is what the agents of a research spent, sub-agents included.
type ResearchUsage struct {
	Steps        int     `json:"steps"`
	ToolCalls    int     `json:"tool_calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Subagents    int     `json:"subagents"`
}

// Finished reports whether the event ends the job.
func (e ResearchEvent) Finished() bool {
	return e.Type == ResearchEventStatus && (e.Status == ResearchCompleted || e.Status == ResearchFailed || e.Status == ResearchCancelled)
}

// StreamResearchJobEvents follows a research job: it calls GET /research/job_events (Server-Sent Events) and calls fn with each event,
// from the one with index `from` on. The results are those of StreamTrainingJobEvents.
func (c *Client) StreamResearchJobEvents(ctx context.Context, jobID string, from int, fn func(ResearchEvent) error) error {
	return c.streamEvents(ctx, "/research/job_events", jobID, from, func(seq int, data []byte) error {
		var ev ResearchEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return fmt.Errorf("sidecar GET /research/job_events: decode event: %w", err)
		}
		ev.Seq = seq
		return fn(ev)
	})
}

// DownloadResearchExport streams the export file of a research job from the sidecar into w (GET /research/job_export): the archive
// (see internal/db/dbexport) that the agent wrote in its container when it ended. It returns the number of bytes written. A failure is
// a *StatusError (404 if the job has no export, 400 for a bad job ID) or a transport error; a body that ends early is an error too, but
// w may already have got part of the file, so the caller must not trust w after an error (check the size and SHA-256 of the event's
// ResearchExport as well).
func (c *Client) DownloadResearchExport(ctx context.Context, jobID string, w io.Writer) (int64, error) {
	if strings.TrimSpace(jobID) == "" {
		return 0, errors.New("sidecar: job_id is required")
	}
	const path = "/research/job_export"
	resp, err := c.Open(ctx, http.MethodGet, path, url.Values{"job_id": {jobID}}.Encode(), nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, &StatusError{Method: http.MethodGet, Path: path, Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("sidecar GET %s %s: reading the body: %w", path, jobID, err)
	}
	return n, nil
}
