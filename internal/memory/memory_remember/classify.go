package memory_remember

import (
	"context"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/jev"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"

	log "github.com/sirupsen/logrus"
)

// StageClassifyComplexity is the name of the step in the trajectory (and the log) of the Jev classification of the
// incoming text. Unlike the LLM stages of memory_core.Stages it takes no model: it always runs on Jev.
const StageClassifyComplexity = "classify_complexity"

// classifyComplexity is the first stage of Remember: Jev, the system 1 model, judges whether the text is simple, medium
// or complex in the number of entities it will yield, a prediction of the compute the later stages will need per entity
// and edge. For now the result is only logged (and recorded in the trajectory, with the latency of the call).
//
// It runs only if Jev is configured (memory_core.WithJev). It is advisory: a failure is logged and recorded, and never
// fails Remember.
func (j *job) classifyComplexity(ctx context.Context, text string) {
	if j.co.Jev == nil {
		return
	}
	tokens := chunk.EstimateTokens(text)
	args := map[string]any{"text_chars": len(text), "text_tokens": tokens}
	fields := log.Fields{"stage": StageClassifyComplexity, "user_id": j.userID, "dataset": j.dataset, "text_chars": len(text), "text_tokens": tokens}

	cl, call, err := j.co.Jev.Classify(ctx, text)
	fields["latency_ms"] = call.Latency.Milliseconds()
	// The call is a model run whether or not it worked out; a failed one has no usage and no model reported.
	run := relational.ModelRun{Type: CallRemember, Stage: StageClassifyComplexity, Provider: jev.Provider, Reasoning: ai.ReasoningNone}
	if err == nil {
		run.Model, run.InputTokens, run.OutputTokens = cl.Model, cl.InputTokens, cl.OutputTokens
	}
	if _, rerr := memory_core.RecordRun(context.WithoutCancel(ctx), j.d, run); rerr != nil {
		log.WithError(rerr).WithFields(fields).Error("record jev model run")
	}
	if err != nil {
		log.WithError(err).WithFields(fields).Warn("jev: classify complexity failed")
		j.tr.TimedTool("Jev (system 1) was asked to classify the text as simple, medium or complex in the number of entities it will yield; the call failed.",
			StageClassifyComplexity, args, map[string]any{"error": err.Error()},
			memory_core.ToolTiming{Provider: jev.Provider, Started: call.Started, Latency: call.Latency})
		return
	}
	fields["complexity"], fields["confidence"], fields["truncated"], fields["model"] = cl.Complexity, cl.Confidence, cl.Truncated, cl.Model
	log.WithFields(fields).Info("jev: remember text complexity")
	j.tr.TimedTool("Jev (system 1) classified the text as simple, medium or complex in the number of entities it will yield, "+
		"to predict the compute per entity and edge.", StageClassifyComplexity, args,
		map[string]any{"complexity": cl.Complexity, "confidence": cl.Confidence, "probabilities": cl.Probabilities, "truncated": cl.Truncated},
		memory_core.ToolTiming{Model: cl.Model, Provider: jev.Provider, Started: call.Started, Latency: call.Latency,
			PromptTokens: cl.InputTokens, CompletionTokens: cl.OutputTokens})
}
