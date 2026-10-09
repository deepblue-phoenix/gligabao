package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivan/gligabao/internal/ai"
	"github.com/ivan/gligabao/internal/db"
)

// echoLLM answers extraction requests with a fixed graph and recall requests with a fixed answer.
type echoLLM struct{}

func (echoLLM) Complete(_ context.Context, r ai.Request) (*ai.Response, error) {
	out := `{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"}],
		"edges":[{"source":"Alice","target":"Acme","relation":"works_at"}]}`
	if r.System == "" || strings.Contains(r.Messages[0].Content, "<question>") {
		out = `{"answer":"ok","entities":[],"edges":[]}`
	}
	return &ai.Response{Text: out, StopReason: "end_turn"}, nil
}

func TestDatasets(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	mux := http.NewServeMux()
	Register(mux, d, echoLLM{})

	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	want := func(rec *httptest.ResponseRecorder, code int) {
		t.Helper()
		if rec.Code != code {
			t.Fatalf("status %d, want %d: %s", rec.Code, code, rec.Body)
		}
	}
	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := d.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	names := func(path string) []string {
		t.Helper()
		rec := do("GET", path, "")
		want(rec, 200)
		var out struct {
			Datasets []struct {
				Name    string   `json:"name"`
				Tags    []string `json:"tags"`
				Deleted bool     `json:"deleted"`
			} `json:"datasets"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var ns []string
		for _, ds := range out.Datasets {
			n := ds.Name
			if ds.Deleted {
				n += "(deleted)"
			}
			ns = append(ns, n)
		}
		return ns
	}

	// A user has no datasets until one is created.
	if got := names("/v1/gb/datasets?user_id=u1"); len(got) != 0 {
		t.Fatalf("initial datasets = %v", got)
	}
	// user_id is required everywhere.
	want(do("GET", "/v1/gb/datasets", ""), 400)
	want(do("POST", "/v1/gb/datasets", `{"name":"x"}`), 400)
	want(do("DELETE", "/v1/gb/datasets/x", ""), 400)
	want(do("POST", "/v1/gb/remember", `{"text":"x"}`), 400)
	want(do("POST", "/v1/gb/recall", `{"query":"x"}`), 400)

	// Create.
	rec := do("POST", "/v1/gb/datasets", `{"user_id":"u1","name":"ds1","tags":["a"," b ","a",""],"description":"first"}`)
	want(rec, 201)
	var ds struct {
		ID        int64
		Tags      []string
		CreatedAt string `json:"created_at"`
		Deleted   bool
	}
	json.Unmarshal(rec.Body.Bytes(), &ds)
	if strings.Join(ds.Tags, ",") != "a,b" || ds.CreatedAt == "" || ds.Deleted {
		t.Errorf("created dataset: %s", rec.Body)
	}
	want(do("POST", "/v1/gb/datasets", `{"user_id":"u1","name":"ds1"}`), 409)
	want(do("POST", "/v1/gb/datasets", `{"user_id":"u1","name":""}`), 400)
	want(do("POST", "/v1/gb/datasets", `{"user_id":"u1","name":"a b/c"}`), 400)

	// remember/recall are scoped: the same entity in two datasets is two rows, never linked.
	want(do("POST", "/v1/gb/remember", `{"user_id":"u1","text":"x","dataset":"ds1"}`), 200)
	want(do("POST", "/v1/gb/remember", `{"user_id":"u1","text":"x","dataset":"ds2"}`), 200) // created on demand
	want(do("POST", "/v1/gb/remember", `{"user_id":"u1","text":"x"}`), 200)                 // default: main
	if n := count(`SELECT COUNT(*) FROM gb_entities WHERE name = 'Alice'`); n != 3 {
		t.Errorf("Alice rows = %d, want 3 (one per dataset)", n)
	}
	if n := count(`SELECT COUNT(*) FROM gb_edges e JOIN gb_entities s ON s.id = e.source_id JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id != s.dataset_id OR e.dataset_id != t.dataset_id`); n != 0 {
		t.Errorf("%d edges cross datasets", n)
	}
	if n := count(`SELECT COUNT(*) FROM gb_edges e JOIN gb_datasets d ON d.id = e.dataset_id WHERE d.name = 'main'`); n != 1 {
		t.Errorf("main edges = %d, want 1", n)
	}
	if got := names("/v1/gb/datasets?user_id=u1"); strings.Join(got, ",") != "ds1,ds2,main" {
		t.Errorf("datasets = %v", got)
	}

	// recall: unknown dataset is a 404, an empty one answers without evidence.
	want(do("POST", "/v1/gb/recall", `{"user_id":"u1","query":"Alice?","dataset":"nope"}`), 404)
	want(do("POST", "/v1/gb/recall", `{"user_id":"u1","query":"Alice?","dataset":"bad name"}`), 400)
	want(do("POST", "/v1/gb/recall", `{"user_id":"u1","query":"Alice?","dataset":"ds1"}`), 200)
	want(do("POST", "/v1/gb/recall", `{"user_id":"u1","query":"Alice?"}`), 200)

	// Soft delete: the row stays with deleted = 1, but the dataset is gone for remember/recall/list.
	want(do("DELETE", "/v1/gb/datasets/ds1?user_id=u1", ""), 204)
	want(do("DELETE", "/v1/gb/datasets/ds1?user_id=u1", ""), 404)
	if n := count(`SELECT COUNT(*) FROM gb_datasets WHERE name = 'ds1' AND deleted = 1 AND deleted_at IS NOT NULL`); n != 1 {
		t.Errorf("soft-deleted rows = %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM gb_entities WHERE dataset_id = ?`, ds.ID); n != 2 {
		t.Errorf("deleted dataset's entities = %d, want kept (2)", n)
	}
	want(do("POST", "/v1/gb/recall", `{"user_id":"u1","query":"Alice?","dataset":"ds1"}`), 404)
	if got := names("/v1/gb/datasets?user_id=u1"); strings.Join(got, ",") != "ds2,main" {
		t.Errorf("datasets after delete = %v", got)
	}
	if got := names("/v1/gb/datasets?user_id=u1&include_deleted=true"); strings.Join(got, ",") != "ds1(deleted),ds2,main" {
		t.Errorf("datasets incl. deleted = %v", got)
	}

	// The name can be reused: a new dataset with a new id and an empty graph.
	want(do("POST", "/v1/gb/datasets", `{"user_id":"u1","name":"ds1"}`), 201)
	if n := count(`SELECT COUNT(*) FROM gb_entities e JOIN gb_datasets d ON d.id = e.dataset_id WHERE d.name = 'ds1' AND d.deleted = 0`); n != 0 {
		t.Errorf("recreated dataset has %d entities", n)
	}

	// Datasets belong to a user: another user sees none of u1's, can reuse its names, and cannot delete or recall them.
	if got := names("/v1/gb/datasets?user_id=u2"); len(got) != 0 {
		t.Errorf("u2 datasets = %v, want none", got)
	}
	want(do("POST", "/v1/gb/recall", `{"user_id":"u2","query":"Alice?","dataset":"ds2"}`), 404)
	want(do("DELETE", "/v1/gb/datasets/ds2?user_id=u2", ""), 404)
	want(do("POST", "/v1/gb/datasets", `{"user_id":"u2","name":"ds2"}`), 201)
	want(do("POST", "/v1/gb/remember", `{"user_id":"u2","text":"x","dataset":"ds2"}`), 200)
	if n := count(`SELECT COUNT(*) FROM gb_datasets WHERE name = 'ds2' AND deleted = 0`); n != 2 {
		t.Errorf("live ds2 datasets = %d, want 2 (one per user)", n)
	}
	if n := count(`SELECT COUNT(*) FROM gb_entities e JOIN gb_datasets d ON d.id = e.dataset_id WHERE d.user_id = 'u2'`); n != 2 {
		t.Errorf("u2 entities = %d, want 2", n)
	}
}
