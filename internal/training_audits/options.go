package training_audits

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/training"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
)

const (
	defaultMaxTokens = 1024
	// defaultTemperature is low: an audit checks what the model learned, not how varied it can be.
	defaultTemperature = 0.1
)

// Options are the fields of the body of the request that starts an audit.
type Options struct {
	// Limit asks only the first n examples of the dataset (0 = all of them).
	Limit int `json:"limit"`
	// BatchSize is the number of question/answer pairs per judge call (default DefaultBatchSize, at most MaxBatchSize).
	BatchSize int `json:"batch_size"`
	// JudgeModel is the LLM model that judges (default: the LLM's default).
	JudgeModel string `json:"judge_model"`
	// MaxTokens and Temperature are those of the model's answers (default 1024 and 0.1).
	MaxTokens   int      `json:"max_tokens"`
	Temperature *float64 `json:"temperature"`
}

// RunParams are the settings an audit was started with: the Params of its AuditRun.
type RunParams struct {
	BatchSize   int     `json:"batch_size"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
	// Limit is the number of questions asked: the first ones of the dataset (0 = all of them).
	Limit int `json:"limit"`
	// JudgeModel is the model asked to judge ("" = the LLM's default); the model that did is AuditStats.JudgeModel.
	JudgeModel string `json:"judge_model,omitempty"`
}

// Params is p as the core.Params of an AuditRun.
func (p RunParams) Params() core.Params {
	b, _ := json.Marshal(p)
	var out core.Params
	_ = json.Unmarshal(b, &out)
	return out
}

// Resolve checks the options and returns the engine's config and the params to store. The error is safe to show.
func (o Options) Resolve() (Config, RunParams, error) {
	cfg, err := Config{BatchSize: o.BatchSize, JudgeModel: o.JudgeModel}.Normalize()
	switch {
	case err != nil:
		return cfg, RunParams{}, err
	case o.Limit < 0:
		return cfg, RunParams{}, errors.New("limit must be 0 (all) or more")
	case o.MaxTokens < 0 || o.MaxTokens > training.MaxInferTokens:
		return cfg, RunParams{}, errors.New("max_tokens must be between 1 and " + strconv.Itoa(training.MaxInferTokens))
	case o.Temperature != nil && (*o.Temperature <= 0 || *o.Temperature > 2):
		return cfg, RunParams{}, errors.New("temperature must be above 0 and at most 2")
	}
	p := RunParams{BatchSize: cfg.BatchSize, MaxTokens: o.MaxTokens, Temperature: defaultTemperature, Limit: o.Limit, JudgeModel: cfg.JudgeModel}
	if p.MaxTokens == 0 {
		p.MaxTokens = defaultMaxTokens
	}
	if o.Temperature != nil {
		p.Temperature = *o.Temperature
	}
	return cfg, p, nil
}
