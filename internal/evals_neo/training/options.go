package training

import (
	"errors"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
	trainingcore "github.com/deepblue-phoenix/gligabao/internal/training"
)

const (
	defaultMaxTokens = 1024
	// defaultTemperature is low: an eval checks what the model learned, not how varied it can be.
	defaultTemperature = 0.1

	// MaxExperimentName is the longest name of an experiment that an eval is started in.
	MaxExperimentName = 120
)

// Options are the fields of the body of the request that starts an eval.
type Options struct {
	// Eval is the eval to run (default: the one named like the model's training dataset). Cases, if any, are the names of the
	// cases to run (default: all of the eval's).
	Eval  string   `json:"eval"`
	Cases []string `json:"cases"`
	// Limit asks only the first n cases (0 = all of them).
	Limit int `json:"limit"`
	// BatchSize is the number of question/answer pairs per judge call (default DefaultBatchSize, at most MaxBatchSize).
	BatchSize int `json:"batch_size"`
	// JudgeModel is the LLM model that judges (default: the LLM's default).
	JudgeModel string `json:"judge_model"`
	// MaxTokens and Temperature are those of the model's answers (default 1024 and 0.1).
	MaxTokens   int      `json:"max_tokens"`
	Temperature *float64 `json:"temperature"`

	// Experiment makes the eval a run of the experiment of that name (created if needed); ExperimentDescription and
	// ExperimentConfig (the settings under test) are stored when it is created.
	Experiment            string         `json:"experiment"`
	ExperimentDescription string         `json:"experiment_description"`
	ExperimentConfig      map[string]any `json:"experiment_config"`
}

// RunParams are the settings an eval was started with: the Params of its eval run.
type RunParams struct {
	BatchSize   int     `json:"batch_size"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
	// Limit is the number of questions asked: the first ones of the eval (0 = all of them).
	Limit int `json:"limit"`
	// Cases are the names of the eval's cases that were asked, when only some were.
	Cases []string `json:"cases,omitempty"`
	// JudgeModel is the model asked to judge ("" = the LLM's default); the model that did is TrainingEvalRunMeta.JudgeModel.
	JudgeModel string `json:"judge_model,omitempty"`
}

// Params is p as the core.Params of an eval run.
func (p RunParams) Params() core.Params {
	out := core.Params{"batch_size": p.BatchSize, "max_tokens": p.MaxTokens, "temperature": p.Temperature, "limit": p.Limit}
	if len(p.Cases) > 0 {
		out["cases"] = p.Cases
	}
	if p.JudgeModel != "" {
		out["judge_model"] = p.JudgeModel
	}
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
	case o.MaxTokens < 0 || o.MaxTokens > trainingcore.MaxInferTokens:
		return cfg, RunParams{}, errors.New("max_tokens must be between 1 and " + strconv.Itoa(trainingcore.MaxInferTokens))
	case o.Temperature != nil && (*o.Temperature <= 0 || *o.Temperature > 2):
		return cfg, RunParams{}, errors.New("temperature must be above 0 and at most 2")
	case len(strings.TrimSpace(o.Experiment)) > MaxExperimentName:
		return cfg, RunParams{}, errors.New("experiment must be at most " + strconv.Itoa(MaxExperimentName) + " characters")
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
