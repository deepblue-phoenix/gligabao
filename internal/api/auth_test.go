package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestAuth(t *testing.T) { dbtest.Run(t, testAuth) }

func testAuth(t *testing.T, d *sql.DB) {
	keys := map[string]string{"key-alice": "alice", "key-bob": "bob", "key-empty": ""}
	auth := func(_ context.Context, key string) (string, error) {
		u, ok := keys[key]
		if !ok {
			return "", errors.New("unknown key")
		}
		return u, nil
	}
	mux := http.NewServeMux()
	Register(mux, mgrFor(t, &db.DB{Relational: d, Graph: d}, nil), &db.DB{Relational: d, Graph: d}, nil, nil, nil, nil, nil, WithAuth(auth))

	do := func(key, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// no key, a wrong key, a key of nobody: 401, and nothing is created
	for _, key := range []string{"", "nope", "key-empty"} {
		if rec := do(key, "POST", "/v1/gb/datasets", `{"user_id":"alice","name":"work"}`); rec.Code != 401 {
			t.Fatalf("key %q: status %d, want 401: %s", key, rec.Code, rec.Body)
		}
	}
	if rec := do("", "GET", "/v1/gb/datasets?user_id=alice", ""); rec.Code != 401 {
		t.Fatalf("no key on GET: %d", rec.Code)
	}

	// the user is the owner of the key, whatever user_id the caller sends (or leaves out)
	rec := do("key-alice", "POST", "/v1/gb/datasets", `{"user_id":"mallory","name":"work"}`)
	if rec.Code != 201 && rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var ds struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ds); err != nil || ds.UserID != "alice" {
		t.Fatalf("dataset user = %q (%v), want alice: %s", ds.UserID, err, rec.Body)
	}
	if rec := do("key-alice", "POST", "/v1/gb/datasets", `{"name":"notes"}`); rec.Code >= 300 { // no user_id at all
		t.Fatalf("create without user_id: %d %s", rec.Code, rec.Body)
	}

	// GET and DELETE: the query's user_id is replaced too, so bob cannot list or delete alice's datasets
	list := func(key, path string) int {
		rec := do(key, "GET", path, "")
		if rec.Code != 200 {
			t.Fatalf("list: %d %s", rec.Code, rec.Body)
		}
		var out []json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			var wrapped struct {
				Datasets []json.RawMessage `json:"datasets"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &wrapped); err != nil {
				t.Fatalf("list body: %s", rec.Body)
			}
			out = wrapped.Datasets
		}
		return len(out)
	}
	if n := list("key-alice", "/v1/gb/datasets"); n != 2 {
		t.Fatalf("alice lists %d datasets, want 2", n)
	}
	if n := list("key-bob", "/v1/gb/datasets?user_id=alice"); n != 0 {
		t.Fatalf("bob lists %d datasets of alice, want 0", n)
	}
	if rec := do("key-bob", "DELETE", "/v1/gb/datasets/work?user_id=alice", ""); rec.Code != 404 {
		t.Fatalf("bob deletes alice's dataset: %d, want 404", rec.Code)
	}

	// a body that is not a JSON object is a 400
	if rec := do("key-alice", "POST", "/v1/gb/datasets", `[1]`); rec.Code != 400 {
		t.Fatalf("array body: %d, want 400", rec.Code)
	}
}

func TestNoAuthTrustsUserID(t *testing.T) { dbtest.Run(t, testNoAuthTrustsUserID) }

func testNoAuthTrustsUserID(t *testing.T, d *sql.DB) {
	mux := http.NewServeMux()
	Register(mux, mgrFor(t, &db.DB{Relational: d, Graph: d}, nil), &db.DB{Relational: d, Graph: d}, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/datasets", strings.NewReader(`{"user_id":"u1","name":"work"}`)))
	if rec.Code >= 300 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
