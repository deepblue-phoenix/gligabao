package timeseries

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_timeseries"
)

func TestTimeseries(t *testing.T) { dbtest.Run(t, testTimeseries) }

func testTimeseries(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/timeseries", Attach(d))
	mux.HandleFunc("GET /v1/gb/timeseries", List(d))
	mux.HandleFunc("GET /v1/gb/timeseries/{id}", Get(d))
	do := func(method, url, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, url, strings.NewReader(body)))
		return rec
	}

	// The dataset must exist.
	if rec := do("POST", "/v1/gb/timeseries", `{"user_id":"u1","series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]]}]}`); rec.Code != 404 {
		t.Fatalf("missing dataset: status %d: %s", rec.Code, rec.Body)
	}

	// Something stored to anchor to.
	dsID, err := relational.EnsureDataset(ctx, d, "u1", "main")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	now := relational.Now()
	artID, _ := tx.InsertArtifact(ctx, dsID, graph.ArtifactEpisode, "Apple fell.", "", now, now)
	entID, err := tx.UpsertEntity(ctx, dsID, graph.Entity{Name: "Apple", Type: "organization"}, "apple")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Attach by ID and by selector, with a window; the standalone endpoint has no artifact of its own.
	rec := do("POST", "/v1/gb/timeseries", `{"user_id":"u1","series":[{"key":"AAPL.close","unit":"USD","frequency":"1d",
		"points":[["2025-01-29T00:00:00Z",235.1],["2025-01-30T00:00:00Z",237.6],["2025-01-31T00:00:00Z",222.0]],
		"anchors":[{"entity":{"name":"Apple"}},{"artifact_id":`+itoa(artID)+`,"window":["2025-01-30T00:00:00Z","2025-01-31T00:00:00Z"],"role":"event"}]},
		{"key":"unanchored","points":[["2025-01-31T00:00:00Z",5]]}]}`)
	if rec.Code != 200 {
		t.Fatalf("attach: status %d: %s", rec.Code, rec.Body)
	}
	var att struct {
		Dataset string
		Series  []memory_timeseries.Result
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &att); err != nil {
		t.Fatal(err)
	}
	if att.Dataset != "main" || len(att.Series) != 2 {
		t.Fatalf("attach: %s", rec.Body)
	}
	a := att.Series[0]
	if len(a.Anchors) != 2 || a.Anchors[0].Type != graph.AnchorEntity || a.Anchors[0].ID != entID || a.Anchors[1].Type != graph.AnchorArtifact || a.Anchors[1].ID != artID || a.Anchors[1].Role != "event" {
		t.Errorf("anchors: %+v", a.Anchors)
	}
	if len(att.Series[1].Anchors) != 0 {
		t.Errorf("unanchored series has anchors: %+v", att.Series[1].Anchors)
	}

	// Rejected: the artifact flag (nothing to anchor to), an unknown ID, a malformed series.
	for body, code := range map[string]int{
		`{"user_id":"u1","series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]],"anchors":[{"artifact":true}]}]}`:   400,
		`{"user_id":"u1","series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]],"anchors":[{"entity_id":999}]}]}`:   422,
		`{"user_id":"u1","series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]],"anchors":[{"artifact_id":999}]}]}`: 422,
		`{"user_id":"u1","series":[]}`: 400,
		`{"user_id":"","series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]]}]}`: 400,
		`{`: 400,
	} {
		if rec := do("POST", "/v1/gb/timeseries", body); rec.Code != code {
			t.Errorf("%s: status %d, want %d: %s", body, rec.Code, code, rec.Body)
		}
	}
	if all, _ := graph.ListSeries(ctx, d, dsID); len(all) != 2 { // the rejected ones left nothing
		t.Errorf("series stored: %d", len(all))
	}

	// List: stats and anchors, no points.
	rec = do("GET", "/v1/gb/timeseries?user_id=u1", "")
	var list struct {
		Series []struct {
			ID         int64
			Key        string
			PointCount int `json:"point_count"`
			Stats      struct{ Trend string }
			Anchors    []struct{ Type string }
			Points     []any
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 {
		t.Fatalf("list: %d %v %s", rec.Code, err, rec.Body)
	}
	if len(list.Series) != 2 || list.Series[0].Key != "AAPL.close" || list.Series[0].PointCount != 3 || list.Series[0].Stats.Trend != "down" || len(list.Series[0].Anchors) != 2 || list.Series[0].Points != nil {
		t.Errorf("list: %s", rec.Body)
	}

	// Get: all the points, a window (with its own stats), a limit.
	url := "/v1/gb/timeseries/" + itoa(a.ID) + "?user_id=u1"
	type got struct {
		Points      [][]any
		Truncated   bool
		Stats       struct{ Count int }
		WindowStats *struct {
			Count int
			Min   float64
		} `json:"window_stats"`
	}
	var g got
	rec = do("GET", url, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil || rec.Code != 200 || len(g.Points) != 3 || g.Truncated || g.WindowStats != nil || g.Stats.Count != 3 {
		t.Fatalf("get: %d %v %s", rec.Code, err, rec.Body)
	}
	if g.Points[0][0] != "2025-01-29T00:00:00Z" || g.Points[0][1] != 235.1 {
		t.Errorf("first point: %v", g.Points[0])
	}
	g = got{}
	rec = do("GET", url+"&from=2025-01-30T00:00:00Z", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil || len(g.Points) != 2 || g.WindowStats == nil || g.WindowStats.Count != 2 || g.WindowStats.Min != 222.0 {
		t.Fatalf("get window: %d %v %s", rec.Code, err, rec.Body)
	}
	g = got{}
	rec = do("GET", url+"&limit=2", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil || len(g.Points) != 2 || !g.Truncated {
		t.Fatalf("get limit: %d %v %s", rec.Code, err, rec.Body)
	}
	for u, code := range map[string]int{
		"/v1/gb/timeseries/999?user_id=u1":             404,
		"/v1/gb/timeseries/abc?user_id=u1":             404,
		"/v1/gb/timeseries/1?user_id=u2":               404, // another user's dataset
		url + "&from=yesterday":                        400,
		url + "&limit=0":                               400,
		"/v1/gb/timeseries?user_id=u1&dataset=nothing": 404,
	} {
		if rec := do("GET", u, ""); rec.Code != code {
			t.Errorf("GET %s: status %d, want %d: %s", u, rec.Code, code, rec.Body)
		}
	}

	// Deleting the artifact or the entity removes only the anchor: the series stays.
	if _, err := d.ExecContext(ctx, `DELETE FROM gb_artifacts WHERE id = `+itoa(artID)); err != nil {
		t.Fatal(err)
	}
	if anchors, _ := graph.ListSeriesAnchors(ctx, d, a.ID); len(anchors) != 1 || anchors[0].Type != graph.AnchorEntity {
		t.Errorf("anchors after deleting the artifact: %+v", anchors)
	}
	if _, err := graph.GetSeries(ctx, d, dsID, a.ID); err != nil {
		t.Errorf("series gone with its artifact: %v", err)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
