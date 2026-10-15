package remember

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

const obsLLM = `{"entities":[{"name":"Apple close","type":"concept"}],"edges":[{"source":"Apple","target":"Apple close","relation":"had_series"}]}`

func TestRememberSeriesObservations(t *testing.T) { dbtest.Run(t, testRememberSeriesObservations) }

func testRememberSeriesObservations(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	var texts []string // what the extraction was asked to read
	llm := llmFunc(func(req ai.Request) (*ai.Response, error) {
		if strings.Contains(req.Messages[0].Content, "Observation of the timeseries") {
			texts = append(texts, req.Messages[0].Content)
			return &ai.Response{Text: obsLLM, StopReason: "end_turn", Model: "fake-model"}, nil
		}
		return &ai.Response{Text: seriesLLM, StopReason: "end_turn", Model: "fake-model"}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm))

	body := `{"user_id":"u1","kind":"episode","reference_time":"2025-01-31T21:00:00Z","text":"Apple fell after the earnings miss.","timeseries":{"observations":true,"series":[
		{"key":"AAPL.close","name":"Apple close","unit":"USD","points":[["2025-01-29T00:00:00Z",235.1],["2025-01-30T00:00:00Z",237.6],["2025-01-31T00:00:00Z",222]],
		 "anchors":[{"entity":{"name":"Apple","type":"organization"}}]},
		{"key":"AAPL.volume","points":[["2025-01-31T00:00:00Z",9000000]]}]}}`
	rec := seriesPost(t, mux, body)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res memory_remember.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 2 || len(res.SeriesObservations) != 2 || len(texts) != 2 {
		t.Fatalf("series %d, observations %d, texts %d: %s", len(res.Series), len(res.SeriesObservations), len(texts), rec.Body)
	}
	o := res.SeriesObservations[0]
	if o.Error != "" || o.ArtifactID == 0 || o.SeriesID != res.Series[0].ID || o.ParentArtifactID != res.ArtifactID || o.Key != "AAPL.close" ||
		o.Query != "AAPL.close from 2025-01-29 to 2025-01-31" || len(o.Edges) != 1 {
		t.Fatalf("observation: %+v", o)
	}
	for _, want := range []string{"for Apple (organization), from 2025-01-29 to 2025-01-31 (3 points)", "Apple close fell 5.57%", "<reference_time>\n2025-01-31T00:00:00Z"} {
		if !strings.Contains(texts[0], want) {
			t.Errorf("text lacks %q:\n%s", want, texts[0])
		}
	}

	// The artifact: a child of the artifact of the call, kind series_observation, with the series and the query.
	dsID, _ := relational.DatasetID(ctx, d, "u1", relational.DefaultDataset)
	arts, _ := graph.ListArtifacts(ctx, d, dsID)
	if len(arts) != 3 {
		t.Fatalf("artifacts: %+v", arts)
	}
	a := arts[1]
	if a.ID != o.ArtifactID || a.Kind != graph.ArtifactSeriesObservation || a.ParentID != res.ArtifactID || a.SeriesID != res.Series[0].ID ||
		a.Query != o.Query || !strings.Contains(a.Text, "Apple close fell 5.57%") || a.IsChunk() || !a.IsDerived() ||
		!a.ValidAt.Equal(time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("observation artifact: %+v", a)
	}
	if arts[0].SeriesID != 0 || arts[0].Kind != "episode" {
		t.Errorf("the text's artifact: %+v", arts[0])
	}
	// The series was not stored again by the observation's own store, and its edge links to the observation.
	if all, _ := graph.ListSeries(ctx, d, dsID); len(all) != 2 {
		t.Errorf("series: %d", len(all))
	}
	links, _ := graph.ListEdgeArtifacts(ctx, d, dsID)
	linked := false
	for _, l := range links {
		linked = linked || l.ArtifactID == o.ArtifactID
	}
	if !linked {
		t.Errorf("no edge is linked to the observation artifact: %+v", links)
	}
	if res.SeriesObservations[1].Key != "AAPL.volume" || strings.Contains(texts[1], " for ") {
		t.Errorf("second observation (about no entity): %+v\n%s", res.SeriesObservations[1], texts[1])
	}

	// Without the flag nothing is derived.
	rec = seriesPost(t, mux, strings.Replace(body, `"observations":true,`, "", 1))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "series_observations") {
		t.Errorf("without observations: %d %s", rec.Code, rec.Body)
	}
	if arts, _ = graph.ListArtifacts(ctx, d, dsID); len(arts) != 4 {
		t.Errorf("artifacts after a call without observations: %d", len(arts))
	}
}

func TestRememberSeriesObservationsFetched(t *testing.T) {
	dbtest.Run(t, testRememberSeriesObservationsFetched)
}

func testRememberSeriesObservationsFetched(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	providers, err := memory_core.ResolveSeriesProviders([]memory_core.SeriesProvider{{
		Name: "prices", Description: "Daily stock prices.",
		Fetch: func(context.Context, memory_core.SeriesFetchRequest) ([]memory_core.SeriesData, error) {
			return []memory_core.SeriesData{{Key: "AAPL", Field: "close", Name: "Apple close", Unit: "USD", Points: []memory_core.SeriesPoint{
				{TS: day("2025-01-30"), Value: 237.6}, {TS: day("2025-01-31"), Value: 222}}}}, nil
		}}})
	if err != nil {
		t.Fatal(err)
	}
	llm := llmFunc(func(req ai.Request) (*ai.Response, error) {
		out := seriesLLM
		switch {
		case strings.Contains(req.System, "timeseries providers"):
			out = `{"assignments":[{"entity":0,"provider":"prices","query":"AAPL"}]}`
		case strings.Contains(req.Messages[0].Content, "Observation of the timeseries"):
			if !strings.Contains(req.Messages[0].Content, "from the provider prices") || !strings.Contains(req.Messages[0].Content, "for Apple (organization)") {
				t.Errorf("observation text: %s", req.Messages[0].Content)
			}
			out = obsLLM
		}
		return &ai.Response{Text: out, StopReason: "end_turn", Model: "fake-model"}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm, memory_core.WithSeriesProviders(providers)))
	rec := seriesPost(t, mux, `{"user_id":"u1","reference_time":"2025-01-31T21:00:00Z","text":"Apple fell after the earnings miss.",
		"timeseries":{"fetch":{},"observations":true}}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res memory_remember.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.SeriesObservations) != 1 || res.SeriesObservations[0].Error != "" || res.SeriesObservations[0].ArtifactID == 0 {
		t.Fatalf("observations: %s", rec.Body)
	}
	o := res.SeriesObservations[0]
	dsID, _ := relational.DatasetID(ctx, d, "u1", relational.DefaultDataset)
	arts, _ := graph.ListArtifacts(ctx, d, dsID)
	var got graph.Artifact
	for _, a := range arts {
		if a.ID == o.ArtifactID {
			got = a
		}
	}
	if got.Kind != graph.ArtifactSeriesObservation || got.ParentID != res.ArtifactID || got.SeriesID != res.SeriesFetches[0].Series[0].ID {
		t.Errorf("artifact: %+v", got)
	}
}

func TestRememberSeriesObservationsLongText(t *testing.T) {
	dbtest.Run(t, testRememberSeriesObservationsLongText)
}

func testRememberSeriesObservationsLongText(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	llm := llmFunc(func(req ai.Request) (*ai.Response, error) {
		out := seriesLLM
		if strings.Contains(req.Messages[0].Content, "Observation of the timeseries") {
			out = obsLLM
		}
		return &ai.Response{Text: out, StopReason: "end_turn", Model: "fake-model"}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm, memory_core.WithChunking(chunk.Config{Size: 40, OverlapPercent: 20})))
	var paras []string
	for i := 0; i < 8; i++ {
		paras = append(paras, "Apple walked along the river and talked about the weather and its plans for the summer holidays.")
	}
	body, _ := json.Marshal(map[string]any{"user_id": "u1", "text": strings.Join(paras, "\n\n"), "timeseries": map[string]any{"observations": true,
		"series": []map[string]any{{"key": "AAPL.close", "points": [][]any{{"2025-01-31T00:00:00Z", 1.5}}}}}})
	rec := seriesPost(t, mux, string(body))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res memory_remember.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) < 2 || len(res.SeriesObservations) != 1 || res.SeriesObservations[0].ParentArtifactID != res.ArtifactID || res.SeriesObservations[0].ArtifactID == 0 {
		t.Fatalf("chunks %d: %s", len(res.Chunks), rec.Body)
	}
	// An observation is no chunk of the parent: the dataset still has as many chunks as the text made.
	dsID, _ := relational.DatasetID(ctx, d, "u1", relational.DefaultDataset)
	if c, _ := graph.DatasetCounts(ctx, d, dsID); c.Chunks != len(res.Chunks) || c.Artifacts != 1+2*len(res.Chunks)+1 {
		t.Errorf("counts: %+v, chunks %d", c, len(res.Chunks))
	}
}

func TestRememberSeriesObservationsRejected(t *testing.T) {
	dbtest.Run(t, testRememberSeriesObservationsRejected)
}

func testRememberSeriesObservationsRejected(t *testing.T, d *sql.DB) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, fakeLLM{seriesLLM}))
	// observations alone has nothing to observe.
	if rec := seriesPost(t, mux, `{"user_id":"u1","text":"x","timeseries":{"observations":true}}`); rec.Code != 400 {
		t.Errorf("observations alone: %d %s", rec.Code, rec.Body)
	}
}
