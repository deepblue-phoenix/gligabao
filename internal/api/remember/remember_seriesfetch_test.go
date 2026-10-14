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
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

func day(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }

func TestRememberSeriesFetch(t *testing.T) { dbtest.Run(t, testRememberSeriesFetch) }

func testRememberSeriesFetch(t *testing.T, d *sql.DB) {
	ctx := context.Background()

	var mu sync.Mutex
	var got []memory_core.SeriesFetchRequest
	prices := memory_core.SeriesProvider{
		Name:        "prices",
		Description: "Daily stock prices. Use for companies whose shares are publicly traded.",
		QueryFormat: "the ticker symbol, e.g. AAPL",
		Fetch: func(_ context.Context, req memory_core.SeriesFetchRequest) ([]memory_core.SeriesData, error) {
			mu.Lock()
			got = append(got, req)
			mu.Unlock()
			// More than was asked for: before the period, and after the reference time.
			return []memory_core.SeriesData{
				{Key: "AAPL", Field: "close", Name: "Apple close", Unit: "USD", Frequency: "1d", Points: []memory_core.SeriesPoint{
					{TS: day("2024-12-01"), Value: 1}, {TS: day("2025-01-30"), Value: 237.6}, {TS: day("2025-01-31"), Value: 222}, {TS: day("2025-02-05"), Value: 1000}}},
				{Key: "AAPL", Field: "volume", Points: []memory_core.SeriesPoint{{TS: day("2025-01-31"), Value: 9e6}}},
				{Key: "AAPL", Field: "empty", Points: []memory_core.SeriesPoint{{TS: day("2020-01-01"), Value: 1}}}, // nothing in the period
			}, nil
		},
	}
	broken := memory_core.SeriesProvider{Name: "broken", Description: "Always fails.",
		Fetch: func(context.Context, memory_core.SeriesFetchRequest) ([]memory_core.SeriesData, error) { panic("boom") }}
	failing := memory_core.SeriesProvider{Name: "failing", Description: "Returns an error.",
		Fetch: func(context.Context, memory_core.SeriesFetchRequest) ([]memory_core.SeriesData, error) {
			return nil, errors.New("rate limited")
		}}
	providers, err := memory_core.ResolveSeriesProviders([]memory_core.SeriesProvider{prices, broken, failing})
	if err != nil {
		t.Fatal(err)
	}

	var resolvePrompt string
	llm := llmFunc(func(req ai.Request) (*ai.Response, error) {
		out := seriesLLM
		if strings.Contains(req.System, "timeseries providers") {
			resolvePrompt = req.Messages[0].Content
			// "foo" is unknown and entity 7 does not exist: both are dropped. The repeat is dropped too.
			out = `{"assignments":[
				{"entity":0,"provider":"prices","query":"AAPL","reason":"Apple is public"},
				{"entity":0,"provider":"prices","query":"AAPL"},
				{"entity":0,"provider":"broken","query":"x"},
				{"entity":0,"provider":"failing"},
				{"entity":1,"provider":"foo","query":"x"},
				{"entity":7,"provider":"prices","query":"x"}]}`
		}
		return &ai.Response{Text: out, StopReason: "end_turn", Model: "fake-model"}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, llm, memory_core.WithSeriesProviders(providers)))

	rec := seriesPost(t, mux, `{"user_id":"u1","kind":"episode","reference_time":"2025-01-31T21:00:00Z","text":"Apple fell after the earnings miss.",
		"timeseries":{"fetch":{"providers":["prices","broken","failing"],"before":"2mo"}}}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res memory_remember.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 0 || len(res.SeriesFetches) != 3 {
		t.Fatalf("fetches: %s", rec.Body)
	}
	for _, want := range []string{"prices", "the ticker symbol, e.g. AAPL", "Use for companies whose shares are publicly traded", "[0] Apple | organization"} {
		if !strings.Contains(resolvePrompt, want) {
			t.Errorf("the LLM prompt lacks %q:\n%s", want, resolvePrompt)
		}
	}

	// What the callback was asked for: the period ends at the reference time (nothing after it by default).
	if len(got) != 1 || got[0].Query != "AAPL" || got[0].Entity.Name != "Apple" || got[0].Entity.Type != "organization" ||
		!got[0].To.Equal(time.Date(2025, 1, 31, 21, 0, 0, 0, time.UTC)) || !got[0].From.Equal(time.Date(2025, 1, 31, 21, 0, 0, 0, time.UTC).Add(-60*24*time.Hour)) {
		t.Errorf("fetch requests: %+v", got)
	}

	f := res.SeriesFetches[0]
	if f.Provider != "prices" || f.Entity != "Apple" || f.Query != "AAPL" || f.Reason != "Apple is public" || f.Error != "" || len(f.Series) != 2 {
		t.Fatalf("first fetch: %+v", f)
	}
	closeS := f.Series[0]
	if !closeS.Created || closeS.Provider != "prices" || closeS.Field != "close" || closeS.PointCount != 2 || closeS.Stats.Last != 222 || closeS.Stats.First != 237.6 {
		t.Errorf("close series (clipped to the period: 2 points): %+v %+v", closeS, closeS.Stats)
	}
	if len(closeS.Anchors) != 2 || closeS.Anchors[0].Type != graph.AnchorEntity || closeS.Anchors[1].Type != graph.AnchorArtifact || closeS.Anchors[1].ID != res.ArtifactID || closeS.Anchors[0].Window == "" {
		t.Errorf("close anchors: %+v", closeS.Anchors)
	}
	if e := res.SeriesFetches[1]; e.Provider != "broken" || !strings.Contains(e.Error, "panicked") || len(e.Series) != 0 {
		t.Errorf("broken provider: %+v", e)
	}
	if e := res.SeriesFetches[2]; e.Provider != "failing" || !strings.Contains(e.Error, "rate limited") {
		t.Errorf("failing provider: %+v", e)
	}

	dsID, _ := relational.DatasetID(ctx, d, "u1", relational.DefaultDataset)
	all, _ := graph.ListSeries(ctx, d, dsID)
	if len(all) != 2 || all[0].Source != graph.SeriesFetched || all[0].Provider != "prices" || !strings.Contains(all[0].Definition, `"query":"AAPL"`) {
		t.Fatalf("stored series: %+v", all)
	}
	as, _ := graph.ListSeriesAnchors(ctx, d, all[0].ID)
	if len(as) != 2 || as[0].Role != "prices" || as[0].WindowStart == nil {
		t.Errorf("stored anchors: %+v", as)
	}

	// Fetching again adds to the same series instead of making new ones.
	rec = seriesPost(t, mux, `{"user_id":"u1","reference_time":"2025-01-31T21:00:00Z","text":"Apple fell after the earnings miss.","timeseries":{"fetch":{"providers":["prices"],"after":"7d"}}}`)
	if rec.Code != 200 {
		t.Fatalf("second status %d: %s", rec.Code, rec.Body)
	}
	if all, _ = graph.ListSeries(ctx, d, dsID); len(all) != 2 {
		t.Errorf("series after refetching: %d", len(all))
	}
}

func TestRememberSeriesFetchRejected(t *testing.T) { dbtest.Run(t, testRememberSeriesFetchRejected) }

func testRememberSeriesFetchRejected(t *testing.T, d *sql.DB) {
	p := memory_core.SeriesProvider{Name: "prices", Description: "Prices.",
		Fetch: func(context.Context, memory_core.SeriesFetchRequest) ([]memory_core.SeriesData, error) {
			return nil, nil
		}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, fakeLLM{seriesLLM}, memory_core.WithSeriesProviders([]memory_core.SeriesProvider{p})))
	for name, c := range map[string]struct {
		body string
		code int
	}{
		"unknown provider": {`{"fetch":{"providers":["nope"]}}`, 400},
		"bad window":       {`{"fetch":{"before":"soon"}}`, 400},
		"negative window":  {`{"fetch":{"after":"-3d"}}`, 400},
		"huge window":      {`{"fetch":{"before":"900y"}}`, 400},
		"ok":               {`{"fetch":{}}`, 200}, // nothing for the fake LLM to assign: no fetch
	} {
		rec := seriesPost(t, mux, `{"user_id":"u1","text":"Apple fell.","timeseries":`+c.body+`}`)
		if rec.Code != c.code {
			t.Errorf("%s: status %d, want %d: %s", name, rec.Code, c.code, rec.Body)
		}
	}
}
