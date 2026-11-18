package remember

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

func geoManagerLLM() llmFunc {
	return llmFunc(func(req llm.Request) (*llm.Response, error) {
		out := geoLLM
		if strings.Contains(req.System, "geographic places") {
			out = `{"places":[{"entity":0,"kind":"city","query":"Paris, France"}]}`
		}
		return &llm.Response{
			Text:       out,
			StopReason: "end_turn",
			Model:      "fake-model",
		}, nil
	})
}

func rememberWith(t *testing.T,
	d *sql.DB,
	body string,
	opts ...memory_core.Option) memory_remember.Result {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t,
		d,
		geoManagerLLM(),
		opts...))
	rec := seriesPost(t,
		mux,
		body)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s",
			rec.Code,
			rec.Body)
	}
	var out memory_remember.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// With the geo manager remember does not wait for the resolver: the place is returned as pending, and the manager stores it later.
func TestRememberGeoManagerAsync(t *testing.T) { dbtest.Run(t, testRememberGeoManagerAsync) }

func testRememberGeoManagerAsync(t *testing.T,
	d *sql.DB) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := &stubResolver{}
	m := memory_geo.NewManager(ctx, res)
	body := `{"user_id":"u1","text":"Alice lives in Paris and works at Acme."}`

	out := rememberWith(t,
		d,
		body,
		memory_core.WithGeoResolver(m))
	if len(out.Geo) != 1 || !out.Geo[0].Pending || out.Geo[0].Resolved || out.Geo[0].EntityID == 0 || out.Geo[0].Entity != "Paris" {
		t.Fatalf("geo results: %+v", out.Geo)
	}
	id := out.Geo[0].EntityID
	if err := m.Flush(ctx); err != nil { // wait for the manager (what the call did not)
		t.Fatal(err)
	}
	g, err := graph.GetEntityGeo(ctx,
		d,
		id)
	if err != nil || g.Latitude != 48.8566 || g.Provider != "stub" {
		t.Fatalf("stored geo: %+v, %v", g, err)
	}
	tx, _ := graph.Begin(ctx, d)
	defer tx.Rollback()
	if left, err := tx.ListGeoPending(ctx,
		5,
		10); err != nil || len(left) != 0 {
		t.Errorf("pending after resolving: %+v, %v", left, err)
	}
}

// geo_sync waits for the manager: the coordinates are in the result.
func TestRememberGeoManagerSync(t *testing.T) { dbtest.Run(t, testRememberGeoManagerSync) }

func testRememberGeoManagerSync(t *testing.T,
	d *sql.DB) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := memory_geo.NewManager(ctx, &stubResolver{})
	out := rememberWith(t,
		d,
		`{"user_id":"u1","text":"Alice lives in Paris and works at Acme.","geo_sync":true}`,
		memory_core.WithGeoResolver(m))
	if len(out.Geo) != 1 || out.Geo[0].Pending || !out.Geo[0].Resolved || out.Geo[0].Latitude != 48.8566 || out.Geo[0].Provider != "stub" {
		t.Fatalf("geo results: %+v", out.Geo)
	}
	if _, err := graph.GetEntityGeo(ctx,
		d,
		out.Geo[0].EntityID); err != nil {
		t.Fatalf("stored geo: %v", err)
	}
}

// Deferred (the worker): the place is only recorded as pending; the sweeper of a server with a manager resolves it.
func TestRememberGeoDeferredAndSweeper(t *testing.T) {
	dbtest.Run(t, testRememberGeoDeferredAndSweeper)
}

func testRememberGeoDeferredAndSweeper(t *testing.T,
	d *sql.DB) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := &stubResolver{}
	out := rememberWith(t,
		d,
		`{"user_id":"u1","text":"Alice lives in Paris and works at Acme."}`,
		memory_core.WithGeoDeferred(true))
	if len(out.Geo) != 1 || !out.Geo[0].Pending {
		t.Fatalf("geo results: %+v", out.Geo)
	}
	if len(res.asked) != 0 {
		t.Fatal("the resolver was asked by a deferred call")
	}
	id := out.Geo[0].EntityID
	if _, err := graph.GetEntityGeo(ctx,
		d,
		id); err == nil {
		t.Fatal("a deferred call stored coordinates")
	}

	m := memory_geo.NewManager(ctx, res)
	go memory_remember.RunGeoSweeper(ctx,
		d,
		m,
		"stub",
		time.Hour)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if g, err := graph.GetEntityGeo(ctx,
			d,
			id); err == nil {
			if g.Latitude != 48.8566 {
				t.Fatalf("stored geo: %+v", g)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweeper did not resolve the pending place")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
