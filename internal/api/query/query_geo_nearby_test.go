package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// mapResolver knows a few places by the query it is asked for, and records the queries.
type mapResolver struct {
	mu     sync.Mutex
	places map[string][2]float64
	asked  []string
}

func (m *mapResolver) Name() string { return "map" }
func (m *mapResolver) Resolve(_ context.Context, q geo.Query) (*geo.Place, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, q.Text)
	c, ok := m.places[q.Text]
	if !ok {
		return nil, geo.ErrNotFound
	}
	return &geo.Place{Latitude: c[0], Longitude: c[1], DisplayName: q.Text, Provider: "map"}, nil
}

// TestQueryGeoNearby checks the proximity step of query: it is off unless the request asks (geo_nearby, geo_radius_km); then the geo
// entities within the radius of the entities that match the question and of the places it mentions are given to the answering LLM,
// the places of the question that the resolver finds are stored, the radius is the default one, the server's, or the request's, and
// a bad one is a 400.
func TestQueryGeoNearby(t *testing.T) { dbtest.Run(t, testQueryGeoNearby) }

func testQueryGeoNearby(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	dsID, err := relational.EnsureDataset(ctx, d, "u2", "main")
	check(err)
	now := relational.Now()
	tx, err := graph.Begin(ctx, d)
	check(err)
	defer tx.Rollback()
	ent := func(name, typ string, lat, lon float64) int64 {
		id, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: name, Type: typ, Description: name + " description"}, name)
		check(err)
		if typ == "place" {
			check(tx.UpsertEntityGeo(ctx, graph.NewEntityGeo{EntityID: id, Kind: "city", Latitude: lat, Longitude: lon, Provider: "stub", Now: now}))
		}
		return id
	}
	ent("Paris", "place", 48.8566, 2.3522)
	versailles := ent("Versailles", "place", 48.8049, 2.1204) // 17 km from Paris
	ent("Reims", "place", 49.2583, 4.0317)                    // 130 km
	ent("Villeurbanne", "place", 45.7665, 4.8795)             // 5 km from Lyon
	alice := ent("Alice", "person", 0, 0)
	artID, err := tx.InsertArtifact(ctx, dsID, graph.ArtifactEpisode, "text", "", now, now)
	check(err)
	_, err = tx.InsertEdge(ctx, graph.NewEdge{DatasetID: dsID, SourceID: alice, TargetID: versailles, Relation: "lives_in",
		Description: "Alice lives in Versailles", ValidAt: now, CreatedAt: now, ArtifactID: artID})
	check(err)
	check(tx.Commit())

	llm := &geoStubLLM{places: `{"places":[{"text":"Lyon","kind":"city","query":"Lyon, France"},{"text":"Paris","kind":"city","query":"Paris, France"}]}`}
	resolver := &mapResolver{places: map[string][2]float64{"Lyon, France": {45.764, 4.8357}, "Paris, France": {48.8566, 2.3522}}}
	post := func(h http.HandlerFunc, body string) (*httptest.ResponseRecorder, string) {
		t.Helper()
		llm.reqs = nil
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/query", strings.NewReader(body)))
		var user string
		if r := llm.requestOf("ONLY a knowledge graph"); r != nil {
			user = r.Messages[0].Content
		}
		return rec, user
	}
	const q = `"query":"What is near Paris and Lyon?"`
	h := Handler(d, llm, memory_core.WithGeoResolver(resolver))
	entityNamed := func(name string) (id int64, g *graph.EntityGeo) {
		t.Helper()
		tx, err := graph.Begin(ctx, d)
		check(err)
		defer tx.Rollback()
		id, err = tx.FindEntityID(ctx, dsID, memory_core.Key(name))
		if err != nil {
			return 0, nil
		}
		g, err = tx.GetEntityGeo(ctx, id)
		if err != nil {
			g = nil
		}
		return id, g
	}

	// OFF BY DEFAULT: no nearby section, the resolver is not asked, nothing is stored.
	rec, user := post(h, `{"user_id":"u2",`+q+`}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(user, "<nearby") || strings.Contains(rec.Body.String(), `"nearby"`) || len(resolver.asked) != 0 {
		t.Errorf("nearby by default: asked %v\n%s\n%s", resolver.asked, user, rec.Body)
	}
	if id, _ := entityNamed("Lyon"); id != 0 {
		t.Error("Lyon was stored by a query that did not ask for the nearby search")
	}
	if _, user := post(h, `{"user_id":"u2",`+q+`,"geo_nearby":false}`); strings.Contains(user, "<nearby") {
		t.Errorf("geo_nearby false:\n%s", user)
	}

	// geo_nearby: the default radius, 50 km: Versailles (near the entity Paris) and Villeurbanne (near Lyon, which is only in the question).
	rec, user = post(h, `{"user_id":"u2",`+q+`,"geo_nearby":true}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	nearby := user[strings.Index(user, "<nearby"):strings.Index(user, "</nearby>")]
	if !strings.HasPrefix(nearby, `<nearby radius_km="50">`) || !strings.Contains(nearby, "Versailles is 17.9 km from Paris") ||
		!strings.Contains(nearby, "Villeurbanne is 3.") || !strings.Contains(nearby, "km from Lyon") || strings.Contains(nearby, "Reims") {
		t.Errorf("nearby section:\n%s", nearby)
	}
	if !strings.Contains(user, "Alice lives in Versailles") { // the facts around the nearby entities come along
		t.Errorf("the edge of a nearby entity is not in the graph:\n%s", user)
	}
	if strings.Contains(user, "Reims") {
		t.Errorf("Reims is out of the radius:\n%s", user)
	}
	// Paris is an entity with coordinates: the resolver is only asked for Lyon.
	if strings.Join(resolver.asked, "|") != "Lyon, France" {
		t.Errorf("the resolver was asked %v", resolver.asked)
	}
	var res struct {
		Nearby struct {
			RadiusKm float64 `json:"radius_km"`
			Anchors  []struct {
				Name     string
				EntityID int64 `json:"entity_id"`
				Stored   bool
			}
			Nearby []struct {
				Name   string
				Anchor string
			}
		}
	}
	check(json.Unmarshal(rec.Body.Bytes(), &res))
	if res.Nearby.RadiusKm != 50 || len(res.Nearby.Anchors) != 2 || len(res.Nearby.Nearby) != 2 || res.Nearby.Nearby[0].Name != "Villeurbanne" {
		t.Errorf("response: %s", rec.Body)
	}
	if a := res.Nearby.Anchors; a[0].Stored || !a[1].Stored || a[1].Name != "Lyon" || a[1].EntityID == 0 {
		t.Errorf("anchors: %+v", a)
	}

	// The place of the question was stored: an entity "Lyon" of the type place, with its coordinates.
	lyon, g := entityNamed("Lyon")
	if lyon == 0 || g == nil || g.Latitude != 45.764 || g.Longitude != 4.8357 || g.Kind != "city" || g.Query != "Lyon, France" || g.Provider != "map" {
		t.Fatalf("Lyon after the query: id %d, geo %+v", lyon, g)
	}
	if e, err := graph.EntitiesByID(ctx, d, []int64{lyon}); err != nil || len(e) != 1 || e[0].Type != "place" || e[0].Name != "Lyon" {
		t.Errorf("Lyon entity: %+v, %v", e, err)
	}
	// ... so the same question finds it in the database: the resolver is not asked again, and it is not stored again.
	resolver.asked = nil
	rec, user = post(h, `{"user_id":"u2",`+q+`,"geo_nearby":true}`)
	if len(resolver.asked) != 0 || !strings.Contains(user, "km from Lyon") || strings.Contains(rec.Body.String(), `"stored"`) {
		t.Errorf("second ask: the resolver was asked %v\n%s\n%s", resolver.asked, user, rec.Body)
	}

	// The radius: the request sets it, and setting it turns the search on by itself.
	_, user = post(h, `{"user_id":"u2",`+q+`,"geo_radius_km":200}`)
	if !strings.Contains(user, `<nearby radius_km="200">`) || !strings.Contains(user, "Reims is 1") {
		t.Errorf("radius 200:\n%s", user)
	}
	_, user = post(h, `{"user_id":"u2",`+q+`,"geo_nearby":true,"geo_radius_km":1}`)
	if strings.Contains(user, "<nearby") {
		t.Errorf("radius 1 finds nothing:\n%s", user)
	}

	// The server's default radius (it does not turn the search on), which the request overrides.
	h10 := Handler(d, llm, memory_core.WithGeoResolver(resolver), memory_core.WithGeoRadius(10))
	if _, user = post(h10, `{"user_id":"u2",`+q+`}`); strings.Contains(user, "<nearby") {
		t.Errorf("a server radius turned the search on:\n%s", user)
	}
	_, user = post(h10, `{"user_id":"u2",`+q+`,"geo_nearby":true}`)
	if !strings.Contains(user, `<nearby radius_km="10">`) || strings.Contains(user, "Versailles is") {
		t.Errorf("server radius 10:\n%s", user)
	}
	_, user = post(h10, `{"user_id":"u2",`+q+`,"geo_radius_km":25}`)
	if !strings.Contains(user, `<nearby radius_km="25">`) || !strings.Contains(user, "Versailles is 17.9") {
		t.Errorf("request radius 25 over server's 10:\n%s", user)
	}

	// A place that is an entity of the dataset without coordinates gets them; an entity of that name that is a person is left alone
	// and a new place is made.
	resolver.places["Lisbon, Portugal"] = [2]float64{38.7223, -9.1393}
	resolver.places["Orange, France"] = [2]float64{44.138, 4.8067}
	tx, err = graph.Begin(ctx, d)
	check(err)
	lisbon, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Lisbon", Type: "organization"}, "lisbon")
	check(err)
	orangePerson, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Orange", Type: "person"}, "orange")
	check(err)
	check(tx.Commit())
	llm.places = `{"places":[{"text":"Lisbon","kind":"city","query":"Lisbon, Portugal"},{"text":"Orange","kind":"city","query":"Orange, France"}]}`
	rec, _ = post(h, `{"user_id":"u2","query":"What is near Lisbon and Orange?","geo_nearby":true}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if id, g := entityNamed("Lisbon"); id != lisbon || g == nil || g.Latitude != 38.7223 {
		t.Errorf("Lisbon: id %d (was %d), geo %+v", id, lisbon, g)
	}
	if _, err := graph.GetEntityGeo(ctx, d, orangePerson); err == nil {
		t.Error("the person Orange got coordinates")
	}
	var orangePlace int64
	if err := d.QueryRow(`SELECT id FROM gb_entities WHERE dataset_id = ` + fmt.Sprint(dsID) + ` AND name = 'Orange' AND type = 'place'`).Scan(&orangePlace); err != nil {
		t.Fatalf("no place Orange: %v", err)
	}
	if g, err := graph.GetEntityGeo(ctx, d, orangePlace); err != nil || g.Latitude != 44.138 {
		t.Errorf("place Orange: %+v, %v", g, err)
	}
	llm.places = `{"places":[{"text":"Lyon","kind":"city","query":"Lyon, France"},{"text":"Paris","kind":"city","query":"Paris, France"}]}`

	// A bad radius, or a contradiction, is a 400.
	for _, r := range []string{`0`, `-5`, `20016`, `"far"`} {
		if rec, _ := post(h, `{"user_id":"u2",`+q+`,"geo_radius_km":`+r+`}`); rec.Code != 400 {
			t.Errorf("geo_radius_km %s: status %d", r, rec.Code)
		}
	}
	if rec, _ := post(h, `{"user_id":"u2",`+q+`,"geo_nearby":false,"geo_radius_km":5}`); rec.Code != 400 {
		t.Errorf("geo_nearby false with a radius: status %d", rec.Code)
	}

	// Without a geo resolver the server cannot do it: a request that asks for it is a 400, one that does not is as before.
	for _, body := range []string{`,"geo_nearby":true`, `,"geo_radius_km":200`} {
		if rec, _ := post(Handler(d, llm), `{"user_id":"u2",`+q+body+`}`); rec.Code != 400 {
			t.Errorf("no geo on the server, %s: status %d", body, rec.Code)
		}
	}
	if rec, user := post(Handler(d, llm), `{"user_id":"u2",`+q+`}`); rec.Code != 200 || strings.Contains(user, "<nearby") {
		t.Errorf("no geo on the server: %d\n%s", rec.Code, user)
	}
}
