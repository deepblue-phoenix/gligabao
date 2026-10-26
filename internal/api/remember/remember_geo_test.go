package remember

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

const geoLLM = `{"entities":[
	{"name":"Alice","type":"person"},
	{"name":"Paris","type":"place","description":"Capital of France"},
	{"name":"Acme","type":"organization"},
	{"name":"Atlantis","type":"place"}],
 "edges":[{"source":"Alice","target":"Paris","relation":"lives_in"}]}`

// stubResolver answers from a table and counts what it was asked.
type stubResolver struct {
	mu    sync.Mutex
	asked []geo.Query
	fail  bool
}

func (s *stubResolver) Name() string { return "stub" }
func (s *stubResolver) Resolve(_ context.Context, q geo.Query) (*geo.Place, error) {
	s.mu.Lock()
	s.asked = append(s.asked, q)
	s.mu.Unlock()
	if s.fail {
		return nil, errors.New("geocoder down")
	}
	switch q.Text {
	case "Paris, France":
		return &geo.Place{Latitude: 48.8566, Longitude: 2.3522, DisplayName: "Paris, Île-de-France, France", Country: "France", CountryCode: "fr",
			Region: "Île-de-France", Type: "city", BBox: &[4]float64{48.8, 2.2, 48.9, 2.5}, Importance: 0.9, Provider: "stub", Ref: "relation/7444"}, nil
	}
	return nil, geo.ErrNotFound
}

func TestRememberGeo(t *testing.T) { dbtest.Run(t, testRememberGeo) }

func testRememberGeo(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	var mu sync.Mutex
	var detectCalls int
	var detectPrompt string
	llm := llmFunc(func(req ai.Request) (*ai.Response, error) {
		out := geoLLM
		if strings.Contains(req.System, "geographic places") {
			mu.Lock()
			detectCalls++
			detectPrompt = req.Messages[0].Content
			mu.Unlock()
			// The 2nd candidate of the second call differs, so answer by name rather than by number.
			out = `{"places":[{"entity":0,"kind":"City","query":"Paris, France"},{"entity":0,"kind":"city","query":"dup"},{"entity":9,"kind":"city"}]}`
			if strings.Contains(req.Messages[0].Content, "[1] Atlantis") {
				out = `{"places":[{"entity":1,"kind":"region","query":""}]}`
			} else if strings.Contains(req.Messages[0].Content, "[2] Atlantis") {
				out = `{"places":[{"entity":0,"kind":"City","query":"Paris, France"},{"entity":2,"kind":"nonsense","query":""},{"entity":0,"kind":"city","query":"dup"},{"entity":9,"kind":"city"}]}`
			}
		}
		return &ai.Response{Text: out, StopReason: "end_turn", Model: "fake-model"}, nil
	})
	res := &stubResolver{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm, memory_core.WithGeoResolver(res)))
	body := `{"user_id":"u1","text":"Alice lives in Paris and works at Acme. Atlantis is a myth."}`

	rec := seriesPost(t, mux, body)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out memory_remember.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	// The LLM is shown the entities that may be places: not the person.
	for _, want := range []string{"[0] Paris | place | Capital of France", "[1] Acme | organization", "[2] Atlantis | place"} {
		if !strings.Contains(detectPrompt, want) {
			t.Errorf("the detect_geo prompt lacks %q:\n%s", want, detectPrompt)
		}
	}
	if strings.Contains(detectPrompt, "Alice") {
		t.Errorf("a person was offered:\n%s", detectPrompt)
	}
	if len(out.Geo) != 2 {
		t.Fatalf("geo results: %+v", out.Geo)
	}
	paris, atlantis := out.Geo[0], out.Geo[1]
	if !paris.Resolved || paris.Entity != "Paris" || paris.Kind != "city" || paris.Query != "Paris, France" || paris.Latitude != 48.8566 ||
		paris.Country != "France" || paris.CountryCode != "fr" || paris.Provider != "stub" || paris.EntityID == 0 || paris.Error != "" {
		t.Errorf("paris: %+v", paris)
	}
	if atlantis.Resolved || atlantis.Entity != "Atlantis" || atlantis.Kind != "place" || atlantis.Query != "Atlantis" || atlantis.Error != "" {
		t.Errorf("atlantis (unknown to the resolver, not a failure): %+v", atlantis)
	}

	// The metadata is stored against the entity.
	dsID, _ := relational.DatasetID(ctx, d, "u1", relational.DefaultDataset)
	all, err := graph.ListEntityGeo(ctx, d, dsID)
	if err != nil || len(all) != 1 {
		t.Fatalf("stored geo: %+v, %v", all, err)
	}
	g := all[0]
	if g.EntityID != paris.EntityID || g.EntityName != "Paris" || g.EntityType != "place" || g.Kind != "city" || g.Query != "Paris, France" ||
		g.Latitude != 48.8566 || g.Longitude != 2.3522 || g.DisplayName != "Paris, Île-de-France, France" || g.Country != "France" ||
		g.CountryCode != "fr" || g.Region != "Île-de-France" || g.PlaceType != "city" || g.Importance != 0.9 || g.Provider != "stub" ||
		g.ProviderRef != "relation/7444" || g.BBox == nil || *g.BBox != [4]float64{48.8, 2.2, 48.9, 2.5} || g.CreatedAt.IsZero() {
		t.Errorf("stored: %+v", g)
	}
	if one, err := graph.GetEntityGeo(ctx, d, paris.EntityID); err != nil || one.Latitude != 48.8566 {
		t.Errorf("GetEntityGeo: %+v, %v", one, err)
	}
	if _, err := graph.GetEntityGeo(ctx, d, paris.EntityID+1000); !errors.Is(err, graph.ErrNotFound) {
		t.Errorf("GetEntityGeo of another entity: %v", err)
	}

	// The run log has the stage, with the detect_geo inference.
	run, err := relational.GetRememberRun(ctx, d, out.RememberRunID)
	if err != nil {
		t.Fatal(err)
	}
	if st := run.Stages[relational.RememberStageResolveGeo]; st.Status != relational.RememberSucceeded || len(st.InferenceIDs) != 1 {
		t.Errorf("resolve_geo stage: %+v", st)
	} else if inf, err := relational.GetInference(ctx, d, st.InferenceIDs[0]); err != nil || inf.Stage != memory_core.StageDetectGeo {
		t.Errorf("inference: %+v, %v", inf, err)
	}

	// Remembering it again does not look Paris up a second time; Atlantis has no metadata, so it is asked again.
	rec = seriesPost(t, mux, body)
	if rec.Code != 200 {
		t.Fatalf("second status %d: %s", rec.Code, rec.Body)
	}
	if detectCalls != 2 || len(res.asked) != 3 {
		t.Errorf("detect calls %d (want 2), resolver asked %d times (want 3: Paris, Atlantis, Atlantis): %+v", detectCalls, len(res.asked), res.asked)
	}
	if all, _ = graph.ListEntityGeo(ctx, d, dsID); len(all) != 1 {
		t.Errorf("stored geo after the second call: %d", len(all))
	}
}

// A resolver that fails, and a call without one: neither fails Remember.
func TestRememberGeoFailureAndOff(t *testing.T) { dbtest.Run(t, testRememberGeoFailureAndOff) }

func testRememberGeoFailureAndOff(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	detects := 0
	llm := llmFunc(func(req ai.Request) (*ai.Response, error) {
		out := geoLLM
		if strings.Contains(req.System, "geographic places") {
			detects++
			out = `{"places":[{"entity":0,"kind":"city","query":"Paris, France"}]}`
		}
		return &ai.Response{Text: out, StopReason: "end_turn", Model: "fake-model"}, nil
	})
	body := `{"user_id":"u1","text":"Alice lives in Paris."}`

	// Off: no resolver, no detection, the stage is not started.
	off := http.NewServeMux()
	off.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm))
	rec := seriesPost(t, off, body)
	if rec.Code != 200 || detects != 0 || strings.Contains(rec.Body.String(), `"geo"`) {
		t.Fatalf("without a resolver: status %d, detect calls %d: %s", rec.Code, detects, rec.Body)
	}

	// Failing resolver: the call succeeds, the failure is in the result and the stage.
	on := http.NewServeMux()
	on.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm, memory_core.WithGeoResolver(&stubResolver{fail: true})))
	rec = seriesPost(t, on, `{"user_id":"u2","text":"Alice lives in Paris."}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out memory_remember.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Geo) != 1 || out.Geo[0].Resolved || !strings.Contains(out.Geo[0].Error, "geocoder down") {
		t.Fatalf("geo: %+v", out.Geo)
	}
	run, _ := relational.GetRememberRun(ctx, d, out.RememberRunID)
	if run.Status != relational.RememberSucceeded || run.Stages[relational.RememberStageResolveGeo].Status != relational.RememberFailed {
		t.Errorf("run %s, resolve_geo %s", run.Status, run.Stages[relational.RememberStageResolveGeo].Status)
	}
	dsID, _ := relational.DatasetID(ctx, d, "u2", relational.DefaultDataset)
	if all, _ := graph.ListEntityGeo(ctx, d, dsID); len(all) != 0 {
		t.Errorf("stored geo: %+v", all)
	}
}
