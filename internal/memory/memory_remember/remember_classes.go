package memory_remember

// Graph classes (see docs/remember/remember.md#graph-classes): the classify_graph stage asks an LLM, for the graph of every artifact the call
// stored (the text, or each chunk of a document), which class or classes it belongs to, and stores each answer as a record of
// gb_artifact_classes that references the run (gb_inferences) of the model call, so that the configuration of the run (model, reasoning
// level, tokens, cost) can be fetched later. The classes are what the classifying training hook gets as the classes of the graphs.
// The graphs of the web search results and series observations the call makes are not classified.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"

	log "github.com/sirupsen/logrus"
)

// The most of a graph the LLM is shown: a larger graph is cut (its first entities and facts).
const (
	classifyMaxEntities = 150
	classifyMaxEdges    = 300
	// classifyKnownClasses is how many of the classes the dataset already uses the LLM is told of.
	classifyKnownClasses = 30
	classifyMaxTokens    = 256
)

// artifactGraph is the graph of one artifact the call stored: what the text (or the chunk) yielded, in its stored state.
type artifactGraph struct {
	ArtifactID int64
	// Label says which text it is of when the call has several ("chunk 2 of 5"); "" for the text of the call.
	Label    string
	Entities []Entity
	Edges    []Edge
}

// graphClassification is what the LLM answered for one graph.
type graphClassification struct {
	// Classes are the classes it chose, normalized (memory_core.NormalizeGraphClass), at most memory_core.MaxClassesPerGraph.
	Classes []string
	// Provider and Model name the model that answered; InferenceID is its run (gb_inferences), 0 if it could not be recorded.
	Provider, Model string
	InferenceID     int64
	Reasoning       string
	// Raw is the text the LLM answered, kept for the stage outputs; "" if it did not answer.
	Raw string
}

// ClassResult is a class stored for the graph of an artifact, as Remember reports it.
type ClassResult struct {
	ArtifactID  int64    `json:"artifact_id"`
	Classes     []string `json:"classes"`
	InferenceID int64    `json:"inference_id,omitempty"`
	Model       string   `json:"model,omitempty"`
	Reasoning   string   `json:"reasoning,omitempty"`
	// Error is why the artifact has no classes: the LLM call failed, its answer was no use, or storing it failed.
	Error string `json:"error,omitempty"`
}

// noteGraph keeps the graph of an artifact the call stored (res is what store returned for it), for classify_graph. An artifact that
// yielded no entity and no fact has no graph.
func (j *job) noteGraph(res *Result) {
	if !j.co.ClassifyGraph || len(res.Entities)+len(res.Edges) == 0 {
		return
	}
	label := ""
	if j.u != nil && j.u.part != nil {
		label = strings.Trim(j.u.part.label(), " ()")
	}
	j.graphs = append(j.graphs, artifactGraph{
		ArtifactID: res.ArtifactID,
		Label:      label,
		Entities:   res.Entities,
		Edges:      res.Edges,
	})
}

func (j *job) hasClassifyGraph() bool { return j.co.ClassifyGraph }

// classifyGraphStage is the classify_graph stage: the LLM classifies the graph of every artifact the call stored, and the classes are
// stored. The graphs are classified one after the other; a graph that cannot be (the call failed, no usable answer) does not stop
// the others, and the stage fails with the errors of those.
func (j *job) classifyGraphStage(ctx context.Context) error {
	if len(j.graphs) == 0 {
		return nil
	}
	dsID, err := memory_core.DatasetID(ctx,
		j.d,
		j.userID,
		j.dataset)
	if err != nil {
		return fmt.Errorf("memory: classify graph: %w", err)
	}
	var errs []error
	results := make([]ClassResult,
		0,
		len(j.graphs))
	for _, g := range j.graphs {
		r, err := j.classifyOne(ctx,
			dsID,
			g)
		results = append(results, r)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if j.res != nil {
		j.res.Classes = results
	}
	return errors.Join(errs...)
}

// classifyOne classifies one graph and stores its classes.
func (j *job) classifyOne(ctx context.Context,
	dsID int64,
	g artifactGraph) (ClassResult, error) {
	res := ClassResult{
		ArtifactID: g.ArtifactID,
		Classes:    []string{},
	}
	fields := log.Fields{"stage": memory_core.StageClassifyGraph, "user_id": j.userID, "dataset": j.dataset, "artifact_id": g.ArtifactID}

	var known []string
	if len(j.co.GraphClasses) == 0 { // free choice: tell the LLM what the dataset already uses, so that it reuses it
		var err error
		if known, err = graph.ClassNames(ctx,
			j.d,
			dsID,
			classifyKnownClasses); err != nil {
			log.WithError(err).WithFields(fields).Warn("memory: reading the classes of the dataset failed; the LLM is not told of them")
		}
	}
	c, err := classifyGraph(ctx,
		j.co.ModelLLM(j.lm, memory_core.StageClassifyGraph),
		g,
		j.co.GraphClasses,
		known)
	if c != nil {
		res.InferenceID, res.Model, res.Reasoning = c.InferenceID, c.Model, c.Reasoning
		j.run.Output(memory_core.StageClassifyGraph,
			"classify_graph",
			g.Label,
			map[string]any{"artifact_id": g.ArtifactID,
				"classes": c.Classes, "model": c.Model, "reasoning": c.Reasoning, "inference_id": c.InferenceID, "raw": c.Raw})
	}
	if err != nil {
		err = fmt.Errorf("memory: classify graph of artifact %d: %w",
			g.ArtifactID,
			err)
		res.Error = err.Error()
		return res, err
	}
	if _, err := graph.InsertArtifactClasses(ctx,
		j.d,
		dsID,
		g.ArtifactID,
		c.InferenceID,
		c.Classes,
		j.now); err != nil {
		err = fmt.Errorf("memory: store the classes of artifact %d: %w",
			g.ArtifactID,
			err)
		res.Error = err.Error()
		return res, err
	}
	res.Classes = c.Classes
	j.tr.Tool("The LLM classified the graph of the artifact; each class is stored as a record of its own (gb_artifact_classes), with the run of the call."+
		labelSuffix(g.Label),
		memory_core.StageClassifyGraph,
		map[string]any{"artifact_id": g.ArtifactID, "entities": len(g.Entities), "edges": len(g.Edges),
			"allowed": j.co.GraphClasses, "known": known},
		map[string]any{"classes": c.Classes, "model": c.Model, "reasoning": c.Reasoning, "inference_id": c.InferenceID})
	return res, nil
}

func labelSuffix(label string) string {
	if label == "" {
		return ""
	}
	return " (" + label + ")"
}

// classifyGraph asks the LLM for the classes of g. lm must be the one of the classify_graph stage (see
// memory_core.CallOptions.ModelLLM). allowed, if not empty, are the only classes it may choose (it is then not told of known); otherwise it
// chooses freely, and known are the classes the dataset already uses, which it should reuse when one fits. If the LLM answered but its
// answer is no use, the error comes with a classification that has only its InferenceID and Raw.
func classifyGraph(ctx context.Context,
	lm memory_core.LLM,
	g artifactGraph,
	allowed,
	known []string) (*graphClassification, error) {
	resp, err := lm.Complete(ctx, llm.Request{
		System: classifySystemPrompt,
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: classifyUserMessage(g,
				allowed,
				known),
		}},
		MaxTokens: classifyMaxTokens,
	})
	if err != nil {
		return nil, err
	}
	level, _ := llm.NormalizeReasoning(resp.Reasoning)
	c := &graphClassification{
		Provider:    resp.Provider,
		Model:       resp.Model,
		InferenceID: resp.InferenceID,
		Reasoning:   level,
		Raw:         resp.Text,
	}
	if resp.StopReason == "max_tokens" {
		return c, errors.New("memory: LLM class answer truncated")
	}
	classes, err := parseClasses(resp.Text, allowed)
	if err != nil {
		return c, err
	}
	c.Classes = classes
	return c, nil
}

// parseClasses reads the answer {"classes": ["a", "b"]}: the classes are normalized, repeats dropped, only the first
// memory_core.MaxClassesPerGraph kept (only the first if allowed is not empty), and, if allowed is not empty, those not in it left out. It is an error if none is left.
func parseClasses(answer string,
	allowed []string) ([]string, error) {
	js, err := memory_core.JSONObject(answer)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Classes []string `json:"classes"`
	}
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		return nil, fmt.Errorf("memory: decode LLM class answer: %w", err)
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range raw.Classes {
		n := memory_core.NormalizeGraphClass(c)
		if n == "" || seen[n] || (len(allowed) > 0 && !ok[n]) {
			continue
		}
		seen[n] = true
		out = append(out, n)
		// With a set to choose from the LLM is asked for the one closest class, so only the first is kept.
		if len(out) == memory_core.MaxClassesPerGraph || len(allowed) > 0 {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("memory: the LLM named no usable class")
	}
	return out, nil
}

// classifyUserMessage describes the graph (and the classes to choose from) for the LLM.
func classifyUserMessage(g artifactGraph,
	allowed,
	known []string) string {
	var b strings.Builder
	if len(allowed) > 0 {
		b.WriteString("<allowed_classes>\n" + strings.Join(allowed, "\n") + "\n</allowed_classes>\n\n")
	} else if len(known) > 0 {
		b.WriteString("<existing_classes>\n" + strings.Join(known, "\n") + "\n</existing_classes>\n\n")
	}
	b.WriteString("<graph>\nentities:\n")
	for i, e := range g.Entities {
		if i == classifyMaxEntities {
			fmt.Fprintf(&b,
				"... (%d more entities)\n",
				len(g.Entities)-i)
			break
		}
		fmt.Fprintf(&b,
			"- %s (%s)",
			e.Name,
			e.Type)
		if e.Description != "" {
			b.WriteString(": " + e.Description)
		}
		b.WriteByte('\n')
	}
	b.WriteString("facts:\n")
	for i, e := range g.Edges {
		if i == classifyMaxEdges {
			fmt.Fprintf(&b,
				"... (%d more facts)\n",
				len(g.Edges)-i)
			break
		}
		fmt.Fprintf(&b,
			"- %s --%s--> %s\n",
			e.Source,
			e.Relation,
			e.Target)
	}
	b.WriteString("</graph>")
	return b.String()
}
