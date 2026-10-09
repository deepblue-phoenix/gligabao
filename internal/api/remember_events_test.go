package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivan/gligabao/internal/db"
	"github.com/ivan/gligabao/internal/memory"
)

func TestRememberEventsBiTemporal(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ents := func(names ...string) string {
		var es []string
		for _, n := range names {
			es = append(es, `{"name":"`+n+`","type":"person"}`)
		}
		return `{"entities":[` + strings.Join(es, ",") + `],"edges":[]}`
	}
	llm := &recordingLLM{outs: []string{
		// 1) Alice joined Acme in 2020.
		ents("Alice", "Acme"),
		`{"events":[{"name":"Joined Company","description":"Alice joined Acme.","participants":["Alice","Acme"],"valid_at":"2020-01-01","invalid_at":null}]}`,
		// 2) Alice joined Globex in 2023: contradicts event 1, which must end at the start of event 2.
		ents("Alice", "Globex"),
		`{"events":[{"name":"joined_company","description":"Alice joined Globex.","participants":["Alice","Globex"],"valid_at":"2023-06-01T00:00:00Z"}]}`,
		`{"duplicates":[],"contradictions":[{"new":0,"existing":1},{"new":0,"existing":999},{"new":7,"existing":1}]}`,
		// 3) Late news about 2019: older than event 1, so the new event is the superseded one.
		ents("Alice", "Initech"),
		`{"events":[{"name":"joined_company","description":"Alice joined Initech.","participants":["Alice","Initech"],"valid_at":"2019-01-01T00:00:00Z"}]}`,
		`{"duplicates":[],"contradictions":[{"new":0,"existing":2}]}`,
		// 4) Exact repeat of event 2: deduplicated without an LLM resolve call.
		ents("Alice", "Globex"),
		`{"events":[{"name":"joined_company","description":"alice joined  globex.","participants":["globex","Alice"],"valid_at":null,"invalid_at":"2025-01-01T00:00:00Z"}]}`,
	}}
	mux := http.NewServeMux()
	Register(mux, d, llm)

	post := func(body string) memory.Result {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var res memory.Result
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	r1 := post(`{"text":"Alice joined Acme in 2020.","reference_time":"2024-01-01T00:00:00Z"}`)
	if len(r1.Events) != 1 || r1.Events[0].ID != 1 || r1.Events[0].Name != "joined_company" ||
		r1.Events[0].ValidAt == nil || r1.Events[0].ValidAt.Year() != 2020 || r1.Events[0].ExpiredAt != nil {
		t.Fatalf("event 1: %+v", r1.Events)
	}
	if got := llm.reqs[1].Messages[0].Content; !strings.Contains(got, "2024-01-01T00:00:00Z") || !strings.Contains(got, "- Alice (person)") {
		t.Errorf("event prompt lacks reference time / entities:\n%s", got)
	}

	r2 := post(`{"text":"Alice joined Globex in June 2023."}`)
	if len(r2.InvalidatedEvents) != 1 || r2.InvalidatedEvents[0] != 1 {
		t.Fatalf("invalidated = %v", r2.InvalidatedEvents)
	}
	var invalid, expired *string
	d.QueryRow(`SELECT invalid_at, expired_at FROM gb_events WHERE id = 1`).Scan(&invalid, &expired)
	if invalid == nil || *invalid != "2023-06-01T00:00:00Z" || expired == nil {
		t.Errorf("event 1 invalid_at=%v expired_at=%v", invalid, expired)
	}
	// Only event 1 (sharing Alice) was offered as candidate.
	if p := llm.reqs[4].Messages[0].Content; !strings.Contains(p, "[1] Alice joined Acme.") {
		t.Errorf("resolve prompt:\n%s", p)
	}

	// Event 1 is expired, so it is no longer a candidate; only event 2 is.
	r3 := post(`{"text":"Alice joined Initech in 2019."}`)
	if len(r3.InvalidatedEvents) != 1 || r3.InvalidatedEvents[0] != 3 {
		t.Fatalf("invalidated = %v", r3.InvalidatedEvents)
	}
	e3 := r3.Events[0]
	if e3.InvalidAt == nil || e3.InvalidAt.Year() != 2023 || e3.ExpiredAt == nil {
		t.Errorf("event 3: %+v", e3)
	}
	// Event 2 untouched by the older news.
	d.QueryRow(`SELECT invalid_at, expired_at FROM gb_events WHERE id = 2`).Scan(&invalid, &expired)
	if invalid != nil || expired != nil {
		t.Errorf("event 2 invalid_at=%v expired_at=%v", invalid, expired)
	}

	// Repeat of event 2: merged, missing dates filled, no extra LLM call (all outs consumed exactly).
	r4 := post(`{"text":"Alice joined Globex."}`)
	if len(r4.Events) != 1 || r4.Events[0].ID != 2 || r4.Events[0].InvalidAt == nil || r4.Events[0].InvalidAt.Year() != 2025 {
		t.Fatalf("event 4: %+v", r4.Events)
	}
	if len(llm.outs) != 0 {
		t.Errorf("unused LLM outputs: %d", len(llm.outs))
	}

	var n int
	d.QueryRow(`SELECT COUNT(*) FROM gb_events`).Scan(&n)
	if n != 3 {
		t.Errorf("events = %d, want 3", n)
	}
	d.QueryRow(`SELECT COUNT(*) FROM gb_event_participants WHERE event_id = 2`).Scan(&n)
	if n != 2 {
		t.Errorf("participants of event 2 = %d, want 2", n)
	}
	var ref string
	d.QueryRow(`SELECT reference_time FROM gb_memories WHERE id = 1`).Scan(&ref)
	if ref != "2024-01-01T00:00:00Z" {
		t.Errorf("reference_time = %q", ref)
	}
}

func TestRememberBadReferenceTime(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, nil, fakeLLM{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(`{"text":"x","reference_time":"yesterday"}`)))
	if rec.Code != 400 {
		t.Errorf("status = %d", rec.Code)
	}
}
