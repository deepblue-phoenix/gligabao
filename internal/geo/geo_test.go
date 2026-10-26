package geo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormKind(t *testing.T) {
	for in, want := range map[string]string{"City": KindCity, " country ": KindCountry, "state": KindRegion, "landmark": KindPlace, "person": "", "": ""} {
		if got := NormKind(in); got != want {
			t.Errorf("NormKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlaceValidate(t *testing.T) {
	if err := (Place{Latitude: 48.8, Longitude: 2.3}).Validate(); err != nil {
		t.Error(err)
	}
	for _, p := range []Place{{Latitude: 91}, {Longitude: -181}} {
		if p.Validate() == nil {
			t.Errorf("%+v should be invalid", p)
		}
	}
}

const parisJSON = `[{"place_id":1,"osm_type":"relation","osm_id":7444,"lat":"48.8588897","lon":"2.3200410","category":"boundary","type":"administrative",
"addresstype":"city","place_rank":16,"importance":0.83,"display_name":"Paris, Île-de-France, France",
"boundingbox":["48.8155755","48.9021560","2.2241220","2.4697602"],
"address":{"city":"Paris","state":"Île-de-France","country":"France","country_code":"fr"}}]`

func TestNominatim(t *testing.T) {
	var gotQ, gotUA, gotCC string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQ, gotUA, gotCC = r.URL.Query().Get("q"), r.UserAgent(), r.URL.Query().Get("countrycodes")
		if r.URL.Query().Get("q") == "Nowhere" {
			w.Write([]byte(`[]`))
			return
		}
		if r.URL.Query().Get("q") == "Boom" {
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(parisJSON))
	}))
	defer srv.Close()
	r := NewNominatim(NominatimOptions{BaseURL: srv.URL, UserAgent: "test-agent", MinInterval: -1})

	p, err := r.Resolve(t.Context(), Query{Text: " Paris, France ", CountryCode: "FR"})
	if err != nil {
		t.Fatal(err)
	}
	if gotQ != "Paris, France" || gotUA != "test-agent" || gotCC != "fr" {
		t.Errorf("request: q=%q ua=%q countrycodes=%q", gotQ, gotUA, gotCC)
	}
	if p.Latitude != 48.8588897 || p.Longitude != 2.3200410 || p.Kind != KindCity || p.CountryCode != "fr" || p.Country != "France" ||
		p.Region != "Île-de-France" || p.Ref != "relation/7444" || p.Provider != "nominatim" || p.Importance != 0.83 {
		t.Errorf("place = %+v", p)
	}
	if p.BBox == nil || *p.BBox != [4]float64{48.8155755, 2.2241220, 48.9021560, 2.4697602} { // south, west, north, east
		t.Errorf("bbox = %v", p.BBox)
	}
	if _, err := r.Resolve(t.Context(), Query{Text: "Nowhere"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, err := r.Resolve(t.Context(), Query{Text: " "}); !errors.Is(err, ErrNotFound) {
		t.Errorf("blank: err = %v, want ErrNotFound", err)
	}
	if _, err := r.Resolve(t.Context(), Query{Text: "Boom"}); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want a failure", err)
	}
}

func TestNominatimInterval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(parisJSON)) }))
	defer srv.Close()
	r := NewNominatim(NominatimOptions{BaseURL: srv.URL, MinInterval: 100 * time.Millisecond})
	start := time.Now()
	for range 3 {
		if _, err := r.Resolve(t.Context(), Query{Text: "Paris"}); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Errorf("3 requests took %v, want at least 200ms", d)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := r.Resolve(ctx, Query{Text: "Paris"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's", err)
	}
}

func TestNominatimKind(t *testing.T) {
	for _, c := range []struct {
		r    nominatimResult
		want string
	}{
		{nominatimResult{AddressType: "country"}, KindCountry},
		{nominatimResult{AddressType: "state"}, KindRegion},
		{nominatimResult{AddressType: "town"}, KindCity},
		{nominatimResult{AddressType: "tourism"}, KindPlace},
		{nominatimResult{PlaceRank: 4}, KindCountry},
		{nominatimResult{PlaceRank: 16}, KindCity},
		{nominatimResult{PlaceRank: 30}, KindPlace},
		{nominatimResult{}, ""},
	} {
		if got := nominatimKind(c.r); got != c.want {
			t.Errorf("%+v: kind = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestCached(t *testing.T) {
	var calls atomic.Int32
	fail := true
	r := Cached(Func{ResolverName: "stub", Fn: func(ctx context.Context, q Query) (*Place, error) {
		calls.Add(1)
		switch q.Text {
		case "Paris":
			return &Place{Latitude: 1, Longitude: 2}, nil
		case "Flaky":
			if fail {
				return nil, errors.New("down")
			}
			return &Place{}, nil
		}
		return nil, ErrNotFound
	}})
	if r.Name() != "stub" {
		t.Errorf("name = %q", r.Name())
	}
	for _, text := range []string{"Paris", " paris "} {
		if p, err := r.Resolve(t.Context(), Query{Text: text}); err != nil || p.Latitude != 1 {
			t.Fatalf("paris: %v %v", p, err)
		}
		if _, err := r.Resolve(t.Context(), Query{Text: "Atlantis"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("atlantis: %v", err)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (one per distinct query)", calls.Load())
	}
	for range 2 { // a failure is not cached
		if _, err := r.Resolve(t.Context(), Query{Text: "Flaky"}); err == nil {
			t.Fatal("want an error")
		}
	}
	if calls.Load() != 4 {
		t.Errorf("calls = %d, want 4", calls.Load())
	}
}
