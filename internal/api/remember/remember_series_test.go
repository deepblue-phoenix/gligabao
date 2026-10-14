package remember

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

const seriesLLM = `{"entities":[{"name":"Apple","type":"organization"}],"edges":[{"source":"Apple","target":"Earnings miss","relation":"reported"}]}`

func seriesPost(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
	return rec
}

func TestRememberSeries(t *testing.T) { dbtest.Run(t, testRememberSeries) }

func testRememberSeries(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, fakeLLM{seriesLLM}))

	// One series with no anchors (anchored to the artifact), one anchored to an entity the text yields, one to an
	// entity it does not mention (created), one to the fact, and one to a fact that does not exist (ignored).
	rec := seriesPost(t, mux, `{"user_id":"u1","kind":"episode","reference_time":"2025-01-31T21:00:00Z","text":"Apple fell after the earnings miss.","timeseries":{"series":[
		{"key":"AAPL.close","unit":"USD","frequency":"1d","points":[["2025-01-30T00:00:00Z",237.6],["2025-01-29T00:00:00Z",235.1],["2025-01-31T00:00:00Z",222.0]]},
		{"key":"AAPL.volume","anchors":[{"entity":{"name":" apple ","type":"Organization"},"role":"volume","window":["2025-01-01T00:00:00Z","2025-02-01T00:00:00Z"]}],"points":[["2025-01-31T00:00:00Z",9000000]]},
		{"key":"MSFT.close","anchors":[{"entity":{"name":"Microsoft","type":"organization"}}],"points":[["2025-01-31T00:00:00Z",410.5]]},
		{"key":"AAPL.reported","anchors":[{"edge":{"source":"Apple","relation":"Reported","target":"Earnings miss"}},{"edge":{"source":"Apple","relation":"bought","target":"Nobody"}}],"points":[["2025-01-31T00:00:00Z",1]]}
	]}}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res memory_remember.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 4 {
		t.Fatalf("series: %s", rec.Body)
	}
	dsID, _ := relational.DatasetID(ctx, d, "u1", relational.DefaultDataset)

	price := res.Series[0]
	if !price.Created || price.Points != 3 || price.PointCount != 3 || price.Field != "value" || price.Name != "AAPL.close" || price.Unit != "USD" {
		t.Errorf("price series: %+v", price)
	}
	if len(price.Anchors) != 1 || price.Anchors[0].Type != graph.AnchorArtifact || price.Anchors[0].ID != res.ArtifactID {
		t.Errorf("price anchors (want the artifact %d): %+v", res.ArtifactID, price.Anchors)
	}
	st := price.Stats
	if st == nil || st.First != 235.1 || st.Last != 222.0 || st.Min != 222.0 || st.Max != 237.6 || st.Trend != "down" || st.LargestMove == nil || st.LargestMove.To.Format("2006-01-02") != "2025-01-31" {
		t.Errorf("price stats: %+v", st)
	}

	// The points are stored sorted, whatever the order sent.
	pts, err := graph.SeriesPoints(ctx, d, price.ID, nil, nil, 0)
	if err != nil || len(pts) != 3 || pts[0].Value != 235.1 || pts[1].Value != 237.6 || pts[2].Value != 222.0 {
		t.Fatalf("points: %+v, %v", pts, err)
	}

	// Entity selector: matched the entity of the text (normalized name, type lowercased), with role and window.
	ents, _ := graph.ListEntities(ctx, d, dsID)
	var appleID, msftID int64
	for _, e := range ents {
		switch e.Name {
		case "Apple":
			appleID = e.ID
		case "Microsoft":
			msftID = e.ID
		}
	}
	vol := res.Series[1]
	if len(vol.Anchors) != 1 || vol.Anchors[0].Type != graph.AnchorEntity || vol.Anchors[0].ID != appleID || vol.Anchors[0].Created || vol.Anchors[0].Role != "volume" || vol.Anchors[0].Window == "" {
		t.Errorf("volume anchors (want Apple %d): %+v", appleID, vol.Anchors)
	}
	stored, err := graph.ListSeriesAnchors(ctx, d, vol.ID)
	if err != nil || len(stored) != 1 || stored[0].WindowStart == nil || stored[0].WindowStart.Format("2006-01-02") != "2025-01-01" || stored[0].WindowEnd.Format("2006-01-02") != "2025-02-01" {
		t.Errorf("stored anchors: %+v, %v", stored, err)
	}

	// An entity the text never mentioned is created by the anchor, and is not linked to the artifact.
	msft := res.Series[2]
	if msftID == 0 || len(msft.Anchors) != 1 || msft.Anchors[0].ID != msftID || !msft.Anchors[0].Created {
		t.Errorf("msft anchors (want created %d): %+v", msftID, msft.Anchors)
	}
	links, _ := graph.ListEntityArtifacts(ctx, d, dsID)
	for _, l := range links {
		if l.EntityID == msftID {
			t.Errorf("entity %d created by an anchor is linked to artifact %d", msftID, l.ArtifactID)
		}
	}

	// Edge selector: found (relation normalized); the missing fact is skipped.
	fact := res.Series[3]
	if len(fact.Anchors) != 1 || fact.Anchors[0].Type != graph.AnchorEdge || fact.Anchors[0].ID != res.Edges[0].ID || len(fact.Skipped) != 1 {
		t.Errorf("fact anchors: %+v skipped %v (edge %d)", fact.Anchors, fact.Skipped, res.Edges[0].ID)
	}

	// Sending the key again adds points (and replaces the value at a known time) and re-summarizes everything.
	rec = seriesPost(t, mux, `{"user_id":"u1","text":"Apple fell after the earnings miss.","timeseries":{"series":[
		{"key":"AAPL.close","points":[["2025-01-31T00:00:00Z",220.0],["2025-02-03T00:00:00Z",225.0]]}]}}`)
	if rec.Code != 200 {
		t.Fatalf("second: status %d: %s", rec.Code, rec.Body)
	}
	var res2 memory_remember.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res2); err != nil {
		t.Fatal(err)
	}
	again := res2.Series[0]
	if again.Created || again.ID != price.ID || again.Points != 2 || again.PointCount != 4 || again.Stats.Last != 225.0 || again.Stats.Min != 220.0 {
		t.Errorf("second series: %+v %+v", again, again.Stats)
	}
	if again.Anchors[0].ID != res2.ArtifactID || again.Anchors[0].ID == res.ArtifactID {
		t.Errorf("second anchors: %+v", again.Anchors)
	}
	as, _ := graph.ListSeriesAnchors(ctx, d, price.ID)
	if len(as) != 2 { // both artifacts
		t.Errorf("anchors of the price series: %+v", as)
	}
	if all, _ := graph.ListSeries(ctx, d, dsID); len(all) != 4 {
		t.Errorf("series stored: %d", len(all))
	}
}

func TestRememberSeriesRejected(t *testing.T) { dbtest.Run(t, testRememberSeriesRejected) }

func testRememberSeriesRejected(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, fakeLLM{seriesLLM}))
	pre := `{"user_id":"u1","text":"Apple fell.","timeseries":`
	for name, c := range map[string]struct {
		body string
		code int
	}{
		"no series":         {pre + `{}}`, 400},
		"fetch no provider": {pre + `{"fetch":{"providers":["prices"]},"series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]]}]}}`, 503},
		"no key":            {pre + `{"series":[{"points":[["2025-01-31T00:00:00Z",1]]}]}}`, 400},
		"no points":         {pre + `{"series":[{"key":"k","points":[]}]}}`, 400},
		"bad time":          {pre + `{"series":[{"key":"k","points":[["yesterday",1]]}]}}`, 400},
		"bad point":         {pre + `{"series":[{"key":"k","points":[["2025-01-31T00:00:00Z"]]}]}}`, 400},
		"duplicate key":     {pre + `{"series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]]},{"key":"k","points":[["2025-01-31T00:00:00Z",1]]}]}}`, 400},
		"two selectors":     {pre + `{"series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]],"anchors":[{"entity":{"name":"a"},"entity_id":1}]}]}}`, 400},
		"no selector":       {pre + `{"series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]],"anchors":[{"role":"x"}]}]}}`, 400},
		"create for edge":   {pre + `{"series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]],"anchors":[{"edge":{"source":"a","relation":"b","target":"c"},"on_unmatched":"create"}]}]}}`, 400},
		"bad window":        {pre + `{"series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]],"anchors":[{"entity":{"name":"a"},"window":["2025-02-01T00:00:00Z","2025-01-01T00:00:00Z"]}]}]}}`, 400},
		"unmatched error":   {pre + `{"series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]],"anchors":[{"entity":{"name":"Nobody"},"on_unmatched":"error"}]}]}}`, 422},
		"entity id absent":  {pre + `{"series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]],"anchors":[{"entity_id":999}]}]}}`, 422},
	} {
		rec := seriesPost(t, mux, c.body)
		if rec.Code != c.code {
			t.Errorf("%s: status %d, want %d: %s", name, rec.Code, c.code, rec.Body)
		}
	}

	// A 422 comes from a series that could not be anchored: nothing of the call is stored, not even the text.
	if dsID, err := relational.DatasetID(ctx, d, "u1", relational.DefaultDataset); err == nil {
		c, _ := graph.DatasetCounts(ctx, d, dsID)
		if c != (graph.Counts{}) {
			t.Errorf("counts after rejected calls: %+v", c)
		}
		if s, _ := graph.ListSeries(ctx, d, dsID); len(s) != 0 {
			t.Errorf("series after rejected calls: %+v", s)
		}
	}
}

func TestRememberSeriesLongText(t *testing.T) { dbtest.Run(t, testRememberSeriesLongText) }

func testRememberSeriesLongText(t *testing.T, d *sql.DB) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, fakeLLM{seriesLLM}, memory_core.WithChunking(chunk.Config{Size: 40, OverlapPercent: 20})))
	var paras []string
	for i := 0; i < 8; i++ {
		paras = append(paras, "Apple walked along the river and talked about the weather and its plans for the summer holidays.")
	}
	body, _ := json.Marshal(map[string]any{"user_id": "u1", "text": strings.Join(paras, "\n\n"), "timeseries": map[string]any{"series": []map[string]any{
		{"key": "AAPL.close", "points": [][]any{{"2025-01-31T00:00:00Z", 1.5}}},
		{"key": "AAPL.volume", "points": [][]any{{"2025-01-31T00:00:00Z", 2}}, "anchors": []map[string]any{{"entity": map[string]any{"name": "Apple"}}}},
	}}})
	rec := seriesPost(t, mux, string(body))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res memory_remember.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) < 2 || len(res.Series) != 2 {
		t.Fatalf("chunks %d, series %d: %s", len(res.Chunks), len(res.Series), rec.Body)
	}
	// Anchored to the parent artifact, and the entity selector found Apple, which only the chunks yielded.
	if a := res.Series[0].Anchors; len(a) != 1 || a[0].Type != graph.AnchorArtifact || a[0].ID != res.ArtifactID {
		t.Errorf("anchors (want the parent %d): %+v", res.ArtifactID, a)
	}
	if a := res.Series[1].Anchors; len(a) != 1 || a[0].Type != graph.AnchorEntity || a[0].Created {
		t.Errorf("entity anchors: %+v", a)
	}
}

func TestRememberSeriesImage(t *testing.T) { dbtest.Run(t, testRememberSeriesImage) }

func testRememberSeriesImage(t *testing.T, d *sql.DB) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, fakeLLM{seriesLLM}))
	rec := seriesPost(t, mux, `{"user_id":"u1","image":"aGVsbG8=","timeseries":{"series":[{"key":"k","points":[["2025-01-31T00:00:00Z",1]]}]}}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "timeseries") {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
}
