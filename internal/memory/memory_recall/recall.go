// Package memory_recall answers a question from the knowledge graph of a dataset with an LLM.
package memory_recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// CallRecall is the type of the LLM calls of Recall, stored in gb_llm_calls.type.
const CallRecall = "recall"

const (
	maxQueryTokens  = 12
	maxSeedEntities = 10  // entities matched directly by the query
	maxContextEdges = 100 // edges (1 hop around the seeds) given to the LLM
	maxFallbackSeed = 25  // most recent entities used when nothing matches the query
)

//---------------------------------------------------------------------------------------------

// EvidenceEntity is a graph entity cited by an answer.
type EvidenceEntity = graph.EvidenceEntity

// EvidenceEdge is a graph edge cited by an answer, with the bi-temporal validity of the fact (see temporal.go).
type EvidenceEdge = graph.EvidenceEdge

// Evidence lists the entities and edges the answer is based on.
type Evidence struct {
	Entities []EvidenceEntity `json:"entities"`
	Edges    []EvidenceEdge   `json:"edges"`
}

// RecallResult is the answer to a query together with its supporting evidence.
type RecallResult struct {
	// SessionID is the session the call was made in (WithSessionID); omitted if there was none. It only labels the
	// call, so that a recall can be connected to the artifacts remembered in the same session: it does not change
	// what is retrieved or answered.
	SessionID string   `json:"session_id,omitempty"`
	Answer    string   `json:"answer"`
	Evidence  Evidence `json:"evidence"`
	// TrajectoryID and Trajectory are the ATIF trajectory of this call (also stored in TrajectoryDir); only set when
	// the call was made with WithEval.
	TrajectoryID string           `json:"trajectory_id,omitempty"`
	Trajectory   *atif.Trajectory `json:"trajectory,omitempty"`
}

//---------------------------------------------------------------------------------------------

// Recall answers query using the knowledge graph: relevant entities and their edges are retrieved,
// the LLM answers from that context, and the entities/edges it used are returned as evidence. Only the named
// dataset of userID (empty means DefaultDataset) is searched; ErrDatasetNotFound is returned if it does not exist.
//
// With WithSessionID the result carries that session ID, and so does the call's trajectory (as its session_id,
// the same one the trajectories of the session's Remember calls have). The session does not influence retrieval or
// the answer: Recall is the same function of the graph and the query with or without it.
//
// Only with WithEval is the call recorded as an ATIF trajectory (stored as JSON in TrajectoryDir and returned in
// RecallResult.Trajectory, with its ID in RecallResult.TrajectoryID).
func Recall(ctx context.Context, d *sql.DB, llm memory_core.LLM, userID, dataset, query string, opts ...memory_core.Option) (res *RecallResult, err error) {
	userID, err = memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	dataset, err = memory_core.ResolveDatasetName(dataset)
	if err != nil {
		return nil, err
	}
	dsID, err := memory_core.DatasetID(ctx, d, userID, dataset)
	if err != nil {
		return nil, err
	}

	co := memory_core.NewCallOptions(opts)
	sessionID, err := memory_core.ResolveSessionID(co.SessionID)
	if err != nil {
		return nil, err
	}
	co.SessionID = sessionID
	tr := memory_core.StartTrace(CallRecall, co)
	defer func() {
		if traj := tr.Finish(err); res != nil && traj != nil {
			res.TrajectoryID, res.Trajectory = traj.TrajectoryID, traj
		}
	}()
	tr.User(query)
	tr.Input(map[string]any{"user_id": userID, "dataset": dataset, "session_id": sessionID})

	rc, err := assembleContext(ctx, d, dsID, query)
	if err != nil {
		return nil, fmt.Errorf("retrieve context: %w", err)
	}
	tr.Tool("Retrieved the subgraph around the entities matching the query.", "retrieve_graph",
		map[string]any{"user_id": userID, "dataset": dataset, "query": query}, rc.Graph.render())
	if len(rc.Graph.entities) == 0 {
		const none = "I don't have any information in memory to answer that."
		tr.Agent(none)
		return &RecallResult{
			SessionID: sessionID,
			Answer:    none,
			Evidence:  Evidence{Entities: []EvidenceEntity{}, Edges: []EvidenceEdge{}},
		}, nil
	}

	out, err := recallLLM(ctx, tr.Wrap(memory_core.WithRecording(llm, d, CallRecall)), rc)
	if err != nil {
		return nil, err
	}
	return &RecallResult{SessionID: sessionID, Answer: out.Answer, Evidence: getEvidence(rc.Graph, out)}, nil
}

//---------------------------------------------------------------------------------------------

// recallContext is everything the recall LLM is given to answer a question: the memory (the retrieved subgraph),
// the current time (to interpret the validity of temporal facts) and the question itself.
type recallContext struct {
	Now      time.Time
	Graph    *graphContext
	Question string
}

// assembleContext builds the recallContext for query: it retrieves the subgraph of dataset dsID around the
// entities matching the query and stamps it with the current time.
func assembleContext(ctx context.Context, d *sql.DB, dsID int64, query string) (*recallContext, error) {
	g, err := retrieve(ctx, d, dsID, query)
	if err != nil {
		return nil, err
	}
	return &recallContext{Now: relational.Now(), Graph: g, Question: query}, nil
}

// render returns the user message sent to the LLM.
func (c *recallContext) render() string {
	return "<current_time>\n" + relational.FormatTime(c.Now) + "\n</current_time>\n\n" +
		"<graph>\n" + c.Graph.render() + "</graph>\n\n" +
		"<question>\n" + c.Question + "\n</question>"
}

// recallLLM asks the LLM to answer rc.Question from rc and parses its structured output.
func recallLLM(ctx context.Context, llm memory_core.LLM, rc *recallContext) (*recallOutput, error) {
	resp, err := llm.Complete(ctx, ai.Request{
		System:    recallSystemPrompt,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: rc.render()}},
		MaxTokens: 2048,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, errors.New("memory: LLM output truncated")
	}
	return parseRecall(resp.Text)
}

//---------------------------------------------------------------------------------------------

type recallOutput struct {
	Answer   string  `json:"answer"`
	Entities []int64 `json:"entities"`
	Edges    []int64 `json:"edges"`
}

func parseRecall(s string) (*recallOutput, error) {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return nil, errors.New("memory: no JSON object in LLM output")
	}
	var o recallOutput
	if err := json.Unmarshal([]byte(s[start:end+1]), &o); err != nil {
		return nil, fmt.Errorf("memory: decode LLM output: %w", err)
	}
	o.Answer = strings.TrimSpace(o.Answer)
	if o.Answer == "" {
		return nil, errors.New("memory: LLM returned an empty answer")
	}
	return &o, nil
}

//---------------------------------------------------------------------------------------------

// graphContext is the subgraph handed to the LLM.
type graphContext struct {
	entities map[int64]EvidenceEntity
	edges    map[int64]EvidenceEdge
}

func (g *graphContext) render() string {
	var sb strings.Builder
	for _, id := range sortedKeys(g.entities) {
		e := g.entities[id]
		fmt.Fprintf(&sb, "[entity %d] %s (%s)", e.ID, e.Name, e.Type)
		if e.Description != "" {
			sb.WriteString(": " + e.Description)
		}
		sb.WriteByte('\n')
	}
	for _, id := range sortedKeys(g.edges) {
		sb.WriteString(memory_core.FactFromEvidence(g.edges[id]).ContextLine() + "\n")
	}
	return sb.String()
}

// getEvidence returns the entities and edges the LLM cited in o, keeping only cited IDs that were present in the
// context. Endpoints of cited edges are included as cited entities.
func getEvidence(g *graphContext, o *recallOutput) Evidence {
	ev := Evidence{Entities: []EvidenceEntity{}, Edges: []EvidenceEdge{}}
	ents := map[int64]bool{}
	for _, id := range o.Entities {
		if _, ok := g.entities[id]; ok {
			ents[id] = true
		}
	}
	seen := map[int64]bool{}
	for _, id := range o.Edges {
		e, ok := g.edges[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		ev.Edges = append(ev.Edges, e)
		ents[e.SourceID], ents[e.TargetID] = true, true
	}
	sort.Slice(ev.Edges, func(i, j int) bool { return ev.Edges[i].ID < ev.Edges[j].ID })
	for _, id := range sortedKeys(g.entities) {
		if ents[id] {
			ev.Entities = append(ev.Entities, g.entities[id])
		}
	}
	return ev
}

func sortedKeys[V any](m map[int64]V) []int64 {
	ks := make([]int64, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}

//---------------------------------------------------------------------------------------------

// retrieve finds entities matching the query text, then adds the edges touching them and the entities
// at the other end. If no entity matches, the most recently updated entities are used instead.
func retrieve(ctx context.Context, d *sql.DB, dsID int64, query string) (*graphContext, error) {
	g := &graphContext{entities: map[int64]EvidenceEntity{}, edges: map[int64]EvidenceEdge{}}

	seeds, err := graph.MatchEntities(ctx, d, dsID, memory_core.Key(query), tokenize(query), maxSeedEntities)
	if err != nil {
		return nil, err
	}
	if len(seeds) == 0 {
		if seeds, err = graph.FallbackSeedEntities(ctx, d, dsID, maxFallbackSeed); err != nil {
			return nil, err
		}
	}
	if len(seeds) == 0 {
		return g, nil
	}

	edges, err := graph.EdgesAround(ctx, d, dsID, seeds, maxContextEdges)
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	for _, id := range seeds {
		ids[id] = true
	}
	for _, e := range edges {
		g.edges[e.ID] = e
		ids[e.SourceID], ids[e.TargetID] = true, true
	}

	all := make([]int64, 0, len(ids))
	for id := range ids {
		all = append(all, id)
	}
	ents, err := graph.EntitiesByID(ctx, d, all)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		g.entities[e.ID] = e
	}
	return g, nil
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be by did do does for from had has have how i in is it its me my of on or
		that the their them there they this to was we were what when where which who whom whose why will with you your about tell
		know can could would should been being into than then these those his her him she he us our`) {
		stopwords[w] = true
	}
}

// tokenize lowercases the query and returns up to maxQueryTokens distinct, non-trivial terms.
func tokenize(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) < 2 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) == maxQueryTokens {
			break
		}
	}
	return out
}
