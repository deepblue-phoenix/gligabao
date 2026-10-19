// Package model holds the types shared by package relational and its backends (sqlite, postgres): the records they
// read and write. It has no dependencies so that every backend can import it. Graph records live in
// internal/db/graph/model.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Errors returned by the backends.
var (
	// ErrNotFound means the requested row does not exist (or is not visible, e.g. soft-deleted).
	ErrNotFound = errors.New("not found")
	// ErrExists means a live row with that name already exists.
	ErrExists = errors.New("already exists")
)

//---------------------------------------------------------------------------------------------

// Dataset describes a dataset.
type Dataset struct {
	ID          int64      `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Tags        []string   `json:"tags"`
	CreatedAt   time.Time  `json:"created_at"`
	Deleted     bool       `json:"deleted"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

// Ontology is the metadata of a loaded ontology. The ontology itself (classes and relationships) is stored in the
// graph database (see internal/db/graph).
type Ontology struct {
	ID          int64     `json:"id"`
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	IRI         string    `json:"iri,omitempty"`   // the owl:Ontology IRI declared by the document
	Title       string    `json:"title,omitempty"` // its label
	Format      string    `json:"format"`          // serialization of the loaded document, e.g. "rdfxml"
	SizeBytes   int       `json:"size_bytes"`      // size of the loaded document
	SHA256      string    `json:"sha256"`          // hex digest of the loaded document
	Classes     int       `json:"classes"`
	Properties  int       `json:"properties"` // distinct properties that became edges
	Edges       int       `json:"edges"`      // relationships between classes, subclass_of ones included
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"` // when the ontology was last loaded or replaced; equals created_at until updated
}

// Inference is one invocation of a model (gb_inferences): the dynamic counterpart of Model, which is the static
// description of a model. Every LLM call, embedding request, web search and Jev classification the system makes is
// recorded as one, with what that invocation used and cost.
type Inference struct {
	ID int64 `json:"id"`
	// Type is the operation that made the call: "remember", "remember_image" or "recall".
	Type string `json:"type"`
	// Stage is the pipeline stage that made it (e.g. "extract_graph", "summarize_chunk", "recall", "embed"); "" if unknown.
	Stage string `json:"stage,omitempty"`
	// ModelID is the ID (gb_models) of the model that was run, "" if the API did not say. On RecordInference it is filled
	// in from Model.
	ModelID string `json:"model_id,omitempty"`
	// Model, Provider and Kind describe the model. RecordInference uses them to register it if it is not stored yet (Kind
	// "" means "llm"); reading a run fills them from gb_models.
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Kind     string `json:"kind,omitempty"`
	// Reasoning is the thinking level the model ran with: "none", "low", "medium" or "high". "" is stored as "none".
	Reasoning    string    `json:"reasoning"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"` // includes the thinking tokens
	CostUSD      float64   `json:"cost_usd"`      // 0 if the model's price is unknown
	CreatedAt    time.Time `json:"created_at"`
}

// InferenceStat sums the recorded inferences of one type.
type InferenceStat struct {
	Inferences   int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// InferenceFilter selects inferences; an empty field matches all. ModelID is gb_models.id.
type InferenceFilter struct {
	Type      string
	Stage     string
	ModelID   string
	Reasoning string
}

// InferenceGroup sums the inferences that share a type, a stage, a model and a thinking level. Model is the model's name
// and ModelID its ID, "" for runs without one.
type InferenceGroup struct {
	Type         string  `json:"type"`
	Stage        string  `json:"stage,omitempty"`
	ModelID      string  `json:"model_id,omitempty"`
	Model        string  `json:"model,omitempty"`
	Reasoning    string  `json:"reasoning"`
	Inferences   int64   `json:"inferences"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

//---------------------------------------------------------------------------------------------

// Model is an AI model (gb_models). Entities and edges of the graph reference the model that inferred them by ID.
type Model struct {
	// ID is derived from Name alone (see ModelID), so the same model has the same ID in every database.
	ID string `json:"id"`
	// Name is the model name as the provider's API reports it (or, for a supported family priced by prefix, the
	// price-table key, e.g. "claude-haiku-4-5").
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"` // "anthropic", "openai", ... ("" if unknown)
	Kind     string `json:"kind"`               // "llm" or "embedding"
	// Supported is true for the models gligabao supports at its last start (they are re-inserted on every startup),
	// false for a model that was only seen in an API response (e.g. a dated snapshot of a supported family).
	Supported bool      `json:"supported"`
	CreatedAt time.Time `json:"created_at"`
}

// ModelID returns the ID of the model called name: the first 8 bytes of the SHA-256 of the name (trimmed and
// lowercased), in hex. It is deterministic, so every database gives a model the same ID, and "" for an empty name.
func ModelID(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:8])
}
