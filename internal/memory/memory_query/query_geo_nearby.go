package memory_query

// The proximity step of Query: the geo entities near the places of the question are given to the LLM that answers. It is OFF by
// default: the call has to ask for it (memory_core.WithGeoNearby, the geo_nearby or geo_radius_km of the request), and it needs a
// geo resolver (the geo switch of the server).
//
// The places are the anchors:
//   - the entities that matched the question (the seeds of the retrieval) and have coordinates in gb_entity_geo (they were
//     resolved when they were remembered), and
//   - the places the LLM found in the question (query_geo.go) that are not one of those: an entity of the dataset with that name
//     that already has coordinates is used as it is, and otherwise the geo resolver is asked for them, and a place it finds is
//     STORED (an entity of the type "place" if the dataset has none with that name, and its coordinates in gb_entity_geo), so the
//     next question with the same place finds it in the database, and it is a candidate for the nearby searches of later questions.
//     This is the one write of Query; a failure of it only means the place is not stored (it is still an anchor).
//
// For each anchor the geo index of the database finds the geo entities of the dataset within the radius (graph.NearbyEntityGeo;
// the radius is memory_core.DefaultGeoRadiusKm unless the config or the call sets it, see memory_core.WithGeoRadius). They and the
// edges around them are added to the graph the LLM reads, with a <nearby> section that says how far each is from which anchor.
// A failure here is logged and recorded, and never fails Query.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"

	log "github.com/sirupsen/logrus"
)

const (
	maxGeoAnchors      = 5  // places the search is centered on
	maxNearbyPerAnchor = 20 // nearest geo entities taken around each anchor
	maxNearbyEntities  = 30 // geo entities given to the LLM, however many anchors there are
	maxNearbyEdges     = 50 // edges around the nearby entities given to the LLM
	geoResolveTimeout  = 10 * time.Second
	// geoEntityType is the type of the entities that are made for the places of a question.
	geoEntityType = "place"
)

// GeoAnchor is a place the proximity search was centered on.
type GeoAnchor struct {
	// Name is the name of the entity, or the place as the question writes it.
	Name string `json:"name"`
	// EntityID is the entity; 0 only if the place is not an entity of the dataset and storing it failed.
	EntityID int64 `json:"entity_id,omitempty"`
	// Stored says the place was found in the question, resolved by the geo resolver and stored by this call (as an entity, or as the
	// coordinates of an entity that had none), so that later questions find it in the database.
	Stored bool `json:"stored,omitempty"`
	// Latitude and Longitude are in degrees.
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// NearbyEntity is a geo entity within the radius of an anchor.
type NearbyEntity struct {
	EntityID   int64   `json:"entity_id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	DistanceKm float64 `json:"distance_km"`
	// Anchor is the name of the nearest anchor, the one DistanceKm is from.
	Anchor string `json:"anchor"`
}

// GeoContext is what the proximity step found.
type GeoContext struct {
	// RadiusKm is the radius that was used.
	RadiusKm float64        `json:"radius_km"`
	Anchors  []GeoAnchor    `json:"anchors"`
	Nearby   []NearbyEntity `json:"nearby"`
}

// geoRadius returns the radius the call sets, or the default.
func geoRadius(co memory_core.CallOptions) (float64, error) {
	if co.GeoRadiusKm == 0 {
		return memory_core.DefaultGeoRadiusKm, nil
	}
	return co.GeoRadiusKm, memory_core.ValidateGeoRadius(co.GeoRadiusKm)
}

// findAnchors returns the places to center the search on, at most maxGeoAnchors: first the matched entities with coordinates,
// then the places of the question: those the dataset has as an entity with coordinates, and for the others the resolver is asked
// and what it finds is stored (storePlace). Errors only shorten the list.
func findAnchors(ctx context.Context, d *sql.DB, dsID int64, r memory_geo.Resolver, matched []int64, names map[int64]string, places []QueryPlace) (anchors []GeoAnchor, errs []string) {
	known := map[string]bool{}
	for _, id := range matched {
		if len(anchors) == maxGeoAnchors {
			break
		}
		g, err := graph.GetEntityGeo(ctx, d, id)
		if errors.Is(err, graph.ErrNotFound) {
			continue
		}
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		anchors = append(anchors, GeoAnchor{Name: names[id], EntityID: id, Latitude: g.Latitude, Longitude: g.Longitude})
		known[strings.ToLower(names[id])] = true
	}
	for _, p := range places {
		if len(anchors) == maxGeoAnchors {
			break
		}
		if known[strings.ToLower(p.Text)] {
			continue
		}
		known[strings.ToLower(p.Text)] = true
		// stored already (by remember, or by an earlier question) under that name, but not matched by this question's tokens
		if id, g, err := storedPlace(ctx, d, dsID, p.Text); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p.Text, err))
			continue
		} else if g != nil {
			anchors = append(anchors, GeoAnchor{Name: p.Text, EntityID: id, Latitude: g.Latitude, Longitude: g.Longitude})
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, geoResolveTimeout)
		pl, err := r.Resolve(rctx, memory_geo.Query{Text: p.Query, Kind: p.Kind})
		cancel()
		if err == nil {
			err = pl.Validate()
		}
		if errors.Is(err, memory_geo.ErrNotFound) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p.Text, err))
			continue
		}
		a := GeoAnchor{Name: p.Text, Latitude: pl.Latitude, Longitude: pl.Longitude}
		if id, err := storePlace(ctx, d, dsID, r, p, pl); err != nil {
			errs = append(errs, fmt.Sprintf("%s: could not store the place: %v", p.Text, err))
		} else {
			a.EntityID, a.Stored = id, true
		}
		anchors = append(anchors, a)
	}
	return anchors, errs
}

// storedPlace looks for an entity of the dataset named text (of a type that can be a place) and returns its ID and coordinates;
// the coordinates are nil if there is none or the entity has none.
func storedPlace(ctx context.Context, d *sql.DB, dsID int64, text string) (int64, *graph.EntityGeo, error) {
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback() // only reads
	id, err := tx.FindEntityID(ctx, dsID, memory_core.Key(text))
	if errors.Is(err, graph.ErrNotFound) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	if e, err := tx.GetEntity(ctx, dsID, id); err != nil || memory_geo.NotPlaceTypes[e.Type] {
		return 0, nil, err // an entity of that name that is not a place ("Paris", a person)
	}
	g, err := tx.GetEntityGeo(ctx, id)
	if errors.Is(err, graph.ErrNotFound) {
		return id, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	return id, g, nil
}

// storePlace stores a place of the question that the resolver found, in one transaction: the entity named p.Text (the one of the
// dataset if it has one of a type that can be a place, else a new entity of the type "place") and its coordinates. It returns the
// entity's ID.
func storePlace(ctx context.Context, d *sql.DB, dsID int64, r memory_geo.Resolver, p QueryPlace, pl *memory_geo.Place) (int64, error) {
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	key := memory_core.Key(p.Text)
	id, err := tx.FindEntityID(ctx, dsID, key)
	if err == nil {
		if e, gerr := tx.GetEntity(ctx, dsID, id); gerr != nil || memory_geo.NotPlaceTypes[e.Type] {
			id, err = 0, graph.ErrNotFound // an entity of that name that is not a place ("Paris", a person): a new one is made
		}
	}
	if errors.Is(err, graph.ErrNotFound) {
		id, err = tx.UpsertEntity(ctx, dsID, graph.Entity{Name: p.Text, Type: geoEntityType}, key)
	}
	if err != nil {
		return 0, err
	}
	provider := pl.Provider
	if provider == "" {
		provider = r.Name()
	}
	err = tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{EntityID: id, Kind: p.Kind, Query: p.Query, Latitude: pl.Latitude, Longitude: pl.Longitude,
		DisplayName: pl.DisplayName, Country: pl.Country, CountryCode: pl.CountryCode, Region: pl.Region, PlaceType: pl.Type, BBox: pl.BBox,
		Importance: pl.Importance, Provider: provider, ProviderRef: pl.Ref, Now: relational.Now()})
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// searchNearby is the one place that decides which geo entities are "near" some anchors: the proximity step of Query (nearbyGeo) and
// Nearby (GET /v1/gb/geo/nearby, and the geo view of the admin UI) both call it, so they always select the same entities. It returns
// the geo entities of the dataset within radiusKm of the anchors (see findNearby for the limits and the order); errs are the anchors
// that could not be searched.
func searchNearby(ctx context.Context, d *sql.DB, dsID int64, anchors []GeoAnchor, radiusKm float64) (nearby []NearbyEntity, errs []string) {
	return findNearby(ctx, d, dsID, anchors, radiusKm)
}

// findNearby returns the geo entities of the dataset within radiusKm of the anchors, nearest first, at most maxNearbyEntities. An
// entity near several anchors is listed once, with the nearest. The anchors that are entities are left out.
func findNearby(ctx context.Context, d *sql.DB, dsID int64, anchors []GeoAnchor, radiusKm float64) (out []NearbyEntity, errs []string) {
	var exclude []int64
	for _, a := range anchors {
		if a.EntityID != 0 {
			exclude = append(exclude, a.EntityID)
		}
	}
	best := map[int64]NearbyEntity{}
	for _, a := range anchors {
		res, err := graph.NearbyEntityGeo(ctx, d, dsID, a.Latitude, a.Longitude, radiusKm, maxNearbyPerAnchor, exclude...)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", a.Name, err))
			continue
		}
		for _, n := range res {
			if b, ok := best[n.EntityID]; ok && b.DistanceKm <= n.DistanceKm {
				continue
			}
			best[n.EntityID] = NearbyEntity{EntityID: n.EntityID, Name: n.EntityName, Type: n.EntityType, Latitude: n.Latitude,
				Longitude: n.Longitude, DistanceKm: math.Round(n.DistanceKm*10) / 10, Anchor: a.Name}
		}
	}
	for _, n := range best {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DistanceKm != out[j].DistanceKm {
			return out[i].DistanceKm < out[j].DistanceKm
		}
		return out[i].EntityID < out[j].EntityID
	})
	if len(out) > maxNearbyEntities {
		out = out[:maxNearbyEntities]
	}
	return out, errs
}

// addToGraph adds the nearby entities, and the edges around them with the entities at their other end, to g.
func addToGraph(ctx context.Context, d *sql.DB, dsID int64, g *graphContext, nearby []NearbyEntity) error {
	ids := make([]int64, 0, len(nearby))
	for _, n := range nearby {
		ids = append(ids, n.EntityID)
	}
	edges, err := graph.EdgesAround(ctx, d, dsID, ids, maxNearbyEdges)
	if err != nil {
		return err
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for _, e := range edges {
		want[e.SourceID], want[e.TargetID] = true, true
	}
	var missing []int64
	for id := range want {
		if _, ok := g.entities[id]; !ok {
			missing = append(missing, id)
		}
	}
	ents, err := graph.EntitiesByID(ctx, d, missing)
	if err != nil {
		return err
	}
	for _, e := range ents {
		g.entities[e.ID] = e
	}
	for _, e := range edges {
		g.edges[e.ID] = e
	}
	return nil
}

// nearbyGeo is the proximity step of Query (see the top of this file): it returns what it found, nil if the call has no geo
// resolver, did not ask for the search (co.GeoNearby) or there is no place to search around, and adds the nearby entities to rc.Graph.
// It never fails the call.
func nearbyGeo(ctx context.Context, d *sql.DB, co memory_core.CallOptions, tr *memory_core.Trace, userID, dataset string, dsID int64, rc *queryContext, places []QueryPlace, radiusKm float64) *GeoContext {
	if co.GeoResolver == nil || !co.GeoNearby {
		return nil
	}
	fields := log.Fields{"stage": "nearby_geo", "user_id": userID, "dataset": dataset}
	names := make(map[int64]string, len(rc.Graph.matched))
	for _, id := range rc.Graph.matched {
		names[id] = rc.Graph.entities[id].Name
	}
	anchors, errs := findAnchors(ctx, d, dsID, co.GeoResolver, rc.Graph.matched, names, places)
	var nearby []NearbyEntity
	if len(anchors) > 0 {
		var more []string
		nearby, more = searchNearby(ctx, d, dsID, anchors, radiusKm)
		errs = append(errs, more...)
	}
	if len(nearby) > 0 {
		if err := addToGraph(ctx, d, dsID, rc.Graph, nearby); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for _, e := range errs {
		log.WithFields(fields).Warn("query: proximity search: " + e)
	}
	if len(anchors) == 0 && len(errs) == 0 {
		return nil // nothing in the question is a place with coordinates: not worth a step in the trajectory
	}
	if anchors == nil {
		anchors = []GeoAnchor{}
	}
	if nearby == nil {
		nearby = []NearbyEntity{}
	}
	gc := &GeoContext{RadiusKm: radiusKm, Anchors: anchors, Nearby: nearby}
	out := map[string]any{"radius_km": radiusKm, "anchors": anchors, "nearby": nearby}
	if len(errs) > 0 {
		out["errors"] = errs
	}
	tr.Tool("Looked up the geo entities near the places of the question, to give the LLM as context.", "nearby_geo",
		map[string]any{"user_id": userID, "dataset": dataset, "radius_km": radiusKm}, out)
	rc.Nearby = gc
	return gc
}

// render returns the <nearby> section of the user message: how far each nearby entity is from the place it is near.
func (g *GeoContext) render() string {
	var sb strings.Builder
	for _, n := range g.Nearby {
		fmt.Fprintf(&sb, "[entity %d] %s is %s km from %s\n", n.EntityID, n.Name, strings.TrimSuffix(fmt.Sprintf("%.1f", n.DistanceKm), ".0"), n.Anchor)
	}
	return sb.String()
}
