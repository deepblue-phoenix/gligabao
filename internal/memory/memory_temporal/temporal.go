// Package memory_temporal is the bi-temporal side of the remember pipeline (see docs/temporal/temporal.md): what a new fact means
// for the facts already stored, and how it is written.
//
// Every edge carries two timelines:
//   - event time:  ValidAt / InvalidAt  - when the fact holds in the real world (InvalidAt nil = still true);
//   - system time: CreatedAt / ExpiredAt - when we learned about it / when newer knowledge superseded it.
//
// Pipeline (run by memory_remember, after Extract; Graphiti's entity edges are the model):
//  0. ApplyReference: an extracted edge without a start gets the time of the artifact, and its span is made consistent.
//  1. LoadCandidates + ResolveEdges: stored live edges touching the same entities (by name, and by similarity when
//     vectors are on) are compared with the new ones; exact repeats are found in Go, the LLM flags other duplicates and contradictions.
//  2. StoreEdges: duplicates are merged; for each contradiction the older fact is invalidated
//     (InvalidAt = start of the newer one, ExpiredAt = now) instead of being deleted. Everything it does to a fact is
//     also appended to the edge event log (gb_edge_events, see graph.EdgeEvent), which the admin UI plots.
package memory_temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// EdgePlan is the outcome of comparing new edges against existing ones.
type EdgePlan struct {
	Duplicates     map[int]int64 // index of new edge -> id of the existing edge it repeats
	Contradictions []Contradiction
	// InferenceID is the inference (gb_inferences) of the LLM call that compared the facts; 0 if there was none.
	InferenceID int64
	// Raw is the text the LLM answered, kept for the stage outputs (see runLog.Output); "" if it was not asked.
	Raw string
}

// Contradiction says that the existing edge can't hold together with the new edge.
type Contradiction struct {
	New      int
	Existing int64
}

// RunID is the inference of the comparison (0 for none, and for a nil plan).
func (p *EdgePlan) RunID() int64 {
	if p == nil {
		return 0
	}
	return p.InferenceID
}

//---------------------------------------------------------------------------------------------

// ResolveEdges compares new edges with existing candidates. Exact repeats (same endpoints and relation as a
// fact that is still true) are detected without the LLM; the LLM is only called for what remains. If the LLM answered but its
// answer is no use, the error comes with a plan that is empty but for its InferenceID.
//
// inContext, if not nil, is called right before the LLM call with the IDs of the stored edges (cands) that its prompt holds, so that
// they can be counted (see memory_core.CountInContext); it is not called when there is no LLM call.
func ResolveEdges(ctx context.Context, llm memory_core.LLM, edges, cands []graph.Edge, inContext func(edgeIDs []int64)) (*EdgePlan, error) {
	plan := &EdgePlan{Duplicates: map[int]int64{}}

	var rest []int // indices of new edges still undecided
	for i, e := range edges {
		dup := false
		for _, c := range cands {
			if c.InvalidAt == nil && memory_core.FactFromEdge(e).SameAs(memory_core.FactFromEdge(c)) {
				plan.Duplicates[i] = c.ID
				dup = true
				break
			}
		}
		if !dup {
			rest = append(rest, i)
		}
	}
	if len(rest) == 0 || len(cands) == 0 {
		return plan, nil
	}

	var b strings.Builder
	b.WriteString("<new_facts>\n")
	for _, i := range rest {
		f := memory_core.FactFromEdge(edges[i])
		fmt.Fprintf(&b, "[%d] %s %s\n", i, f.Statement, f.Span())
	}
	b.WriteString("</new_facts>\n\n<existing_facts>\n")
	known := map[int64]bool{}
	candIDs := make([]int64, 0, len(cands))
	for _, c := range cands {
		known[c.ID] = true
		candIDs = append(candIDs, c.ID)
		f := memory_core.FactFromEdge(c)
		fmt.Fprintf(&b, "[%d] %s %s\n", c.ID, f.Statement, f.Span())
	}
	b.WriteString("</existing_facts>")

	if inContext != nil {
		inContext(candIDs)
	}
	resp, err := llm.Complete(ctx, ai.Request{
		System:    resolveSystemPrompt,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: b.String()}},
		MaxTokens: 2048,
	})
	if err != nil {
		return nil, err
	}
	failed := &EdgePlan{InferenceID: resp.InferenceID, Raw: resp.Text}
	if resp.StopReason == "max_tokens" {
		return failed, errors.New("memory: LLM resolve output truncated")
	}
	js, err := memory_core.JSONObject(resp.Text)
	if err != nil {
		return failed, err
	}
	plan.InferenceID, plan.Raw = resp.InferenceID, resp.Text
	type pair struct {
		New      int   `json:"new"`
		Existing int64 `json:"existing"`
	}
	var out struct {
		Duplicates     []pair `json:"duplicates"`
		Contradictions []pair `json:"contradictions"`
	}
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return failed, fmt.Errorf("memory: decode LLM resolve output: %w", err)
	}

	pending := map[int]bool{}
	for _, i := range rest {
		pending[i] = true
	}
	// Hallucinated numbers/IDs are dropped by checking against what was actually shown.
	for _, p := range out.Duplicates {
		if pending[p.New] && known[p.Existing] {
			plan.Duplicates[p.New] = p.Existing
			delete(pending, p.New)
		}
	}
	seen := map[Contradiction]bool{}
	for _, p := range out.Contradictions {
		c := Contradiction{New: p.New, Existing: p.Existing}
		if pending[p.New] && known[p.Existing] && !seen[c] {
			seen[c] = true
			plan.Contradictions = append(plan.Contradictions, c)
		}
	}
	return plan, nil
}
