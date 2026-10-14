package memory_remember

// Web search for Remember (see docs/web-search.md). It is the last stage of a Remember call that was made with
// memory_core.WithWebSearch(true), and it only exists when an Anthropic API key is supplied (memory_core.WithWebSearcher):
// the searches are made by Anthropic's cloud-based web search tool, through the same client as the LLM calls.
//
// What it does, after the text (or every chunk of a long text) has been extracted and stored as usual:
//
//  1. It picks the KEY ENTITIES of the call: the entities the extraction found, ranked by how many facts (edges) they
//     are in, at most maxWebSearchEntities of them (see pickKeyEntities).
//  2. For each one it searches the web for the latest world events about it (one call of the web search tool each, in
//     parallel). The answer is a short, dated briefing.
//  3. The briefing is processed like any other text: the graph is extracted from it, reconciled with the stored facts
//     (so news can close an outdated fact) and stored, with the entities and edges it yields, which are nodes like all
//     the others (they are linked to the briefing's artifact, can be embedded, and are found by recall).
//  4. The briefing is stored as an ARTIFACT OF ITS OWN, of kind "web_search" (graph.ArtifactWebSearch), that saves the
//     query that was issued (gb_artifacts.query) and has as its PARENT (gb_artifacts.parent_id) the artifact the entity
//     came from: the artifact (for a long text, the chunk) in which the entity was extracted. So the provenance chain
//     runs: a fact from the web -> the web_search artifact -> the artifact whose entity was searched for.
//
// The main text has been stored before any of this starts, and the web search is an enrichment: a failure in it (the
// search, the extraction or the storage of one result) is logged, recorded in the trajectory and in the result, and never
// fails Remember or removes what was already stored.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"

	log "github.com/sirupsen/logrus"
)

const (
	// maxWebSearchEntities is the most entities one Remember call searches the web for, however many it extracts: every
	// search costs money (a fee per search plus the tokens) and time.
	maxWebSearchEntities = 3
	// webSearchMaxUses is the most searches the model may make for one entity.
	webSearchMaxUses = 3
	// webSearchMaxTokens bounds the briefing. Together with the 300 words the prompt asks for, it keeps a result inside
	// one chunk (see docs/chunking.md): a web search result is never chunked.
	webSearchMaxTokens = 1500
	// noWebResults is what the model answers when it finds nothing relevant.
	noWebResults = "NO_RESULTS"
)

// notSearchable are the entity types that have no world events to search for.
var notSearchable = map[string]bool{"concept": true}

const webSearchSystemPrompt = `You research the latest world events involving one entity, using web search.

The entity is given in <entity>; <query> says what to look for and <today> is the current date. Search the web for the most recent, significant news about the entity (for example announcements, deals, leadership changes, legal or regulatory actions, financial results, product launches, conflicts, disasters). Prefer reliable sources and the last few months.

Return ONLY a plain-text briefing: no headings, no markdown, no preamble, no mention of searching, at most 300 words, as short self-contained factual sentences. Every sentence must say WHEN it happened (a date, month or year, as the sources state it) and name the entity and the other people, organizations and places involved by their full names, never "it" or "the company". Only state what the search results say; never guess.

If you find nothing relevant and recent, return exactly: ` + noWebResults

//---------------------------------------------------------------------------------------------

// webPart says that the text stored by store is the result of a web search, and not text the caller sent: it is stored as
// an artifact of kind "web_search" that records the query and hangs off the artifact the searched entity came from.
type webPart struct {
	// ParentID is the artifact from which the searched entity came.
	ParentID int64
	// Query is the web search query that was issued.
	Query string
	// Entity is the name of the entity that was searched for (only used to label the trajectory).
	Entity string
}

// label is appended to the messages of the trajectory steps of a web search result; "" for any other text (or nil).
func (w *webPart) label() string {
	if w == nil {
		return ""
	}
	return fmt.Sprintf(" (web search for %q)", w.Entity)
}

// keyEntity is an entity of the call that may be searched for.
type keyEntity struct {
	Entity
	// ArtifactID is the artifact from which the entity came: the first one (in order) in which it was extracted. For a
	// long text that is a chunk, not the parent.
	ArtifactID int64
	// Edges is how many facts of the call the entity is an endpoint of (summed over the chunks of a long text).
	Edges int
}

// noteKeyEntities records the entities of ex, the extraction of the artifact artifactID, as candidates for the web
// search. It is called for every text the call stores (the text itself, or each chunk) and does nothing unless the call
// searches the web or fetches series (see remember_seriesfetch.go). The entities of a web search result are never recorded: the search does not recurse.
func (j *job) noteKeyEntities(ex *Extraction, artifactID int64) {
	if !(j.co.WebSearch || j.co.SeriesFetch != nil) || j.web != nil || j.obs != nil {
		return
	}
	degree := map[string]int{}
	for _, e := range ex.Edges {
		degree[memory_core.Key(e.Source)]++
		if memory_core.Key(e.Target) != memory_core.Key(e.Source) {
			degree[memory_core.Key(e.Target)]++
		}
	}
	if j.keyEntities == nil {
		j.keyEntities = map[string]*keyEntity{}
	}
	for _, e := range ex.Entities {
		e.Name, e.Type, e.Description = strings.TrimSpace(e.Name), normType(e.Type), strings.TrimSpace(e.Description)
		k := memory_core.Key(e.Name)
		if k == "" {
			continue
		}
		if have, ok := j.keyEntities[k]; ok {
			have.Edges += degree[k]
			if have.Description == "" {
				have.Description = e.Description
			}
			continue
		}
		j.keyEntities[k] = &keyEntity{Entity: e, ArtifactID: artifactID, Edges: degree[k]}
		j.keyOrder = append(j.keyOrder, k)
	}
}

// pickKeyEntities returns the entities to search the web for: those of a type that has world events (not a concept),
// the ones in the most facts first (the entities the text is about; the order of extraction breaks ties), at most max.
func pickKeyEntities(all []*keyEntity, max int) []*keyEntity {
	var out []*keyEntity
	for _, e := range all { // all is in order of extraction
		if !notSearchable[e.Type] {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Edges > out[b].Edges })
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// webSearchQuery is the query issued for an entity.
func webSearchQuery(e Entity) string {
	return fmt.Sprintf("latest world events and news about %s (%s)", e.Name, e.Type)
}

// WebSearchResult is the web search made for one key entity of a Remember call.
type WebSearchResult struct {
	// Entity and EntityType are the key entity that was searched for; Query is the web search query that was issued
	// for it (also saved on the artifact).
	Entity     string `json:"entity"`
	EntityType string `json:"entity_type"`
	Query      string `json:"query"`
	// ParentArtifactID is the artifact from which the entity came (a chunk, for a long text): the parent of the
	// web_search artifact.
	ParentArtifactID int64 `json:"parent_artifact_id"`
	// ArtifactID is the web_search artifact that holds the result; 0 if nothing was stored (the search found nothing, or
	// failed: see Error).
	ArtifactID int64 `json:"artifact_id,omitempty"`
	// Searches are the queries the model sent to the search engine; Sources the pages its answer is based on.
	Searches []string       `json:"searches,omitempty"`
	Sources  []ai.WebSource `json:"sources,omitempty"`
	// Entities, Edges and InvalidatedEdges are what the result's text yielded, as for the artifact of a text (Result):
	// the entities discovered on the web are stored like any other.
	Entities         []Entity `json:"entities,omitempty"`
	Edges            []Edge   `json:"edges,omitempty"`
	InvalidatedEdges []int64  `json:"invalidated_edges,omitempty"`
	// CostUSD is the cost of the search itself (tokens and the fee per search), not of extracting its result.
	CostUSD float64 `json:"cost_usd,omitempty"`
	// Error says why nothing was stored for the entity; "" if all went well, or if the search simply found nothing.
	Error string `json:"error,omitempty"`
}

// webSearched is the outcome of the call to the search tool for one entity.
type webSearched struct {
	resp    *ai.WebSearchResponse
	err     error
	started time.Time
	latency time.Duration
}

// webSearch is the last stage of Remember: it searches the web for the key entities of the call and stores what it
// finds (see the top of this file). It returns one result per entity searched for; nil if the call does not search the
// web or has nothing to search for. It never fails the call.
func (j *job) webSearch(ctx context.Context) []WebSearchResult {
	if !j.co.WebSearch {
		return nil
	}
	all := make([]*keyEntity, 0, len(j.keyOrder))
	for _, k := range j.keyOrder {
		all = append(all, j.keyEntities[k])
	}
	picks := pickKeyEntities(all, maxWebSearchEntities)
	names := make([]string, 0, len(picks))
	for _, p := range picks {
		names = append(names, p.Name)
	}
	j.tr.Tool("Picked the key entities to search the web for: the extracted entities with the most facts (not concepts), at most "+
		fmt.Sprint(maxWebSearchEntities)+".", "select_web_search_entities",
		map[string]any{"entities_extracted": len(all), "max": maxWebSearchEntities},
		map[string]any{"entities": names})
	if len(picks) == 0 {
		return nil
	}

	// SEARCH, all entities at once: the calls are independent and slow.
	done := make([]webSearched, len(picks))
	var wg sync.WaitGroup
	for i, p := range picks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := &done[i]
			d.started = time.Now()
			d.resp, d.err = j.co.WebSearcher.WebSearch(ctx, ai.WebSearchRequest{
				System:    webSearchSystemPrompt,
				Prompt:    webSearchPrompt(p.Entity, j.now),
				Model:     j.co.Models[memory_core.StageWebSearch],
				MaxTokens: webSearchMaxTokens,
				MaxUses:   webSearchMaxUses,
			})
			d.latency = time.Since(d.started)
		}()
	}
	wg.Wait()

	// STORE, one entity at a time (the extraction and the storage use the database and the trajectory in order).
	results := make([]WebSearchResult, 0, len(picks))
	for i, p := range picks {
		results = append(results, j.storeWebSearch(ctx, p, done[i]))
	}
	return results
}

// webSearchPrompt is the user turn of the search for entity e.
func webSearchPrompt(e Entity, today time.Time) string {
	desc := e.Description
	if desc == "" {
		desc = "(none)"
	}
	return fmt.Sprintf("<today>%s</today>\n<query>%s</query>\n<entity>\nname: %s\ntype: %s\ndescription: %s\n</entity>",
		today.UTC().Format("2006-01-02"), webSearchQuery(e), e.Name, e.Type, desc)
}

// storeWebSearch processes the outcome of the search for one key entity: it records the cost, extracts the graph from the
// briefing and stores it as an artifact of kind web_search (a child of the artifact the entity came from) with its
// entities and edges. The returned result says what happened; nothing here fails the call.
func (j *job) storeWebSearch(ctx context.Context, p *keyEntity, ws webSearched) WebSearchResult {
	query := webSearchQuery(p.Entity)
	out := WebSearchResult{Entity: p.Name, EntityType: p.Type, Query: query, ParentArtifactID: p.ArtifactID}
	fields := log.Fields{"stage": memory_core.StageWebSearch, "user_id": j.userID, "dataset": j.dataset, "entity": p.Name,
		"query": query, "parent_artifact_id": p.ArtifactID, "latency_ms": ws.latency.Milliseconds()}
	args := map[string]any{"entity": p.Name, "type": p.Type, "query": query, "parent_artifact_id": p.ArtifactID}
	fail := func(stage string, err error) WebSearchResult {
		out.Error = stage + ": " + err.Error()
		log.WithError(err).WithFields(fields).Warn("remember: web search failed for " + stage)
		return out
	}

	//--------------------------------------
	// THE SEARCH
	if ws.err != nil {
		j.tr.TimedTool("The web search for the entity failed.", memory_core.StageWebSearch, args, map[string]any{"error": ws.err.Error()},
			memory_core.ToolTiming{Provider: ai.ProviderAnthropic, Started: ws.started, Latency: ws.latency})
		return fail("search", ws.err)
	}
	resp := ws.resp
	// Detached from ctx so a cancelled request still records money that was spent.
	if err := memory_core.RecordCall(context.WithoutCancel(ctx), j.d, CallRemember, resp.Provider, resp.Model, resp.InputTokens, resp.OutputTokens, resp.CostUSD); err != nil {
		log.WithError(err).WithFields(fields).Error("record web search call")
	}
	out.Searches, out.Sources, out.CostUSD = resp.Queries, resp.Sources, resp.CostUSD
	briefing := strings.TrimSpace(resp.Text)
	found := briefing != "" && !strings.HasPrefix(briefing, noWebResults)
	fields["searches"], fields["sources"], fields["cost_usd"], fields["found"] = resp.Searches, len(resp.Sources), resp.CostUSD, found
	j.tr.TimedTool("Searched the web with Anthropic's web search tool for the latest world events about the entity.",
		memory_core.StageWebSearch, args,
		map[string]any{"found": found, "briefing": briefing, "searches": resp.Queries, "sources": resp.Sources, "search_errors": resp.SearchErrors, "billed_searches": resp.Searches},
		memory_core.ToolTiming{Model: resp.Model, Provider: resp.Provider, Started: ws.started, Latency: ws.latency,
			PromptTokens: resp.InputTokens, CompletionTokens: resp.OutputTokens, CostUSD: resp.CostUSD})
	if !found {
		log.WithFields(fields).Info("remember: web search found nothing")
		return out
	}

	//--------------------------------------
	// THE RESULT IS A TEXT LIKE ANY OTHER: a job of its own that stores a "web_search" artifact. Its facts are valid from
	// now, when they were read, not from the reference time of the text that mentioned the entity.
	wj := *j
	wj.web = &webPart{ParentID: p.ArtifactID, Query: query, Entity: p.Name}
	wj.kind, wj.ref = graph.ArtifactWebSearch, j.now

	// The graph is extracted from the briefing alone; the artifact keeps the sources next to it.
	ex, err := Extract(ctx, wj.co.ModelLLM(wj.llm, memory_core.StageExtractGraph), briefing, wj.ref)
	if err != nil {
		return fail("extract", err)
	}
	wj.tr.Tool("The LLM extracted these entities and edges (new facts) from the web search result, before any stored fact is considered."+wj.web.label(),
		"extract_graph", map[string]any{"text_chars": len(briefing)},
		map[string]any{"entities": ex.Entities, "edges": candidateFacts(ex.Edges, nil)})
	res, err := wj.resolveAndStore(ctx, webSearchArtifactText(briefing, resp.Sources), ex, nil)
	if err != nil {
		return fail("store", err)
	}
	out.ArtifactID, out.Entities, out.Edges, out.InvalidatedEdges = res.ArtifactID, res.Entities, res.Edges, res.InvalidatedEdges
	fields["artifact_id"], fields["entities"], fields["edges"] = res.ArtifactID, len(res.Entities), len(res.Edges)
	log.WithFields(fields).Info("remember: web search stored")
	return out
}

// webSearchArtifactText is the text of a web_search artifact: the briefing, then the pages it is based on.
func webSearchArtifactText(briefing string, sources []ai.WebSource) string {
	if len(sources) == 0 {
		return briefing
	}
	var sb strings.Builder
	sb.WriteString(briefing)
	sb.WriteString("\n\nSources:")
	for _, s := range sources {
		sb.WriteString("\n- ")
		if s.Title != "" {
			sb.WriteString(s.Title + " ")
		}
		sb.WriteString("<" + s.URL + ">")
		if s.PageAge != "" {
			sb.WriteString(" (" + s.PageAge + ")")
		}
	}
	return sb.String()
}
