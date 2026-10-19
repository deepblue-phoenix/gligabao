package admin

// Web search in the admin UI (see docs/web-search.md). Two sources describe the web searches `remember` made:
//
//   - the DATABASE: every search that found something is an artifact of kind "web_search" (its query, the briefing and
//     its sources as text, its parent artifact) with the entities and edges linked to it. That is all there is for the
//     standard database, and it says nothing about searches that found nothing, the queries the model sent to the search
//     engine, or how long they took;
//   - the ATIF TRAJECTORIES the eval endpoints record (eval database only): the "web_search" step of a remember call holds
//     all that, and the "store_artifact" step after it what the result yielded.
//
// Both are turned into the same WebSearch view, so the UI shows them alike; the fields only a trajectory knows are empty
// for a search read from the database.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/experiments"
	log "github.com/sirupsen/logrus"
)

// WebEntity is an entity that a web search result yielded.
type WebEntity struct {
	// ID is the entity's ID in its dataset (only known when read from the database).
	ID          int64  `json:"id,omitempty"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	// OnlyWeb says that the entity is linked to web search results only: no text the user sent mentions it (only known
	// when read from the database).
	OnlyWeb bool `json:"only_web,omitempty"`
}

// WebEdge is a fact that a web search result yielded.
type WebEdge struct {
	ID          int64  `json:"id,omitempty"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	Relation    string `json:"relation"`
	Description string `json:"description,omitempty"`
	// Created says that this result created the fact; otherwise it restated a fact that was already stored (only known
	// when read from the database).
	Created bool `json:"created,omitempty"`
}

// WebSearch is one web search made by remember for a key entity of a text.
type WebSearch struct {
	Entity     string `json:"entity,omitempty"`
	EntityType string `json:"entity_type,omitempty"`
	// Query is the web search query that was issued for the entity.
	Query string `json:"query"`
	// ParentArtifactID is the artifact the entity came from; ParentText its text, shortened (read from the database only).
	ParentArtifactID int64  `json:"parent_artifact_id,omitempty"`
	ParentText       string `json:"parent_text,omitempty"`
	// ArtifactID is the web_search artifact that holds the result; 0 if nothing was stored.
	ArtifactID int64 `json:"artifact_id,omitempty"`
	// Found says that the search returned something (and so a web_search artifact was stored, unless Error says otherwise).
	Found bool   `json:"found"`
	Error string `json:"error,omitempty"`
	// Briefing is the result the entities and edges were extracted from.
	Briefing string `json:"briefing,omitempty"`
	// Searches are the queries the model sent to the search engine (trajectory only); BilledSearches how many searches
	// were made and billed, which can differ when a search failed (trajectory only).
	Searches       []string `json:"searches,omitempty"`
	BilledSearches int      `json:"billed_searches,omitempty"`
	SearchErrors   []string `json:"search_errors,omitempty"`
	// Sources are the pages the result is based on.
	Sources          []ai.WebSource `json:"sources,omitempty"`
	Entities         []WebEntity    `json:"entities,omitempty"`
	Edges            []WebEdge      `json:"edges,omitempty"`
	InvalidatedEdges []int64        `json:"invalidated_edges,omitempty"`
	// StartedAt is when the search started (the artifact's time, for a search read from the database). LatencyMS is how
	// long the call took, Model the model that ran it, and the tokens and cost those of the call, the search fee
	// included (trajectory only).
	StartedAt        string   `json:"started_at,omitempty"`
	LatencyMS        *float64 `json:"latency_ms,omitempty"`
	Model            string   `json:"model,omitempty"`
	PromptTokens     int      `json:"prompt_tokens,omitempty"`
	CompletionTokens int      `json:"completion_tokens,omitempty"`
	CostUSD          float64  `json:"cost_usd,omitempty"`
	// TrajectoryID is the trajectory the search is a step of (trajectory only).
	TrajectoryID string `json:"trajectory_id,omitempty"`
}

// Latency summarizes the durations of the web search calls.
type Latency struct {
	Count    int     `json:"count"`
	AvgMS    float64 `json:"avg_ms"`
	MedianMS float64 `json:"median_ms"`
	MaxMS    float64 `json:"max_ms"`
}

// WebSearchTotals are the totals over web searches read from trajectories.
type WebSearchTotals struct {
	// EntitySearches is the number of web searches (one per key entity searched); Found of them returned something,
	// Empty found nothing relevant and Failed failed.
	EntitySearches int `json:"entity_searches"`
	Found          int `json:"found"`
	Empty          int `json:"empty"`
	Failed         int `json:"failed"`
	// EngineSearches is the number of searches the search tool made (and billed): a search for an entity makes up to 3.
	EngineSearches int `json:"engine_searches"`
	Sources        int `json:"sources"`
	// Entities, Edges and InvalidatedEdges are what the results yielded (an entity found by several is counted by each).
	Entities         int     `json:"entities"`
	Edges            int     `json:"edges"`
	InvalidatedEdges int     `json:"invalidated_edges"`
	CostUSD          float64 `json:"cost_usd"`
	Latency          Latency `json:"latency"`
}

func totalsOf(list []WebSearch) WebSearchTotals {
	var t WebSearchTotals
	var lat []float64
	for _, w := range list {
		t.EntitySearches++
		switch {
		case w.Error != "":
			t.Failed++
		case w.Found:
			t.Found++
		default:
			t.Empty++
		}
		t.EngineSearches += w.BilledSearches
		t.Sources += len(w.Sources)
		t.Entities += len(w.Entities)
		t.Edges += len(w.Edges)
		t.InvalidatedEdges += len(w.InvalidatedEdges)
		t.CostUSD += w.CostUSD
		if w.LatencyMS != nil {
			lat = append(lat, *w.LatencyMS)
		}
	}
	if len(lat) > 0 {
		sort.Float64s(lat)
		sum := 0.0
		for _, v := range lat {
			sum += v
		}
		t.Latency = Latency{Count: len(lat), AvgMS: sum / float64(len(lat)), MedianMS: lat[len(lat)/2], MaxMS: lat[len(lat)-1]}
		if len(lat)%2 == 0 {
			t.Latency.MedianMS = (lat[len(lat)/2-1] + lat[len(lat)/2]) / 2
		}
	}
	return t
}

//---------------------------------------------------------------------------------------------
// From the trajectories

// webSearchesOf reads the web searches of a remember trajectory: the "web_search" steps, each with what the
// "store_artifact" step after it (the one with the same query and parent) stored.
func webSearchesOf(t *atif.Trajectory) []WebSearch {
	var out []WebSearch
	for i, st := range t.Steps {
		call := toolCall(st, "web_search")
		if call == nil {
			continue
		}
		w := WebSearch{
			Entity: str(call.Arguments["entity"]), EntityType: str(call.Arguments["type"]), Query: str(call.Arguments["query"]),
			ParentArtifactID: int64(num(call.Arguments["parent_artifact_id"])),
			Model:            st.ModelName, TrajectoryID: t.TrajectoryID,
		}
		w.StartedAt, _ = st.Extra["started_at"].(string)
		if w.StartedAt == "" {
			w.StartedAt = st.Timestamp
		}
		if ms, ok := st.Extra["latency_ms"].(float64); ok {
			w.LatencyMS = &ms
		}
		if m := st.Metrics; m != nil {
			w.PromptTokens, w.CompletionTokens, w.CostUSD = m.PromptTokens, m.CompletionTokens, m.CostUSD
		}
		var res struct {
			Found        bool           `json:"found"`
			Briefing     string         `json:"briefing"`
			Searches     []string       `json:"searches"`
			Sources      []ai.WebSource `json:"sources"`
			SearchErrors []string       `json:"search_errors"`
			Billed       int            `json:"billed_searches"`
			Error        string         `json:"error"`
		}
		if json.Unmarshal([]byte(observed(st, call)), &res) == nil {
			w.Found, w.Error, w.Searches, w.Sources, w.SearchErrors, w.BilledSearches = res.Found, res.Error, res.Searches, res.Sources, res.SearchErrors, res.Billed
			if res.Found {
				w.Briefing = res.Briefing
			}
		}
		for _, later := range t.Steps[i+1:] {
			sc := toolCall(later, "store_artifact")
			if sc == nil || str(sc.Arguments["query"]) != w.Query || int64(num(sc.Arguments["parent_id"])) != w.ParentArtifactID {
				continue
			}
			var stored struct {
				ArtifactID       int64          `json:"artifact_id"`
				Entities         []graph.Entity `json:"entities"`
				Edges            []graph.Edge   `json:"edges"`
				InvalidatedEdges []int64        `json:"invalidated_edges"`
			}
			if json.Unmarshal([]byte(observed(later, sc)), &stored) == nil {
				w.ArtifactID, w.InvalidatedEdges = stored.ArtifactID, stored.InvalidatedEdges
				for _, e := range stored.Entities {
					w.Entities = append(w.Entities, WebEntity{Name: e.Name, Type: e.Type, Description: e.Description})
				}
				for _, e := range stored.Edges {
					w.Edges = append(w.Edges, WebEdge{ID: e.ID, Source: e.Source, Target: e.Target, Relation: e.Relation, Description: e.Description})
				}
			}
			break
		}
		out = append(out, w)
	}
	return out
}

// toolCall returns the call of the named tool that step st made, or nil.
func toolCall(st atif.Step, name string) *atif.ToolCall {
	for i := range st.ToolCalls {
		if st.ToolCalls[i].FunctionName == name {
			return &st.ToolCalls[i]
		}
	}
	return nil
}

// observed is the result the step recorded for call.
func observed(st atif.Step, call *atif.ToolCall) string {
	if st.Observation == nil {
		return ""
	}
	for _, r := range st.Observation.Results {
		if r.SourceCallID == call.ToolCallID {
			return r.Content
		}
	}
	return ""
}

func str(v any) string { s, _ := v.(string); return s }

func num(v any) float64 { f, _ := v.(float64); return f }

// trajectorySearches caches the web searches of each trajectory by its ID: a recorded trajectory never changes, and
// reading the files of all runs for every request would be slow.
var trajectorySearches = struct {
	sync.Mutex
	m map[string][]WebSearch
}{m: map[string][]WebSearch{}}

// searchesOfRecorded returns the web searches of the recorded remember trajectory ref (nil if it has none, or if its
// file cannot be read: that is not an error, a trajectory directory may be gone).
func searchesOfRecorded(ref *experiments.Trajectory, dir string) []WebSearch {
	if ref.Kind != evalsdb.TrajectoryRemember {
		return nil
	}
	trajectorySearches.Lock()
	cached, ok := trajectorySearches.m[ref.TrajectoryID]
	trajectorySearches.Unlock()
	if ok {
		return cached
	}
	data, err := readTrajectoryFile(ref, dir)
	if err != nil {
		return nil
	}
	var found []WebSearch
	if bytes.Contains(data, []byte(`"web_search"`)) { // cheap test first: most trajectories have no web search
		var t atif.Trajectory
		if err := json.Unmarshal(data, &t); err != nil {
			log.WithError(err).WithField("trajectory_id", ref.TrajectoryID).Warn("admin: web searches: trajectory is not valid")
			return nil
		}
		found = webSearchesOf(&t)
	}
	trajectorySearches.Lock()
	trajectorySearches.m[ref.TrajectoryID] = found
	trajectorySearches.Unlock()
	return found
}

// caseResultWebSearches are the web searches of one result (one case) of a run.
type caseResultWebSearches struct {
	CaseResultID int64           `json:"case_result_id"`
	Totals       WebSearchTotals `json:"totals"`
	Searches     []WebSearch     `json:"searches"`
}

// EvalRunWebSearches serves GET /admin/api/evals/runs/{id}/web-searches: the web searches that the remember calls of the
// run's cases made, per result (only the results that made any), read from their trajectories in dir (see EvalTrajectory).
func EvalRunWebSearches(d *sql.DB, dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, err := evalsdb.GetExperimentRun(r.Context(), d, id)
		if err != nil {
			evalFail(w, err, "run")
			return
		}
		out := struct {
			CaseResults []caseResultWebSearches `json:"case_results"`
		}{CaseResults: []caseResultWebSearches{}}
		for _, e := range run.CaseResults {
			var list []WebSearch
			for i := range e.Trajectories {
				list = append(list, searchesOfRecorded(&e.Trajectories[i], dir)...)
			}
			if len(list) > 0 {
				out.CaseResults = append(out.CaseResults, caseResultWebSearches{CaseResultID: e.ID, Totals: totalsOf(list), Searches: list})
			}
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// allRecordedSearches returns the web searches of every recorded remember trajectory of the experiments in d.
func allRecordedSearches(ctx context.Context, d *sql.DB, dir string) (list []WebSearch, calls int, err error) {
	exps, err := experiments.List(ctx, d)
	if err != nil {
		return nil, 0, err
	}
	for _, e := range exps {
		runs, err := experiments.Runs(ctx, d, e.ID)
		if err != nil {
			return nil, 0, err
		}
		for _, r := range runs {
			run, err := evalsdb.GetExperimentRun(ctx, d, r.ID)
			if err != nil {
				return nil, 0, err
			}
			for _, ev := range run.CaseResults {
				for i := range ev.Trajectories {
					if found := searchesOfRecorded(&ev.Trajectories[i], dir); len(found) > 0 {
						list = append(list, found...)
						calls++
					}
				}
			}
		}
	}
	return list, calls, nil
}

//---------------------------------------------------------------------------------------------
// From the database

const (
	webQueryPrefix = "latest world events and news about "
	// parentTextRunes is how much of the parent artifact's text is returned with a search.
	parentTextRunes = 240
)

var webQueryRE = regexp.MustCompile(`^` + webQueryPrefix + `(.+) \(([^()]+)\)$`)

// splitWebArtifactText splits the text of a web_search artifact into the briefing and the sources listed after it
// (see memory_remember.webSearchArtifactText: "- <title> <url> (page age)" per line after "Sources:").
func splitWebArtifactText(text string) (string, []ai.WebSource) {
	briefing, list, found := strings.Cut(text, "\n\nSources:")
	if !found {
		return strings.TrimSpace(text), nil
	}
	var sources []ai.WebSource
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		open, end := strings.Index(line, "<"), strings.Index(line, ">")
		if line == "" || open < 0 || end < open {
			continue
		}
		s := ai.WebSource{URL: line[open+1 : end], Title: strings.TrimSpace(line[:open])}
		if age := strings.TrimSpace(line[end+1:]); strings.HasPrefix(age, "(") && strings.HasSuffix(age, ")") {
			s.PageAge = age[1 : len(age)-1]
		}
		sources = append(sources, s)
	}
	return strings.TrimSpace(briefing), sources
}

func shorten(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// DatasetWebSummary counts what the web searches of one dataset hold.
type DatasetWebSummary struct {
	// Artifacts is the number of web_search artifacts (the searches that found something) and Parents how many distinct
	// artifacts their searched entities came from.
	Artifacts int `json:"artifacts"`
	Parents   int `json:"parents"`
	// Sources are the pages the results cite, counted once per result.
	Sources int `json:"sources"`
	// Entities are the distinct entities linked to a result, OnlyWeb of them only to results, never to a text the user
	// sent. Edges are the distinct facts a result created, SupportedEdges those a result created or restated.
	Entities       int `json:"entities"`
	OnlyWeb        int `json:"only_web"`
	Edges          int `json:"edges"`
	SupportedEdges int `json:"supported_edges"`
}

// datasetWebSearches reads the web_search artifacts of dataset dsID with their parents, entities and edges. nil if it has none.
func datasetWebSearches(ctx context.Context, d *sql.DB, dsID int64) ([]WebSearch, DatasetWebSummary, error) {
	var sum DatasetWebSummary
	arts, err := graph.ListArtifacts(ctx, d, dsID)
	if err != nil {
		return nil, sum, err
	}
	byID := make(map[int64]graph.Artifact, len(arts))
	var webs []graph.Artifact
	for _, a := range arts {
		byID[a.ID] = a
		if a.Kind == graph.ArtifactWebSearch {
			webs = append(webs, a)
		}
	}
	if len(webs) == 0 {
		return nil, sum, nil
	}
	links, err := graph.ListEntityArtifacts(ctx, d, dsID)
	if err != nil {
		return nil, sum, err
	}
	ents, err := graph.ListEntities(ctx, d, dsID)
	if err != nil {
		return nil, sum, err
	}
	edges, err := graph.ListGraphEdges(ctx, d, dsID)
	if err != nil {
		return nil, sum, err
	}
	edgeLinks, err := graph.ListEdgeArtifacts(ctx, d, dsID)
	if err != nil {
		return nil, sum, err
	}
	entByID := make(map[int64]graph.EvidenceEntity, len(ents))
	for _, e := range ents {
		entByID[e.ID] = e
	}
	edgeByID := make(map[int64]graph.EvidenceEdge, len(edges))
	for _, e := range edges {
		edgeByID[e.ID] = e
	}
	isWeb := func(id int64) bool { return byID[id].Kind == graph.ArtifactWebSearch }
	// An entity is only from the web when every artifact it is linked to is a web search result.
	onlyWeb := map[int64]bool{}
	for _, l := range links {
		if v, seen := onlyWeb[l.EntityID]; !seen {
			onlyWeb[l.EntityID] = isWeb(l.ArtifactID)
		} else {
			onlyWeb[l.EntityID] = v && isWeb(l.ArtifactID)
		}
	}
	entsOf, edgesOf := map[int64][]int64{}, map[int64][]int64{}
	webEntities, webEdges, createdEdges := map[int64]bool{}, map[int64]bool{}, map[int64]bool{}
	for _, l := range links {
		if isWeb(l.ArtifactID) {
			entsOf[l.ArtifactID] = append(entsOf[l.ArtifactID], l.EntityID)
			webEntities[l.EntityID] = true
		}
	}
	for _, l := range edgeLinks {
		if isWeb(l.ArtifactID) {
			edgesOf[l.ArtifactID] = append(edgesOf[l.ArtifactID], l.EdgeID)
			webEdges[l.EdgeID] = true
		}
	}

	parents := map[int64]bool{}
	out := make([]WebSearch, 0, len(webs))
	for _, a := range webs {
		briefing, sources := splitWebArtifactText(a.Text)
		w := WebSearch{
			Query: a.Query, ParentArtifactID: a.ParentID, ArtifactID: a.ID, Found: true, Briefing: briefing, Sources: sources,
			StartedAt: a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}
		if m := webQueryRE.FindStringSubmatch(a.Query); m != nil {
			w.Entity, w.EntityType = m[1], m[2]
		}
		if p, ok := byID[a.ParentID]; ok {
			w.ParentText = shorten(p.Text, parentTextRunes)
		}
		for _, id := range entsOf[a.ID] {
			if e, ok := entByID[id]; ok {
				w.Entities = append(w.Entities, WebEntity{ID: e.ID, Name: e.Name, Type: e.Type, Description: e.Description, OnlyWeb: onlyWeb[id]})
			}
		}
		for _, id := range edgesOf[a.ID] {
			if e, ok := edgeByID[id]; ok {
				created := e.ArtifactID != nil && *e.ArtifactID == a.ID
				if created {
					createdEdges[id] = true
				}
				w.Edges = append(w.Edges, WebEdge{ID: e.ID, Source: e.Source, Target: e.Target, Relation: e.Relation, Description: e.Description, Created: created})
			}
		}
		parents[a.ParentID] = true
		sum.Sources += len(sources)
		out = append(out, w)
	}
	sum.Artifacts, sum.Parents = len(webs), len(parents)
	sum.Entities, sum.Edges, sum.SupportedEdges = len(webEntities), len(createdEdges), len(webEdges)
	for id := range webEntities {
		if onlyWeb[id] {
			sum.OnlyWeb++
		}
	}
	return out, sum, nil
}

// webDataset is a dataset with web searches, as listed in the stats.
type webDataset struct {
	ID      int64  `json:"id"`
	UserID  string `json:"user_id"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
	DatasetWebSummary
}

// webSearchStats is the answer of WebSearchStatsOf.
type webSearchStats struct {
	// Database is the sum over the datasets with web searches (those of deleted datasets only with include_deleted).
	Database struct {
		Datasets int `json:"datasets"`
		DatasetWebSummary
	} `json:"database"`
	Datasets []webDataset `json:"datasets"`
	// Trajectories are the totals over all the web searches recorded in the ATIF trajectories of the experiments; nil if
	// the database has no trajectory directory (only the eval database does). Calls is how many remember calls made some.
	Trajectories *struct {
		Calls int `json:"calls"`
		WebSearchTotals
	} `json:"trajectories,omitempty"`
}

// WebSearchStatsOf serves GET /admin/api/web-searches[?db=<name>][&include_deleted=true] for one of dbs (the first
// without db): the web searches stored in the database (the web_search artifacts and what they yielded, per dataset
// and in total) and, for the eval database, the totals of the web searches its trajectories recorded (searches made,
// latency, cost).
func WebSearchStatsOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		ctx := r.Context()
		list, err := relational.ListAllDatasets(ctx, n.DB, r.URL.Query().Get("include_deleted") == "true")
		if err != nil {
			log.WithError(err).Error("admin: web search stats: list datasets failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list datasets")
			return
		}
		out := webSearchStats{Datasets: []webDataset{}}
		for _, ds := range list {
			_, sum, err := datasetWebSearches(ctx, n.DB, ds.ID)
			if err != nil {
				log.WithError(err).WithField("dataset_id", ds.ID).Error("admin: web search stats failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to read web searches")
				return
			}
			if sum.Artifacts == 0 {
				continue
			}
			out.Datasets = append(out.Datasets, webDataset{ID: ds.ID, UserID: ds.UserID, Name: ds.Name, Deleted: ds.Deleted, DatasetWebSummary: sum})
			t := &out.Database
			t.Datasets++
			t.Artifacts += sum.Artifacts
			t.Parents += sum.Parents
			t.Sources += sum.Sources
			t.Entities += sum.Entities
			t.OnlyWeb += sum.OnlyWeb
			t.Edges += sum.Edges
			t.SupportedEdges += sum.SupportedEdges
		}
		if n.TrajectoryDir != "" {
			searches, calls, err := allRecordedSearches(ctx, n.DB, n.TrajectoryDir)
			if err != nil {
				log.WithError(err).Error("admin: web search stats: read trajectories failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to read recorded web searches")
				return
			}
			out.Trajectories = &struct {
				Calls int `json:"calls"`
				WebSearchTotals
			}{calls, totalsOf(searches)}
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// WebSearchDatasetOf serves GET /admin/api/web-searches/dataset?db=<name>&dataset_id=<id> for one of dbs (the first
// without db): the web searches stored in a dataset (deleted or not), read from its web_search artifacts.
func WebSearchDatasetOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		dsID, err := strconv.ParseInt(r.URL.Query().Get("dataset_id"), 10, 64)
		if err != nil || dsID <= 0 {
			httputil.Error(w, http.StatusBadRequest, "invalid dataset_id")
			return
		}
		ctx := r.Context()
		all, err := relational.ListAllDatasets(ctx, n.DB, true)
		if err != nil {
			log.WithError(err).Error("admin: web searches: list datasets failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list datasets")
			return
		}
		var ds *relational.Dataset
		for i := range all {
			if all[i].ID == dsID {
				ds = &all[i]
			}
		}
		if ds == nil {
			httputil.Error(w, http.StatusNotFound, "dataset not found")
			return
		}
		list, sum, err := datasetWebSearches(ctx, n.DB, ds.ID)
		if err != nil {
			log.WithError(err).WithField("dataset_id", ds.ID).Error("admin: web searches failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read web searches")
			return
		}
		if list == nil {
			list = []WebSearch{}
		}
		httputil.WriteJSON(w, http.StatusOK, struct {
			UserID   string            `json:"user_id"`
			Dataset  string            `json:"dataset"`
			Summary  DatasetWebSummary `json:"summary"`
			Searches []WebSearch       `json:"searches"`
		}{ds.UserID, ds.Name, sum, list})
	}
}

// pick returns the database called name (the first one if name is empty).
func pick(dbs []Named, name string) (Named, bool) {
	if name == "" && len(dbs) > 0 {
		return dbs[0], true
	}
	for _, n := range dbs {
		if n.Name == name {
			return n, true
		}
	}
	return Named{}, false
}
