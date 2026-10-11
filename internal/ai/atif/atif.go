// Package atif generates agent traces in the Agent Trajectory Interchange Format (ATIF): a JSON document that
// describes an agent run as an ordered list of steps (system/user/agent messages, tool calls, observations and
// per-step LLM metrics).
//
// Use a Builder to record a run, then Build to get the Trajectory and WriteFile to store it as pretty-printed JSON.
package atif

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SchemaVersion is the ATIF version the generated trajectories conform to.
const SchemaVersion = "ATIF-v1.6"

// Step sources.
const (
	SourceSystem = "system"
	SourceUser   = "user"
	SourceAgent  = "agent"
)

//---------------------------------------------------------------------------------------------

// Trajectory is a complete ATIF document.
type Trajectory struct {
	SchemaVersion string         `json:"schema_version"`
	TrajectoryID  string         `json:"trajectory_id"`
	SessionID     string         `json:"session_id"`
	Agent         Agent          `json:"agent"`
	Steps         []Step         `json:"steps"`
	Notes         string         `json:"notes,omitempty"`
	FinalMetrics  *FinalMetrics  `json:"final_metrics,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

// Agent describes the agent that produced the trajectory.
type Agent struct {
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	ModelName string         `json:"model_name,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// Step is one turn of the trajectory. StepID starts at 1 and increases by one.
type Step struct {
	StepID           int            `json:"step_id"`
	Timestamp        string         `json:"timestamp,omitempty"` // RFC 3339
	Source           string         `json:"source"`
	ModelName        string         `json:"model_name,omitempty"` // agent steps only
	Message          string         `json:"message"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall     `json:"tool_calls,omitempty"` // agent steps only
	Observation      *Observation   `json:"observation,omitempty"`
	Metrics          *Metrics       `json:"metrics,omitempty"` // agent steps only
	Extra            map[string]any `json:"extra,omitempty"`
}

// ToolCall is a function the agent invoked.
type ToolCall struct {
	ToolCallID   string         `json:"tool_call_id"`
	FunctionName string         `json:"function_name"`
	Arguments    map[string]any `json:"arguments"`
}

// Observation holds the results of a step's tool calls.
type Observation struct {
	Results []ObservationResult `json:"results"`
}

// ObservationResult is the result of one tool call (SourceCallID is the ToolCall.ToolCallID).
type ObservationResult struct {
	SourceCallID string `json:"source_call_id,omitempty"`
	Content      string `json:"content"`
}

// Metrics are the LLM usage of one agent step. PromptTokens includes cached tokens.
type Metrics struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
}

// FinalMetrics are the totals over a whole trajectory.
type FinalMetrics struct {
	TotalPromptTokens     int     `json:"total_prompt_tokens"`
	TotalCompletionTokens int     `json:"total_completion_tokens"`
	TotalCachedTokens     int     `json:"total_cached_tokens,omitempty"`
	TotalCostUSD          float64 `json:"total_cost_usd"`
	TotalSteps            int     `json:"total_steps"`
}

//---------------------------------------------------------------------------------------------

// Builder records a trajectory step by step. It is safe for concurrent use.
type Builder struct {
	mu  sync.Mutex
	now func() time.Time
	t   Trajectory
}

// NewBuilder starts a trajectory with a fresh trajectory ID (also used as the session ID).
func NewBuilder(agent Agent) *Builder {
	id := NewID()
	return &Builder{
		now: time.Now,
		t: Trajectory{
			SchemaVersion: SchemaVersion,
			TrajectoryID:  id,
			SessionID:     id,
			Agent:         agent,
			Steps:         []Step{},
		},
	}
}

// ID returns the trajectory ID.
func (b *Builder) ID() string { return b.t.TrajectoryID }

// SetNotes sets free-form notes on the trajectory (e.g. the error that ended the run).
func (b *Builder) SetNotes(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.t.Notes = s
}

// SetExtra sets a custom key on the trajectory's extra field.
func (b *Builder) SetExtra(key string, v any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.t.Extra == nil {
		b.t.Extra = map[string]any{}
	}
	b.t.Extra[key] = v
}

// System appends a system-prompt step.
func (b *Builder) System(message string) { b.add(Step{Source: SourceSystem, Message: message}) }

// User appends a user-message step.
func (b *Builder) User(message string) { b.add(Step{Source: SourceUser, Message: message}) }

// Agent appends an agent step (an LLM response, optionally with tool calls and their observation).
func (b *Builder) Agent(s Step) {
	s.Source = SourceAgent
	b.add(s)
}

// Tool appends an agent step that calls one tool and records its result. The tool call ID is generated.
func (b *Builder) Tool(message, name string, args map[string]any, result string) {
	if args == nil {
		args = map[string]any{}
	}
	b.mu.Lock()
	callID := fmt.Sprintf("call_%d", len(b.t.Steps)+1)
	b.mu.Unlock()
	b.Agent(Step{
		Message:     message,
		ToolCalls:   []ToolCall{{ToolCallID: callID, FunctionName: name, Arguments: args}},
		Observation: &Observation{Results: []ObservationResult{{SourceCallID: callID, Content: result}}},
	})
}

func (b *Builder) add(s Step) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s.StepID = len(b.t.Steps) + 1
	if s.Timestamp == "" {
		s.Timestamp = b.now().UTC().Format(time.RFC3339Nano)
	}
	b.t.Steps = append(b.t.Steps, s)
}

// Build returns a snapshot of the trajectory with final metrics computed from the agent steps.
func (b *Builder) Build() *Trajectory {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.t
	t.Steps = append([]Step(nil), b.t.Steps...)
	fm := &FinalMetrics{TotalSteps: len(t.Steps)}
	for _, s := range t.Steps {
		if m := s.Metrics; m != nil {
			fm.TotalPromptTokens += m.PromptTokens
			fm.TotalCompletionTokens += m.CompletionTokens
			fm.TotalCachedTokens += m.CachedTokens
			fm.TotalCostUSD += m.CostUSD
		}
	}
	t.FinalMetrics = fm
	return &t
}

//---------------------------------------------------------------------------------------------

// NewID returns a random UUID (v4) string.
func NewID() string {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		panic("atif: crypto/rand: " + err.Error())
	}
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:])
}

// Marshal encodes t as pretty-printed JSON.
func Marshal(t *Trajectory) ([]byte, error) {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WriteFile stores t as pretty-printed JSON in dir (created if missing) and returns the file path,
// which is <dir>/<trajectory_id>.json.
func WriteFile(dir string, t *Trajectory) (string, error) {
	data, err := Marshal(t)
	if err != nil {
		return "", fmt.Errorf("atif: marshal: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("atif: create dir: %w", err)
	}
	path := filepath.Join(dir, t.TrajectoryID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("atif: write: %w", err)
	}
	return path, nil
}
