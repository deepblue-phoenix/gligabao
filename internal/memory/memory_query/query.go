// Package memory_query answers a question from the knowledge graph of a dataset with an LLM.
package memory_query

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

	log "github.com/sirupsen/logrus"
)

// Evidence lists the entities, edges and passages the answer is based on.
type Evidence struct {
	Entities []EvidenceEntity  `json:"entities"`
	Edges    []EvidenceEdge    `json:"edges"`
	Passages []EvidencePassage `json:"passages"`
}

// QueryResult is the answer to a query together with its supporting evidence.
type QueryResult struct {
	// SessionID is the session the call was made in (WithSessionID); omitted if there was none. It only labels the
	// call, so that a query can be connected to the artifacts remembered in the same session: it does not change
	// what is retrieved or answered.
	SessionID string   `json:"session_id,omitempty"`
	Answer    string   `json:"answer"`
	Evidence  Evidence `json:"evidence"`
	// Geo are the places the question mentions; only set when the call has a geo resolver (WithGeoResolver). They are not part of
	// the JSON response (see Nearby).
	Geo []QueryPlace `json:"-"`
	// Nearby is the proximity search of the call (see query_geo_nearby.go): the places it was centered on, the radius and the geo
	// entities found within it, which the LLM was given as context. Only set when there was a place to search around.
	Nearby *GeoContext `json:"nearby,omitempty"`
	// TrajectoryID and Trajectory are the ATIF trajectory of this call (also stored in TrajectoryDir); only set when
	// the call was made with WithEval.
	TrajectoryID string           `json:"trajectory_id,omitempty"`
	Trajectory   *atif.Trajectory `json:"trajectory,omitempty"`
}

// queryContext is everything the query LLM is given to answer a question: the memory (the retrieved subgraph),
// the current time (to interpret the validity of temporal facts) and the question itself.
type queryContext struct {
	Now      time.Time
	Graph    *graphContext
	Question string
	// Nearby is the geo entities near the places of the question (nil: none, or the call has no geo resolver).
	Nearby *GeoContext
}

type queryOutput struct {
	Answer   string  `json:"answer"`
	Entities []int64 `json:"entities"`
	Edges    []int64 `json:"edges"`
	Passages []int64 `json:"passages"`
}

// graphContext is the subgraph handed to the LLM.
type graphContext struct {
	entities map[int64]EvidenceEntity
	edges    map[int64]EvidenceEdge
	// matched are the entities that matched the question, by its words or by similarity (the seeds of the retrieval); empty
	// when none did and the most recent entities were used instead.
	matched []int64
	// passages are the chunks of long texts that are close to the question (see query_vectors.go).
	passages []EvidencePassage
}

// CallQuery is the type of the inferences of Query, stored in gb_inferences.type.
const CallQuery = "query"

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

//---------------------------------------------------------------------------------------------

// Query answers query using the knowledge graph: relevant entities and their edges are retrieved,
// the LLM answers from that context, and the entities/edges it used are returned as evidence. Only the named
// dataset of userID (empty means DefaultDataset) is searched; ErrDatasetNotFound is returned if it does not exist.
//
// With WithSessionID the result carries that session ID, and so does the call's trajectory (as its session_id,
// the same one the trajectories of the session's Remember calls have). The session does not influence retrieval or
// the answer: Query is the same function of the graph and the query with or without it.
//
// The LLM call is recorded as an inference (gb_inferences) with the reasoning level none by default (see
// memory_core.DefaultQueryReasoning).
//
// Only with WithEval is the call recorded as an ATIF trajectory (stored as JSON in TrajectoryDir and returned in
// QueryResult.Trajectory, with its ID in QueryResult.TrajectoryID).
func Query(ctx context.Context, d *sql.DB, llm memory_core.LLM, userID, dataset, query string, opts ...memory_core.Option) (res *QueryResult, err error) {
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
	radiusKm, err := geoRadius(co)
	if err != nil {
		return nil, err
	}
	sessionID, err := memory_core.ResolveSessionID(co.SessionID)
	if err != nil {
		return nil, err
	}
	co.SessionID = sessionID
	tr := memory_core.StartTrace(CallQuery, co)
	defer func() {
		if traj := tr.Finish(err); res != nil && traj != nil {
			res.TrajectoryID, res.Trajectory = traj.TrajectoryID, traj
		}
	}()
	tr.User(query)
	tr.Input(map[string]any{"user_id": userID, "dataset": dataset, "session_id": sessionID})

	j := &job{d: d, llm: llm, tr: tr, co: co, userID: userID, dataset: dataset, dsID: dsID, sessionID: sessionID, radiusKm: radiusKm}
	return j.pipeline(ctx, query)
}

// job is the state of one Query call, after its setup (validation, options, trace).
type job struct {
	d         *sql.DB
	llm       memory_core.LLM // the caller's, not yet recording
	tr        *memory_core.Trace
	co        memory_core.CallOptions
	userID    string
	dataset   string // already resolved
	dsID      int64
	sessionID string
	radiusKm  float64 // the radius of the proximity search (see geoRadius)
}

// pipeline sequences the stages of a Query call on the job j: it retrieves the subgraph around the entities matching the
// query and the passages close to it (assembleContext); with no result it answers that it knows nothing, with no LLM call;
// otherwise it detects the places of the question and the geo entities near them (only with a geo resolver), and asks the
// LLM to answer from that context (queryLLM), whose citations become the evidence (getEvidence). Query does the setup and
// calls it.
func (j *job) pipeline(ctx context.Context, query string) (*QueryResult, error) {
	d, co, tr, userID, dataset, dsID, sessionID := j.d, j.co, j.tr, j.userID, j.dataset, j.dsID, j.sessionID

	//--------------------------------------
	// RETRIEVE
	rc, err := assembleContext(ctx, d, co, tr, dsID, query)
	if err != nil {
		return nil, fmt.Errorf("retrieve context: %w", err)
	}
	tr.Tool("Retrieved the subgraph around the entities matching the query, and the passages close to it.", "retrieve_graph",
		map[string]any{"user_id": userID, "dataset": dataset, "query": query}, rc.Graph.render()+renderPassages(rc.Graph.passages))
	if len(rc.Graph.entities) == 0 && len(rc.Graph.passages) == 0 {
		const none = "I don't have any information in memory to answer that."
		tr.Agent(none)
		return &QueryResult{
			SessionID: sessionID,
			Answer:    none,
			Evidence:  Evidence{Entities: []EvidenceEntity{}, Edges: []EvidenceEdge{}, Passages: []EvidencePassage{}},
		}, nil
	}

	// Every LLM call of the query is recorded as an inference (and a step of the trajectory); each stage runs on the model and
	// the reasoning level the call sets for it (WithModels, WithReasoning), or the default ones.
	rec := tr.Wrap(memory_core.WithRecording(j.llm, d, CallQuery))

	//--------------------------------------
	// DETECT_GEO and NEARBY_GEO (only with a geo resolver): the places the question mentions, and the geo entities near them
	// and near the entities that matched the question, which the answer gets as context.
	places := detectGeo(ctx, co, rec, tr, userID, dataset, query)
	nearby := nearbyGeo(ctx, d, co, tr, userID, dataset, dsID, rc, places, j.radiusKm)

	//--------------------------------------
	// ANSWER: thought through with the reasoning level none unless the call sets another (WithReasoning with
	// memory_core.StageQuery); the inference records it.
	out, err := queryLLM(ctx, co.ModelLLM(rec, memory_core.StageQuery), rc)
	if err != nil {
		return nil, err
	}
	return &QueryResult{SessionID: sessionID, Answer: out.Answer, Evidence: getEvidence(rc.Graph, out), Geo: places, Nearby: nearby}, nil
}

// detectGeo is the geo step of Query (see query_geo.go): it returns the places the question mentions, nil if the call has no geo
// resolver or the question mentions none. It never fails the call.
func detectGeo(ctx context.Context, co memory_core.CallOptions, rec memory_core.LLM, tr *memory_core.Trace, userID, dataset, question string) []QueryPlace {
	if co.GeoResolver == nil {
		return nil
	}
	places, err := detectPlaces(ctx, co.ModelLLM(rec, memory_core.StageDetectGeo), question)
	fields := log.Fields{"stage": memory_core.StageDetectGeo, "user_id": userID, "dataset": dataset}
	if err != nil {
		log.WithError(err).WithFields(fields).Warn("query: detecting the places in the question failed")
		tr.Tool("The LLM could not find the places the question mentions.", "detect_geo", map[string]any{"question": question}, map[string]any{"error": err.Error()})
		return nil
	}
	found := places
	if found == nil {
		found = []QueryPlace{}
	}
	tr.Tool("The LLM found the places the question mentions (cities, countries, regions, landmarks, ...).", "detect_geo",
		map[string]any{"question": question}, map[string]any{"places": found})
	return places
}

//---------------------------------------------------------------------------------------------

// assembleContext builds the queryContext for query: it retrieves the subgraph of dataset dsID around the
// entities matching the query, and the passages close to it, and stamps it with the current time.
func assembleContext(ctx context.Context, d *sql.DB, co memory_core.CallOptions, tr *memory_core.Trace, dsID int64, query string) (*queryContext, error) {
	g, err := retrieve(ctx, d, co, tr, dsID, query)
	if err != nil {
		return nil, err
	}
	return &queryContext{Now: relational.Now(), Graph: g, Question: query}, nil
}

// render returns the user message sent to the LLM.
func (c *queryContext) render() string {
	nearby := ""
	if c.Nearby != nil && len(c.Nearby.Nearby) > 0 {
		nearby = fmt.Sprintf("<nearby radius_km=\"%g\">\n%s</nearby>\n\n", c.Nearby.RadiusKm, c.Nearby.render())
	}
	passages := ""
	if len(c.Graph.passages) > 0 {
		passages = "<passages>\n" + renderPassages(c.Graph.passages) + "</passages>\n\n"
	}
	return "<current_time>\n" + relational.FormatTime(c.Now) + "\n</current_time>\n\n" +
		"<graph>\n" + c.Graph.render() + "</graph>\n\n" + passages + nearby +
		"<question>\n" + c.Question + "\n</question>"
}

// queryLLM asks the LLM to answer rc.Question from rc and parses its structured output.
func queryLLM(ctx context.Context, llm memory_core.LLM, rc *queryContext) (*queryOutput, error) {
	resp, err := llm.Complete(ctx, ai.Request{
		System:    querySystemPrompt,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: rc.render()}},
		MaxTokens: 2048,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, errors.New("memory: LLM output truncated")
	}
	return parseQuery(resp.Text)
}

//---------------------------------------------------------------------------------------------

func parseQuery(s string) (*queryOutput, error) {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return nil, errors.New("memory: no JSON object in LLM output")
	}
	var o queryOutput
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
func getEvidence(g *graphContext, o *queryOutput) Evidence {
	ev := Evidence{Entities: []EvidenceEntity{}, Edges: []EvidenceEdge{}, Passages: []EvidencePassage{}}
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
	cited := map[int64]bool{}
	for _, id := range o.Passages {
		cited[id] = true
	}
	for _, p := range g.passages {
		if cited[p.ArtifactID] {
			ev.Passages = append(ev.Passages, p)
		}
	}
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

// retrieve finds the seeds of the subgraph: the entities that match the words of the query and those whose embedding is
// close to the question (see query_vectors.go), and adds the edges touching them and the entities at the other end. The
// facts whose embedding is close to the question are added too, with their endpoints. If there is no seed and no such fact,
// the most recently updated entities are used instead. The passages close to the question are loaded as well.
func retrieve(ctx context.Context, d *sql.DB, co memory_core.CallOptions, tr *memory_core.Trace, dsID int64, query string) (*graphContext, error) {
	g := &graphContext{entities: map[int64]EvidenceEntity{}, edges: map[int64]EvidenceEdge{}}

	hits, err := searchVectors(ctx, d, co, tr, dsID, query)
	if err != nil {
		return nil, err
	}
	if g.passages, err = loadPassages(ctx, d, dsID, hits); err != nil {
		return nil, err
	}

	keyword, err := graph.MatchEntities(ctx, d, dsID, memory_core.Key(query), tokenize(query), maxSeedEntities)
	if err != nil {
		return nil, err
	}
	seeds := union(keyword, hits.entities)
	var hitEdges []EvidenceEdge
	if len(hits.edges) > 0 {
		if hitEdges, err = graph.EdgesByID(ctx, d, dsID, hits.edges); err != nil {
			return nil, err
		}
	}
	matched := len(seeds) > 0
	if !matched && len(hitEdges) == 0 {
		if seeds, err = graph.FallbackSeedEntities(ctx, d, dsID, maxFallbackSeed); err != nil {
			return nil, err
		}
	}
	if len(seeds) == 0 && len(hitEdges) == 0 {
		return g, nil
	}

	var edges []EvidenceEdge
	if len(seeds) > 0 {
		if edges, err = graph.EdgesAround(ctx, d, dsID, seeds, maxContextEdges); err != nil {
			return nil, err
		}
	}
	ids := map[int64]bool{}
	for _, id := range seeds {
		ids[id] = true
	}
	for _, e := range append(edges, hitEdges...) {
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
	if matched {
		for _, id := range seeds {
			if _, ok := g.entities[id]; ok { // a vector can outlive its entity
				g.matched = append(g.matched, id)
			}
		}
	}
	return g, nil
}

// union returns the IDs of a followed by those of b that are not in a, in order.
func union(a, b []int64) []int64 {
	seen := make(map[int64]bool, len(a)+len(b))
	out := make([]int64, 0, len(a)+len(b))
	for _, l := range [][]int64{a, b} {
		for _, id := range l {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
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
