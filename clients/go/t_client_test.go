package gligabao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRemember(t *testing.T) {
	var got map[string]any
	var auth, user string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/gb/remember" {
			t.Errorf("unexpected %s %s",
				r.Method,
				r.URL.Path)
		}
		auth, user = r.Header.Get("Authorization"), r.Header.Get("gb-user-id")
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"dataset":"main","kind":"episode","artifact_id":7,"valid_at":"2025-01-31T09:00:00Z",
			"entities":[{"name":"Alice","type":"person"}],
			"edges":[{"id":1,"source":"Alice","target":"Acme","relation":"works_at","valid_at":"2025-01-31T09:00:00Z"}],
			"invalidated_edges":[],"remember_run_id":3}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/",
		WithAPIKey("k1"),
		WithUserID("u1"))
	res, err := c.Remember(context.Background(), RememberRequest{
		Text: "Alice works at Acme.",
		Kind: "episode",
		ReferenceTime: time.Date(2025,
			1,
			31,
			9,
			0,
			0,
			0,
			time.UTC),
		Extra: map[string]any{"timeseries": map[string]any{"fetch": map[string]any{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ArtifactID != 7 || len(res.Edges) != 1 || res.Edges[0].Relation != "works_at" || res.RememberRunID != 3 || len(res.Raw) == 0 {
		t.Errorf("bad response: %+v", res)
	}
	if auth != "Bearer k1" {
		t.Errorf("auth = %q", auth)
	}
	if user != "u1" {
		t.Errorf("gb-user-id = %q", user)
	}
	if _, ok := got["user_id"]; ok || got["text"] != "Alice works at Acme." || got["kind"] != "episode" ||
		got["reference_time"] != "2025-01-31T09:00:00Z" || got["timeseries"] == nil {
		t.Errorf("bad body: %v", got)
	}
	for _, k := range []string{"dataset", "session_id", "models", "web_search"} {
		if _, ok := got[k]; ok {
			t.Errorf("empty field %q was sent", k)
		}
	}
}

func TestQuery(t *testing.T) {
	var got map[string]any
	var user string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		user = r.Header.Get("gb-user-id")
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"answer":"Acme.","query_run_id":9,"evidence":{"entities":[{"id":1,"name":"Alice"}],
			"edges":[{"id":1,"source_id":1,"target_id":2,"source":"Alice","target":"Acme","relation":"works_at","artifact_id":4,"valid_at":"2025-01-31T09:00:00Z"}],
			"passages":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, WithUserID("u1"))
	res, err := c.Query(context.Background(), QueryRequest{
		Query:     "Where does Alice work?",
		Reasoning: map[string]string{"query": "low"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "Acme." || res.Evidence.Edges[0].SourceID != 1 || res.Evidence.Edges[0].Relation != "works_at" || res.Evidence.Edges[0].ArtifactID != 4 {
		t.Errorf("bad response: %+v", res)
	}
	if _, ok := got["user_id"]; ok || user != "u1" || got["query"] != "Where does Alice work?" || got["reasoning"] == nil {
		t.Errorf("bad body: %v", got)
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"dataset not found"}`))
	}))
	c := New(srv.URL)
	_, err := c.Query(context.Background(), QueryRequest{
		Query: "x",
	})
	var ge *Error
	if !errors.As(err, &ge) || ge.Status != 404 || ge.Message != "dataset not found" {
		t.Errorf("err = %v", err)
	}
	srv.Close()
	if _, err := c.Query(context.Background(), QueryRequest{
		Query: "x",
	}); !errors.As(err, &ge) || ge.Status != 0 {
		t.Errorf("unreachable: err = %v", err)
	}
	if _, err := c.Remember(context.Background(), RememberRequest{}); err == nil {
		t.Error("empty text accepted")
	}
}
