package memory_remember

// Fetching timeseries from providers for Remember (see docs/timeseries.md). It is a stage after the text (and the web
// search) has been stored, only for a call made with memory_core.WithSeriesFetch, and it needs timeseries providers
// registered by the embedding program (memory_core.WithSeriesProviders).
//
//  1. CANDIDATES: the entities the extraction found, the ones in the most facts first, at most maxSeriesCandidates.
//     No entity type is excluded: whether an entity has a series is for the providers' descriptions to say (a macro
//     series of a concept such as "inflation" is as good a case as the price of a company).
//  2. RESOLVE: one LLM call (the resolve_series stage) reads the natural-language descriptions of the providers and
//     decides which provider is to fetch for which entity, and what to ask it (a ticker, a series ID: the provider's
//     query format). It is told to prefer precision to recall: an entity it is not sure a provider covers is skipped.
//  3. FETCH: the providers' Fetch callbacks run in parallel, for the period around the reference time of the text. What
//     they return is clipped to the period.
//  4. STORE: each outcome in a transaction of its own: the series (identified by provider, key and field, so fetching
//     again adds points), their stats, and anchors to the entity and to the artifact the entity came from, with the
//     period as the window.
//
// The text is already stored: a failure anywhere here (the LLM, a provider, the storage) is logged, recorded in the
// trajectory and in the result, and never fails Remember or removes anything.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_timeseries"

	log "github.com/sirupsen/logrus"
)

// SeriesFetchResult is the outcome of fetching series from one provider for one entity of a Remember call.
type SeriesFetchResult struct {
	// Entity and EntityType are the entity the series were fetched for; ParentArtifactID is the artifact it came from
	// (a chunk, for a long text), which the series are anchored to as well.
	Entity           string `json:"entity,omitempty"`
	EntityType       string `json:"entity_type,omitempty"`
	ParentArtifactID int64  `json:"parent_artifact_id,omitempty"`
	// Provider is the provider the LLM picked, Query what it asked it for and Reason why it picked it.
	Provider string `json:"provider,omitempty"`
	Query    string `json:"query,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// From and To are the period that was asked for (RFC 3339).
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Series are the series that were stored; empty if the provider had none for the entity (see Error).
	Series []memory_timeseries.Result `json:"series,omitempty"`
	// Error says why nothing (or not everything) was stored; "" if all went well, or if the provider simply had nothing.
	Error string `json:"error,omitempty"`
}

// seriesAssignment is the LLM's decision to fetch from a provider for an entity.
type seriesAssignment struct {
	Entity   *keyEntity
	Provider memory_core.SeriesProvider
	Query    string
	Reason   string
}

// seriesFetched is the outcome of one Fetch callback.
type seriesFetched struct {
	data    []memory_core.SeriesData
	err     error
	started time.Time
	latency time.Duration
}

const (
	// maxSeriesCandidates is how many entities the LLM is shown, and maxSeriesFetches how many (entity, provider) fetches
	// one call makes, however many the LLM proposes: a provider call can cost money and time.
	maxSeriesCandidates = 20
	maxSeriesFetches    = 10
	// seriesFetchTimeout bounds each Fetch callback.
	seriesFetchTimeout = 60 * time.Second
)

const resolveSeriesSystemPrompt = `You decide which timeseries providers should be used to fetch data for entities of a knowledge base.

<providers> lists the providers. Each has a name, a description of what it offers and WHEN it should be used, and a query_format that says how to identify an entity to it. <entities> lists the entities, as "[n] name | type | description". <reference_time> is the date of the text the entities come from.

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"assignments": [{"entity": 0, "provider": "prices", "query": "AAPL", "reason": "Apple Inc. is a public company"}]}

Rules:
- Assign a provider to an entity only if the provider's description clearly covers that entity. Prefer precision to recall: when unsure that the provider has data for the entity, or that the entity is the one meant (ambiguous names), leave it out. Most entities need no provider; an empty list is a normal answer.
- Judge the entity as of <reference_time> (e.g. a company that was not yet public then has no stock price series).
- "query" is what the provider needs to identify the entity, following its query_format (for example a ticker symbol). If the query_format is missing, use the entity's full name. Never invent an identifier you are not sure of: leave the entity out instead.
- One entity can be assigned to several providers (one assignment each); one provider can serve several entities.
- "entity" is the number n from <entities>; "provider" is a name from <providers>. Use only those.
- "reason" is one short sentence.`

//---------------------------------------------------------------------------------------------

// pickSeriesCandidates returns the entities to offer the providers: the ones in the most facts first (the entities the
// text is about; the order of extraction breaks ties), at most max.
func pickSeriesCandidates(all []*keyEntity, max int) []*keyEntity {
	out := append([]*keyEntity(nil), all...) // all is in order of extraction
	sort.SliceStable(out, func(a, b int) bool { return out[a].Edges > out[b].Edges })
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// resolveSeriesPrompt is the user turn of the provider resolution.
func resolveSeriesPrompt(cands []*keyEntity, providers []memory_core.SeriesProvider, ref time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<reference_time>%s</reference_time>\n\n<providers>\n", ref.UTC().Format("2006-01-02"))
	for _, p := range providers {
		qf := p.QueryFormat
		if qf == "" {
			qf = "(none: use the entity's full name)"
		}
		fmt.Fprintf(&b, "<provider>\nname: %s\ndescription: %s\nquery_format: %s\n</provider>\n", p.Name, p.Description, qf)
	}
	b.WriteString("</providers>\n\n<entities>\n")
	for i, c := range cands {
		desc := strings.Join(strings.Fields(c.Description), " ")
		if desc == "" {
			desc = "(none)"
		}
		fmt.Fprintf(&b, "[%d] %s | %s | %s\n", i, c.Name, c.Type, desc)
	}
	b.WriteString("</entities>")
	return b.String()
}

// parseSeriesAssignments decodes the LLM's answer and keeps the valid, distinct assignments, in the order given: those
// naming an entity of cands and a provider of providers.
func parseSeriesAssignments(text string, cands []*keyEntity, providers []memory_core.SeriesProvider) ([]seriesAssignment, error) {
	js, err := memory_core.JSONObject(text)
	if err != nil {
		return nil, err
	}
	var out struct {
		Assignments []struct {
			Entity   int    `json:"entity"`
			Provider string `json:"provider"`
			Query    string `json:"query"`
			Reason   string `json:"reason"`
		} `json:"assignments"`
	}
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return nil, fmt.Errorf("memory: decode LLM resolve_series output: %w", err)
	}
	byName := map[string]memory_core.SeriesProvider{}
	for _, p := range providers {
		byName[p.Name] = p
	}
	var res []seriesAssignment
	seen := map[string]bool{}
	for _, a := range out.Assignments {
		p, ok := byName[strings.TrimSpace(a.Provider)]
		if !ok || a.Entity < 0 || a.Entity >= len(cands) {
			continue // the LLM named something that was not offered
		}
		id := fmt.Sprint(a.Entity, "\x00", p.Name)
		if seen[id] {
			continue
		}
		seen[id] = true
		q := strings.TrimSpace(a.Query)
		if q == "" {
			q = cands[a.Entity].Name
		}
		if len(q) > memory_core.MaxProviderQueryLen {
			continue
		}
		res = append(res, seriesAssignment{Entity: cands[a.Entity], Provider: p, Query: q, Reason: strings.TrimSpace(a.Reason)})
	}
	return res, nil
}

//---------------------------------------------------------------------------------------------

// fetchSeries is the fetch_series stage of Remember (see fetchSeriesStage): it fetches series from the providers (see the top of
// this file). It returns one result per (entity, provider) the LLM chose; nil if there was nothing to decide. It never fails the
// call. The call fetches.
func (j *job) fetchSeries(ctx context.Context) (out []SeriesFetchResult) {
	f := j.co.SeriesFetch
	var providers []memory_core.SeriesProvider
	for _, p := range j.co.SeriesProviders {
		for _, n := range f.Providers {
			if p.Name == n {
				providers = append(providers, p)
				break
			}
		}
	}
	all := make([]*keyEntity, 0, len(j.keyOrder))
	for _, k := range j.keyOrder {
		all = append(all, j.keyEntities[k])
	}
	cands := pickSeriesCandidates(all, maxSeriesCandidates)
	names := make([]string, 0, len(cands))
	for _, c := range cands {
		names = append(names, c.Name)
	}
	from, to := j.ref.Add(-f.Before), j.ref.Add(f.After)
	j.tr.Tool("Picked the entities offered to the timeseries providers: the extracted entities with the most facts, at most "+
		fmt.Sprint(maxSeriesCandidates)+".", "select_series_entities",
		map[string]any{"entities_extracted": len(all), "max": maxSeriesCandidates, "providers": f.Providers,
			"from": relational.FormatTime(from), "to": relational.FormatTime(to)},
		map[string]any{"entities": names})
	j.run.Output(StageFetchSeries, "select_series_entities", "", map[string]any{"entities_extracted": len(all), "entities": names,
		"providers": f.Providers, "from": relational.FormatTime(from), "to": relational.FormatTime(to)})
	if len(cands) == 0 || len(providers) == 0 {
		return nil
	}
	fields := log.Fields{"stage": memory_core.StageResolveSeries, "user_id": j.userID, "dataset": j.dataset}

	//--------------------------------------
	// RESOLVE: the LLM matches entities to providers by their descriptions
	resp, err := j.co.ModelLLM(j.llm, memory_core.StageResolveSeries).Complete(ctx, ai.Request{
		System:    resolveSeriesSystemPrompt,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: resolveSeriesPrompt(cands, providers, j.ref)}},
		MaxTokens: 2048,
	})
	var picks []seriesAssignment
	answered := err == nil // the LLM call itself worked
	if answered {
	}
	if err == nil && resp.StopReason == "max_tokens" {
		err = fmt.Errorf("LLM output truncated")
	}
	if err == nil {
		picks, err = parseSeriesAssignments(resp.Text, cands, providers)
	}
	if answered {
		out := map[string]any{"assignments": seriesAssignmentsOutput(picks), "raw": resp.Text}
		if err != nil {
			out["error"] = err.Error()
		}
		j.run.Output(StageFetchSeries, "resolve_series_providers", "", out)
	}
	if err != nil {
		log.WithError(err).WithFields(fields).Warn("remember: resolving timeseries providers failed")
		return []SeriesFetchResult{{Error: "resolve: " + err.Error()}}
	}
	if len(picks) > maxSeriesFetches {
		picks = picks[:maxSeriesFetches]
	}
	chosen := make([]map[string]any, 0, len(picks))
	for _, p := range picks {
		chosen = append(chosen, map[string]any{"entity": p.Entity.Name, "provider": p.Provider.Name, "query": p.Query, "reason": p.Reason})
	}
	j.tr.Tool("The LLM decided, from the providers' descriptions, which provider fetches series for which entity.",
		"resolve_series_providers", map[string]any{"entities": len(cands), "providers": len(providers)},
		map[string]any{"assignments": chosen})
	if len(picks) == 0 {
		return nil
	}

	//--------------------------------------
	// FETCH, all at once: the callbacks are independent and slow
	done := make([]seriesFetched, len(picks))
	var wg sync.WaitGroup
	for i, p := range picks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := &done[i]
			d.started = time.Now()
			d.data, d.err = callProvider(ctx, p.Provider, memory_core.SeriesFetchRequest{
				Entity: memory_core.FetchEntity{Name: p.Entity.Name, Type: p.Entity.Type, Description: p.Entity.Description},
				Query:  p.Query, From: from, To: to, ReferenceTime: j.ref,
			})
			d.latency = time.Since(d.started)
		}()
	}
	wg.Wait()

	//--------------------------------------
	// STORE, one at a time
	results := make([]SeriesFetchResult, 0, len(picks))
	for i, p := range picks {
		results = append(results, j.storeFetchedSeries(ctx, p, done[i], from, to))
	}
	return results
}

// callProvider runs a provider's Fetch callback with a timeout; a panic in it (the code of the embedding program) is an
// error, not a crash.
func callProvider(ctx context.Context, p memory_core.SeriesProvider, req memory_core.SeriesFetchRequest) (data []memory_core.SeriesData, err error) {
	ctx, cancel := context.WithTimeout(ctx, seriesFetchTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			data, err = nil, fmt.Errorf("provider %q panicked: %v", p.Name, r)
		}
	}()
	return p.Fetch(ctx, req)
}

// storeFetchedSeries validates what a provider returned for one assignment, clips it to the period, and stores it with
// its anchors in a transaction. The result says what happened; nothing here fails the call.
func (j *job) storeFetchedSeries(ctx context.Context, p seriesAssignment, sf seriesFetched, from, to time.Time) SeriesFetchResult {
	out := SeriesFetchResult{Entity: p.Entity.Name, EntityType: p.Entity.Type, ParentArtifactID: p.Entity.ArtifactID,
		Provider: p.Provider.Name, Query: p.Query, Reason: p.Reason, From: relational.FormatTime(from), To: relational.FormatTime(to)}
	fields := log.Fields{"stage": "fetch_series", "user_id": j.userID, "dataset": j.dataset, "entity": p.Entity.Name,
		"provider": p.Provider.Name, "query": p.Query, "latency_ms": sf.latency.Milliseconds()}
	args := map[string]any{"entity": p.Entity.Name, "type": p.Entity.Type, "provider": p.Provider.Name, "query": p.Query,
		"from": out.From, "to": out.To}
	timing := memory_core.ToolTiming{Provider: p.Provider.Name, Started: sf.started, Latency: sf.latency}

	if sf.err != nil {
		out.Error = "fetch: " + sf.err.Error()
		log.WithError(sf.err).WithFields(fields).Warn("remember: timeseries fetch failed")
		j.tr.TimedTool("The timeseries provider failed.", "fetch_timeseries", args, map[string]any{"error": out.Error}, timing)
		j.run.Output(StageFetchSeries, "fetch_timeseries", p.Entity.Name, map[string]any{"request": args, "error": out.Error})
		return out
	}

	// Validate each series on its own, so one malformed series does not lose the others.
	def, _ := json.Marshal(map[string]any{"entity": p.Entity.Name, "entity_type": p.Entity.Type, "query": p.Query})
	window := []string{out.From, out.To}
	var specs []memory_core.SeriesSpec
	var problems []string
	seen := map[string]bool{}
	for _, s := range sf.data {
		spec := memory_core.SeriesSpec{Key: s.Key, Field: s.Field, Name: s.Name, Unit: s.Unit, Frequency: s.Frequency,
			Provider: p.Provider.Name, Definition: string(def)}
		for _, pt := range s.Points { // the provider may return more than the period asked for
			if !pt.TS.Before(from) && !pt.TS.After(to) {
				spec.Points = append(spec.Points, pt)
			}
		}
		if len(spec.Points) == 0 {
			continue
		}
		role := p.Provider.Name
		spec.Anchors = []memory_core.SeriesAnchor{
			{Entity: &memory_core.SeriesEntity{Name: p.Entity.Name, Type: p.Entity.Type}, Role: role, Window: window, OnUnmatched: memory_core.OnUnmatchedIgnore},
			{ArtifactID: p.Entity.ArtifactID, Role: role, Window: window},
		}
		r, err := memory_core.ResolveSeries([]memory_core.SeriesSpec{spec}, true)
		if err != nil {
			problems = append(problems, fmt.Sprintf("series %q: %v", s.Key, err))
			continue
		}
		if id := r[0].Key + "\x00" + r[0].Field; seen[id] {
			problems = append(problems, fmt.Sprintf("series %q with field %q returned twice", s.Key, r[0].Field))
			continue
		} else {
			seen[id] = true
		}
		specs = append(specs, r[0])
	}
	returned := map[string]any{"returned": len(sf.data), "valid": len(specs), "problems": problems}
	if len(problems) > 0 {
		out.Error = strings.Join(problems, "; ")
	}
	if len(specs) == 0 {
		log.WithFields(fields).Info("remember: timeseries provider returned no series for the entity")
		j.tr.TimedTool("The timeseries provider returned no series in the period.", "fetch_timeseries", args, returned, timing)
		j.run.Output(StageFetchSeries, "fetch_timeseries", p.Entity.Name, map[string]any{"request": args, "result": returned})
		return out
	}

	res, err := j.storeSeriesSpecs(ctx, specs)
	if err != nil {
		out.Error = strings.TrimPrefix(out.Error+"; store: "+err.Error(), "; ")
		log.WithError(err).WithFields(fields).Warn("remember: storing fetched timeseries failed")
		j.tr.TimedTool("The timeseries provider returned series but storing them failed.", "fetch_timeseries", args,
			map[string]any{"returned": len(sf.data), "error": err.Error()}, timing)
		j.run.Output(StageFetchSeries, "fetch_timeseries", p.Entity.Name, map[string]any{"request": args, "result": returned, "error": err.Error()})
		return out
	}
	out.Series = res
	// The observation of a fetched series is about the entity it was fetched for, over the period that was fetched, and is
	// a child of the artifact the entity came from.
	subjects, windows := make([][2]string, len(res)), make([][2]time.Time, len(res))
	for i := range res {
		subjects[i], windows[i] = [2]string{p.Entity.Name, p.Entity.Type}, [2]time.Time{from, to}
	}
	j.noteObservable(p.Entity.ArtifactID, res, subjects, windows)
	fields["series"] = len(res)
	log.WithFields(fields).Info("remember: timeseries fetched and stored")
	returned["series"] = res
	j.run.Output(StageFetchSeries, "fetch_timeseries", p.Entity.Name, map[string]any{"request": args, "result": returned})
	j.tr.TimedTool("Fetched timeseries from the provider (clipped to the period) and stored them, with their stats, anchored to "+
		"the entity and to the artifact it came from.", "fetch_timeseries", args, returned, timing)
	return out
}

// storeSeriesSpecs stores specs (resolved) in one transaction of their own, in the dataset of the call. The anchors are
// in the specs: there is no artifact to default to.
func (j *job) storeSeriesSpecs(ctx context.Context, specs []memory_core.SeriesSpec) ([]memory_timeseries.Result, error) {
	dsID, err := relational.EnsureDataset(ctx, j.d, j.userID, j.dataset)
	if err != nil {
		return nil, err
	}
	tx, err := graph.Begin(ctx, j.d)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := memory_timeseries.Store(ctx, tx, dsID, 0, j.now, specs)
	if err != nil {
		return nil, err
	}
	return res, tx.Commit()
}

// seriesAssignmentsOutput is how the assignments the LLM made are filed in the run log (see runLog.Output).
func seriesAssignmentsOutput(picks []seriesAssignment) []map[string]any {
	out := make([]map[string]any, 0, len(picks))
	for _, p := range picks {
		out = append(out, map[string]any{"entity": p.Entity.Name, "provider": p.Provider.Name, "query": p.Query, "reason": p.Reason})
	}
	return out
}
