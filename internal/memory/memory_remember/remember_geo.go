package memory_remember

// Geo for Remember (see package internal/memory/memory_geo). It is a stage that runs when the call has a geo resolver
// (memory_core.WithGeoResolver), right after the text (or every chunk of a long text) has been stored and before the web
// search:
//
//  1. CANDIDATES: the entities the extraction found that may be places (every type but the ones that never are: a person,
//     a concept, ...), at most maxGeoCandidates, minus the ones that already have geo metadata (an entity is shared by every
//     artifact that mentions it, so it is looked up once, the first time).
//  2. DETECT: one LLM call (the detect_geo stage) reads the candidates and says which are places (a country, a region, a
//     city, or anything smaller: a landmark, an address, a mountain), their kind, and what to ask the resolver for: the name
//     with whatever disambiguates it ("Paris, Texas").
//  3. RESOLVE: the geo resolver is asked for each place, one at a time (a public geocoder allows no more).
//  4. STORE: the coordinates and the rest of what the resolver says go to gb_entity_geo, keyed by the entity, in one
//     transaction.
//
// The text is already stored: a failure anywhere here (the LLM, the resolver, the storage) is logged, recorded in the
// trajectory and in the result, and never fails Remember or removes anything. A place the resolver does not know is not a
// failure: it just has no geo metadata.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"

	log "github.com/sirupsen/logrus"
)

// GeoResult is the outcome of the geo lookup for one entity of a Remember call that the LLM judged to be a place.
type GeoResult struct {
	// Entity and EntityType are the entity; EntityID is its ID in gb_entities (0 if it could not be found).
	Entity     string `json:"entity"`
	EntityType string `json:"entity_type,omitempty"`
	EntityID   int64  `json:"entity_id,omitempty"`
	// Kind is what the place is ("country", "region", "city" or "place") and Query what the resolver was asked for.
	Kind  string `json:"kind,omitempty"`
	Query string `json:"query,omitempty"`
	// Resolved says the resolver found the place and its geo metadata is stored (in gb_entity_geo): the fields below are then set.
	Resolved    bool    `json:"resolved"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	DisplayName string  `json:"display_name,omitempty"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"country_code,omitempty"`
	Region      string  `json:"region,omitempty"`
	Provider    string  `json:"provider,omitempty"`
	// Error says why nothing was stored: the resolver failed or the storage did. "" if all went well, and also if the resolver
	// simply does not know the place (Resolved is false then).
	Error string `json:"error,omitempty"`
}

// geoPlace is the LLM's decision that an entity is a place.
type geoPlace struct {
	Entity *keyEntity
	ID     int64 // the entity's ID in gb_entities
	Kind   string
	Query  string
}

const (
	// maxGeoCandidates is how many entities the LLM is shown, and maxGeoResolves how many places one call asks the resolver
	// for, however many the LLM finds: a resolver is slow, and rate-limited if it is a public one.
	maxGeoCandidates = 50
	maxGeoResolves   = 25
	// geoResolveTimeout bounds each call of the resolver.
	geoResolveTimeout = 30 * time.Second
	// maxGeoQueryLen bounds the query the LLM chooses; a longer one is replaced by the name of the entity.
	maxGeoQueryLen = 200
)

// notGeoTypes are the entity types that are never a place, so that the LLM is not asked about them.
var notGeoTypes = memory_geo.NotPlaceTypes

const detectGeoSystemPrompt = `You decide which entities of a knowledge base are geographic places, so that their coordinates can be looked up.

<entities> lists the entities, as "[n] name | type | description".

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"places": [{"entity": 0, "kind": "city", "query": "Paris, France"}]}

Rules:
- An entity is a place if it is a location on Earth that has coordinates: a country, a region (a state, province, county, or a named area such as "Silicon Valley" or "the Alps"), a city, town or village, or anything smaller or more specific such as a landmark, a building, an address, a mountain, a lake or an island.
- "kind" is one of: "country", "region", "city", "place" (anything smaller than a city, or not an administrative unit: a landmark, a building, an address, a natural feature).
- An organization, a person, a product, an event or a concept is NOT a place, even if its name contains a place name ("Bank of America", "Paris Hilton", "the Berlin Wall speech"). A company's headquarters or a university's campus is only a place if the entity itself is that location. Judge by the entity's type and description, not just its name.
- "query" is what a geocoder should be asked: the place's name followed by the place it lies in when that is needed to tell it from others of the same name and the entity's description says or implies it ("Springfield, Illinois, USA"; "Paris, France"). Never invent a country or region the entity does not suggest. For a country or a place that is unambiguous, the name alone is enough.
- Prefer precision to recall: when you are not sure that an entity is a place, or which one is meant, leave it out. Most entities are not places, and an empty list is a normal answer.
- "entity" is the number n from <entities>. Use only those.`

//---------------------------------------------------------------------------------------------

func (r GeoResult) errText() string { return r.Error }

// pickGeoCandidates returns the entities that may be a place, in order of extraction, at most max.
func pickGeoCandidates(all []*keyEntity, max int) []*keyEntity {
	var out []*keyEntity
	for _, e := range all {
		if !notGeoTypes[e.Type] {
			out = append(out, e)
		}
		if len(out) == max {
			break
		}
	}
	return out
}

// detectGeoPrompt is the user turn of the detection.
func detectGeoPrompt(cands []*keyEntity) string {
	var b strings.Builder
	b.WriteString("<entities>\n")
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

// parseGeoPlaces decodes the LLM's answer and keeps the valid, distinct places, in the order given: those naming an entity
// of cands. A kind that is not one of geo.Kinds becomes "place"; an empty query becomes the entity's name.
func parseGeoPlaces(text string, cands []*keyEntity) ([]geoPlace, error) {
	js, err := memory_core.JSONObject(text)
	if err != nil {
		return nil, err
	}
	var out struct {
		Places []struct {
			Entity int    `json:"entity"`
			Kind   string `json:"kind"`
			Query  string `json:"query"`
		} `json:"places"`
	}
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return nil, fmt.Errorf("memory: decode LLM detect_geo output: %w", err)
	}
	var res []geoPlace
	seen := map[int]bool{}
	for _, p := range out.Places {
		if p.Entity < 0 || p.Entity >= len(cands) || seen[p.Entity] {
			continue // the LLM named something that was not offered
		}
		seen[p.Entity] = true
		kind := memory_geo.NormKind(p.Kind)
		if kind == "" {
			kind = memory_geo.KindPlace
		}
		q := strings.Join(strings.Fields(p.Query), " ")
		if q == "" {
			q = cands[p.Entity].Name
		}
		if len(q) > maxGeoQueryLen {
			q = cands[p.Entity].Name
		}
		res = append(res, geoPlace{Entity: cands[p.Entity], Kind: kind, Query: q})
	}
	return res, nil
}

//---------------------------------------------------------------------------------------------

// resolveGeo is the stage of Remember that finds the places among the entities and stores their coordinates (see the top of
// this file). It returns one result per entity the LLM judged to be a place (and that was not looked up before); nil if the
// call has no geo resolver or there was nothing to decide. It never fails the call.
func (j *job) resolveGeo(ctx context.Context) (out []GeoResult) {
	resolver := j.co.GeoResolver
	if resolver == nil {
		return nil
	}
	j.run.Start(StageResolveGeo)
	defer func() { j.run.Done(resultsError(out), StageResolveGeo) }()
	fields := log.Fields{"stage": memory_core.StageDetectGeo, "user_id": j.userID, "dataset": j.dataset, "resolver": resolver.Name()}

	all := make([]*keyEntity, 0, len(j.keyOrder))
	for _, k := range j.keyOrder {
		all = append(all, j.keyEntities[k])
	}
	cands := pickGeoCandidates(all, maxGeoCandidates)

	//--------------------------------------
	// CANDIDATES: leave out the entities that already have geo metadata
	ids, cands, err := j.geoUnresolved(ctx, cands)
	if err != nil {
		log.WithError(err).WithFields(fields).Warn("remember: looking up the stored geo metadata failed")
		return []GeoResult{{Error: "load: " + err.Error()}}
	}
	names := make([]string, 0, len(cands))
	for _, c := range cands {
		names = append(names, c.Name)
	}
	j.tr.Tool("Picked the entities that may be places: the extracted entities of a type that can be a place (not a person, concept, "+
		"product or event), without the ones that already have geo metadata, at most "+fmt.Sprint(maxGeoCandidates)+".", "select_geo_entities",
		map[string]any{"entities_extracted": len(all), "max": maxGeoCandidates, "resolver": resolver.Name()},
		map[string]any{"entities": names})
	j.run.Output(StageResolveGeo, "select_geo_entities", "", map[string]any{"entities_extracted": len(all), "entities": names})
	if len(cands) == 0 {
		return nil
	}

	//--------------------------------------
	// DETECT: the LLM says which candidates are places, and what to ask the resolver
	resp, err := j.co.ModelLLM(j.llm, memory_core.StageDetectGeo).Complete(ctx, ai.Request{
		System:    detectGeoSystemPrompt,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: detectGeoPrompt(cands)}},
		MaxTokens: 2048,
	})
	var places []geoPlace
	answered := err == nil // the LLM call itself worked
	if answered {
		j.run.AddInference(StageResolveGeo, resp.InferenceID)
	}
	if err == nil && resp.StopReason == "max_tokens" {
		err = errors.New("LLM output truncated")
	}
	if err == nil {
		places, err = parseGeoPlaces(resp.Text, cands)
	}
	if answered {
		out := map[string]any{"places": geoPlacesOutput(places), "raw": resp.Text}
		if err != nil {
			out["error"] = err.Error()
		}
		j.run.Output(StageResolveGeo, "detect_geo", "", out)
	}
	if err != nil {
		log.WithError(err).WithFields(fields).Warn("remember: detecting the places among the entities failed")
		return []GeoResult{{Error: "detect: " + err.Error()}}
	}
	if len(places) > maxGeoResolves {
		places = places[:maxGeoResolves]
	}
	for i := range places {
		places[i].ID = ids[places[i].Entity]
	}
	found := make([]map[string]any, 0, len(places))
	for _, p := range places {
		found = append(found, map[string]any{"entity": p.Entity.Name, "kind": p.Kind, "query": p.Query})
	}
	j.tr.Tool("The LLM decided which of the entities are places (cities, countries, regions, landmarks, ...) and what to ask the geo resolver for each.",
		"detect_geo", map[string]any{"entities": len(cands)}, map[string]any{"places": found})
	if len(places) == 0 {
		return nil
	}

	//--------------------------------------
	// RESOLVE, one at a time: the resolver may be rate-limited
	results := make([]GeoResult, 0, len(places))
	var upserts []graph.NewEntityGeo
	for _, p := range places {
		r := GeoResult{Entity: p.Entity.Name, EntityType: p.Entity.Type, EntityID: p.ID, Kind: p.Kind, Query: p.Query}
		args := map[string]any{"entity": p.Entity.Name, "type": p.Entity.Type, "kind": p.Kind, "query": p.Query, "resolver": resolver.Name()}
		started := time.Now()
		place, err := callGeoResolver(ctx, resolver, memory_geo.Query{Text: p.Query, Kind: p.Kind})
		timing := memory_core.ToolTiming{Provider: resolver.Name(), Started: started, Latency: time.Since(started)}
		switch {
		case errors.Is(err, memory_geo.ErrNotFound):
			log.WithFields(fields).WithField("entity", p.Entity.Name).Info("remember: the geo resolver does not know the place")
			j.tr.TimedTool("The geo resolver does not know the place.", "resolve_geo", args, map[string]any{"found": false}, timing)
			j.run.Output(StageResolveGeo, "resolve_geo", p.Entity.Name, map[string]any{"request": args, "found": false})
		case err != nil:
			r.Error = "resolve: " + err.Error()
			log.WithError(err).WithFields(fields).WithField("entity", p.Entity.Name).Warn("remember: the geo resolver failed")
			j.tr.TimedTool("The geo resolver failed.", "resolve_geo", args, map[string]any{"error": r.Error}, timing)
			j.run.Output(StageResolveGeo, "resolve_geo", p.Entity.Name, map[string]any{"request": args, "error": r.Error})
		default:
			r.Resolved, r.Latitude, r.Longitude, r.DisplayName = true, place.Latitude, place.Longitude, place.DisplayName
			r.Country, r.CountryCode, r.Region, r.Provider = place.Country, place.CountryCode, place.Region, place.Provider
			if r.Provider == "" {
				r.Provider = resolver.Name()
			}
			j.tr.TimedTool("The geo resolver found the place.", "resolve_geo", args, map[string]any{"found": true, "latitude": place.Latitude,
				"longitude": place.Longitude, "display_name": place.DisplayName, "country": place.Country}, timing)
			j.run.Output(StageResolveGeo, "resolve_geo", p.Entity.Name, map[string]any{"request": args, "found": true, "place": map[string]any{"latitude": place.Latitude, "longitude": place.Longitude,
				"kind": place.Kind, "display_name": place.DisplayName, "country": place.Country, "country_code": place.CountryCode,
				"region": place.Region, "type": place.Type, "bbox": place.BBox, "importance": place.Importance, "provider": r.Provider, "ref": place.Ref}})
			if p.ID != 0 {
				upserts = append(upserts, graph.NewEntityGeo{EntityID: p.ID, Kind: p.Kind, Query: p.Query, Latitude: place.Latitude,
					Longitude: place.Longitude, DisplayName: place.DisplayName, Country: place.Country, CountryCode: place.CountryCode,
					Region: place.Region, PlaceType: place.Type, BBox: place.BBox, Importance: place.Importance,
					Provider: r.Provider, ProviderRef: place.Ref, Now: j.now})
			}
		}
		results = append(results, r)
		if ctx.Err() != nil { // the call was cancelled: stop asking
			break
		}
	}

	//--------------------------------------
	// STORE: all the places in one transaction
	if len(upserts) > 0 {
		if err := j.storeGeo(ctx, upserts); err != nil {
			log.WithError(err).WithFields(fields).Warn("remember: storing the geo metadata failed")
			for i := range results {
				if results[i].Resolved {
					results[i].Resolved, results[i].Error = false, "store: "+err.Error()
				}
			}
			j.tr.Tool("Storing the geo metadata failed.", "store_geo", map[string]any{"places": len(upserts)}, map[string]any{"error": err.Error()})
			j.run.Output(StageResolveGeo, "store_geo", "", map[string]any{"places": len(upserts), "error": err.Error()})
			return results
		}
		j.tr.Tool("Stored the geo metadata (coordinates and where the place lies) of the places, keyed by their entities.", "store_geo",
			map[string]any{"user_id": j.userID, "dataset": j.dataset, "places": len(upserts)}, map[string]any{"stored": len(upserts)})
		j.run.Output(StageResolveGeo, "store_geo", "", map[string]any{"places": len(upserts), "stored": len(upserts)})
	}
	for i := range results { // a place whose entity could not be found has nothing to be stored against
		if results[i].Resolved && results[i].EntityID == 0 {
			results[i].Resolved, results[i].Error = false, "store: entity not found"
		}
	}
	if len(upserts) > 0 {
		fields["places"], fields["stored"] = len(places), len(upserts)
		log.WithFields(fields).Info("remember: geo metadata stored")
	}
	return results
}

// callGeoResolver runs the resolver with a timeout; a panic in it (the code of the embedding program) is an error, not a crash.
func callGeoResolver(ctx context.Context, r memory_geo.Resolver, q memory_geo.Query) (p *memory_geo.Place, err error) {
	ctx, cancel := context.WithTimeout(ctx, geoResolveTimeout)
	defer cancel()
	defer func() {
		if rec := recover(); rec != nil {
			p, err = nil, fmt.Errorf("geo resolver %q panicked: %v", r.Name(), rec)
		}
	}()
	p, err = r.Resolve(ctx, q)
	if err == nil && p == nil {
		return nil, fmt.Errorf("geo resolver %q returned no place and no error", r.Name())
	}
	if err == nil {
		err = p.Validate()
	}
	return p, err
}

// geoUnresolved returns the entities of cands (in order) that exist in the dataset and have no geo metadata yet, with the
// ID of each in gb_entities.
func (j *job) geoUnresolved(ctx context.Context, cands []*keyEntity) (map[*keyEntity]int64, []*keyEntity, error) {
	ids := map[*keyEntity]int64{}
	if len(cands) == 0 {
		return ids, nil, nil
	}
	dsID, err := relational.EnsureDataset(ctx, j.d, j.userID, j.dataset)
	if err != nil {
		return nil, nil, err
	}
	tx, err := graph.Begin(ctx, j.d)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var out []*keyEntity
	for _, c := range cands {
		id, err := tx.FindEntity(ctx, dsID, memory_core.Key(c.Name), c.Type)
		if errors.Is(err, graph.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if _, err := tx.GetEntityGeo(ctx, id); err == nil {
			continue // looked up before (by this or an earlier call)
		} else if !errors.Is(err, graph.ErrNotFound) {
			return nil, nil, err
		}
		ids[c] = id
		out = append(out, c)
	}
	return ids, out, nil
}

// storeGeo writes the geo metadata in one transaction.
func (j *job) storeGeo(ctx context.Context, geos []graph.NewEntityGeo) error {
	tx, err := graph.Begin(ctx, j.d)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, g := range geos {
		if err := tx.UpsertEntityGeo(ctx, g); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// geoPlacesOutput is how the places the LLM picked are filed in the run log (see runLog.Output).
func geoPlacesOutput(places []geoPlace) []map[string]any {
	out := make([]map[string]any, 0, len(places))
	for _, p := range places {
		out = append(out, map[string]any{"entity": p.Entity.Name, "kind": p.Kind, "query": p.Query})
	}
	return out
}
